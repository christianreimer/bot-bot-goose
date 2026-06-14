// Package httpx wires the HTTP server: router, middleware, template loader,
// and the handlers themselves. Keeping them in one package keeps the cmd/server
// main entry small (it only constructs dependencies and starts listening).
package httpx

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/cache"
	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/christianreimer/bot-bot-goose/internal/email"
	"github.com/christianreimer/bot-bot-goose/internal/metrics"
	"github.com/christianreimer/bot-bot-goose/internal/ratelimit"
	"github.com/christianreimer/bot-bot-goose/internal/share"
	"github.com/christianreimer/bot-bot-goose/internal/users"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Config groups dependencies the server needs at boot.
type Config struct {
	Listen       string
	BaseURL      string
	WebDir       string // root of templates/ and static/
	SessionKey   []byte
	SecureCookie bool
	DB           *db.DB
	Cache        *cache.Cache // optional; nil acts as no-op
	Email        email.Sender
	Logger       *slog.Logger

	// MetricsListen, if non-empty, starts a second http.Server bound to that
	// address serving only /metrics. The plan calls for the metrics endpoint
	// to live on a private port so Caddy/Cloudflare don't expose it; binding
	// to e.g. "127.0.0.1:8081" is the recommended shape. Leave empty in
	// dev/test — the metrics counters are still maintained, just not served.
	MetricsListen string
}

// Server is the top-level handler. Construct with New, mount as http.Handler.
//
// templates is keyed by page name (e.g. "pages/play.html"). Each page gets
// its own cloned template set so that `{{ define "content" }}` blocks across
// pages don't shadow each other — a single shared set would let the
// last-parsed page win for every render.
type Server struct {
	cfg       Config
	router    chi.Router
	templates map[string]*template.Template
	assets    *assetIndex
	limiter   ratelimit.Limiter
}

func New(cfg Config) (*Server, error) {
	if cfg.WebDir == "" {
		cfg.WebDir = "web"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Email == nil {
		// Sane default for tests and quick local runs: stdout instead of a
		// real outbound email. Production wiring lives in cmd/server.
		cfg.Email = &email.LogSender{Log: cfg.Logger}
	}
	assets, err := newAssetIndex(cfg.WebDir)
	if err != nil {
		return nil, fmt.Errorf("index static assets: %w", err)
	}
	tpl, err := loadTemplates(cfg.WebDir, assets)
	if err != nil {
		return nil, fmt.Errorf("load templates: %w", err)
	}
	metrics.Init(cfg.DB.Pool)
	share.WarmStaticOG()

	// Limiter selection: Valkey-backed if a cache is wired and reachable,
	// Postgres fallback otherwise. See plans/launch-capacity §2.2.
	pgLimiter := ratelimit.New(cfg.DB.Pool)
	var limiter ratelimit.Limiter = pgLimiter
	if cfg.Cache.Enabled() {
		limiter = ratelimit.NewValkey(cfg.Cache, pgLimiter)
	}

	s := &Server{
		cfg:       cfg,
		templates: tpl,
		assets:    assets,
		limiter:   limiter,
	}
	s.routes()
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.router }

// ListenAndServe is convenience for cmd/server. Starts the main listener
// and, if Config.MetricsListen is set, a second listener that serves the
// /metrics endpoint only. The metrics listener is meant to bind to a
// loopback or private IP — the launch-capacity plan deliberately keeps
// /metrics off the public Caddy/Cloudflare path.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	s.cfg.Logger.Info("listening", "addr", s.cfg.Listen)

	var metricsSrv *http.Server
	if s.cfg.MetricsListen != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", metrics.Handler())
		metricsSrv = &http.Server{
			Addr:              s.cfg.MetricsListen,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			s.cfg.Logger.Info("metrics listening", "addr", s.cfg.MetricsListen)
			if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.cfg.Logger.Warn("metrics server", "err", err)
			}
		}()
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if metricsSrv != nil {
			_ = metricsSrv.Shutdown(shutdownCtx)
		}
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if metricsSrv != nil {
			_ = metricsSrv.Shutdown(context.Background())
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) routes() {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	// Compress middleware deliberately removed: Caddy + Cloudflare already
	// encode responses on the way out. Doing it twice was wasted CPU under
	// load. See plans/launch-capacity.md §1.5.
	r.Use(instrumentHTTP)
	r.Use(headSupport)

	// System endpoints — no session, no CSRF, no template overhead.
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	// Privacy disclosure. Outside the session-middleware group on purpose:
	// reading the privacy page should not be the moment we set a cookie.
	r.Get("/privacy", s.handlePrivacy)
	r.Get("/readyz", s.handleReadyz)
	r.Get("/robots.txt", s.handleRobots)

	// Public share + OG image routes — outside the session-middleware group
	// on purpose. These handlers don't read users.FromContext, and routing
	// them under Middleware caused a row in `users` and a Set-Cookie header
	// for every unfurl-bot scrape of a shared link (Twitter, Slack, Discord,
	// etc. each fetch the og:image independently). See plans/launch-capacity
	// §1.1.
	r.Get("/d/{short}", s.handleDecoyShare)
	r.Get("/d/{short}/og.png", s.handleDecoyShareOG)
	r.Get("/r/{short}", s.handleResultShare)
	r.Get("/r/{short}/og.png", s.handleResultShareOG)
	r.Get("/harvest/og.png", s.handleHarvestOG)

	// Themed 404 for any path that doesn't match a route above or below.
	// Outside the session-middleware group too: minting a cookie just
	// because someone hit a typo'd URL is bad UX (and would mask the
	// session-mint error path).
	r.NotFound(s.renderNotFound)

	// Static — content-hashed URLs (?v=<hash>) come from the `asset` template
	// helper, so we can set far-future Cache-Control here without risking a
	// stale CSS/JS surviving a deploy. The manifest and service worker live
	// at the root (unversioned) and stay short-cache so PWA updates roll.
	fileServer := http.StripPrefix("/static/", http.FileServer(http.Dir(filepath.Join(s.cfg.WebDir, "static"))))
	r.Handle("/static/*", cacheImmutable(fileServer))
	r.Handle("/manifest.json", http.FileServer(http.Dir(filepath.Join(s.cfg.WebDir, "static"))))
	r.Handle("/service-worker.js", noStore(http.FileServer(http.Dir(filepath.Join(s.cfg.WebDir, "static")))))

	// Player routes — all behind device-cookie session middleware.
	r.Group(func(r chi.Router) {
		r.Use(users.Middleware(s.cfg.DB, s.cfg.Cache, s.cfg.SessionKey, s.cfg.SecureCookie))
		r.Use(users.CSRFMiddleware(s.cfg.SecureCookie))

		// Only one entry point to the play surface: "/" serves today's
		// puzzle (or today's result, if the user has completed it).
		// Historical access is by share URL only (/r/<short>, /d/<short>),
		// keying on row IDs rather than puzzle numbers.
		r.Get("/", s.handlePlayLanding)

		r.Get("/me", s.handleMe)
		r.Get("/leaderboard/originals", s.handleLeaderboardOriginals)
		r.Get("/leaderboard/spotters", s.handleLeaderboardSpotters)

		// Phase-0 harvest campaign. The deck filters by user ID so this one
		// stays behind the session middleware (unlike /harvest/og.png, which
		// is mounted above). Submissions land in pre_launch_submissions and
		// do NOT auto-flow into the live game. See plans/harvest.
		r.Get("/harvest", s.handleHarvest)
		r.Post("/api/harvest/submit", s.handleHarvestSubmit)

		// Magic-link sign-in. The GET is the email-tap path; the POST is
		// the form that mails the link. Both run behind the session
		// middleware so we know which device cookie to bind.
		r.Post("/api/auth/magic/request", s.handleMagicRequest)
		r.Get("/auth/magic/{token}", s.handleMagicConsume)
		r.Post("/api/auth/logout", s.handleLogout)
		r.Post("/api/auth/logout-all", s.handleLogoutAll)

		r.Route("/api", func(r chi.Router) {
			r.Post("/play/start", s.handleAPIPlayStart)
			r.Post("/play/round/{n}/hint", s.handleAPIHint)
			r.Post("/play/round/{n}/guess", s.handleAPIGuess)
			r.Post("/play/round/{n}/realest", s.handleAPIRealest)
			r.Post("/decoy/submit", s.handleAPIDecoySubmit)
			r.Patch("/me/handle", s.handlePatchHandle)
		})
	})

	s.router = r
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.cfg.DB.Ping(ctx); err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("User-agent: *\nDisallow: /api/\n"))
}

