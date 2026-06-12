package httpx

import (
	"net/http"

	"github.com/christianreimer/bot-bot-goose/internal/game"
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
		http.NotFound(w, r)
		return
	}
	pp, err := s.cfg.DB.PlayByShortID(r.Context(), short)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	baseURL := s.requestBaseURL(r)
	shortID := share.PlayShortID(pp.PlayID)
	pageURL := baseURL + "/r/" + shortID
	imgURL := baseURL + "/r/" + shortID + "/og.png"

	statLabel := "Bot-Dar"
	titleVerb := "spotted the goose"
	if pp.Mode == game.FindTheHuman {
		statLabel = "Human-Dar"
		titleVerb = "spotted the human"
	}

	// Server-rendered card line for the page body (the shared text bubble
	// stays minimal — see result.js).
	cardLine := share.Grid(pp.Outcomes)

	s.renderHTML(w, http.StatusOK, "pages/result_share.html", map[string]any{
		"PuzzleNumber": pp.PuzzleNumber,
		"Mode":         string(pp.Mode),
		"Grid":         cardLine,
		"ScorePct":     pp.ScorePct,
		"StatLabel":    statLabel,
		"Streak":       pp.Streak,
		"Author":       pp.AuthorHandle,
		"TitleVerb":    titleVerb,
		"ShareURL":     pageURL,
		"OGImageURL":   imgURL,
		"BaseURL":      baseURL,
	})
}

// handleResultShareOG renders the 1200x630 PNG behind <meta og:image>.
// Cached aggressively because once a play is complete its image never
// changes — same input → same bytes.
func (s *Server) handleResultShareOG(w http.ResponseWriter, r *http.Request) {
	rawShort := chi.URLParam(r, "short")
	short, err := share.ParseShortID(rawShort)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pp, err := s.cfg.DB.PlayByShortID(r.Context(), short)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	png, err := share.RenderResultOG(share.ResultOG{
		PuzzleNumber: pp.PuzzleNumber,
		Outcomes:     pp.Outcomes,
		Mode:         pp.Mode,
		Streak:       pp.Streak,
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
