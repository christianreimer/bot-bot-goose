package httpx

import (
	"context"
	"fmt"
	"math"
	"net/http"

	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/christianreimer/bot-bot-goose/internal/game"
	"github.com/christianreimer/bot-bot-goose/internal/leaderboard"
	"github.com/christianreimer/bot-bot-goose/internal/share"
	"github.com/christianreimer/bot-bot-goose/internal/users"
)

// meDecoyView is a per-decoy row for the /me template. It carries both the
// raw rate (with the right baseline drawn on the chart) and the adjusted
// rate so the user sees the friendly number AND understands the ranking.
type meDecoyView struct {
	PromptText   string
	Text         string
	Status       string
	TotalImp     int64
	TotalPicked  int64
	RawPct       int    // raw fool rate as a percentage
	AdjustedPct  int    // adjusted, for tier math
	BaselinePct  int    // mode-weighted baseline shown alongside
	BeyondChance int    // forger points: max(0, picked - baseline*imp)
	ModeMix      string // "mostly find_the_bot" etc, for the right copy
	ShareCard    string // pre-built decoy-report share text
}

// mePayoff is the §4 "312 people just accused you of being a bot" block.
type mePayoff struct {
	Visible          bool   // hide entirely until any impressions exist
	Eligible         bool   // false = under the leaderboard impressions gate
	TotalImpressions int64
	TotalPicked      int64
	AdjustedPct      int
	BeyondChance     int
	Tier             string
	Rank             int
	OfTotal          int
	NextTier         string
	FoolsToNext      int
	GateMin          int64
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := users.FromContext(r.Context())
	ctx := r.Context()
	baseURL := s.requestBaseURL(r)

	decoys, err := s.cfg.DB.UserDecoys(ctx, u.ID)
	if err != nil {
		s.cfg.Logger.Error("user decoys", "err", err)
		http.Error(w, "db", http.StatusInternalServerError)
		return
	}
	streak, _ := s.cfg.DB.StreakFor(ctx, u.ID)

	// Pull the user's payoff once; both the per-decoy share card and the
	// big banner share its rank/tier so the totals tie out.
	payoff := s.payoffFor(ctx, u)

	views := make([]meDecoyView, 0, len(decoys))
	for _, d := range decoys {
		shortID := share.DecoyShortID(d.ID)
		shareURL := baseURL + "/d/" + shortID
		views = append(views, decoyViewWithShare(d, payoff, baseURL, shareURL))
	}

	signedIn := u.Email != nil && *u.Email != ""
	var emailDisplay string
	if signedIn {
		emailDisplay = *u.Email
	}
	handleDisplay := ""
	if u.Handle != nil {
		handleDisplay = *u.Handle
	}

	s.renderHTML(w, http.StatusOK, "pages/me.html", map[string]any{
		"PuzzleNumber":     int32(0), // header padding cosmetic
		"Streak":           streak,
		"Decoys":           views,
		"Payoff":           payoff,
		"BaseURL":          baseURL,
		"SignedIn":         signedIn,
		"Email":            emailDisplay,
		"Handle":           handleDisplay,
		"DisplayAnonymous": u.DisplayAnonymous,
		// `?signed_in=1` after a successful magic-link consume — let the
		// page show a one-time toast.
		"JustSignedIn": r.URL.Query().Get("signed_in") == "1",
	})
}

func decoyViewWithShare(d db.UserDecoy, payoff mePayoff, baseURL, shareURL string) meDecoyView {
	totalImp := d.BotImp + d.HumanImp
	totalPicked := d.BotPicked + d.HumanPicked

	// Per-decoy "primary mode" — pick the baseline the user expects on
	// their chart. We don't try to be clever about mixed-mode decoys; the
	// raw % is what's shown, the line is just for context.
	mode := game.FindTheBot
	mixCopy := "mostly Find the Bot"
	if d.HumanImp > d.BotImp {
		mode = game.FindTheHuman
		mixCopy = "mostly Find the Human"
	}
	if d.HumanImp > 0 && d.BotImp > 0 {
		mixCopy = "mixed modes"
	}
	if totalImp == 0 {
		mixCopy = "awaiting first impressions"
	}

	rawPct := 0
	if totalImp > 0 {
		rawPct = int(math.Round(100 * float64(totalPicked) / float64(totalImp)))
	}
	adj := game.AdjustedFoolRate(int(totalPicked), int(totalImp), mode)
	beyond := game.ForgerPoints(int(totalPicked), int(totalImp), mode)
	baselinePct := int(math.Round(100 * game.BaselineFor(mode)))

	report := share.DecoyReport{
		Text:         d.Text,
		RawPct:       rawPct,
		Impressions:  totalImp,
		Fooled:       totalPicked,
		BeyondChance: beyond,
		Eligible:     payoff.Eligible,
		Rank:         payoff.Rank,
		OfTotal:      payoff.OfTotal,
		Tier:         payoff.Tier,
		Status:       d.Status,
		ShareURL:     shareURL,
	}
	card := share.DecoyReportCard(report, baseURL)

	return meDecoyView{
		PromptText:   d.PromptText,
		Text:         d.Text,
		Status:       d.Status,
		TotalImp:     totalImp,
		TotalPicked:  totalPicked,
		RawPct:       rawPct,
		AdjustedPct:  int(math.Round(100 * adj)),
		BaselinePct:  baselinePct,
		BeyondChance: beyond,
		ModeMix:      mixCopy,
		ShareCard:    card,
	}
}

