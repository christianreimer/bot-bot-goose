# bbg-admin — content & operations CLI

`bbg-admin` is the single entry point for Bot Bot Goose operations. It is
designed to be driven both by humans at the terminal and by autonomous agents
(Claude / Hermes) scripting against the production database.

Two principles guide the surface:

1. **JSON is the default.** Every read verb prints structured JSON to stdout;
   every write verb prints a success envelope. Pass `--table` on read verbs to
   get a human-readable view.
2. **Errors are structured.** On failure, the process writes a JSON envelope
   to **stderr** and exits non-zero. The envelope shape is:

   ```json
   {"error": "human msg", "code": "not_found|invalid|db|has_plays|...", "details": {...}}
   ```

   Agents can branch on `code` without parsing prose.

## Subcommand groups

```
bbg-admin puzzle  <list|show|create|compose|edit|set-round|set-answer|delete|schedule>
bbg-admin decoy   <list|show|review|bulk-review>
bbg-admin bot     <list|show|review|bulk-review>
bbg-admin prompt  <list|show|create|edit|retire|delete>
bbg-admin harvest <list|show|review|bulk-review|prompts>
bbg-admin stats   <overview|players|decoys|harvest>
```

Pre-existing one-shots (unchanged): `seed`, `promote`, `vapid-gen`, `import`,
`rollup`.

## Connecting to the database

Every subcommand accepts the same `--db*` flags. **Never put the password on the
command line** — the CLI is built to read it from env or a file so it doesn't
leak into shell history or `ps` output.

### Flags

| Flag                  | Env                       | Purpose                                              |
|-----------------------|---------------------------|------------------------------------------------------|
| `--db <url>`          | `BBG_DB_URL`              | Full DSN, used verbatim. Overrides every `--db-*`.   |
| `--db-host`           | `BBG_DB_HOST`             | Hostname.                                            |
| `--db-port`           | `BBG_DB_PORT` (def 5432)  | Port (DigitalOcean managed: 25060).                  |
| `--db-name`           | `BBG_DB_NAME` (def bbg)   | Database name.                                       |
| `--db-user`           | `BBG_DB_USER` (def bbg)   | User.                                                |
| `--db-password-env`   | def `BBG_DB_PASSWORD`     | Name of env var holding the password.                |
| `--db-password-file`  | `BBG_DB_PASSWORD_FILE`    | File containing the password (trailing \n trimmed).  |
| `--db-sslmode`        |                           | `disable\|require\|verify-ca\|verify-full`. Auto: `require` for remote hosts, `disable` for localhost. |
| `--db-sslrootcert`    | `BBG_DB_SSLROOTCERT`      | Path to CA bundle (needed for `verify-ca`/`verify-full`). |

Resolution order: `--db` > `BBG_DB_URL` > assembled `--db-host`/etc. If
nothing is provided, falls back to the local-dev default
`postgres://bbg:bbg@localhost:5432/bbg?sslmode=disable`.

### Recipe: DigitalOcean Managed Postgres

```bash
BBG_DB_PASSWORD='paste-from-do-console' bbg-admin puzzle list \
  --db-host  db-bbg-do-user-xxx.b.db.ondigitalocean.com \
  --db-port  25060 \
  --db-name  defaultdb \
  --db-user  doadmin \
  --db-sslmode     verify-full \
  --db-sslrootcert /etc/bbg/do-ca.crt
```

The connection log line shows host + db + sslmode only; the password is never
printed, even on error.

## JSON shapes

### `puzzle list`

Returns an array. `puzzle_date` is `YYYY-MM-DD`; `frozen_at` is RFC3339 UTC
(it's a creation timestamp — `daily_puzzles.frozen_at` is NOT NULL DEFAULT
NOW() in the schema). Mutations are gated on the puzzle having any **plays**,
not on `frozen_at`.

```json
[
  {
    "puzzle_number": 5,
    "puzzle_date":   "2026-06-15",
    "mode":          "find_the_bot",
    "frozen_at":     "2026-06-13T09:51:02Z",
    "theme":         null
  }
]
```

### `puzzle show`

```json
{
  "puzzle_number": 5,
  "puzzle_date": "2026-06-15",
  "mode": "find_the_bot",
  "frozen_at": "2026-06-13T09:51:02Z",
  "theme": null,
  "has_plays": false,
  "rounds": [
    {
      "round_index": 0,
      "prompt_id": "uuid",
      "prompt_text": "...",
      "target_kind": "bot",
      "target_count": 1,
      "answers": [
        {
          "slot": 0,
          "content_kind": "bot",
          "answer_text": "...",
          "is_trap": false,
          "author_user_id": null,
          "bot_candidate_id": "uuid",
          "decoy_id": null
        }
      ]
    }
  ]
}
```

Slots are 0..3 in canonical (by-id) order. Per-play permutation is layered on
top at serve time — the CLI exposes the canonical view.

### Mutation success envelope