// cacheImmutable sets a year-long Cache-Control on responses. Safe only for
// routes whose URLs change when content changes (we content-hash /static/*
// via the `asset` template helper).
func cacheImmutable(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		h.ServeHTTP(w, r)
	})
}

// noStore tells the browser to revalidate every time. Used for the service
// worker so a bug-fix doesn't get pinned by an intermediate cache.
func noStore(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		h.ServeHTTP(w, r)
	})
}

// loadTemplates reads layouts/ as the shared base and produces one template
// set per file under pages/. Each page is a clone of the layout chain plus
// the page's own defines, so a `{{ define "content" }}` in pages/play.html
// only affects play renders — not result.html or no_puzzle.html.
func loadTemplates(webDir string, assets *assetIndex) (map[string]*template.Template, error) {
	root := filepath.Join(webDir, "templates")
	funcs := template.FuncMap{
		"pad3":  func(n int32) string { return fmt.Sprintf("%03d", n) },
		"asset": assets.url,
	}

	// 1. Parse every layout/partial file into a shared base template.
	base := template.New("").Funcs(funcs)
	layoutsDir := filepath.Join(root, "layouts")
	if err := filepath.WalkDir(layoutsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".html" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = base.Parse(string(b))
		return err
	}); err != nil {
		return nil, fmt.Errorf("parse layouts: %w", err)
	}

	// 2. For each page, clone the base and parse the page's body. The page's
	//    `{{ define }}` blocks land in the clone only.
	out := map[string]*template.Template{}
	pagesDir := filepath.Join(root, "pages")
	if err := filepath.WalkDir(pagesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".html" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name, _ := filepath.Rel(root, path)
		cloned, err := base.Clone()
		if err != nil {
			return fmt.Errorf("clone base for %s: %w", name, err)
		}
		page, err := cloned.New(name).Parse(string(b))
		if err != nil {
			return fmt.Errorf("parse %s: %w", name, err)
		}
		out[name] = page
		return nil
	}); err != nil {
		return nil, fmt.Errorf("parse pages: %w", err)
	}
	return out, nil
}
