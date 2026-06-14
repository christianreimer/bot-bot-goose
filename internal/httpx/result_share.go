package httpx

import (
	"net/http"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/collective"
	"github.com/christianreimer/bot-bot-goose/internal/share"
	"github.com/go-chi/chi/v5"
)

// resultOGCacheTTL is how long ValKey holds a rendered result OG PNG. The
// underlying plays.og_png column outlives it; the cache exists to keep
// Postgres from getting hit on every CDN miss during a burst. See plan §2.4.
const resultOGCacheTTL = 7 * 24 * time.Hour

func resultOGCacheKey(short string) string { return "og:r:" + short }

// handleResultShare renders the public per-play result page that lives behind
// every shared result URL. Spoiler-free: it shows the grid + score but NEVER
// the prompts or answers (those are tomorrow's content for anyone who
// hasn't played yet).
func (s *Server) handleResultShare(w http.ResponseWriter, r *http.Request) {
	rawShort := chi.URLParam(r, "short")
	short, err := share.ParseShortID(rawShort)
	if err != nil {
		s.renderNotFound(w, r)
		return
	}
	pp, err := s.cfg.DB.PlayByShortID(r.Context(), short)
	if err != nil {
		s.renderNotFound(w, r)
		return
	}

	baseURL := s.requestBaseURL(r)
	shortID := share.PlayShortID(pp.PlayID)
	pageURL := baseURL + "/r/" + shortID
	imgURL := baseURL + "/r/" + shortID + "/og.png"

	// Sweep (0/3) is not a success — don't frame it as one. Mirrors the
	// strike-through "caught → got away" treatment on the player-facing
	// result page. The kicker on the share page and the og:title both
	// flip to the honest verb.
	titleVerb := "spotted the goose"
	if pp.ScorePct == 0 {
		titleVerb = "let the goose get away"
	}

	// Server-rendered card line for the page body (the shared text bubble
	// stays minimal — see result.js).
	cardLine := share.Grid(pp.Outcomes)

	// Yesterday's collective catch rate. Same source as the play result page.
	hasCollective := false
	humansYesterdayPct := -1
	if stat, ok, err := collective.Latest(r.Context(), s.cfg.DB, s.cfg.Cache); err == nil && ok {
		humansYesterdayPct = stat.CatchPct
		hasCollective = true
	} else if err != nil {
		s.cfg.Logger.Warn("collective stat read", "err", err)
	}

	s.renderHTML(w, http.StatusOK, "pages/result_share.html", map[string]any{
		"PuzzleNumber":       pp.PuzzleNumber,
		"Grid":               cardLine,
		"ScorePct":           pp.ScorePct,
		"StatLabel":          "Bot-Dar",
		"Streak":             pp.Streak,
		"Author":             pp.AuthorHandle,
		"TitleVerb":          titleVerb,
		"ShareURL":           pageURL,
		"OGImageURL":         imgURL,
		"BaseURL":            baseURL,
		"HasHumansYesterday": hasCollective,
		"HumansYesterdayPct": humansYesterdayPct,
	})
}

// handleResultShareOG serves the 1200x630 PNG behind <meta og:image>. Cached
// aggressively because once a play is complete its image never changes —
// same input → same bytes.
//
// Three-tier lookup, per plans/launch-capacity §2.4:
//
//	1. ValKey og:r:<short>      ~1ms — protects Postgres on the CDN-miss path
//	2. plays.og_png column      ~5–10ms — populated by the post-Complete
//	                            background render; survives a Valkey restart
//	3. share.RenderResultOG     ~80ms — write back to both layers so the
//	                            next request hits tier 1
//
// Persistence at every tier is best-effort; a write failure just means
// the renderer runs again next time.
func (s *Server) handleResultShareOG(w http.ResponseWriter, r *http.Request) {
	rawShort := chi.URLParam(r, "short")
	short, err := share.ParseShortID(rawShort)
	if err != nil {
		s.renderNotFound(w, r)
		return
	}
	ctx := r.Context()
	key := resultOGCacheKey(short)

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("X-Robots-Tag", "noindex")

	// Tier 1: ValKey.
	if b, ok := s.cfg.Cache.Get(ctx, "og_result", key); ok {
		_, _ = w.Write(b)
		return
	}

	// Tier 2: Postgres plays.og_png.
	playID, cached, err := s.cfg.DB.PlayOGBundleByShortID(ctx, short)
	if err != nil {
		s.renderNotFound(w, r)
		return
	}
	if cached != nil {
		s.cfg.Cache.Set(ctx, "og_result", key, cached, resultOGCacheTTL)
		_, _ = w.Write(cached)
		return
	}

	// Tier 3: render.
	pp, err := s.cfg.DB.PlayByShortID(ctx, short)
	if err != nil {
		s.renderNotFound(w, r)
		return
	}

	// Same collective number we paint on the page body — keeps text and
	// image in lockstep. -1 omits the OG line entirely.
	humansYesterdayPct := -1
	if stat, ok, err := collective.Latest(ctx, s.cfg.DB, s.cfg.Cache); err == nil && ok {
		humansYesterdayPct = stat.CatchPct
	} else if err != nil {
		s.cfg.Logger.Warn("collective stat read (og)", "err", err)
	}

	png, err := share.RenderResultOG(share.ResultOG{
		PuzzleNumber:       pp.PuzzleNumber,
		Outcomes:           pp.Outcomes,
		Streak:             pp.Streak,
		HumansYesterdayPct: humansYesterdayPct,
	})
	if err != nil {
		s.cfg.Logger.Error("render og", "err", err)
		http.Error(w, "render", http.StatusInternalServerError)
		return
	}
	if err := s.cfg.DB.WritePlayOGPNG(ctx, playID, png); err != nil {
		s.cfg.Logger.Warn("persist og_png", "play", playID, "err", err)
	}
	s.cfg.Cache.Set(ctx, "og_result", key, png, resultOGCacheTTL)
	_, _ = w.Write(png)
}