func (s *Server) payoffFor(ctx context.Context, u *db.User) mePayoff {
	gate := int64(leaderboard.MinImpressionsEligible)
	rank, err := s.cfg.DB.ForgerRankingFor(ctx, u.ID, gate)
	if err != nil {
		// No forger row yet (no decoys with any impressions). Stay hidden.
		return mePayoff{GateMin: gate}
	}
	total, _ := s.cfg.DB.EligibleForgerCount(ctx, gate)

	d, _ := s.cfg.DB.UserDecoys(ctx, u.ID)
	var botImp, botPicked, humanImp, humanPicked int64
	for _, x := range d {
		botImp += x.BotImp
		botPicked += x.BotPicked
		humanImp += x.HumanImp
		humanPicked += x.HumanPicked
	}
	foolsToNext, nextTier := leaderboard.FoolsToNextTier(botPicked, botImp, humanPicked, humanImp)

	beyond := int(float64(botPicked+humanPicked) - leaderboard.WeightedBaseline(botImp, humanImp)*float64(botImp+humanImp) + 0.5)
	if beyond < 0 {
		beyond = 0
	}

	return mePayoff{
		Visible:          true,
		Eligible:         rank.Rank > 0,
		TotalImpressions: rank.TotalImpressions,
		TotalPicked:      rank.TotalPickedAsBot,
		AdjustedPct:      int(math.Round(100 * rank.AdjustedFoolRate)),
		BeyondChance:     beyond,
		Tier:             rank.Tier,
		Rank:             rank.Rank,
		OfTotal:          total,
		NextTier:         nextTier,
		FoolsToNext:      foolsToNext,
		GateMin:          gate,
	}
}

// ---------------------------------------------------------------------------
// Leaderboards
// ---------------------------------------------------------------------------

func (s *Server) handleLeaderboardForgers(w http.ResponseWriter, r *http.Request) {
	gate := int64(leaderboard.MinImpressionsEligible)
	rows, err := s.cfg.DB.TopForgers(r.Context(), 100, gate)
	if err != nil {
		http.Error(w, "db", http.StatusInternalServerError)
		return
	}
	total, _ := s.cfg.DB.EligibleForgerCount(r.Context(), gate)
	s.renderHTML(w, http.StatusOK, "pages/leaderboard_forgers.html", map[string]any{
		"PuzzleNumber": int32(0),
		"Rows":         rowsForTemplate(rows),
		"Total":        total,
		"GateMin":      gate,
		"BaseURL":      s.cfg.BaseURL,
	})
}

type forgerRowView struct {
	Rank             int
	Handle           string
	AdjustedPct      int
	Tier             string
	TotalImpressions int64
	TotalPickedAsBot int64
}

func rowsForTemplate(rows []db.ForgerLeaderboardRow) []forgerRowView {
	out := make([]forgerRowView, 0, len(rows))
	for _, r := range rows {
		out = append(out, forgerRowView{
			Rank:             r.Rank,
			Handle:           r.Handle,
			AdjustedPct:      int(math.Round(100 * r.AdjustedFoolRate)),
			Tier:             r.Tier,
			TotalImpressions: r.TotalImpressions,
			TotalPickedAsBot: r.TotalPickedAsBot,
		})
	}
	return out
}

func (s *Server) handleLeaderboardSpotters(w http.ResponseWriter, r *http.Request) {
	rows, err := s.cfg.DB.TopSpotters(r.Context(), 100, 3)
	if err != nil {
		http.Error(w, "db", http.StatusInternalServerError)
		return
	}
	views := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		views = append(views, map[string]any{
			"Rank":     r.Rank,
			"Handle":   r.Handle,
			"AvgScore": fmt.Sprintf("%.0f%%", r.AvgScore),
			"Plays":    r.Plays,
		})
	}
	s.renderHTML(w, http.StatusOK, "pages/leaderboard_spotters.html", map[string]any{
		"PuzzleNumber": int32(0),
		"Rows":         views,
		"BaseURL":      s.cfg.BaseURL,
	})
}
