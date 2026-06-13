# Changelog

All notable changes to this project are documented here. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Tracked in [BACKLOG.md](BACKLOG.md). Most prominent: moderation queue UI, magic-link auth, AI-detection on submissions, slot E/P/B bandit in the composer.

### Changed — Single-mode revert (migration 0008)

- Dropped the inverted "Find the Human" mode and its scaffolding across schema, scoring, share renderers, intro modal, and admin tooling. Every puzzle is now 1 bot + 3 decoys, period.
- `puzzle_mode` enum removed; `daily_puzzles.mode` and `puzzle_rounds.target_kind` columns dropped; composite PKs on `decoy_daily_stats` and `archetype_daily_stats` collapsed to `(decoy_id, stat_date)` and `(archetype_id, stat_date)`.
- `cmd/puzzle-build` no longer takes `--mode`; `Makefile`'s `MODE=…` plumbing removed.

### Changed — Realest-vote loop (migration 0009)

- Ranking metric flipped from "fool rate" to **Adjusted Most-Human Rate**. After each round's reveal, the player optionally casts a "felt most human?" vote among the three real lines (the bot card is not votable). The realest track is the new primary ranking; the fool track stays as display-only flavor.
- `play_rounds.realest_decoy_id`, `decoy_daily_stats.realest_{impressions,votes}`, and four new `forger_rankings.realest_*` columns added.
- Tier ladder rewritten: **Quiet → Voice → Standout → Unmistakable → The Realest**.

### Changed — Forger → Originals + Decoy → Quiet rename

- Player-facing concept renamed from "Forger" to "Originals" (the AI makes copies; these are the originals). Visible everywhere: leaderboard title and verdict copy, `/me` standing label (`ORIGINAL`), spotters nav + teaser, result CTA, privacy copy, line-report card "Rank #N of M originals".
- Route renamed: `/leaderboard/forgers` → `/leaderboard/originals` (project pre-launch, no incoming links to preserve).
- Handler `handleLeaderboardForgers` → `handleLeaderboardOriginals`; template `pages/leaderboard_forgers.html` → `pages/leaderboard_originals.html` (git rename, history preserved).
- Entry tier `Decoy` → `Quiet` (ties to the existing "Reads quiet" flop copy). Internal identifiers `forger_rankings`, `ForgerPoints`, `EligibleForgerCount`, `standingCard{Kind: "forger"}` kept (table/contract stability).

### Changed — Copy sweep (catch verb, Plant → Add my line, Decoy → Line)

- Intro modal: "Find it" → "Catch it"; round label: "Find the bot" → "Catch the bot". Canonical verbs: **catch** for the action on the goose, **spot** for the AI-descriptor.
- "Plant ▸" → "Add my line ▸" everywhere (result-page button, harvest overlay button, "Added all" headline, "Already added" h3, leaderboard empty-state, flash toasts). JS identifiers (`overlayPlant`, `plantedCount`, `markPlanted`) and CSS classes (`.planted`) left intact — not user-visible.
- Result CTA reframed: "Every real line you add makes it harder for the next bot to hide. Drop yours into a future round."
- Harvest H1/title/og:title swept: "Help build the goose" → "Stack the deck for the humans" / "Help the humans get ready".
- Sweep verdict reads collective: "Swept. The bots had a good day. We'll get them tomorrow."
- Lines empty-state on `/me` now names the why.

### Added — Yesterday's collective catch rate (migration 0010)

- New table `daily_collective_stats(puzzle_number PK, stat_date, catch_pct, total_plays, computed_at)` frozen nightly by `internal/collective.Rollup`. `catch_pct = round(AVG(score_pct))` over completed plays of the most recent prior-day puzzle, with a 20-play floor.
- Surfaces as `Yesterday, humans caught the goose X%.` under the scorecard on `/` and `/r/<short>`, `Humans yesterday: X%` in the share-card text (`share.Card` gained a `humansYesterdayPct int` param; `-1` omits), and the same line painted in muted ink on the 1200×630 OG image.
- Absent on day 1 / sub-floor days — nothing renders. Same number across players, stable for the day, screenshot-proof.

### Added — Realest leaderboard math + tests

- `internal/leaderboard.AdjustedMostHumanRate` shrinks the raw rate toward the 1/3 chance baseline by `k` impressions (same shrinkage as the legacy `AdjustedFoolRate`, just anchored at chance). `VotesToNextTier` tells the user how many votes from the next tier.
- `RealestBeyondChance` reports votes earned above pure-chance, floored at zero. Used as a tiebreaker / "points" display.
- Sweep (0/3) is now treated as failure on the share card and OG image, not as a bare stat (kicker + text + image flip to "Goose got away").

### Changed — Schema housekeeping

- `display_anonymous BOOL` on `users` (migration 0002) — one-tap anonymous toggle.
- `magic_links.email` dropped (migration 0003); the email travels HMAC-signed in the token.
- `users.handle` uniqueness now case-insensitive via partial functional index on `LOWER(handle)` (migration 0004).
- `pre_launch_submissions`: `email` nullable, `user_id` and `requested_ip` added (migration 0005); `rejected_at` added for soft-reject without losing the original text (migration 0006).
- Anonymous users get an auto-assigned `AnonymousGoose<n>` handle from a dedicated sequence (migration 0007).

