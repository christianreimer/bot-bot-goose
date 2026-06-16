# Bot Bot Goose — Operator Playbook

Quick reference for driving the game's content pipeline and schedule.
Every command goes through `bbg-admin` (single entry point). All reads
return JSON; add `--table` for ASCII tables. Writes return
`{"ok":true,...}` envelopes. Errors land on stderr as
`{"error","code","details"}` and exit non-zero — **branch on `code`,
never on prose**.

For the full JSON shapes per verb, see `cmd/admin/README.md`. For
deeper procedural context (triage rhythm, "AI-shaped tells", etc.) see
the `bbg` Claude skill at `.claude/skills/bbg/SKILL.md`.

```
ADMIN=/Users/creimer/code/botbotgoose/bin/bbg-admin
```

If the binary is stale: `make -C /Users/creimer/code/botbotgoose bin/bbg-admin`.

---

## Connection

The binary reads **one** env var for the DB target: `BBG_DB_URL`. Default
points at the local docker-compose stack — no flags needed:

```
postgres://bbg:bbg@localhost:5432/bbg?sslmode=disable
```

### Production (DO Managed Postgres)

1. DO dashboard → Databases → bbg-pg → **Connection Details** → switch
   from "VPC network" to **"Public network"** → copy the URI. **Use the
   connection-pool URI** (`/bbg-pg-pool`), not the direct cluster URI.
2. Same panel → **Settings → Trusted Sources** → add the IP of whatever
   host will run `bbg-admin` (your laptop, or the Hermes agent host).
3. Export:

```
export BBG_DB_URL='postgresql://doadmin:PWD@db-pgsql-bbg-do-user-xxxxx.h.db.ondigitalocean.com:25061/bbg-pg-pool?sslmode=require'
```

Anything that writes to `moderation_reviews` also needs a reviewer
identity:

```
export BBG_REVIEWER_EMAIL=creimer@mudbox.org
```

If that email isn't yet a registered reviewer, promote it once:

```
"$ADMIN" promote --email creimer@mudbox.org --role reviewer
```

When pointed at a non-local DB, **always confirm with the user before
running writes**. Writes are operator-visible and hard to reverse.

---

## One-shot commands

| Command | Purpose |
|---|---|
| `$ADMIN seed` | Insert the prototype puzzle as #001. Useful on a fresh DB. |
| `$ADMIN promote --email --role` | Set a user's role (`player` / `reviewer` / `admin`). Writes `audit_log`. |
| `$ADMIN vapid-gen` | Print a fresh VAPID key pair (for `BBG_VAPID_PUBLIC`/`PRIVATE`). |
| `$ADMIN import [--dry-run] file.json` | Bulk import prompts + bots + decoys + puzzles. See workflow 2 alt path. |
| `$ADMIN rollup` | Recompute Originals leaderboard + freeze yesterday's collective stat. |

---

## Workflow 1: Review `/prelaunch` submissions

Goal: per prompt, pick which prelaunch submissions are worth keeping,
ingest them into `decoy_submissions` as `approved`, and soft-reject the
rest so the deck stops surfacing the prompt as undersupplied.

**Per-prompt sufficiency target: ≥1 bot + ≥4 decoys.** Below 4 decoys a
puzzle still composes for that prompt, just with no reserve answer.

### Step 1 — find prompts that need attention

```
"$ADMIN" prelaunch prompts --table
```

Columns: `PENDING` (live submissions awaiting review), `INGESTED`
(already approved + in live pool), `REJECTED` (soft-rejected),
`APPROVED_DECOYS` (current live-pool count), `GAP` (4 − approved_decoys,
clamped at 0). Default sort: highest `PENDING` first.

### Step 2 — show candidates for one prompt

```
"$ADMIN" prelaunch list --prompt-id <uuid> --table
"$ADMIN" prelaunch show --table <submission-id>          # full row incl. device + IP
```

### Step 3 — triage

Present 5–10 at a time. Watch for:

- **Obvious spam** (random keystrokes, off-topic, slurs) → toss.
- **AI-shaped** (long hedging sentences, em-dashes, generic positivity)
  → toss; the human pool must stay human.
- **Duplicates** — keep the better-written one.

Do not bulk-approve without the user's explicit OK.

