package httpx

import (
	"math"
	"net/http"

	"github.com/christianreimer/bot-bot-goose/internal/game"
	"github.com/christianreimer/bot-bot-goose/internal/leaderboard"
	"github.com/christianreimer/bot-bot-goose/internal/share"
	"github.com/go-chi/chi/v5"
)

// handleDecoyShare renders the public per-decoy page that lives behind every
// share link. Design doc §4 calls this "the second viral artifact" — the
// "could you have caught this?" hook is what makes the link land.
//
// The page is PUBLIC: no session required, no spoiler problem (it shows the
// human-written decoy text, which is the author's own contribution, not the
// puzzle's bot). A future feature can opt authors out per-decoy if the
// privacy default needs to flip.
func (s *Server) handleDecoyShare(w http.ResponseWriter, r *http.Request) {
	rawShort := chi.URLParam(r, "short")
	short, err := share.ParseShortID(rawShort)
	if err != nil {
		s.renderNotFound(w, r)
		return
	}
	pd, err := s.cfg.DB.DecoyByShortID(r.Context(), short)
	if err != nil {
		s.renderNotFound(w, r)
		return
	}

	// Compute the same stats /me shows, so the public page and the user's
	// dashboard tell the same story.
	totalImp := pd.BotImp + pd.HumanImp
	totalPicked := pd.BotPicked + pd.HumanPicked
	mode := game.FindTheBot
	if pd.HumanImp > pd.BotImp {
		mode = game.FindTheHuman
	}
	rawPct := 0
	if totalImp > 0 {
		rawPct = int(math.Round(100 * float64(totalPicked) / float64(totalImp)))
	}
	baselinePct := int(math.Round(100 * game.BaselineFor(mode)))
	beyond := game.ForgerPoints(int(totalPicked), int(totalImp), mode)

	// Pull the author's forger ranking for the rank/tier line.
	var rank int
	var tier string
	var ofTotal int
	gate := int64(leaderboard.MinImpressionsEligible)
	if pd.AuthorUserID != nil {
		if rr, err := s.cfg.DB.ForgerRankingFor(r.Context(), *pd.AuthorUserID, gate); err == nil {
			rank = rr.Rank
			tier = rr.Tier
		}
		ofTotal, _ = s.cfg.DB.EligibleForgerCount(r.Context(), gate)
	}

	baseURL := s.requestBaseURL(r)
	shortID := share.DecoyShortID(pd.ID)
	pageURL := baseURL + "/d/" + shortID

	// Pre-build the share card so the page's "Share this" button hands the
	// exact same text as /me's "Share report ▸".
	card := share.DecoyReportCard(share.DecoyReport{
		Text:         pd.Text,
		RawPct:       rawPct,
		Impressions:  totalImp,
		Fooled:       totalPicked,
		BeyondChance: beyond,
		Eligible:     rank > 0,
		Rank:         rank,
		OfTotal:      ofTotal,
		Tier:         tier,
		Status:       pd.Status,
		ShareURL:     pageURL,
	}, baseURL)

	authorLabel := pd.AuthorHandle
	if authorLabel == "" {
		authorLabel = "anonymous"
	}

	s.renderHTML(w, http.StatusOK, "pages/decoy_share.html", map[string]any{
		"PuzzleNumber": int32(0),
		"PromptText":   pd.PromptText,
		"DecoyText":    pd.Text,
		"Author":       authorLabel,
		"Status":       pd.Status,
		"TotalImp":     totalImp,
		"TotalPicked":  totalPicked,
		"RawPct":       rawPct,
		"BaselinePct":  baselinePct,
		"BeyondChance": beyond,
		"Mode":         string(mode),
		"Tier":         tier,
		"Rank":         rank,
		"OfTotal":      ofTotal,
		"ShareCard":    card,
		"ShareURL":     pageURL,
		"BaseURL":      baseURL,
	})
}

