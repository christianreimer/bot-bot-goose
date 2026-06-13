// Package bbg is the top-level module of Bot Bot Goose — a daily web game
// where players spot the AI hiding among real human answers, served as a
// server-rendered Go web app with a Postgres-backed integrity backbone.
//
// The interesting subpackages:
//
//   - [github.com/christianreimer/bot-bot-goose/internal/play] — the
//     server-authoritative slot permutation + HMAC token machinery that
//     keeps answer labels off the client until a guess is committed.
//   - [github.com/christianreimer/bot-bot-goose/internal/game] — pure
//     scoring + outcome resolution + shrinkage constants for the
//     leaderboard's adjusted-rate math.
//   - [github.com/christianreimer/bot-bot-goose/internal/share] — the
//     spoiler-free emoji grid, share card text, OG PNG renderer, and the
//     short-id derivation used by /r/<short> and /d/<short>.
//   - [github.com/christianreimer/bot-bot-goose/internal/leaderboard] —
//     Adjusted Most-Human Rate, tier math, and the nightly rollup from
//     decoy_daily_stats into forger_rankings (the Originals leaderboard).
//   - [github.com/christianreimer/bot-bot-goose/internal/collective] —
//     nightly freeze of "yesterday, humans caught the goose X%" into
//     daily_collective_stats, surfaced on the result page + share card.
//
// See the project README for the architecture overview, build order, and
// quickstart.
package bbg
