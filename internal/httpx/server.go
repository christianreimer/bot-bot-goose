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

	"github.com/christianreimer/bot-bot-goose/internal/db"
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
	Logger       *slog.Logger
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
}

func New(cfg Config) (*Server, error) {
	if cfg.WebDir == "" {
		cfg.WebDir = "web"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	tpl, err := loadTemplates(cfg.WebDir)
	if err != nil {
		return nil, fmt.Errorf("load templates: %w", err)
	}
	s := &Server{cfg: cfg, templates: tpl}
	s.routes()
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.router }

// ListenAndServe is convenience for cmd/server.
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
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
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
	r.Use(middleware.Compress(5))
	r.Use(headSupport)

	// System endpoints — no session, no CSRF, no template overhead.
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	r.Get("/readyz", s.handleReadyz)
	r.Get("/robots.txt", s.handleRobots)

	// Static — fingerprinted in prod via Caddy headers, but the server still
	// serves the files behind it.
	fileServer := http.StripPrefix("/static/", http.FileServer(http.Dir(filepath.Join(s.cfg.WebDir, "static"))))
	r.Handle("/static/*", fileServer)
	r.Handle("/manifest.json", http.FileServer(http.Dir(filepath.Join(s.cfg.WebDir, "static"))))
	r.Handle("/service-worker.js", http.FileServer(http.Dir(filepath.Join(s.cfg.WebDir, "static"))))

	// Player routes — all behind device-cookie session middleware.
	r.Group(func(r chi.Router) {
		r.Use(users.Middleware(s.cfg.DB, s.cfg.SessionKey, s.cfg.SecureCookie))
		r.Use(users.CSRFMiddleware(s.cfg.SecureCookie))

		r.Get("/", s.handlePlayLanding)
		r.Get("/play/{n}", s.handlePlaySpecific)
		r.Get("/play/{n}/result", s.handlePlayResult)

		r.Get("/me", s.handleMe)
		r.Get("/leaderboard/forgers", s.handleLeaderboardForgers)
		r.Get("/leaderboard/spotters", s.handleLeaderboardSpotters)

		// Public per-decoy share page — viewable without an account; the
		// device-cookie middleware still runs (it's harmless) so visitors
		// who land here from a share link don't need a separate auth path
		// before playing today.
		r.Get("/d/{short}", s.handleDecoyShare)

		// Public per-play result share. The /og.png variant renders the
		// 1200x630 social card via internal/share.RenderResultOG so chat
		// clients unfurl the link into a card, not a text bubble.
		r.Get("/r/{short}", s.handleResultShare)
		r.Get("/r/{short}/og.png", s.handleResultShareOG)

		r.Route("/api", func(r chi.Router) {
			r.Post("/play/start", s.handleAPIPlayStart)
			r.Post("/play/round/{n}/hint", s.handleAPIHint)
			r.Post("/play/round/{n}/guess", s.handleAPIGuess)
			r.Post("/decoy/submit", s.handleAPIDecoySubmit)
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
	_, _ = w.Write([]byte("User-agent: *\nDisallow: /play/\nDisallow: /api/\n"))
}

// loadTemplates reads layouts/ as the shared base and produces one template
// set per file under pages/. Each page is a clone of the layout chain plus
// the page's own defines, so a `{{ define "content" }}` in pages/play.html
// only affects play renders — not result.html or no_puzzle.html.
func loadTemplates(webDir string) (map[string]*template.Template, error) {
	root := filepath.Join(webDir, "templates")
	funcs := template.FuncMap{
		"pad3": func(n int32) string { return fmt.Sprintf("%03d", n) },
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