Every `create`/`edit`/`delete`/`compose`/`review` writes:

```json
{"ok": true, "action": "compose", "puzzle_number": 5, ...}
```

### Error envelope (stderr, exit 1)

```json
{"error": "...", "code": "not_found|invalid|db|has_plays|insufficient_content|referenced|limit_exceeded", "details": {...}}
```

Codes the agent should recognize:

| `code`                  | Meaning                                                                                |
|-------------------------|----------------------------------------------------------------------------------------|
| `invalid`               | Bad flag/argument. Fix and retry.                                                      |
| `not_found`             | Target row missing. Don't retry.                                                       |
| `has_plays`             | Puzzle has at least one play — refuses to mutate or delete. Don't retry.               |
| `insufficient_content`  | Not enough approved bots/decoys for the prompt to compose a round.                     |
| `referenced`            | Prompt is referenced by puzzle_rounds; use `prompt retire` instead.                    |
| `limit_exceeded`        | Bulk operation matched more rows than `--limit`. Raise `--limit` explicitly to proceed.|
| `already_decided`       | Harvest submission already ingested or rejected — pick a different id.                 |
| `db`                    | Unexpected database error. Inspect `error` field.                                      |

## Safety rails

- `puzzle delete`, `puzzle edit`, `puzzle compose`, `puzzle set-round`,
  `puzzle set-answer` all refuse with `has_plays` when any row in `plays`
  references the puzzle. Mutating an answered puzzle would corrupt the
  client-side `slot_permutation` of every prior play.
- `prompt delete` refuses with `referenced` when any `puzzle_rounds` row
  references the prompt. Use `prompt retire` (soft-retire via `retired_at`)
  instead — it removes the prompt from future composition without breaking
  historical puzzles.
- `decoy review` requires `--reviewer-email` (or `BBG_REVIEWER_EMAIL`) because
  `moderation_reviews.reviewer_user_id` is NOT NULL. The reviewer must already
  exist as a user (use `bbg-admin promote --email ... --role reviewer` once to
  create them).
- `decoy bulk-review` caps the number of rows at `--limit` (default 100) and
  refuses with `limit_exceeded` if the filter matches more — the agent must
  re-issue with an explicit higher `--limit` to confirm.
- `harvest review` and `harvest bulk-review` require `--reviewer-email` (or
  `BBG_REVIEWER_EMAIL`); the email must already be a registered user. Approve
  ingests the row into `decoy_submissions` (status='approved') in one
  transaction with `moderation_reviews` + `audit_log`. Reject soft-marks via
  `pre_launch_submissions.rejected_at`. Already-decided rows refuse with
  `already_decided` — no re-review.

## Agent workflow examples

### Schedule one week of puzzles

```bash
bbg-admin puzzle schedule --start 2026-06-15 --days 7
```

Returns one result per date, with `status` of `composed`, `skipped` (date
already present), or `failed` (with the error inline). Idempotent: re-running
the same range only composes the holes.

### Triage pending decoys

```bash
bbg-admin decoy list --status pending --limit 100 | jq -r '.[].id' | while read id; do
  bbg-admin decoy show "$id"          # inspect
  bbg-admin decoy review "$id" --decision approve --reviewer-email me@example.com
done
```

Or, with bulk:

```bash
bbg-admin decoy bulk-review \
  --decision approve --status pending \
  --prompt-id 11111111-1111-1111-1111-111111111111 \
  --reviewer-email me@example.com \
  --limit 50
```

### Replace a single round's prompt last-minute

```bash
bbg-admin puzzle set-round 12 --round 1 --prompt-id <new-prompt-uuid>
```

Re-picks fresh answers for that round only; refuses if puzzle #12 has plays.

### Compose a puzzle with deliberately picked answers

```bash
bbg-admin puzzle compose --date 2026-06-25 \
  --prompts <u1>,<u2>,<u3> \
  --round0-bots <b1> --round0-decoys <d1>,<d2>,<d3> \
  --round1-bots <b2> --round1-decoys <d4>,<d5>,<d6> \
  --round2-bots <b3> --round2-decoys <d7>,<d8>,<d9>
```

Every round is 1 bot + 3 decoys (the only mode). `--roundN-bots` and
`--roundN-decoys` are all-or-nothing — set both or neither (omitting both
falls back to random picks from the approved pool for that round). Bad
picks (not approved, wrong prompt, duplicates, wrong count) fail with
`code: "invalid"`.

`set-round` accepts the same `--bot-ids` / `--decoy-ids` flags for a single
round. `set-answer` accepts `--bot-id` / `--decoy-id` to swap a single slot's
underlying content (the new source must be approved and belong to the
round's prompt); `--text` remains for snapshot-only overrides.

## Local-dev quickstart

```bash
make dev      # postgres + migrations + server
bbg-admin prompt create --text "If your day had a soundtrack, what's track 1?" --theme music
bbg-admin puzzle compose --date 2026-06-20
bbg-admin puzzle show 2 --table
```
