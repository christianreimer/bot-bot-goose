# Backlog

What's deliberately *not* done yet, in roughly the order I'd tackle it. The README's status table is the live checklist; this file is the *why* behind each open item so a contributor can pick something up without reverse-engineering the design.

Each item links to the plan's build-order step it's part of.

---

## Near-term (Phase 2 finishing touches)

### Moderation queue UI (build-order step 7)

**Why it matters.** The contribution loop is wired end-to-end except for reviewer ergonomics. Approving a pending decoy currently means `make psql` + `UPDATE decoy_submissions SET status='approved' ...`. That's a real bottleneck once submissions are flowing.

**What's needed.**

- A page at `/admin/queue` that lists `pending` rows across `bot_candidates` and `decoy_submissions` (the `moderation_reviews` table is the shared queue).
- Approve / reject / retire actions, each writing an `audit_log` row.
- A `users.role >= 'reviewer'` gate.

**Where to start.** `internal/httpx/server.go` already has the role-aware middleware shape. Add a `/admin` route group behind a role check; the rest is templates + form posts.

### Magic-link email auth (build-order step 3)

**Why it matters.** Anonymous device cookies cover first-play, but a phone wipe currently loses your streak, your decoys, and your forger rank. Magic-link upgrade is the lowest-friction account model that solves all three.

**What's needed.**

- `POST /api/auth/magic/request` sends an email with a one-time link.
- `GET /auth/magic/:token` consumes the link and migrates the current device cookie's user_id to the email-bound user (creating one if absent).
- Email provider behind an interface (Postmark / Resend / SES — config decision, not code).

**Schema is already in place** (`magic_links` table, `users.email`, `users.email_verified_at`).

### Harvest promotion tool

**Why it matters.** The Phase-0 collection campaign (`/harvest`) lands rows in `pre_launch_submissions`. They never auto-flow into the live game — that's the safety boundary. Today, promoting a hand-picked harvested answer into `decoy_submissions` is a SQL one-liner; once the campaign is generating volume, a reviewer UI keeps the gate cheap to operate.

**What's needed.**

- A page at `/admin/harvest` listing harvested rows (with prompt + author IP for spam triage), filterable by "not yet promoted."
- One-click promotion that inserts into `decoy_submissions(status='approved')` and sets `pre_launch_submissions.ingested_decoy_id`.
- Bulk reject / hide for obvious spam.

**Where to start.** Same role-gate pattern as the moderation queue UI above. The promotion SQL is already in `plans/harvest.md`'s verification section.

### AI-detection on submitted decoys

**Why it matters.** The vetting gate (design §4) is load-bearing — a single missed AI submission contaminates the human pool and the brand. Right now we rely entirely on human review.

**What's needed.**

- An interface `internal/moderation.Detector` with `Score(text string) (float64, error)`.
- A stub heuristic implementation for v1 (length, hedge-word density, em-dash count).
- Wire into the submit path so `ai_detector_score` is populated before the row hits the moderation queue.
- Reviewer UI shows the score as a hint, not a verdict.

---

## Phase 3 anti-decay engines

### Slot E/P/B bandit in the composer (build-order step 9)

**Why it matters.** Today's composer picks decoys at random from the approved pool. The plan's bandit makes the daily puzzle dynamic — exploration slot guarantees new submitters get exposure, proven slot rewards forgers with track record, bandit slot does the discovery in between.

**What's needed.**

- Thompson sampling on a Beta prior, mode-baselined (centered at 0.25 for find_the_bot, 0.75 for find_the_human).
- Author exclusion at serve time, not just build time (a puzzle is served to many users for the same UTC day).
- Replace `pickApprovedDecoys` in `cmd/puzzle-build/main.go` — the contract there is the stable surface.

### Adaptive difficulty + spotter ELO (build-order step 12)

**Why it matters.** Best players churn first when difficulty doesn't climb with them. ELO already has its column (`users.spotter_elo`); the update step + the archetype-by-difficulty pick at puzzle-build time are missing.

**What's needed.**

- Update `spotter_elo` on each completed play (K-factor TBD).
- Composer reads each archetype's `difficulty` (already on `archetypes.difficulty`) and weights toward harder archetypes for higher-ELO players. Implementation note: the daily puzzle is shared globally, so adaptive *content* doesn't fit cleanly; the per-archetype rotation is the right knob.

### Variable target count + traps (build-order step 13)

**Why it matters.** "Find the one weird one" is the main exploit players develop. Variable target count breaks the assumption; traps (human decoys curated to look bot-ish) punish lazy "polished = bot" pattern-matching.

**Schema is ready** (`puzzle_rounds.target_count`, `puzzle_round_answers.is_trap`, `decoy_submissions.is_trap`). What's needed is composer logic to pick 0 or 2 bots occasionally, and reviewer-tagged traps to substitute into proven slots.

### Seasons + format rotation + analytics events (build-order step 14)

**Why it matters.** Seasons reset leaderboards with a fresh archetype roster — natural marketing cadence. Format rotation (group-chat rounds, reverse rounds, image days) keeps the daily from going stale.

**Schema is ready** (`seasons`, `events`). What's needed is the season-aware composer + a `format` enum migration when the first non-default format ships.

---

## Operational / polish

### sqlc adoption

Replace the hand-rolled `internal/db/*.go` query files with sqlc-generated equivalents. Function signatures stay identical; `sqlc.yaml` is already in place. Single PR, mechanical.

### Push notifications

`push_subscriptions` table and VAPID keys generator exist. Missing: a `POST /api/push/subscribe` endpoint, a per-user send routine, the daily-puzzle-ready notification (and the per-decoy payoff notification).

### Email reminders

`email_reminders` table exists. A daily cron that sends "today's goose is loose" to users who opted in but don't have a push subscription. Provider behind the same interface as magic-link.

### Local-midnight rollover

Design §7 explicitly defers this to Phase 3. Global UTC day is what makes shared grids meaningful; local-midnight needs careful UX so neighbors don't see different content.

### Decoy report OG image renderer

`/d/<short>` currently uses the brand-default `og:title`/`og:description` and no `og:image`. The equivalent of `share.RenderResultOG` for decoy reports would unfurl per-decoy share links into cards. The data plumbing in `internal/httpx/decoy_share.go` is ready; what's missing is the image render.

### Integration test harness

Every test today is hermetic and runs in under a second — by design. As the moderation queue and the bandit land, a small set of integration tests against a real Postgres (in CI behind a `// +build integration` tag) will pull weight.

---

## Won't-do (unless something changes)

These are noted so contributors don't burn time pitching them.

- **A JS framework on the front end.** The no-build-step property is load-bearing.
- **LLM calls on the live request path.** Generation stays offline (`cmd/bot-candidates`).
- **Scraping any platform for human answers.** Design §3 lays out why; *solicit, don't scrape*.
- **Per-submission compensation.** Reward performance, not volume, always behind the vetting gate.
