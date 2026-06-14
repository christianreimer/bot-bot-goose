package httpx

import (
	"math"
	"net/http"

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

	// Compute the same stats /me shows so the public page and the user's
	// dashboard tell the same story. Realest = primary, fool = flavor.
	realestImp := pd.RealestImpressions
	realestVotes := pd.RealestVotes
	foolImp := pd.Impressions
	foolPicked := pd.PickedAsBot

	realestRaw := 0
	if realestImp > 0 {
		realestRaw = int(math.Round(100 * float64(realestVotes) / float64(realestImp)))
	}
	foolRaw := 0
	if foolImp > 0 {
		foolRaw = int(math.Round(100 * float64(foolPicked) / float64(foolImp)))
	}
	realestBaselinePct := int(math.Round(100 * leaderboard.RealestBaseline))
	realestBeyond := leaderboard.RealestBeyondChance(realestVotes, realestImp)

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
	ogImageURL := baseURL + "/d/" + shortID + "/og.png"

	// Pre-build the share card so the page's "Share this" button hands the
	// exact same text as /me's "Share report ▸".
	card := share.DecoyReportCard(share.DecoyReport{
		Text:               pd.Text,
		RealestRawPct:      realestRaw,
		RealestImpressions: realestImp,
		RealestVotes:       realestVotes,
		RealestBeyond:      realestBeyond,
		FoolImpressions:    foolImp,
		FoolPicked:         foolPicked,
		FoolRawPct:         foolRaw,
		Eligible:           rank > 0,
		Rank:               rank,
		OfTotal:            ofTotal,
		Tier:               tier,
		Status:             pd.Status,
		ShareURL:           pageURL,
	}, baseURL)

	authorLabel := pd.AuthorHandle
	if authorLabel == "" {
		authorLabel = "anonymous"
	}

	s.renderHTML(w, http.StatusOK, "pages/decoy_share.html", map[string]any{
		"PuzzleNumber":       int32(0),
		"PromptText":         pd.PromptText,
		"DecoyText":          pd.Text,
		"Author":             authorLabel,
		"Status":             pd.Status,
		"RealestImp":         realestImp,
		"RealestVotes":       realestVotes,
		"RealestRawPct":      realestRaw,
		"RealestBaselinePct": realestBaselinePct,
		"RealestBeyond":      realestBeyond,
		"FoolImp":            foolImp,
		"FoolPicked":         foolPicked,
		"FoolRawPct":         foolRaw,
		"Tier":               tier,
		"Rank":               rank,
		"OfTotal":            ofTotal,
		"ShareCard":          card,
		"ShareURL":           pageURL,
		"OGImageURL":         ogImageURL,
		"BaseURL":            baseURL,
	})
}

// handleDecoyShareOG serves the 1200x630 decoy poster behind <meta og:image>.
// The image is generic across decoys (per-decoy stats live in the page's
// og:title), so the bytes are precomputed once via DecoyOGBytes.
func (s *Server) handleDecoyShareOG(w http.ResponseWriter, r *http.Request) {
	png, err := share.DecoyOGBytes()
	if err != nil {
		s.cfg.Logger.Error("render decoy og", "err", err)
		http.Error(w, "render", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("X-Robots-Tag", "noindex")
	_, _ = w.Write(png)
}