### Step 4 — apply decisions

**Flag order matters.** All flags BEFORE the submission id:

```
"$ADMIN" prelaunch review --decision approve [--note "..."] <id>
"$ADMIN" prelaunch review --decision reject  [--note "..."] <id>
```

Approve writes a `decoy_submissions` row (`status='approved'`, author
preserved), sets `pre_launch_submissions.ingested_decoy_id`,
and writes `moderation_reviews` + `audit_log` in one transaction.

Reject sets `pre_launch_submissions.rejected_at = NOW()` and writes the
same audit pair.

`already_decided` in the error envelope → row already ingested or
rejected. Pick a different id.

Bulk variants (default `--limit 100`, refuses above):

```
"$ADMIN" prelaunch bulk-review --decision reject --ids id1,id2,id3                --note "..."
"$ADMIN" prelaunch bulk-review --decision reject --prompt-id <uuid> --limit 50    --note "..."
```

### Step 5 — verify

Re-run Step 1. Confirm `PENDING` dropped and `INGESTED` rose. Stop
triaging a prompt once it crosses ≥4 approved decoys.

---

## Workflow 2: Build a puzzle

A puzzle = 1 date + 3 rounds. Each round = 1 prompt + 1 bot + 3 decoys
(single mode since migration 0008 — every round, every puzzle).

### Step 1 — pick the date

```
"$ADMIN" puzzle list --table                       # today + future
```

### Step 2 — pick the prompts

```
"$ADMIN" prompt list   --table [--theme music] [--limit 200]
"$ADMIN" prompt supply --table [--only-unused] [--only-unready]
```

`prompt supply` columns: `BOTS`, `DECOYS`, `PENDING` (un-decided
prelaunch rows), `USED_IN` (puzzle numbers this prompt already appears
in), `READY` (Y/n), `GAP`. Unready prompts sort first.

If decoys are short, loop back to Workflow 1 for that prompt before
composing.

### Step 3 — refill bot pool if any prompt has zero approved bots

```
BBG_ANTHROPIC_API_KEY=... /Users/creimer/code/botbotgoose/bin/bbg-bot-candidates \
  --prompt "<exact prompt text>" --n 4
```

Then review:

```
"$ADMIN" bot list --status pending --prompt-id <uuid> --table
"$ADMIN" bot show --table <id>
"$ADMIN" bot review --decision approve [--note "..."] <id>
"$ADMIN" bot review --decision reject  [--note "..."] <id>

# Bulk
"$ADMIN" bot bulk-review --decision approve --prompt-id    <uuid> --limit 50
"$ADMIN" bot bulk-review --decision reject  --archetype-id <uuid> --limit 50
"$ADMIN" bot bulk-review --decision approve --ids id1,id2,id3
```

### Step 4 — compose

Random pick from the approved pool (good for filling the slate quickly):

```
"$ADMIN" puzzle compose --date 2026-06-25 --prompts <u1>,<u2>,<u3>
```

Deliberate pick every answer (1 bot + 3 decoys per round):

```
"$ADMIN" puzzle compose --date 2026-06-25 \
  --prompts <u1>,<u2>,<u3> \
  --round0-bots <b1> --round0-decoys <d1>,<d2>,<d3> \
  --round1-bots <b2> --round1-decoys <d4>,<d5>,<d6> \
  --round2-bots <b3> --round2-decoys <d7>,<d8>,<d9>
```

**All-or-nothing per round.** If you set `--roundN-bots`, you must also
set `--roundN-decoys`. Mix-and-match across rounds is fine.

Bad picks → `code: "invalid"` with the offending id named (not
approved, wrong prompt, listed twice, wrong count). Anything that
mutates rounds refuses with `has_plays` once any user has played.

Surgical edits on unplayed puzzles:

```
"$ADMIN" puzzle set-round  --round 1 --prompt-id <u> \
                           --bot-ids <b> --decoy-ids <d1>,<d2>,<d3> <puzzle-number>

"$ADMIN" puzzle set-answer --round 0 --slot 2 --bot-id   <b>   <puzzle-number>
"$ADMIN" puzzle set-answer --round 0 --slot 2 --decoy-id <d>   <puzzle-number>
"$ADMIN" puzzle set-answer --round 0 --slot 2 --text "..."     <puzzle-number>
```

