// Package share renders the spoiler-free share artifact.
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

// IconFindBot is the goose mark that prefixes every share card. The game is
// single-mode: three humans, one bot, tap the bot.
const IconFindBot = "🪿"

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

// Grid is the emoji-only line.
func Grid(outcomes []game.Outcome) string {
	var sb strings.Builder
	for _, o := range outcomes {
		sb.WriteString(EmojiFor(o))
	}
	return sb.String()
}

// Card is the full multi-line share string. humansYesterdayPct, when >= 0,
// inserts a collective rally line ("Humans yesterday: X%") between the
// scoreLine and the URL — see internal/collective for the source of the
// number. Pass -1 to omit the line entirely (no qualifying prior puzzle, or
// the caller doesn't need it).
func Card(puzzleNumber int32, outcomes []game.Outcome, streak int, humansYesterdayPct int, baseURL string) string {
	pct := game.ScorePct(outcomes)
	// 0/3 sweep is not a success. Prefix the score line so the share text
	// states the outcome honestly rather than reading like a bare stat.
	scoreLine := fmt.Sprintf("Bot-Dar %d%%  ·  🔥%d", pct, streak)
	if pct == 0 {
		scoreLine = fmt.Sprintf("Goose got away  ·  Bot-Dar 0%%  ·  🔥%d", streak)
	}
	// Full URL (with scheme) so iMessage/WhatsApp/etc. auto-detect it and
	// can render a rich preview from the og:image at /r/<short>/og.png.
	url := withScheme(baseURL)
	if humansYesterdayPct >= 0 {
		return fmt.Sprintf("%s Daily Goose #%03d\n%s\n%s\nHumans yesterday: %d%%\n%s",
			IconFindBot, puzzleNumber, Grid(outcomes), scoreLine, humansYesterdayPct, url)
	}
	return fmt.Sprintf("%s Daily Goose #%03d\n%s\n%s\n%s",
		IconFindBot, puzzleNumber, Grid(outcomes), scoreLine, url)
}

// DecoyReport is the per-decoy share artifact — the second viral surface
// alongside the play grid. The flex is now "most human": when other players
// pick your decoy after the reveal, you read more human than the actual
// humans did. Fool-rate stays as a flavor line.
type DecoyReport struct {
	Text string // the decoy answer itself, in quotes
	// Realest (primary).
	RealestRawPct      int   // 0..100; raw most-human vote rate
	RealestImpressions int64 // times shown in a votable round
	RealestVotes       int64 // votes earned
	RealestBeyond      int   // votes above baseline (1/3)
	// Fool (flavor, displayed only when non-trivial).
	FoolImpressions int64
	FoolPicked      int64
	FoolRawPct      int
	// Standings.
	Eligible bool   // crossed the leaderboard realest-impressions gate
	Rank     int    // 0 if not eligible
	OfTotal  int    // total eligible forgers
	Tier     string // current tier label (Decoy → The Realest)
	Status   string // 'pending' | 'approved' | 'rejected' | 'retired'
	ShareURL string // standalone /d/<short> URL; falls back to baseURL host if empty
}

// DecoyReportCard renders the share artifact text. Variant is picked off the
// report's state so the caller doesn't have to branch.
func DecoyReportCard(rep DecoyReport, baseURL string) string {
	// Use the full URL (scheme included) on its own line. iMessage,
	// WhatsApp, Slack, and most other clients only generate a rich
	// preview card when the URL is autodetectable, which requires the
	// `https://` prefix. Earlier versions trimmed the scheme for visual
	// neatness; that broke iMessage unfurls.
	url := withScheme(baseURL)
	if rep.ShareURL != "" {
		url = withScheme(rep.ShareURL)
	}
	switch {
	case rep.Status == "pending":
		return fmt.Sprintf("🪿 Bot Bot Goose · Line Report\n%q\n\n🪶 Planted. Goes live after review. We'll tell you how human you read.\n%s",
			rep.Text, url)
	case rep.Status == "rejected", rep.Status == "retired":
		return fmt.Sprintf("🪿 Bot Bot Goose · Line Report\n%q\n\nretired\n%s",
			rep.Text, url)
	case rep.RealestImpressions == 0:
		return fmt.Sprintf("🪿 Bot Bot Goose · Line Report\n%q\n\n🪶 Live. Waiting for its first votes.\n%s",
			rep.Text, url)
	case rep.RealestRawPct < 20:
		// Below chance (33%) by a wide margin. Warm reframe, not punishing.
		return fmt.Sprintf("🪿 Bot Bot Goose · Line Report\n%q\n\n🧑 Reads quiet. Only %d%% picked it as the most human.\nOut of %d rounds, the room kept looking elsewhere.\n%s",
			rep.Text, rep.RealestRawPct, rep.RealestImpressions, url)
	default:
		// Payoff card.
		main := fmt.Sprintf("🪿 Bot Bot Goose · Line Report\n%q\n\n🪶 %d%% picked it as the most human · %d votes",
			rep.Text, rep.RealestRawPct, rep.RealestVotes)
		if rep.RealestBeyond > 0 {
			main += fmt.Sprintf(" · +%d beyond chance", rep.RealestBeyond)
		}
		if rep.FoolImpressions > 0 && rep.FoolRawPct > 0 {
			main += fmt.Sprintf("\n🤖 Also fooled %d%% (%d of %d called it a bot)",
				rep.FoolRawPct, rep.FoolPicked, rep.FoolImpressions)
		}
		if rep.Eligible && rep.Rank > 0 {
			main += fmt.Sprintf("\nRank #%d of %d originals · %s", rep.Rank, rep.OfTotal, rep.Tier)
		} else if rep.Tier != "" {
			main += fmt.Sprintf("\n%s · still building rep", rep.Tier)
		}
		return main + "\n" + url
	}
}

// withScheme ensures the URL carries an explicit `https://` (or whatever
// scheme it came with) so messaging-app URL autodetection works. If the
// caller handed us a bare host like "botbotgoose.fun/d/abc", we prepend
// https:// since that's the only scheme we serve.
func withScheme(u string) string {
	u = strings.TrimSuffix(u, "/")
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	return "https://" + u
}
