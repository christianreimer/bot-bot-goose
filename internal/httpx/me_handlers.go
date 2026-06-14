package httpx

import (
	"context"
	"fmt"
	"math"
	"net/http"

	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/christianreimer/bot-bot-goose/internal/leaderboard"
	"github.com/christianreimer/bot-bot-goose/internal/share"
	"github.com/christianreimer/bot-bot-goose/internal/users"
)

// meDecoyView is a per-decoy row for the /me template. The realest fields
// drive the primary stat ("X% most human"); fool fields stay as flavor
// ("X picked you as the bot").
type meDecoyView struct {
	PromptText   string
	Text         string
	Status       string
	// Realest (primary) — votes / impressions on the post-reveal vote.
	RealestImp        int64
	RealestVotes      int64
	RealestRawPct     int
	RealestAdjPct     int
	RealestBaselinePct int  // chance-line for the realest vote (33%)
	RealestBeyond     int   // votes earned above chance
	// Fool (flavor) — kept for the "X of N called you a bot" line.
	FoolImp     int64
	FoolPicked  int64
	FoolRawPct  int
	ShareCard   string
}

// mePayoff is the §4 forger payoff block. Switched to realest math: the
// ranked metric is "most human" voting; fool rate stays as display flavor.
type mePayoff struct {
	Visible      bool // hide entirely until any realest impressions exist
	Eligible     bool // false = under the realest impressions gate
	// Realest (the ranked track).
	RealestImpressions int64
	RealestVotes       int64
	RealestAdjPct      int
	RealestBeyond      int
	Tier               string
	Rank               int
	OfTotal            int
	NextTier           string
	VotesToNext        int
	// Fool (flavor).
	FoolImpressions int64
	FoolPicked      int64
	FoolAdjPct      int
	GateMin         int64
}

// standingCard is one row in the "Standings" block at the top of /me.
// Two cards render: spotter + forger. Eligible=true means the user has
// crossed the gate (≥3 completed plays for spotter; ≥100 impressions
// for forger) and the headline stat is meaningful. Eligible=false
// renders the "X to go" prompt instead.
type standingCard struct {
	Kind     string // "spotter" or "forger" — picks the row label + link
	Href     string // /leaderboard/spotters or /leaderboard/originals
	Eligible bool
	Rank     int    // 1-based; 0 when not yet eligible
	OfTotal  int    // population size on the board
	Tier     string // forger only; empty for spotter
	Stat     string // headline number (e.g. "73%" or "42%")
	Note     string // contextual line below
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
	streak := s.streakFor(ctx, u.ID)

	// Pull the user's payoff once; the per-decoy share card uses it so
	// per-decoy totals tie out with the standings.
	payoff := s.payoffFor(ctx, u)
	standings := s.standingsFor(ctx, u, payoff)

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
		"PuzzleNumber": int32(0), // header padding cosmetic
		"Streak":       streak,
		"Decoys":       views,
		"Standings":    standings,
		"BaseURL":      baseURL,
		"SignedIn":     signedIn,
		"Email":        emailDisplay,
		"Handle":       handleDisplay,
		// `?signed_in=1` after a successful magic-link consume — let the
		// page show a one-time toast.
		"JustSignedIn": r.URL.Query().Get("signed_in") == "1",
	})
}

