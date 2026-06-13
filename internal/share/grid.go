// Package share renders the spoiler-free, mode-aware share artifact.
//
// The share grid is the only free acquisition channel (design doc §2,
// §6). It must:
//
//   - render perfectly when pasted into iMessage, WhatsApp, X, Discord
//     and Instagram (emoji-grid compatibility is non-negotiable),
//   - be spoiler-free: never reveal which answer was the target, and
//   - lead with the identity hook, not the mechanics.
package share

import (
	"fmt"
	"strings"

	"github.com/christianreimer/bot-bot-goose/internal/game"
)

const (
	// Mode-icon prefixes used on share cards. They differentiate the two
	// modes visually without spoiling which answer was the target.
	IconFindBot   = "🪿"
	IconFindHuman = "🧍"
)

// EmojiFor maps an outcome to its share-grid cell.
func EmojiFor(o game.Outcome) string {
	switch o {
	case game.Green:
		return "🟩"
	case game.Yellow:
		return "🟨"
	case game.Red:
		return "🟥"
	}
	return "⬜"
}

// Grid is the emoji-only line. Same vocabulary in both modes so grids
// stay visually comparable across the inversion.
func Grid(outcomes []game.Outcome) string {
	var sb strings.Builder
	for _, o := range outcomes {
		sb.WriteString(EmojiFor(o))
	}
	return sb.String()
}

// Card is the full multi-line share string. Mode-aware copy ("Daily Goose"
// vs "Daily Human") but identical emoji vocabulary.
func Card(puzzleNumber int32, outcomes []game.Outcome, mode game.Mode, streak int, baseURL string) string {
	pct := game.ScorePct(outcomes)
	icon := IconFindBot
	title := "Daily Goose"
	statLabel := "Bot-Dar"
	if mode == game.FindTheHuman {
		icon = IconFindHuman
		title = "Daily Human"
		statLabel = "Human-Dar"
	}
	return fmt.Sprintf("%s %s #%03d\n%s\n%s %d%%  ·  🔥%d\n%s",
		icon, title, puzzleNumber, Grid(outcomes), statLabel, pct, streak, trimScheme(baseURL))
}

// DecoyReport is the per-decoy share artifact from design doc §4 — the
// SECOND viral surface alongside the play grid. It carries an "I'm a bot
// (compliment)" identity flex when the decoy is fooling people, and a
// warm "too human" reframe when it's not.
type DecoyReport struct {
	Text         string  // the decoy answer itself, in quotes
	RawPct       int     // 0..100; raw fool rate
	Impressions  int64
	Fooled       int64
	BeyondChance int     // forger points, design §4
	Eligible     bool    // crossed the leaderboard impressions gate
	Rank         int     // 0 if not eligible
	OfTotal      int     // total eligible forgers
	Tier         string  // current tier label
	Status       string  // 'pending' | 'approved' | 'rejected' | 'retired'
	ShareURL     string  // standalone /d/<short> URL; falls back to baseURL host if empty
}

// DecoyReportCard renders the share artifact text. Variant is picked off the
// report's state so the caller doesn't have to branch.
func DecoyReportCard(rep DecoyReport, baseURL string) string {
	host := trimScheme(baseURL)
	if rep.ShareURL != "" {
		host = trimScheme(rep.ShareURL)
	}
	switch {
	case rep.Status == "pending":
		// Anticipation copy from §4 payoff loop beat 1.
		return fmt.Sprintf("🪿 Bot Bot Goose · Decoy Report\n%q\n\n🪶 Planted. Goes live after review. We'll tell you how many you fool.\n%s",
			rep.Text, host)
	case rep.Status == "rejected", rep.Status == "retired":
		return fmt.Sprintf("🪿 Bot Bot Goose · Decoy Report\n%q\n\nretired\n%s",
			rep.Text, host)
	case rep.Impressions == 0:
		return fmt.Sprintf("🪿 Bot Bot Goose · Decoy Report\n%q\n\n🪶 Live. Waiting for its first impressions.\n%s",
			rep.Text, host)
	case rep.RawPct < 15:
		// §4 flop copy: warm reframe, not punishing.
		return fmt.Sprintf("🪿 Bot Bot Goose · Decoy Report\n%q\n\n🧑 Too human. Only %d%% thought I was a bot.\nOut of %d impressions, you're unmistakably one of us.\n%s",
			rep.Text, rep.RawPct, rep.Impressions, host)
	default:
		// §4 payoff card.
		main := fmt.Sprintf("🪿 Bot Bot Goose · Decoy Report\n%q\n\n🤖 %d%% of humans think I'm a bot · %d fooled",
			rep.Text, rep.RawPct, rep.Fooled)
		if rep.BeyondChance > 0 {
			main += fmt.Sprintf(" · +%d beyond chance", rep.BeyondChance)
		}
		if rep.Eligible && rep.Rank > 0 {
			main += fmt.Sprintf("\nRank #%d of %d forgers · %s", rep.Rank, rep.OfTotal, rep.Tier)
		} else if rep.Tier != "" {
			main += fmt.Sprintf("\n%s · still building rep", rep.Tier)
		}
		return main + "\n" + host
	}
}

func trimScheme(u string) string {
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	return strings.TrimSuffix(u, "/")
}