## [0.1.0] — Initial public release

> Project was never published; the 0.1.0 entries below describe a snapshot from before the changes in the Unreleased section. Kept verbatim as a historical record. **Do not rely on this section as a current-state description** — the Unreleased section above is canonical.

### Added — Play loop and integrity backbone

- Server-authoritative daily puzzle. Answer labels never leave the server until a guess is committed.
- Per-play `slot_permutation`, generated with `crypto/rand` — defeats data-mining the day's bot across players.
- HMAC-signed play tokens binding `(play_id, round_index, perm_hash, issued_at)`, with cross-play, out-of-order, and expired-token rejection.
- Suspicious-fast-guess flagging (no hard reject, per the plan's anti-cheat rules).
- Anonymous device-cookie sessions, CSRF double-submit middleware, `HttpOnly; Secure; SameSite=Lax` cookie attributes.
- HEAD-to-GET middleware so link-preview crawlers that probe via HEAD don't get 405s.

### Added — Two game modes (REVERTED in Unreleased)

- `find_the_bot` (1 bot + 3 decoys) and `find_the_human` (3 bots from distinct archetypes + 1 decoy).
- Composer's `pickMode` rotates with anti-streak limits.
- Unified Adjusted Fool Rate that shrinks toward 0.25 (find_the_bot) or 0.75 (find_the_human), so a contributor's forger ranking is honest across mixed exposure.

### Added — You vs the Room (subsequently renamed; see Unreleased)

- `POST /api/decoy/submit` writes player-authored decoys as `pending`.
- `/me` shows per-decoy stats (raw fool rate vs. baseline, beyond-chance forger points) and the §4 payoff copy ("X people thought you were a bot · Rank #Y of Z forgers").
- `/leaderboard/forgers` and `/leaderboard/spotters` with the 50-impression eligibility gate.
- Per-decoy share artifact at `/d/<short>` ("could you have caught this?") with author attribution.
- Tier ladder: Decoy → Mimic → Forger → Doppelgänger → Honorary Bot.
- Nightly rollup via `make rollup` or `bbg-admin rollup`.

### Added — Share artifacts and OG images

- Spoiler-free emoji grid card (text), mode-aware copy + icon (🪿 / 🧍).
- Public per-play result page at `/r/<short>` with a 1200×630 OG image renderer (`internal/share/og.go`) so chat clients unfurl into a card.
- `navigator.share()` for mobile share sheet, clipboard fallback for desktop.
- Request-derived base URL (X-Forwarded-Proto / X-Forwarded-Host aware) so shares carry the host the player actually browsed (works correctly behind Caddy, Cloudflare Tunnel, and ngrok).

### Added — Content pipeline

- `cmd/bot-candidates` calls the Anthropic Messages API offline; inserts as `pending` for human review.
- `bbg-admin import` ingests a JSON content file (prompts + bots + decoys + puzzle definitions) into approved rows.
- `cmd/puzzle-build` composer is idempotent on `puzzle_number`; `--check` is the 22:00 UTC alarm.
- Archetype roster from design §5 seeded by `make seed`.

### Added — Operator surface

- One `Makefile` is the operator UI (`up`, `down`, `rebuild`, `logs`, `psql`, `migrate`, `seed`, `build-daily`, `rollup`, `backup`, `restore`, `admin-promote`).
- Multi-stage Dockerfile produces a static binary; docker-compose ships bbg + postgres + Caddy (auto-TLS).
- `POSTGRES_PASSWORD` is required (no `bbg/bbg` default) — compose refuses to start without it.
- `bbg-admin vapid-gen` generates VAPID keys for future push.
- PWA manifest and service worker (cache app shell only; never cache `/`, `/api/*`, `/play/*`).

### Schema

- One migration (`db/migrations/0001_init.sql`) lays down all tables forward-compatible to Phase 3.
- Forward-compat columns: `users.spotter_elo`, `puzzle_rounds.target_count`, `puzzle_round_answers.is_trap`, `daily_puzzles.season_id`, `decoy_submissions.ai_detector_score`.

### Tests

- Hermetic unit suite covering token forge/replay/expiry, slot-permutation uniformity, scoring + adjusted fool rate edge cases, OG PNG render, share-card variants, short-ID round-trip, tier math.

### Repo housekeeping

- MIT license, CONTRIBUTING + SECURITY policies, BACKLOG of deliberate non-goals.
- GitHub Actions CI: `go vet` + `go test -race` + multi-binary `go build` on push and PR.
- Dependabot for Go modules, GitHub Actions, and Docker base images.
- Hardened `.gitignore` against accidental commit of envs / certs / coverage output / IDE cruft.

[Unreleased]: https://github.com/christianreimer/bot-bot-goose/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/christianreimer/bot-bot-goose/releases/tag/v0.1.0
