package httpx

import (
	"net/http"

	"github.com/christianreimer/bot-bot-goose/internal/collective"
	"github.com/christianreimer/bot-bot-goose/internal/share"
	"github.com/go-chi/chi/v5"
)

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
	if stat, ok, err := s.cfg.DB.LatestCollectiveStat(r.Context(), collective.MinPlaysFloor); err == nil && ok {
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

// handleResultShareOG renders the 1200x630 PNG behind <meta og:image>.
// Cached aggressively because once a play is complete its image never
// changes — same input → same bytes.
func (s *Server) handleResultShareOG(w http.ResponseWriter, r *http.Request) {
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

	// Same collective number we paint on the page body — keeps text and
	// image in lockstep. -1 omits the OG line entirely.
	humansYesterdayPct := -1
	if stat, ok, err := s.cfg.DB.LatestCollectiveStat(r.Context(), collective.MinPlaysFloor); err == nil && ok {
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
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("X-Robots-Tag", "noindex")
	_, _ = w.Write(png)
}