`--bot-id` / `--decoy-id` / `--text` are mutually exclusive.

### Step 5 — verify

```
"$ADMIN" puzzle show <number> --table
```

Confirm prompt texts and 4 answers per round.

### Alt path — hand-author a whole puzzle (`bbg-admin import`)

When you want **specific** answer text (decoys verbatim, bot drafts
that aren't yet `bot_candidates`, a one-off seasonal puzzle), use
`import` instead of `compose`. The JSON file writes prompts + bots +
decoys + the puzzle in one shot, all marked `approved`.

```json
{
  "puzzles": [{
    "puzzle_number": 6,
    "date": "2026-06-25",
    "theme": "everyday",
    "rounds": [
      {
        "prompt": "What's the weirdest food combination you genuinely enjoy?",
        "decoys": ["Corn and onion pizza", "Pickles + cream cheese", "Toast through peach"],
        "bots": [
          {"archetype": "hedger", "text": "I guess pickles dipped in cream cheese? Although it's kind of normal in some places."}
        ]
      },
      { "prompt": "...", "decoys": ["...", "...", "..."], "bots": [{...}] },
      { "prompt": "...", "decoys": ["...", "...", "..."], "bots": [{...}] }
    ]
  }]
}
```

Per-round counts: 1 bot + 3 decoys. Archetype slugs from
`internal/content`: `hedger | sunbeam | lecturer | lister | dodger |
romantic | over-corrector | mirror`. Prompt text dedupes via
`UpsertPrompt`.

```
"$ADMIN" import --dry-run /tmp/puzzle-2026-06-25.json
"$ADMIN" import           /tmp/puzzle-2026-06-25.json
```

### Replacing a puzzle that has plays

`puzzle delete` refuses puzzles with plays. `puzzle replace` is the
destructive variant — it removes the puzzle and all attached plays,
then re-imports from JSON. The operator must restate the play count
via `--confirm-plays` so the loss is explicit.

```
"$ADMIN" puzzle list --include-past --table          # PLAYS column on the target
"$ADMIN" import --dry-run /tmp/puzzle-replacement.json
"$ADMIN" puzzle replace --number 2 \
  --content /tmp/puzzle-replacement.json \
  --confirm-plays 13
```

Failure codes:
- `invalid` — `--number` doesn't match `puzzle_number` in the JSON.
- `plays_mismatch` — current play count differs from `--confirm-plays`.
- `import_failed_after_delete` — delete committed, import errored. Re-run `bbg-admin import file.json` to refill the empty slot.

---

## Workflow 3: Schedule monitoring (7-day buffer)

The rule is **always 7 future days of puzzles scheduled**. Use
`--check` to monitor; use the same verb without `--check` to fill.

### Daily check (Hermes / cron entry point)

```
"$ADMIN" puzzle schedule --check
```

Defaults: `--start` = tomorrow UTC, `--days` = 7. Returns:

- **Exit 0** + JSON `{start, days, covered, missing: [], gaps: 0}` when
  every day is covered.
- **Exit 1** + `{error, code: "gaps", details: {covered, missing, gaps}}`
  when any day is missing.

Branch on the exit code (or the `code` field) to decide whether to page
the operator or auto-fill.

### Bulk fill empty dates (idempotent)

```
"$ADMIN" puzzle schedule --start 2026-06-20 --days 14
```

Skips dates that already have a puzzle. Result envelope lists per-date
`status` (`composed | skipped | failed`). A failed date is usually
`insufficient_content` on at least one prompt — loop back to
Workflow 1.

### Other schedule reads

```
"$ADMIN" puzzle list --table                                # today + future
"$ADMIN" puzzle list --from 2026-06-15 --to 2026-06-30 --table
"$ADMIN" puzzle list --include-past --limit 30 --table
"$ADMIN" puzzle show 5 --table                               # by number
"$ADMIN" puzzle show --date 2026-06-15 --table               # by date
```

---

## Workflow 4: Usage stats

All four sub-verbs accept `--since YYYY-MM-DD` or `--days N` (default
30) and `--table`.

```
"$ADMIN" stats overview  --table [--days 30]                              # snapshot
"$ADMIN" stats players   --table [--days 14]                              # active / started / completed / avg_score
"$ADMIN" stats decoys    --table [--days 14]                              # submitted / approved / pending / rejected
"$ADMIN" stats decoys    --top 10 --min-impressions 25 --days 14 --table  # best forgers
"$ADMIN" stats prelaunch --table [--days 14]                              # /prelaunch volume + ingest rate
```

When reporting to the user, lead with a one-sentence interpretation
("completion rate is 62% — within typical range; no anomalies"), then
the table. Don't dump raw rows.

---

## Workflow 5: Recurring housekeeping

| Cadence | Task | Invocation | Where it runs in prod |
|---|---|---|---|
| Nightly 01:30 UTC | Originals leaderboard + collective stat | `"$ADMIN" rollup` | DO cron |
| Nightly 12:00 UTC | Compose tomorrow's puzzle if missing | `bbg-puzzle-build` | DO cron |
| Nightly 22:00 UTC | Alarm if tomorrow's puzzle missing | `bbg-puzzle-build --check` | DO cron |
| Daily | 7-day schedule check | `"$ADMIN" puzzle schedule --check` | Hermes / cron |
| Weekly | Prelaunch backlog review | Workflow 1 | Operator |
| Weekly | Bot-pool refresh on under-stocked prompts | `prompt supply --only-unready` → `bbg-bot-candidates` → review | Operator |
| Weekly | Audit-log skim (one reviewer doing everything, IP flood) | `psql "$BBG_DB_URL" -c "SELECT action, count(*) FROM audit_log WHERE at > NOW() - INTERVAL '7 days' GROUP BY action"` | Operator |
| Monthly | Prune `plays.og_png` older than 30 days (~50 KB / play) | `psql "$BBG_DB_URL" -c "UPDATE plays SET og_png = NULL WHERE completed_at < NOW() - INTERVAL '30 days'"` | Operator |
| Monthly | Verify DO Postgres backup retention | DO dashboard | DO-managed |
| Monthly | Resend delivery health (bounces, complaint rate) | Resend dashboard | Operator |
| Quarterly | Trim `events` table > 90 days | `psql "$BBG_DB_URL" -c "DELETE FROM events WHERE at < NOW() - INTERVAL '90 days'"` | Operator |
| Quarterly | Prune orphan anonymous users (no cookies/plays/decoys, >90 days) | Custom SQL | Operator |

---

## Direct DB pokes

When the CLI can't answer something cheaply (and it isn't worth
extending for), use `psql` against `BBG_DB_URL`:

