# Changelog

All notable changes to this project are documented here. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Tracked in [BACKLOG.md](BACKLOG.md). Most prominent: moderation queue UI, magic-link auth, AI-detection on submissions, slot E/P/B bandit in the composer.

## [0.1.0] — Initial public release

The first cut. Production-grade integrity backbone; Phase 2 (You vs the Room) wired end-to-end; some Phase 3 engines scaffolded.

### Added — Play loop and integrity backbone

- Server-authoritative daily puzzle. Answer labels never leave the server until a guess is committed.
- Per-play `slot_permutation`, generated with `crypto/rand` — defeats data-mining the day's bot across players.
- HMAC-signed play tokens binding `(play_id, round_index, perm_hash, issued_at)`, with cross-play, out-of-order, and expired-token rejection.
- Suspicious-fast-guess flagging (no hard reject, per the plan's anti-cheat rules).
- Anonymous device-cookie sessions, CSRF double-submit middleware, `HttpOnly; Secure; SameSite=Lax` cookie attributes.
- HEAD-to-GET middleware so link-preview crawlers that probe via HEAD don't get 405s.

### Added — Two game modes

- `find_the_bot` (1 bot + 3 decoys) and `find_the_human` (3 bots from distinct archetypes + 1 decoy).
- Composer's `pickMode` rotates with anti-streak limits.
- Unified Adjusted Fool Rate that shrinks toward 0.25 (find_the_bot) or 0.75 (find_the_human), so a contributor's forger ranking is honest across mixed exposure.

### Added — You vs the Room

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
