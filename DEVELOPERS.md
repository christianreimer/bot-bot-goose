# Bot Bot Goose — developer notes

The public README is the pitch. This file is everything you need to read, build, and run the codebase.

[Quickstart](#quickstart) · [How it works](#how-it-works) · [Architecture](#architecture) · [Status](#detailed-status) · [Limitations](#limitations) · [Contributing](CONTRIBUTING.md) · [Security](SECURITY.md) · [Operator playbook](PLAYBOOK.md)

---

## What's in the box

- **Server-authoritative daily puzzle** — answer labels never reach the client until a guess is committed. The shared grid stays meaningful because the day's bot can't be data-mined.
- **Per-play slot permutation + HMAC'd play tokens** — the anti-cheat backbone, with tests in `internal/play/`.
- **You vs the Room** — players submit human-written lines that go into a moderation queue and, once approved, get rotated into future puzzles. After each round's reveal, players cast a one-shot "felt most human?" vote among the three real lines; contributors rank on the Originals leaderboard by how often the room picks theirs as the most human (see `internal/leaderboard`).
- **Collective rally stat** — every nightly rollup freezes `Yesterday, humans caught the goose X%` into `daily_collective_stats`. It surfaces on the result page, the share-card text, and the OG image — identical for everyone, stable for the day.
- **Public share artifacts** at stable URLs:
  - `/r/<short>` — per-play result page with 1200×630 OG image (the grid as a PNG) so link previews unfurl into a card, not a text bubble.
  - `/d/<short>` — per-decoy report ("could you have caught this?") with author attribution.
- **Pre-launch collection campaign** at `/prelaunch` — anonymous, no email, lands rows in `pre_launch_submissions` that a reviewer can promote to the live decoy pool.
- **PWA + push hooks** — manifest, service worker, VAPID generator. The push *subscriber* endpoint is on the TODO list.
- **Offline LLM bot-candidate generator** (`cmd/bot-candidates`) so the live request path never talks to an LLM.
- **Operator-friendly**: one `Makefile`, four Docker services in dev (postgres + valkey + bbg + Caddy), self-applying migrations on boot, idempotent puzzle composer, nightly leaderboard rollup, JSON content imports under version control.

## Quickstart

You need: Docker, `make`, and Go 1.25+ (only for `make dev` without containers).

```bash
git clone https://github.com/christianreimer/bot-bot-goose && cd bot-bot-goose
cp deploy/compose.env.example deploy/compose.env
```

Edit `deploy/compose.env` and set **both** required values:

```bash
BBG_SESSION_KEY=$(openssl rand -hex 32)      # signs cookies + play tokens
POSTGRES_PASSWORD=$(openssl rand -hex 24)    # postgres user password — REQUIRED
```

Then:

```bash
make up                 # postgres + valkey + bbg (auto-applies migrations), plus a Cloudflare Quick Tunnel
make seed               # insert prototype puzzle #001
open http://localhost:8080
```

`make up` also spawns a Cloudflare Quick Tunnel and prints a `trycloudflare.com` URL so you can hit the local stack from a phone or share a link without router/firewall work. `make tunnel-down` (or `make down`) stops it.

> **Security note.** `POSTGRES_PASSWORD` is intentionally required (compose refuses to start without it). The previous default was the literal string `bbg`, which is fine for loopback dev but a footgun the moment anyone exposes the DB.

Other day-to-day:

```bash
make logs                                                       # tail server logs
make psql                                                       # \i into the running db
make rollup                                                     # rebuild Originals leaderboard + freeze yesterday's collective catch rate
make backup                                                     # pg_dump → ./backups/<ts>.sql.gz
bin/bbg-admin puzzle schedule --start 2026-06-25 --days 14      # bulk-fill upcoming dates
bin/bbg-admin puzzle schedule --check                           # report-only 7-day health check
bin/bbg-admin import content/sample-2026-06.json                # bulk content import
```

For the full operator surface, see [`PLAYBOOK.md`](PLAYBOOK.md). For pure-Go local dev (skips containers; postgres must still be reachable at `BBG_DB_URL`):

```bash
make dev
```

## How it works

### The integrity backbone

These rules are non-negotiable; everything else can change. The prototype's client-side puzzle would fall over to data-mining within a day — the server has to own labels.

| Rule | Lives in |
|---|---|
| Answers reordered per play (per-play `slot_permutation` defeats data-mining) | `internal/play/slot.go` + `internal/db.UpsertPlayRound` |
| Play tokens are HMAC-signed with the per-play `plays.hmac_secret` | `internal/play/token.go` |
| Token binds `(play_id, round_index, perm_hash, issued_at)` | `play.Token` |
| Token signature uses strict RawURL base64 (no non-canonical encodings) | `internal/play/token.go` |
| Reveal only on guess commit — no GET ever leaks the target | `internal/httpx.handleAPIGuess` |
| Cross-play submission rejected (token's play_id must match session user) | `loadVerified` in `play_handlers.go` |
| Out-of-order rounds rejected (round N requires rounds <N committed) | `DB.PriorRoundsAllCommitted` |
| Realest vote is one-shot per round (race-free first-vote-wins) | `internal/db.CastRealestVote` |
| Suspicious-fast guesses flagged, not rejected | `play.SuspiciousFastGuessFloor` |
| CSRF double-submit on every state change | `internal/users/csrf.go` |
| Cookies `HttpOnly; Secure; SameSite=Lax` | `internal/users/session.go` |

Run the integrity tests:

```bash
go test ./internal/play/... ./internal/game/... ./internal/share/...
```

### The puzzle

Every round shows a prompt and four answers: 1 bot + 3 human decoys. The player taps the bot. The share grid uses 🟩🟨🟥 (green / yellow / red) for catch / hint-assisted catch / miss.

### What ranks contributors

After each round's reveal the player casts a one-shot "felt most human?" vote among the three real decoys (the bot card is not votable). The **Adjusted Most-Human Rate** (`internal/leaderboard.AdjustedMostHumanRate`) shrinks each contributor's raw rate toward the 1/3 chance baseline by `k` impressions. That rate is what the Originals leaderboard orders on; the legacy fool rate is still tracked but appears only as display-only flavor on the per-line report card.

### You vs the Room (contribution loop)

Players can submit human-written lines from the result page. Submissions go to moderation as `pending`. Once approved, the composer can drop them into future puzzles. Each line's impressions and realest-track votes roll up nightly into `forger_rankings` (table name kept for stability; the surface is the Originals leaderboard). Users see their stats on `/me` and rank on `/leaderboard/originals`.

Every approved line gets a stable shareable URL at `/d/<short>` — a "could you have caught this?" hook with the author's handle, most-human rate, and tier. Result pages share a stable URL at `/r/<short>` whose OG image is the player's grid rendered as a 1200×630 PNG; both the page and the image carry the day's `Humans yesterday: X%` collective line when one is available.

### Pre-launch campaign

A separate anonymous surface at `/prelaunch` collects human-written lines before the game officially launches. Submissions land in `pre_launch_submissions` (not auto-flowed into the live game). A reviewer promotes them via `bbg-admin prelaunch review` — see [`PLAYBOOK.md`](PLAYBOOK.md) Workflow 1.

When `BBG_PRELAUNCH_MODE=1`, the front page serves an on-brand "coming soon" placeholder; `/prelaunch`, `/privacy`, and system routes stay live. Flip the flag off at launch.

## Architecture

Go 1.25, server-rendered HTML, vanilla JS sprinkles, Postgres via `pgx/v5`, embedded goose migrations, optional Valkey/Redis cache. No SPA framework, no build step on the front-end. The whole production system is one container (DO App Platform) plus managed Postgres + Valkey.

```
cmd/
  server/           HTTP server (the main binary). Self-applies embedded migrations on boot.
  puzzle-build/     cron: composes the next daily puzzle (idempotent; --check is the 22:00 UTC alarm)
  bot-candidates/   offline Anthropic-driven generator → bot_candidates as 'pending'
  og-render/        share-image renderer stub (server now renders inline; cmd left for future cache)
  admin/            seed, promote, vapid-gen, import, rollup, puzzle/decoy/bot/prompt/prelaunch/stats verbs
internal/
  play/             slot permutation + HMAC token issue/verify — the load-bearing anti-cheat code
  game/             pure scoring + outcome resolution + shrinkage constants
  share/            spoiler-free emoji grid, share card text, OG PNG renderer, short IDs
  leaderboard/      adjusted-most-human-rate + tier math + nightly rollup → forger_rankings
  collective/       nightly freeze of yesterday's catch-rate (daily_collective_stats)
  content/          archetype roster
  users/            device-cookie session + CSRF middleware + cache invalidation
  cache/            thin Valkey/Redis wrapper with circuit breaker (nil-safe when unconfigured)
  ratelimit/        Limiter interface; Postgres-backed and Valkey/Lua-backed implementations
  metrics/          expvar publication tree (pool stats, http counters, cache counters)
  db/               pgx-based query layer (one file per domain)
  httpx/            handlers, routes, template loader, base-URL detection (X-Forwarded-Proto aware)
db/
  migrations/       goose .sql + embed.FS (single squashed 0001_init.sql)
web/
  templates/        html/template pages (layouts/base + per-page clones)
  static/           css (from prototype), vanilla js, manifest, sw, icons
deploy/             Dockerfile, docker-compose.yml, Caddyfile, compose.env.example
.do/                DigitalOcean App Platform spec template
content/            JSON imports — version-controlled puzzles (see `bbg-admin import`)
```

## Detailed status

Tracks the 14-step build order from the implementation plan. See [`BACKLOG.md`](BACKLOG.md) for the *why* behind each open item.

- [x] **1. Project skeleton** — Makefile, Docker, compose, Caddy, goose, slog, healthz.
- [x] **2. Schema 0001 + seed** — single squashed migration covers all phases.
- [x] **3. Auth + sessions** — anonymous device cookie, CSRF, rate limit, magic-link email upgrade with merge/promotion.
- [x] **4. Play loop, server-authoritative** — slot permutation, HMAC tokens (strict base64 decode), guess commit, reveal.
- [x] **5. Share artifacts** — emoji grid, share card, `navigator.share` + clipboard, **per-play OG PNG** at `/r/<short>/og.png` (cached in `plays.og_png` + 3-tier ValKey cache).
- [x] **6. Streaks + PWA hooks** — streaks live, manifest + service worker shipped. *Push subscribe endpoint + email reminders: TODO.*
- [x] **7. Decoy submission** — `POST /api/decoy/submit`, `/me` payoff page, `bbg-admin decoy review` for moderation.
- [x] **8. Bot-candidate generator** — `cmd/bot-candidates` with Anthropic Messages API.
- [x] **9. `puzzle-build` cron** — idempotent composer with 22:00 UTC alarm. *Slot E/P/B bandit: uniform-random stub; replace per plan.*
- [~] **10. Find-the-Human mode** — *built then reverted; single mode is the only mode now.*
- [x] **11. Originals leaderboard + line report share card** — `/leaderboard/originals`, `/leaderboard/spotters`, `/d/<short>`, post-reveal one-shot "felt most human?" vote, `Humans yesterday: X%` collective stat.
- [x] **12. Pre-launch campaign** — `/prelaunch`, env-gated coming-soon poster (`BBG_PRELAUNCH_MODE`).
- [x] **13. Launch capacity** — pgxpool sizing, ValKey caches at every hot read (users / rounds / OG / collective / streak / prelaunch pool), Postgres-fallback rate limiter, `/metrics` on a private listener.
- [ ] **14. Adaptive difficulty + seasons** — `spotter_elo`, `target_count`, `seasons`, `events` columns exist; populators TODO. (The earlier `is_trap` boolean was dropped in 0002 — see BACKLOG for the rationale.)

## Limitations

Deliberate scope choices. Each will become a separate effort when needed.

- **No sqlc yet.** Queries are hand-written on `pgx/v5` so the codebase compiles from a fresh checkout without a generator step. Function signatures in `internal/db/` are the stable contract.
- **Composer's bandit is uniform-random** in `cmd/puzzle-build`. The bandit math is specified in the plan and slots into `composeRoundAnswers` without a contract change.
- **Local-midnight rollover is not implemented.** Global UTC day is what makes shared grids meaningful across timezones.
- **No AI-detection on submitted decoys.** The `ai_detector_score` column exists; the heuristic / external-service call is TODO.
- **`cmd/og-render` is now mostly redundant** — the server renders OG PNGs inline. The CLI is kept as a placeholder for a future static-image cache job.
- **DO App Platform cron jobs aren't native.** The three daily jobs (puzzle-build, --check alarm, rollup) need an external scheduler (GitHub Actions, DO Functions, etc.) since App Platform's job `kind` only supports `PRE_DEPLOY` / `POST_DEPLOY` / `FAILED_DEPLOY`.

## Verifying it works

```bash
go test -race -count=1 ./...
make up && make seed
curl -sf http://localhost:8080/healthz             # → ok
open http://localhost:8080                         # play through three rounds
```

Anti-cheat smoke test — server must reject a tampered guess token:

```bash
curl -sX POST http://localhost:8080/api/play/round/0/guess \
     -H "Content-Type: application/json" \
     -H "X-CSRF-Token: $(awk -F= '/bbg_csrf_v1/{print $2}' /tmp/cookies.txt)" \
     --cookie /tmp/cookies.txt \
     -d '{"token":"deadbeef.0.aaaa.0.zzz","slot":0}'
# → {"code":"token_invalid"} or {"code":"bad_token"}
```

Spoiler-free grid:

```bash
go test ./internal/share/...      # includes TestGridIsSpoilerFree
```

## Contributing

PRs welcome. See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the loop. The integrity backbone in `internal/play/` carries the load — changes there should ship with tests in the same package.

## Security

Found a vulnerability? Please report it privately via [GitHub Security Advisories](https://github.com/christianreimer/bot-bot-goose/security/advisories/new) — see [`SECURITY.md`](SECURITY.md) for the response policy.

## License

[MIT](LICENSE) © 2026 Christian Reimer.