```
psql "$BBG_DB_URL" -v ON_ERROR_STOP=1 <<'SQL'
SELECT count(*) FROM plays;
SQL
```

Local docker shortcut (avoids trusted-sources dance):

```
docker exec deploy-postgres-1 psql -U bbg -d bbg -t -A -c "SELECT count(*) FROM plays;"
```

For heredocs into `docker exec`, **always pass `-i`** — without it the
SQL is silently dropped and the command exits 0:

```
docker exec -i deploy-postgres-1 psql -U bbg -d bbg -v ON_ERROR_STOP=1 <<'SQL'
SELECT ...
SQL
```

---

## Stop-and-confirm rules

These apply on every operator session. The agent reads them top-to-
bottom before running anything.

1. **Any write against a non-local DB → ask the user first.**
   "Non-local" = `BBG_DB_URL` doesn't start with
   `postgres://bbg:bbg@localhost`. State what you're about to write
   and wait for explicit confirmation.
2. `decoy bulk-review`, `prelaunch bulk-review`, `bot bulk-review`,
   `puzzle delete` → confirm count and scope before running.
3. `puzzle replace` → destroys plays. Require `--confirm-plays N` to
   match the actual count and surface the loss to the user before
   running.
4. `bbg-bot-candidates` (with API key) → confirm prompt and `--n`
   first. It costs Anthropic API tokens.
5. After any composing write, run `puzzle show` and read back what
   landed. The DB is the source of truth; the success envelope only
   tells you the call returned.
6. Never echo `BBG_DB_URL`, `BBG_RESEND_API_KEY`, `BBG_VAPID_PRIVATE`,
   or `BBG_ANTHROPIC_API_KEY` back to the user — those values may
   carry production secrets.