// standingsFor builds the spotter + forger card pair shown at the top
// of /me. Order matters: spotter first (most users have plays before
// they have decoys, so it's the more familiar surface). Each card
// degrades to an "X to go" prompt when the user hasn't crossed the gate.
func (s *Server) standingsFor(ctx context.Context, u *db.User, forger mePayoff) []standingCard {
	const spotterMinPlays = 3
	out := make([]standingCard, 0, 2)

	// Spotter
	spotter := standingCard{Kind: "spotter", Href: "/leaderboard/spotters"}
	if r, err := s.cfg.DB.SpotterRankingFor(ctx, u.ID, spotterMinPlays); err == nil {
		total, _ := s.cfg.DB.EligibleSpotterCount(ctx, spotterMinPlays)
		spotter.Eligible = r.Rank > 0
		spotter.Rank = r.Rank
		spotter.OfTotal = total
		spotter.Stat = fmt.Sprintf("%.0f%%", r.AvgScore)
		if spotter.Eligible {
			spotter.Note = fmt.Sprintf("Avg Bot-Dar across %d plays.", r.Plays)
		} else {
			need := spotterMinPlays - r.Plays
			spotter.Note = fmt.Sprintf("%d more play%s to rank.", need, plural(need))
		}
	} else {
		spotter.Note = "Finish 3 plays to appear here."
	}
	out = append(out, spotter)

	// Forger — ranked on the realest track.
	fc := standingCard{Kind: "forger", Href: "/leaderboard/originals"}
	if forger.Eligible {
		fc.Eligible = true
		fc.Rank = forger.Rank
		fc.OfTotal = forger.OfTotal
		fc.Tier = forger.Tier
		fc.Stat = fmt.Sprintf("%d%%", forger.RealestAdjPct)
		if forger.NextTier != "" {
			fc.Note = fmt.Sprintf("+%d beyond chance. %d vote%s to %s.",
				forger.RealestBeyond, forger.VotesToNext, plural(forger.VotesToNext), forger.NextTier)
		} else {
			fc.Note = fmt.Sprintf("+%d beyond chance. Top of the pond.", forger.RealestBeyond)
		}
	} else if forger.Visible {
		need := forger.GateMin - forger.RealestImpressions
		if need < 1 {
			need = 1
		}
		fc.Note = fmt.Sprintf("%d more vote%s to rank.", need, plural(int(need)))
	} else {
		fc.Note = "Plant a decoy from any result page to start."
	}
	out = append(out, fc)

	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func decoyViewWithShare(d db.UserDecoy, payoff mePayoff, baseURL, shareURL string) meDecoyView {
	realestImp := d.RealestImpressions
	realestVotes := d.RealestVotes
	foolImp := d.Impressions
	foolPicked := d.PickedAsBot

	realestRaw := 0
	if realestImp > 0 {
		realestRaw = int(math.Round(100 * float64(realestVotes) / float64(realestImp)))
	}
	foolRaw := 0
	if foolImp > 0 {
		foolRaw = int(math.Round(100 * float64(foolPicked) / float64(foolImp)))
	}

	realestAdj := leaderboard.AdjustedMostHumanRate(realestVotes, realestImp)
	realestBeyond := leaderboard.RealestBeyondChance(realestVotes, realestImp)

	report := share.DecoyReport{
		Text:               d.Text,
		RealestRawPct:      realestRaw,
		RealestImpressions: realestImp,
		RealestVotes:       realestVotes,
		RealestBeyond:      realestBeyond,
		FoolImpressions:    foolImp,
		FoolPicked:         foolPicked,
		FoolRawPct:         foolRaw,
		Eligible:           payoff.Eligible,
		Rank:               payoff.Rank,
		OfTotal:            payoff.OfTotal,
		Tier:               payoff.Tier,
		Status:             d.Status,
		ShareURL:           shareURL,
	}
	card := share.DecoyReportCard(report, baseURL)

	return meDecoyView{
		PromptText:         d.PromptText,
		Text:               d.Text,
		Status:             d.Status,
		RealestImp:         realestImp,
		RealestVotes:       realestVotes,
		RealestRawPct:      realestRaw,
		RealestAdjPct:      int(math.Round(100 * realestAdj)),
		RealestBaselinePct: int(math.Round(100 * leaderboard.RealestBaseline)),
		RealestBeyond:      realestBeyond,
		FoolImp:            foolImp,
		FoolPicked:         foolPicked,
		FoolRawPct:         foolRaw,
		ShareCard:          card,
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

	votesToNext, nextTier := leaderboard.VotesToNextTier(rank.RealestTotalVotes, rank.RealestTotalImpressions)

	return mePayoff{
		Visible:            true,
		Eligible:           rank.Rank > 0,
		RealestImpressions: rank.RealestTotalImpressions,
		RealestVotes:       rank.RealestTotalVotes,
		RealestAdjPct:      int(math.Round(100 * rank.AdjustedRealestRate)),
		RealestBeyond:      rank.RealestBeyondChance,
		Tier:               rank.Tier,
		Rank:               rank.Rank,
		OfTotal:            total,
		NextTier:           nextTier,
		VotesToNext:        votesToNext,
		FoolImpressions:    rank.TotalImpressions,
		FoolPicked:         rank.TotalPickedAsBot,
		FoolAdjPct:         int(math.Round(100 * rank.AdjustedFoolRate)),
		GateMin:            gate,
	}
}

// ---------------------------------------------------------------------------
// Leaderboards
// ---------------------------------------------------------------------------

func (s *Server) handleLeaderboardOriginals(w http.ResponseWriter, r *http.Request) {
	gate := int64(leaderboard.MinImpressionsEligible)
	rows, err := s.cfg.DB.TopForgers(r.Context(), 100, gate)
	if err != nil {
		http.Error(w, "db", http.StatusInternalServerError)
		return
	}
	total, _ := s.cfg.DB.EligibleForgerCount(r.Context(), gate)
	var teaser map[string]any
	top, err := s.cfg.DB.TopSpotters(r.Context(), 1, 3)
	if err != nil {
		s.cfg.Logger.Warn("originals page: top-spotter teaser query failed", "err", err)
	} else if len(top) > 0 {
		teaser = map[string]any{
			"Handle":   top[0].Handle,
			"AvgScore": fmt.Sprintf("%.0f%%", top[0].AvgScore),
			"Plays":    top[0].Plays,
		}
	}
	s.renderHTML(w, http.StatusOK, "pages/leaderboard_originals.html", map[string]any{
		"PuzzleNumber": int32(0),
		"Rows":         rowsForTemplate(rows),
		"Total":        total,
		"GateMin":      gate,
		"Teaser":       teaser,
		"BaseURL":      s.cfg.BaseURL,
	})
}

type forgerRowView struct {
	Rank             int
	Handle           string
	RealestPct       int    // primary stat — adjusted most-human rate
	Tier             string
	RealestVotes     int64
	RealestImps      int64
	FoolPct          int    // flavor — adjusted fool rate
	FoolPicked       int64
	FoolImps         int64
}

func rowsForTemplate(rows []db.ForgerLeaderboardRow) []forgerRowView {
	out := make([]forgerRowView, 0, len(rows))
	for _, r := range rows {
		out = append(out, forgerRowView{
			Rank:         r.Rank,
			Handle:       r.Handle,
			RealestPct:   int(math.Round(100 * r.AdjustedRealestRate)),
			Tier:         r.Tier,
			RealestVotes: r.RealestTotalVotes,
			RealestImps:  r.RealestTotalImpressions,
			FoolPct:      int(math.Round(100 * r.AdjustedFoolRate)),
			FoolPicked:   r.TotalPickedAsBot,
			FoolImps:     r.TotalImpressions,
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
	gate := int64(leaderboard.MinImpressionsEligible)
	var teaser map[string]any
	top, err := s.cfg.DB.TopForgers(r.Context(), 1, gate)
	if err != nil {
		s.cfg.Logger.Warn("spotters page: top-forger teaser query failed", "err", err)
	} else if len(top) > 0 {
		teaser = map[string]any{
			"Handle":     top[0].Handle,
			"Tier":       top[0].Tier,
			"RealestPct": int(math.Round(100 * top[0].AdjustedRealestRate)),
		}
	}
	s.renderHTML(w, http.StatusOK, "pages/leaderboard_spotters.html", map[string]any{
		"PuzzleNumber": int32(0),
		"Rows":         views,
		"Teaser":       teaser,
		"BaseURL":      s.cfg.BaseURL,
	})
}
