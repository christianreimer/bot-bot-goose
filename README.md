# Bot Bot Goose

> A daily web game where you hunt the AI hiding among real humans. One bot. Three humans. Tap the goose.

<!-- TODO: drop a 1200x630 screenshot of the result grid here once the UI is polished. -->

Bot Bot Goose (`bbg`) is a server-rendered Go web app for a Wordle-style daily game built around the question *can you still tell a human from an AI?* Every day, three rounds. Each round shows a prompt and four answers — one bot, three humans (or, on inverted days, three bots and one human). You tap the odd one out. After three rounds you get a Bot-Dar score and a Wordle-grid you can share.

The cultural bet is that this question is genuinely loaded right now, and the result is more of an identity statement than a trivia score — which is what makes the grid shareable.

[Quickstart](#quickstart) · [How it works](#how-it-works) · [Architecture](#architecture) · [Status](#status) · [Limitations](#limitations) · [Contributing](CONTRIBUTING.md) · [Security](SECURITY.md) · [License](LICENSE)

---

## What's in the box

- **Server-authoritative daily puzzle** — answer labels never reach the client until a guess is committed. The shared grid stays meaningful because the day's bot can't be data-mined.
- **Per-play slot permutation + HMAC'd play tokens** — the anti-cheat backbone, with tests in `internal/play/`.
- **Two game modes**: classic *Find the Bot* (1 bot + 3 humans) and inverted *Find the Human* (3 bots + 1 human). Mode is chosen per puzzle by the composer; same scoring, same share-grid vocabulary.
- **You vs the Room** (design §4) — players submit human-written decoys that go into the moderation queue and, once approved, get rotated into future puzzles. There's a forger leaderboard, a `/me` payoff page, and a per-decoy share artifact.
- **Public share artifacts** at stable URLs:
  - `/r/<short>` — per-play result page with 1200×630 OG image (the grid as a PNG) so link previews unfurl into a card, not a text bubble.
  - `/d/<short>` — per-decoy report ("could you have caught this?") with author attribution.
- **PWA + push hooks** — manifest, service worker, VAPID generator. The push *subscriber* endpoint is on the TODO list.
- **Offline LLM bot-candidate generator** (`cmd/bot-candidates`) so the live request path never talks to an LLM.
- **Operator-friendly**: one `Makefile`, three Docker services (postgres + bbg + Caddy with auto-TLS), idempotent puzzle composer cron, nightly leaderboard rollup, JSON content imports under version control.

## Status

Production-grade for the integrity backbone and the play loop. Phase-2 (You vs the Room) is wired end-to-end; some Phase-3 anti-decay engines are scaffolded as stubs. Full state in [Status](#detailed-status).

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
make up        # postgres + bbg + Caddy
make migrate   # apply schema
make seed      # insert prototype puzzle #001
open http://localhost
```

> **Security note.** `POSTGRES_PASSWORD` is intentionally required (compose refuses to start without it). The previous default was the literal string `bbg`, which is fine for loopback dev but a footgun the moment anyone exposes the DB.

Other day-to-day:

```bash
make logs                                                       # tail server + Caddy logs
make psql                                                       # \i into the running db
make build-daily DATE=2026-06-13                                # compose a puzzle for a date
make build-daily MODE=find_the_human                            # force inverted mode
make bot-gen PROMPT="What's the worst advice you've ever been given?" N=8
make rollup                                                     # rebuild forger leaderboards
make backup                                                     # pg_dump → ./backups/<ts>.sql.gz
go run ./cmd/admin import content/sample-2026-06.json           # bulk content import
```

For pure-Go local dev (skips containers; postgres must still be reachable at `BBG_DB_URL`):

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
| Reveal only on guess commit — no GET ever leaks the target | `internal/httpx.handleAPIGuess` |
| Cross-play submission rejected (token's play_id must match session user) | `loadVerified` in `play_handlers.go` |
| Out-of-order rounds rejected (round N requires rounds <N committed) | `DB.PriorRoundsAllCommitted` |
| Suspicious-fast guesses flagged, not rejected | `play.SuspiciousFastGuessFloor` |
| CSRF double-submit on every state change | `internal/users/csrf.go` |
| Cookies `HttpOnly; Secure; SameSite=Lax` | `internal/users/session.go` |

Run the integrity tests:

```bash
go test ./internal/play/... ./internal/game/... ./internal/share/...
```

### Two game modes

| Mode | Per round | Player taps | Icon |
|---|---|---|---|
| `find_the_bot` (default) | 1 bot + 3 human decoys | The bot | 🪿 |
| `find_the_human` (inverted) | 3 bots (from distinct archetypes) + 1 human decoy | The human | 🧍 |

Mode is chosen by the composer per the rotation policy (≈5 find_the_bot per 1 find_the_human). The share grid uses identical 🟩🟨🟥 vocabulary in both modes, so grids stay visually comparable across the inversion. The mode-icon (🪿 vs 🧍) signals the variant on the share card.

Scoring is identical across modes; the unified **Adjusted Fool Rate** (`internal/leaderboard/tier.go`) shrinks toward 0.25 for find_the_bot impressions and 0.75 for find_the_human impressions, so a contributor's forger ranking is honest across mixed exposure.

### You vs the Room (contribution loop)

Players can submit human-written decoys from the result page. Submissions go to moderation as `pending`. Once approved, the composer's bandit can plant them in future puzzles. Each decoy's impressions and "thought-I-was-a-bot" counts roll up nightly into `forger_rankings`; users see their stats on `/me` and rank on `/leaderboard/originals`.

Every approved decoy gets a stable shareable URL at `/d/<short>` — a "could you have caught this?" hook with the author's handle, fool rate, and tier. Result pages also share a stable URL at `/r/<short>` whose OG image is the player's grid rendered as a 1200×630 PNG.

## Architecture

Go 1.25, server-rendered HTML, vanilla JS sprinkles, Postgres via `pgx/v5`, goose migrations, Caddy reverse-proxy with auto-TLS. No SPA framework, no build step on the front-end. The whole production system is one container plus postgres.

```
cmd/
  server/           HTTP server (the main binary)
  puzzle-build/     cron: composes the next daily puzzle (idempotent; --check is the 22:00 UTC alarm)
  bot-candidates/   offline Anthropic-driven generator → bot_candidates as 'pending'
  og-render/        share-image renderer stub (server now renders inline; cmd left for future cache)
  admin/            seed, promote, vapid-gen, import (JSON content), rollup
internal/
  play/             slot permutation + HMAC token issue/verify — the load-bearing anti-cheat code
  game/             pure scoring + unified fool rate + outcome resolution
  share/            spoiler-free emoji grid, share card text, OG PNG renderer, short IDs
  leaderboard/      tier math + nightly rollup of decoy_daily_stats → forger_rankings
  content/          archetype roster (design §5)
  users/            device-cookie session + CSRF middleware
  db/               pgx-based query layer (one file per domain)
  httpx/            handlers, routes, template loader, base-URL detection (X-Forwarded-Proto aware)
db/
  migrations/       goose .sql files (0001_init covers all phases forward-compatible)
  queries/          placeholder — sqlc adoption is a future PR
web/
  templates/        html/template pages (layouts/base + per-page clones)
  static/           css (from prototype), vanilla js, manifest, sw, icons
deploy/             Dockerfile, docker-compose.yml, Caddyfile, compose.env.example
content/            JSON imports — version-controlled puzzles (see `bbg-admin import`)
```

## Detailed status

Tracks the 14-step build order from the implementation plan. See [`BACKLOG.md`](BACKLOG.md) for the *why* behind each open item.

- [x] **1. Project skeleton** — Makefile, Docker, compose, Caddy, sqlc.yaml, goose, slog, healthz.
- [x] **2. Schema 0001 + seed** — all tables forward-compatible to Phase 3.
- [x] **3. Auth + sessions** — anonymous device cookie, CSRF, rate limit. *Magic-link email upgrade: TODO.*
- [x] **4. Play loop, server-authoritative** — slot permutation, HMAC tokens, guess commit, reveal.
- [x] **5. Share artifacts** — emoji grid, share card, `navigator.share` + clipboard, **per-play OG PNG** at `/r/<short>/og.png`.
- [x] **6. Streaks + PWA hooks** — streaks live, manifest + service worker shipped. *Push subscribe endpoint + email reminders: TODO.*
- [x] **7. Decoy submission** — `POST /api/decoy/submit`, `/me` payoff page. *Moderation review UI: TODO (approve via `make psql` for now).*
- [x] **8. Bot-candidate generator** — `cmd/bot-candidates` with Anthropic Messages API.
- [x] **9. `puzzle-build` cron** — idempotent composer with mode rotation + 22:00 UTC alarm. *Slot E/P/B bandit: uniform-random stub; replace per plan.*
- [x] **10. Find-the-Human mode** — `puzzle_mode` enum, composer recipe, mode-baselined fool rate.
- [x] **11. Originals leaderboard + decoy report share card** — `/leaderboard/originals`, `/leaderboard/spotters`, `/d/<short>` artifact.
- [ ] **12. Adaptive difficulty** — `spotter_elo` column exists; ELO update + archetype-by-difficulty pick TODO.
- [ ] **13. Variable target count + traps** — `target_count` column + `is_trap` flag exist; composer wiring TODO.
- [ ] **14. Seasons + format rotation + analytics events** — `seasons` and `events` tables exist; populator TODO.

## Limitations

Deliberate scope choices for v1. Each will become a separate effort when needed.

- **No sqlc yet.** Queries are hand-written on `pgx/v5` so the codebase compiles from a fresh checkout without a generator step. `sqlc.yaml` and `db/queries/` are placeholders for the future migration. Function signatures in `internal/db/` are the stable contract.
- **`go.sum` committed via `go mod tidy`.** The Dockerfile runs it; local clones may need a manual `go mod tidy` once.
- **Magic-link email auth is a stub.** First play is anonymous via a device cookie. Cross-device streaks, contributor attribution that survives a phone wipe, and leaderboard handles all benefit from the magic-link upgrade; the schema and `magic_links` table are in place.
- **No moderation review UI.** Approve pending decoys via SQL until the `/admin/queue` page lands. The schema, routes, and `users.role` column are ready.
- **Composer's bandit is uniform-random** in `cmd/puzzle-build`. The bandit math (slot E/P/B with Thompson sampling against the per-mode baseline) is specified in the plan and slots into `composeRoundAnswers` without a contract change.
- **Local-midnight rollover is not implemented.** Global UTC day is what makes shared grids meaningful across timezones, per design §7. Local-midnight UX is a Phase 3 follow-up.
- **No AI-detection on submitted decoys.** The `ai_detector_score` column exists; the heuristic / external-service call is TODO.
- **`cmd/og-render` is now mostly redundant** — the server renders OG PNGs inline. The CLI is kept as a placeholder for a future static-image cache job.

## Sample content

`content/sample-2026-06.json` ships four puzzles (`#2`–`#5`, alternating modes) so you can try the inversion immediately:

```bash
make seed                                                # adds puzzle #001 from the prototype
go run ./cmd/admin import content/sample-2026-06.json    # adds puzzles #002–#005
```

Then visit `/play/2`, `/play/3`, `/play/4`, `/play/5` to preview each. (The root URL still serves whichever puzzle's `puzzle_date <= today UTC` has the highest `puzzle_number`.)

## Verifying it works

```bash
go test ./...
make up && make migrate && make seed
curl -sf http://localhost:8080/healthz             # → ok
open http://localhost                              # play through three rounds
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
