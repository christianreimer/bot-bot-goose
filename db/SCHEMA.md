# Database schema — Bot Bot Goose

PostgreSQL, 27 tables. Defined in `db/migrations/0001_init.sql` and patched by 0002–0010.

**Enums:**
- `moderation_status` ∈ `pending` | `approved` | `rejected` | `retired`

> The `puzzle_mode` enum and the dual-mode "Find the Human" plumbing it carried were dropped by migration `0008_drop_dual_mode.sql`. Every puzzle is now 1 bot + 3 decoys.

In example rows, UUIDs are shown as 8-char prefixes (`a1b2c3d4…`) for readability — real values are full UUIDs.

---

## 1. Identity & sessions

### `users`

The account/profile row. Anonymous device users get one too.

| Column              | Type                 | Notes                                                                |
|---------------------|----------------------|----------------------------------------------------------------------|
| `id`                | UUID PK              | `gen_random_uuid()`                                                  |
| `handle`            | TEXT UNIQUE          | nullable; case-insensitive uniqueness via partial `LOWER(handle)` idx (0004) |
| `email`             | CITEXT UNIQUE        | nullable; present after magic-link auth                              |
| `email_verified_at` | TIMESTAMPTZ          | nullable                                                             |
| `role`              | TEXT, default 'player' | also `reviewer`, `admin`                                            |
| `spotter_elo`       | NUMERIC, default 1200 | catcher skill ELO                                                   |
| `display_anonymous` | BOOL, default false  | added in 0002 — hides handle on public surfaces                      |
| `created_at`        | TIMESTAMPTZ          |                                                                      |
| `deleted_at`        | TIMESTAMPTZ          | nullable; soft delete                                                |

**FKs out:** none.
**FKs in:** referenced by ~15 tables; central identity row.

**Auto-handle on insert:** migration 0007 introduced an auto-handle sequence; migration 0012 renamed it to `human_seq` and renamed the existing `AnonymousGoose<n>` handles to `Human<n>`. The runtime `CreateAnonymousUser` assigns `'Human' || nextval('human_seq')` on every new device cookie, so the `handle` column is effectively always non-empty on live rows. The manual-handle path reserves the `Human<digits>` shape to prevent collision (substrings like `humanity` or `humanlike` stay pickable).

**Example rows:**

| id           | handle               | email                 | role     | spotter_elo | display_anonymous |
|--------------|----------------------|-----------------------|----------|-------------|-------------------|
| `a1b2c3d4…`  | `alice`              | `alice@example.com`   | player   | 1247        | false             |
| `b5c6d7e8…`  | `bob_42`             | `bob@example.com`     | reviewer | 1380        | true              |
| `c9d0e1f2…`  | `Human1184`          | *(null)*              | player   | 1200        | false             |

### `device_cookies`

Maps a hashed cookie to a user — anonymous session identity.

| Column         | Type                            | Notes                       |
|----------------|---------------------------------|-----------------------------|
| `id`           | UUID PK                         |                             |
| `user_id`      | UUID NOT NULL                   | FK `→ users.id` CASCADE     |
| `cookie_hash`  | BYTEA NOT NULL UNIQUE           | `sha256(cleartext)`         |
| `ua`           | TEXT                            | user-agent                  |
| `last_seen_at` | TIMESTAMPTZ, default NOW()      |                             |
| `created_at`   | TIMESTAMPTZ, default NOW()      |                             |

**FKs out:** `user_id → users.id` (CASCADE).

**Example rows:**

| id          | user_id      | cookie_hash       | ua                | last_seen_at           |
|-------------|--------------|-------------------|-------------------|------------------------|
| `d1…`       | `a1b2c3d4…`  | `\x9a8b…(32B)`    | Chrome/iOS        | 2026-06-13 10:13:17+00 |
| `d2…`       | `c9d0e1f2…`  | `\x4f7e…(32B)`    | Safari/Mac        | 2026-06-12 22:01:55+00 |

### `sessions`

Email-based authenticated sessions (post magic-link).

| Column         | Type                  | Notes                    |
|----------------|-----------------------|--------------------------|
| `id`           | UUID PK               |                          |
| `user_id`      | UUID NOT NULL         | FK `→ users.id` CASCADE  |
| `cookie_hash`  | BYTEA NOT NULL UNIQUE |                          |
| `created_at`   | TIMESTAMPTZ           |                          |
| `expires_at`   | TIMESTAMPTZ NOT NULL  |                          |

**Example rows:**

| id    | user_id     | expires_at             |
|-------|-------------|------------------------|
| `s1…` | `a1b2c3d4…` | 2026-07-13 10:13:17+00 |

### `magic_links`

One-time auth tokens. Migration 0003 dropped `email` — it travels HMAC-signed inside the token; the table only holds `sha256(token)` so a DB dump can't reveal recipient addresses.

| Column         | Type                       | Notes                            |
|----------------|----------------------------|----------------------------------|
| `token_hash`   | BYTEA PK                   |                                  |
| `expires_at`   | TIMESTAMPTZ NOT NULL       |                                  |
| `consumed_at`  | TIMESTAMPTZ                | one-time use                     |
| `requested_ip` | INET                       | abuse triage                     |
| `requested_at` | TIMESTAMPTZ, default NOW() |                                  |

**FKs out:** none (intentional).

**Example rows:**

| token_hash       | expires_at             | consumed_at            | requested_ip |
|------------------|------------------------|------------------------|--------------|
| `\xab12…(32B)`   | 2026-06-13 10:28:00+00 | 2026-06-13 10:14:55+00 | `203.0.113.7`|
| `\xcd34…(32B)`   | 2026-06-13 10:30:00+00 | *(null)*               | `198.51.100.4`|

---

## 2. Content pool

### `prompts`

The question text shown atop each round.

| Column                | Type                       | Notes                                |
|-----------------------|----------------------------|--------------------------------------|
| `id`                  | UUID PK                    |                                      |
| `text`                | TEXT NOT NULL              |                                      |
| `theme`               | TEXT                       | nullable tag                         |
| `retired_at`          | TIMESTAMPTZ                | soft retire                          |
| `created_by_user_id`  | UUID                       | FK `→ users.id`                      |
| `created_at`          | TIMESTAMPTZ, default NOW() |                                      |

**FKs in:** `bot_candidates.prompt_id`, `decoy_submissions.prompt_id`, `puzzle_rounds.prompt_id`, `pre_launch_submissions.prompt_id`.

**Example rows:**

| id          | text                                                | theme | retired_at | created_by_user_id |
|-------------|-----------------------------------------------------|-------|------------|--------------------|
| `p1a2b3c4…` | What's the worst advice you've ever been given?     | -     | *(null)*   | *(null)*           |
| `p5d6e7f8…` | Describe your morning routine in one sentence.      | -     | *(null)*   | *(null)*           |
| `p9a0b1c2…` | What would you do with one extra hour every day?    | -     | *(null)*   | *(null)*           |

### `archetypes`

The 8-archetype roster that shapes how bots write.

| Column            | Type                | Notes                              |
|-------------------|---------------------|------------------------------------|
| `id`              | UUID PK             |                                    |
| `slug`            | TEXT NOT NULL UNIQUE | stable identity (e.g., `hedger`)  |
| `name`            | TEXT NOT NULL       | human name                         |
| `tell`            | TEXT NOT NULL       | one-line description of the "tell" |
| `difficulty`      | SMALLINT, default 1 | 1..5                                |
| `prompt_template` | TEXT                | generator template                 |
| `retired_at`      | TIMESTAMPTZ         |                                    |

**FKs in:** `bot_candidates.archetype_id`, `archetype_daily_stats.archetype_id`.

**Example rows:**

| id          | slug      | name        | tell                                      | difficulty |
|-------------|-----------|-------------|-------------------------------------------|------------|
| `ar1…`      | `hedger`  | The Hedger  | softens every claim with qualifiers       | 2          |
| `ar2…`      | `lecturer`| The Lecturer| answers like a slightly bored textbook    | 3          |
| `ar3…`      | `mirror`  | The Mirror  | echoes the prompt's structure back at you | 5          |

### `bot_candidates`

LLM-generated bot answers in the moderation pool.

| Column              | Type                                       | Notes                                  |
|---------------------|--------------------------------------------|----------------------------------------|
| `id`                | UUID PK                                    |                                        |
| `prompt_id`         | UUID NOT NULL                              | FK `→ prompts.id`                      |
| `archetype_id`      | UUID NOT NULL                              | FK `→ archetypes.id`                   |
| `text`              | TEXT NOT NULL                              |                                        |
| `llm_model`         | TEXT                                       | e.g. `claude-opus-4-7`                 |
| `generator_run_id`  | UUID                                       | groups a batch                         |
| `status`            | `moderation_status`, default `pending`     |                                        |
| `created_at`        | TIMESTAMPTZ                                |                                        |

**FKs in:** `puzzle_round_answers.bot_candidate_id`.
**Indexed:** `(prompt_id, status)` for the composer's "approved bots for this prompt" lookup; `(archetype_id)`.

**Example rows:**

| id          | prompt_id   | archetype_id | text                                                                                                         | status   |
|-------------|-------------|--------------|--------------------------------------------------------------------------------------------------------------|----------|
| `b1…`       | `p1a2b3c4…` | `ar2…`       | Someone once told me to never accept criticism. Looking back, I realize that learning to embrace constructive feedback has been essential for both personal and professional growth. | approved |
| `b2…`       | `p5d6e7f8…` | `ar1…`       | I begin each morning with a glass of water, a few minutes of mindfulness, and a healthy breakfast.           | approved |
| `b3…`       | `p9a0b1c2…` | `ar1…`       | I would dedicate that time to reading, exercising, and connecting with loved ones.                           | pending  |

### `decoy_submissions`

User-submitted human answers (the contribution loop).

| Column               | Type                                    | Notes                                            |
|----------------------|-----------------------------------------|--------------------------------------------------|
| `id`                 | UUID PK                                 |                                                  |
| `prompt_id`          | UUID NOT NULL                           | FK `→ prompts.id`                                |
| `user_id`            | UUID                                    | FK `→ users.id`; nullable for seed/system rows   |
| `text`               | TEXT NOT NULL                           |                                                  |
| `status`             | `moderation_status`, default `pending`  |                                                  |
| `is_trap`            | BOOL, default false                     | bait set to catch bandit drift                   |
| `ai_detector_score`  | NUMERIC                                 | nullable; heuristic 0..1                         |
| `submitted_at`       | TIMESTAMPTZ                             |                                                  |
| `deleted_at`         | TIMESTAMPTZ                             | soft delete                                      |

**FKs in:** `puzzle_round_answers.decoy_id`, `decoy_daily_stats.decoy_id`, `pre_launch_submissions.ingested_decoy_id`.
**Unique partial index:** `(user_id, prompt_id) WHERE user_id IS NOT NULL AND deleted_at IS NULL` — one decoy per user per prompt.

**Example rows:**

| id          | prompt_id   | user_id      | text                                                                                       | status   |
|-------------|-------------|--------------|--------------------------------------------------------------------------------------------|----------|
| `dc1…`      | `p1a2b3c4…` | `a1b2c3d4…`  | my uncle told me to dump my savings into beanie babies in 2003. man still has a tub in his garage labeled 'retirement' | approved |
| `dc2…`      | `p5d6e7f8…` | `b5c6d7e8…`  | alarm at 6, snooze until 6:54, panic, leave.                                               | approved |
| `dc3…`      | `p9a0b1c2…` | `a1b2c3d4…`  | learn bass. i've said this for 9 years. i will not learn bass.                             | pending  |

### `moderation_reviews`

Reviewer paper trail across content kinds.

| Column              | Type                       | Notes                                                       |
|---------------------|----------------------------|-------------------------------------------------------------|
| `id`                | UUID PK                    |                                                             |
| `target_kind`       | TEXT NOT NULL              | `'bot_candidate'` \| `'decoy_submission'` \| `'prompt'`     |
| `target_id`         | UUID NOT NULL              | **no FK** — see Non-FK joins                                |
| `reviewer_user_id`  | UUID NOT NULL              | FK `→ users.id`                                             |
| `decision`          | `moderation_status` NOT NULL |                                                            |
| `note`              | TEXT                       |                                                             |
| `reviewed_at`       | TIMESTAMPTZ                |                                                             |

**Non-FK join (polymorphic):** `(target_kind, target_id)` points to whichever of `bot_candidates` / `decoy_submissions` / `prompts` matches `target_kind`. No FK declared because the table fans out to multiple parents. Indexed on `(target_kind, target_id)`.

**Example rows:**

| id    | target_kind        | target_id | reviewer_user_id | decision | note                |
|-------|--------------------|-----------|------------------|----------|---------------------|
| `m1…` | `decoy_submission` | `dc1…`    | `b5c6d7e8…`      | approved | good voice          |
| `m2…` | `decoy_submission` | `dc3…`    | `b5c6d7e8…`      | rejected | low effort          |
| `m3…` | `bot_candidate`    | `b3…`     | `b5c6d7e8…`      | approved | reads natural       |

### `pre_launch_submissions`

Reddit-prelaunch campaign submissions. Migration 0005 made them anonymous-capable; 0006 added soft-reject.

| Column                | Type                       | Notes                                                |
|-----------------------|----------------------------|------------------------------------------------------|
| `id`                  | UUID PK                    |                                                      |
| `email`               | CITEXT                     | nullable post-0005                                   |
| `prompt_id`           | UUID NOT NULL              | FK `→ prompts.id`                                    |
| `text`                | TEXT NOT NULL              |                                                      |
| `consent_at`          | TIMESTAMPTZ                |                                                      |
| `ingested_decoy_id`   | UUID                       | FK `→ decoy_submissions.id`; bridge into live pool   |
| `user_id`             | UUID                       | FK `→ users.id` (SET NULL); added in 0005            |
| `requested_ip`        | INET                       | added in 0005                                        |
| `rejected_at`         | TIMESTAMPTZ                | nullable; soft-reject from prelaunch reviewer (0006)   |

**Three terminal states (post-0006):** still-pending (`ingested_decoy_id IS NULL AND rejected_at IS NULL`), ingested (`ingested_decoy_id` set), soft-rejected (`rejected_at` set). The text + IP stay on rejected rows for spam triage instead of being destroyed by DELETE.

**Unique partial index:** `(user_id, prompt_id) WHERE user_id IS NOT NULL` — one per device per prompt.
**Plain index:** `(prompt_id)` for the prelaunch-deck counter.
**Partial index (0006):** `(prompt_id) WHERE rejected_at IS NULL` — keeps the under-supplied counter cheap as the rejected pile grows.

**Example rows:**

| id    | email             | prompt_id   | text                                  | ingested_decoy_id | rejected_at            | user_id     |
|-------|-------------------|-------------|---------------------------------------|-------------------|------------------------|-------------|
| `pl1…`| *(null)*          | `p1a2b3c4…` | take the freeway, never the bridge    | `dc4…`            | *(null)*               | `c9d0e1f2…` |
| `pl2…`| `fan@example.com` | `p5d6e7f8…` | I open my laptop before my eyes work. | *(null)*          | *(null)*               | *(null)*    |
| `pl3…`| *(null)*          | `p9a0b1c2…` | asdfasdf                              | *(null)*          | 2026-06-12 18:02:11+00 | `c9d0e1f2…` |

---

## 3. Daily puzzle

### `seasons`

Long-lived buckets carrying a roster snapshot.

| Column                  | Type             | Notes                       |
|-------------------------|------------------|-----------------------------|
| `id`                    | UUID PK          |                             |
| `slug`                  | TEXT NOT NULL UNIQUE | e.g., `summer-26`       |
| `started_on`            | DATE NOT NULL    |                             |
| `ended_on`              | DATE             |                             |
| `archetype_roster_json` | JSONB            | frozen roster per season    |

**FKs in:** `daily_puzzles.season_id`.

**Example rows:**

| id    | slug         | started_on | ended_on | archetype_roster_json (excerpt)             |
|-------|--------------|------------|----------|---------------------------------------------|
| `se1…`| `season-1`   | 2026-04-01 | *(null)* | `[{"slug":"hedger","weight":1.0}, ...]`     |

### `daily_puzzles`

One row per playable date. The `mode` column was dropped by migration 0008 (single mode).

| Column          | Type                           | Notes                                                                |
|-----------------|--------------------------------|----------------------------------------------------------------------|
| `id`            | UUID PK                        |                                                                      |
| `puzzle_number` | INT NOT NULL UNIQUE            | the natural/external key (e.g. "puzzle #142")                        |
| `puzzle_date`   | DATE NOT NULL                  |                                                                      |
| `frozen_at`     | TIMESTAMPTZ NOT NULL, default NOW() | **informational**, not an editability gate — set on insert     |
| `theme`         | TEXT                           |                                                                      |
| `season_id`     | UUID                           | FK `→ seasons.id`                                                    |

**FKs in:** `puzzle_rounds.daily_puzzle_id` (CASCADE), `plays.daily_puzzle_id`.
**Indexed on `puzzle_date`.**
**Non-FK ref in:** `streaks.last_played_puzzle_number` carries `puzzle_number` (the natural key), not `id`. `daily_collective_stats.puzzle_number` does too (see §5).

**Example rows:**

| id    | puzzle_number | puzzle_date | frozen_at              | theme |
|-------|---------------|-------------|------------------------|-------|
| `dp1…`| 1             | 2026-06-12  | 2026-06-12 00:00:00+00 | -     |
| `dp2…`| 2             | 2026-06-13  | 2026-06-12 12:00:00+00 | -     |
| `dp3…`| 6             | 2026-06-17  | 2026-06-12 12:00:00+00 | music |

### `puzzle_rounds`

The three rounds of a puzzle. The `target_kind` column was dropped by migration 0008 (every round targets the bot now).

| Column            | Type                  | Notes                                            |
|-------------------|-----------------------|--------------------------------------------------|
| `id`              | UUID PK               |                                                  |
| `daily_puzzle_id` | UUID NOT NULL         | FK `→ daily_puzzles.id` CASCADE                  |
| `round_index`     | SMALLINT NOT NULL     | 0, 1, or 2                                       |
| `prompt_id`       | UUID NOT NULL         | FK `→ prompts.id`                                |
| `target_count`    | SMALLINT, default 1   | forward-compat for variable-target days          |

**Unique:** `(daily_puzzle_id, round_index)`.
**FKs in:** `puzzle_round_answers.round_id` (CASCADE).

**Example rows:**

| id    | daily_puzzle_id | round_index | prompt_id   | target_count |
|-------|-----------------|-------------|-------------|--------------|
| `r1…` | `dp1…`          | 0           | `p1a2b3c4…` | 1            |
| `r2…` | `dp1…`          | 1           | `p5d6e7f8…` | 1            |
| `r3…` | `dp1…`          | 2           | `p9a0b1c2…` | 1            |

### `puzzle_round_answers`

The 4 answer choices per round — the canonical pool the play-time `slot_permutation` shuffles into.

| Column              | Type                          | Notes                                                                 |
|---------------------|-------------------------------|-----------------------------------------------------------------------|
| `id`                | UUID PK                       | also the canonical ordering key (`ORDER BY id`)                       |
| `round_id`          | UUID NOT NULL                 | FK `→ puzzle_rounds.id` CASCADE                                       |
| `content_kind`      | TEXT CHECK ∈ ('bot','decoy')  |                                                                       |
| `bot_candidate_id`  | UUID                          | FK `→ bot_candidates.id`; **exactly one** of (bot_candidate_id, decoy_id) is set, matching `content_kind` |
| `decoy_id`          | UUID                          | FK `→ decoy_submissions.id`                                           |
| `is_trap`           | BOOL, default false           |                                                                       |
| `author_user_id`    | UUID                          | FK `→ users.id`; nullable                                             |
| `answer_text`       | TEXT NOT NULL                 | **denormalized snapshot** — historical puzzles read this directly     |

**Tagged-union CHECK:** enforces the bot/decoy column matches `content_kind`.
**Indexed on `round_id`.**

**Example rows** (canonical order = by id; `slot_permutation` reorders per play):

| id    | round_id | content_kind | bot_candidate_id | decoy_id | is_trap | author_user_id | answer_text                                                              |
|-------|----------|--------------|------------------|----------|---------|----------------|--------------------------------------------------------------------------|
| `a1…` | `r1…`    | bot          | `b1…`            | *(null)* | false   | *(null)*       | Someone once told me to never accept criticism. Looking back…            |
| `a2…` | `r1…`    | decoy        | *(null)*         | `dc1…`   | false   | `a1b2c3d4…`    | my uncle told me to dump my savings into beanie babies in 2003…          |
| `a3…` | `r1…`    | decoy        | *(null)*         | `dc5…`   | false   | `c9d0e1f2…`    | 'just be yourself' before a job interview. myself does not want the job. |
| `a4…` | `r1…`    | decoy        | *(null)*         | `dc6…`   | false   | `b5c6d7e8…`    | a teacher said don't rely on a calculator…                               |

---

## 4. Per-user play state

### `plays`

One per (user, puzzle).

| Column            | Type                  | Notes                                              |
|-------------------|-----------------------|----------------------------------------------------|
| `id`              | UUID PK               |                                                    |
| `user_id`         | UUID NOT NULL         | FK `→ users.id` CASCADE                            |
| `daily_puzzle_id` | UUID NOT NULL         | FK `→ daily_puzzles.id`                            |
| `started_at`      | TIMESTAMPTZ           |                                                    |
| `completed_at`    | TIMESTAMPTZ           | nullable                                           |
| `score_pct`       | SMALLINT              | 0..100                                             |
| `hmac_secret`     | BYTEA NOT NULL        | per-play random secret used to sign client tokens  |

**Unique:** `(user_id, daily_puzzle_id)`.
**FKs in:** `play_rounds.play_id` (CASCADE).

**Example rows:**

| id    | user_id     | daily_puzzle_id | started_at             | completed_at           | score_pct |
|-------|-------------|-----------------|------------------------|------------------------|-----------|
| `p1…` | `a1b2c3d4…` | `dp1…`          | 2026-06-12 13:04:11+00 | 2026-06-12 13:06:58+00 | 67        |
| `p2…` | `b5c6d7e8…` | `dp1…`          | 2026-06-12 19:22:00+00 | *(null)*               | *(null)*  |

### `play_rounds`

One row per round inside a play.

| Column              | Type                       | Notes                                                                  |
|---------------------|----------------------------|------------------------------------------------------------------------|
| `id`                | UUID PK                    |                                                                        |
| `play_id`           | UUID NOT NULL              | FK `→ plays.id` CASCADE                                                |
| `round_index`       | SMALLINT NOT NULL          | 0/1/2                                                                  |
| `slot_permutation`  | SMALLINT[] NOT NULL        | `slot_permutation[i]` = canonical ordinal at client slot `i`           |
| `hint_used`         | BOOL, default false        |                                                                        |
| `removed_slot`      | SMALLINT                   | nullable; remembered hint                                              |
| `started_at`        | TIMESTAMPTZ                |                                                                        |
| `committed_at`      | TIMESTAMPTZ                | once the guess locks in                                                |
| `realest_decoy_id`  | UUID                       | FK `→ decoy_submissions.id` (SET NULL); the player's post-reveal "felt most human?" vote (0009). NULL = vote skipped. Re-voting on the same round overwrites this. |

**Unique:** `(play_id, round_index)`.
**Non-FK join to `puzzle_rounds`:** matched by `(plays.daily_puzzle_id, round_index)` — no direct FK. Pattern from `internal/db/play.go:320-322`:

```sql
JOIN plays p ON p.id = pr.play_id
JOIN puzzle_rounds qr ON qr.daily_puzzle_id = p.daily_puzzle_id
                      AND qr.round_index = pr.round_index
JOIN puzzle_round_answers pra ON pra.round_id = qr.id
```

**Non-FK ref (array of ordinals):** `slot_permutation[i]` is the 0-based index into `puzzle_round_answers ORDER BY id` for that round. Not a UUID, not a FK — a tiny int that indexes into a query result. Anti-cheat shuffle.

**Example rows:**

| id     | play_id | round_index | slot_permutation | hint_used | removed_slot | committed_at           |
|--------|---------|-------------|------------------|-----------|--------------|------------------------|
| `pr1…` | `p1…`   | 0           | `{2,0,3,1}`      | false     | *(null)*     | 2026-06-12 13:04:48+00 |
| `pr2…` | `p1…`   | 1           | `{1,3,0,2}`      | true      | 2            | 2026-06-12 13:05:35+00 |
| `pr3…` | `p1…`   | 2           | `{0,1,2,3}`      | false     | *(null)*     | 2026-06-12 13:06:58+00 |

> Reading row `pr1…`: client slot 0 shows canonical answer 2, slot 1 shows canonical answer 0, etc. So a guess at "client slot 2" really means canonical answer 3.

### `play_guesses`

One row per slot guessed in a play round.

| Column           | Type                                       | Notes                                |
|------------------|--------------------------------------------|--------------------------------------|
| `id`             | UUID PK                                    |                                      |
| `play_round_id`  | UUID NOT NULL                              | FK `→ play_rounds.id` CASCADE        |
| `slot`           | SMALLINT NOT NULL                          | the **client-side** slot 0..3        |
| `outcome`        | TEXT CHECK ∈ ('green','yellow','red')      | green = correct, yellow = trap, red = miss |
| `guessed_at`     | TIMESTAMPTZ                                |                                      |

**Example rows:**

| id    | play_round_id | slot | outcome | guessed_at             |
|-------|---------------|------|---------|------------------------|
| `g1…` | `pr1…`        | 2    | green   | 2026-06-12 13:04:48+00 |
| `g2…` | `pr2…`        | 0    | red     | 2026-06-12 13:05:35+00 |
| `g3…` | `pr3…`        | 1    | green   | 2026-06-12 13:06:58+00 |

---

## 5. Engagement & scoring

### `streaks`

Singleton per user.

| Column                       | Type                 | Notes                                       |
|------------------------------|----------------------|---------------------------------------------|
| `user_id`                    | UUID PK              | also FK `→ users.id` CASCADE                |
| `current`                    | INT, default 0       |                                             |
| `longest`                    | INT, default 0       |                                             |
| `last_played_puzzle_number`  | INT, default 0       | natural key into `daily_puzzles.puzzle_number` (no FK) |

**Example rows:**

| user_id     | current | longest | last_played_puzzle_number |
|-------------|---------|---------|----------------------------|
| `a1b2c3d4…` | 4       | 11      | 2                          |
| `b5c6d7e8…` | 0       | 3       | 1                          |

### `decoy_daily_stats`

Per-decoy, per-day counters. Two tracks: the legacy **fool** track (display-only flavor now) and the **realest** track (the new primary ranking surface). Migration 0008 collapsed the PK to `(decoy_id, stat_date)`; migration 0009 added the realest columns.

| Column                | Type                           | Notes                              |
|-----------------------|--------------------------------|------------------------------------|
| `decoy_id`            | UUID NOT NULL                  | part of PK; FK `→ decoy_submissions.id` CASCADE |
| `stat_date`           | DATE NOT NULL                  | part of PK                         |
| `impressions`         | INT, default 0                 | fool-track impressions             |
| `picked_as_bot`       | INT, default 0                 | fool-track: how often guessers picked this decoy thinking it was the bot |
| `realest_impressions` | INT, default 0                 | realest-track impressions (added 0009) — +1 to each of the 3 human decoys when the player casts a vote |
| `realest_votes`       | INT, default 0                 | realest-track votes earned (added 0009) — +1 to the chosen decoy |

**Composite PK:** `(decoy_id, stat_date)`.

**Example rows:**

| decoy_id | stat_date  | impressions | picked_as_bot | realest_impressions | realest_votes |
|----------|------------|-------------|---------------|---------------------|---------------|
| `dc1…`   | 2026-06-12 | 312         | 47            | 201                 | 88            |
| `dc2…`   | 2026-06-12 | 298         | 18            | 198                 | 41            |

### `archetype_daily_stats`

Per-archetype catch/impression counters. Migration 0008 collapsed the PK to `(archetype_id, stat_date)`.

| Column         | Type                   | Notes                              |
|----------------|------------------------|------------------------------------|
| `archetype_id` | UUID NOT NULL          | part of PK; FK `→ archetypes.id` CASCADE |
| `stat_date`    | DATE NOT NULL          | part of PK                         |
| `impressions`  | INT, default 0         |                                    |
| `catches`      | INT, default 0         | how often guessers correctly caught this archetype |

**Composite PK:** `(archetype_id, stat_date)`.

**Example rows:**

| archetype_id | stat_date  | impressions | catches |
|--------------|------------|-------------|---------|
| `ar1…`       | 2026-06-12 | 102         | 64      |
| `ar3…`       | 2026-06-12 | 102         | 22      |

### `forger_rankings`

Cached leaderboard row per author. Table name kept for stability; the **player-facing surface is the Originals leaderboard** at `/leaderboard/originals`. Recomputed nightly by `bbg-admin rollup` → `internal/leaderboard.Rollup`.

Migration 0009 added the four `realest_*` columns alongside the legacy fool-rate columns. The leaderboard orders by `adjusted_realest_rate`; the fool columns ride along as display-only flavor on the per-line report card.

| Column                       | Type                  | Notes                                                          |
|------------------------------|-----------------------|----------------------------------------------------------------|
| `user_id`                    | UUID PK               | also FK `→ users.id` CASCADE                                   |
| `adjusted_fool_rate`         | NUMERIC, default 0.25 | 0..1; flavor only since 0009                                   |
| `total_impressions`          | INT, default 0        | fool-track impressions                                         |
| `total_picked_as_bot`        | INT, default 0        | fool-track picks                                               |
| `adjusted_realest_rate`      | NUMERIC, default 0.3333 | 0..1; **the ranked metric** (0009). Anchored at chance = 1/3 |
| `realest_total_impressions`  | INT, default 0        | realest-track impressions (0009)                               |
| `realest_total_votes`        | INT, default 0        | realest-track votes (0009)                                     |
| `realest_beyond_chance`      | INT, default 0        | `votes − impressions·(1/3)`, floored at 0 (0009)               |
| `tier`                       | TEXT, default 'Decoy' | **DB default still says 'Decoy'**, but the application writes one of the current ladder values on every rollup. The default is never observed except on a never-rolled-up user. |
| `computed_at`                | TIMESTAMPTZ           |                                                                |

**Tier ladder (current, written by `internal/leaderboard.TierFor`):** `Quiet → Voice → Standout → Unmistakable → The Realest`. The entry tier was renamed from `Decoy` to `Quiet` in the codebase (ties to the "Reads quiet" flop copy); the DB column default was left as `'Decoy'` since it isn't observable in practice.

**Example rows:**

| user_id     | adjusted_realest_rate | realest_total_impressions | realest_total_votes | realest_beyond_chance | adjusted_fool_rate | total_impressions | total_picked_as_bot | tier      |
|-------------|-----------------------|---------------------------|---------------------|-----------------------|--------------------|-------------------|---------------------|-----------|
| `a1b2c3d4…` | 0.46                  | 1208                      | 542                 | 139                   | 0.41               | 1208              | 491                 | Standout  |
| `b5c6d7e8…` | 0.24                  | 642                       | 154                 | 0                     | 0.18               | 642               | 117                 | Quiet     |

### `daily_collective_stats`

Frozen "yesterday, humans caught the goose X%" stat (migration 0010). One row per past puzzle. Computed and upserted by `internal/collective.Rollup` during the nightly job; the application layer applies a min-plays floor (currently `collective.MinPlaysFloor = 20`) before publishing.

| Column          | Type                       | Notes                                                                |
|-----------------|----------------------------|----------------------------------------------------------------------|
| `puzzle_number` | INT PK                     | natural key into `daily_puzzles.puzzle_number` (no FK declared)      |
| `stat_date`     | DATE NOT NULL              | the puzzle's date, copied from `daily_puzzles.puzzle_date`           |
| `catch_pct`     | INT NOT NULL               | 0..100, `round(AVG(score_pct))` over completed plays of that puzzle  |
| `total_plays`   | INT NOT NULL               | count of contributing plays — read-side floor decides if it qualifies |
| `computed_at`   | TIMESTAMPTZ NOT NULL       | when the row was frozen                                              |

**Why a frozen row?** The number must be identical across all viewers, stable across the day, and screenshot-proof. A live `AVG()` query would drift as more plays land. The rollup picks the most recent puzzle whose `puzzle_date < CURRENT_DATE UTC`, averages the per-play Bot-Dar, and upserts. Idempotent on `puzzle_number`.

**Surfaces using this:** result page (`Yesterday, humans caught the goose X%.`), shared text card (`Humans yesterday: X%`), and the OG image's muted footer line.

**Example rows:**

| puzzle_number | stat_date  | catch_pct | total_plays | computed_at            |
|---------------|------------|-----------|-------------|------------------------|
| 11            | 2026-06-12 | 64        | 1842        | 2026-06-13 03:01:11+00 |
| 10            | 2026-06-11 | 71        | 1620        | 2026-06-12 03:01:09+00 |

---

## 6. Notifications

### `push_subscriptions`

Web Push endpoints per user/device.

| Column       | Type                  | Notes                       |
|--------------|-----------------------|-----------------------------|
| `id`         | UUID PK               |                             |
| `user_id`    | UUID NOT NULL         | FK `→ users.id` CASCADE     |
| `endpoint`   | TEXT NOT NULL         |                             |
| `p256dh`     | TEXT NOT NULL         | client public key           |
| `auth`       | TEXT NOT NULL         | client auth secret          |
| `created_at` | TIMESTAMPTZ           |                             |

**Unique:** `(user_id, endpoint)`.

**Example rows:**

| id    | user_id     | endpoint (truncated)                    |
|-------|-------------|------------------------------------------|
| `ps1…`| `a1b2c3d4…` | `https://fcm.googleapis.com/fcm/send/…` |

### `email_reminders`

Singleton per user — opt-in flag + last-sent timestamp.

| Column         | Type            | Notes                       |
|----------------|-----------------|-----------------------------|
| `user_id`      | UUID PK         | also FK `→ users.id` CASCADE|
| `opted_in_at`  | TIMESTAMPTZ     |                             |
| `last_sent_at` | TIMESTAMPTZ     | nullable                    |

**Example rows:**

| user_id     | opted_in_at            | last_sent_at           |
|-------------|------------------------|------------------------|
| `a1b2c3d4…` | 2026-06-01 09:30:00+00 | 2026-06-13 12:00:00+00 |

---

## 7. Ops & safety

### `audit_log`

Universal audit trail. Written by `SetUserRole` (`internal/db/users.go:87`), decoy reviews (`internal/db/decoys.go:165`), and `internal/db/auth.go:196`.

| Column          | Type           | Notes                                               |
|-----------------|----------------|-----------------------------------------------------|
| `id`            | UUID PK        |                                                     |
| `actor_user_id` | UUID           | FK `→ users.id`; nullable (system actions)          |
| `action`        | TEXT NOT NULL  | free text — e.g. `role_change`, `decoy_review`      |
| `target_kind`   | TEXT           | polymorphic                                         |
| `target_id`     | UUID           | **no FK** — see Non-FK joins                        |
| `payload`       | JSONB          | per-action context                                  |
| `at`            | TIMESTAMPTZ    |                                                     |

**Non-FK join (polymorphic):** `(target_kind, target_id)` points to whichever table matches. Common kinds: `'user'`, `'decoy_submission'`, `'bot_candidate'`, `'prompt'`. Indexed on `(target_kind, target_id)` and `at DESC`.

**Example rows:**

| id    | actor_user_id | action        | target_kind        | target_id | payload                                        |
|-------|---------------|---------------|--------------------|-----------|------------------------------------------------|
| `al1…`| `b5c6d7e8…`   | role_change   | user               | `c9d0e1f2…` | `{"role":"reviewer"}`                         |
| `al2…`| `b5c6d7e8…`   | decoy_review  | decoy_submission   | `dc1…`    | `{"decision":"approved","note":"good voice"}` |
| `al3…`| *(null)*      | system_rollup | -                  | -         | `{"authors_updated":312}`                     |

### `rate_limit_buckets`

Token-bucket rate-limit state.

| Column        | Type                  | Notes                                       |
|---------------|-----------------------|---------------------------------------------|
| `key`         | TEXT PK               | caller-defined identity                     |
| `tokens`      | NUMERIC NOT NULL      |                                             |
| `refilled_at` | TIMESTAMPTZ           |                                             |

**Example rows:**

| key                              | tokens | refilled_at            |
|----------------------------------|--------|------------------------|
| `magic_link:alice@example.com`   | 4.8    | 2026-06-13 10:13:00+00 |
| `decoy_submit:c9d0e1f2…`         | 1.2    | 2026-06-13 09:58:30+00 |

### `events`

Analytics event firehose.

| Column     | Type         | Notes                              |
|------------|--------------|------------------------------------|
| `id`       | UUID PK      |                                    |
| `user_id`  | UUID         | FK `→ users.id`; nullable          |
| `kind`     | TEXT NOT NULL | e.g. `play_started`, `share_clicked` |
| `payload`  | JSONB        |                                    |
| `at`       | TIMESTAMPTZ  |                                    |

**Indexed on `(kind, at DESC)`.**

**Example rows:**

| id    | user_id     | kind          | payload                              |
|-------|-------------|---------------|--------------------------------------|
| `e1…` | `a1b2c3d4…` | play_started  | `{"puzzle":2}`                       |
| `e2…` | `a1b2c3d4…` | share_clicked | `{"surface":"emoji_grid"}`           |
| `e3…` | *(null)*    | landing_view  | `{"path":"/","referrer":"reddit"}`   |

---

## Quick FK map (who-points-at-whom)

```
users  ←—— device_cookies, sessions, plays, streaks (PK),
            push_subscriptions, email_reminders (PK),
            forger_rankings (PK), audit_log.actor,
            puzzle_round_answers.author, prompts.created_by,
            decoy_submissions.user, pre_launch_submissions.user,
            moderation_reviews.reviewer, events.user

prompts  ←—— bot_candidates, decoy_submissions, puzzle_rounds,
              pre_launch_submissions

archetypes  ←—— bot_candidates, archetype_daily_stats

bot_candidates    ←—— puzzle_round_answers.bot_candidate_id   (one branch of the tagged union)
decoy_submissions ←—— puzzle_round_answers.decoy_id           (other branch)
                  ←—— decoy_daily_stats, pre_launch_submissions.ingested_decoy_id

seasons  ←—— daily_puzzles
daily_puzzles  ←—— puzzle_rounds (CASCADE), plays
puzzle_rounds  ←—— puzzle_round_answers (CASCADE)

plays         ←—— play_rounds (CASCADE)
play_rounds   ←—— play_guesses (CASCADE)
play_rounds.realest_decoy_id  → decoy_submissions  (SET NULL; per-round vote, 0009)

daily_collective_stats  → daily_puzzles  (natural key on puzzle_number; no FK, 0010)
```

## The non-FK joins to know about

| Where                                | Join key                                                  | Why no FK                                                                 |
|--------------------------------------|-----------------------------------------------------------|----------------------------------------------------------------------------|
| `play_rounds` ↔ `puzzle_rounds`      | `(plays.daily_puzzle_id, round_index)`                    | `play_rounds` only stores `round_index`; the puzzle is reached via `plays`. Saves a column at the cost of a 3-table join (`play.go:320-322`). |
| `play_rounds.slot_permutation[i]`    | ordinal into `puzzle_round_answers ORDER BY id` for that round | Array of small ints, not UUIDs. Anti-cheat per-play shuffle.              |
| `moderation_reviews.target_*`        | polymorphic: `bot_candidate \| decoy_submission \| prompt`| One table fans out to three parents. Indexed for lookup.                  |
| `audit_log.target_*`                 | polymorphic: any table                                    | Universal trail; intentionally untyped.                                   |
| `streaks.last_played_puzzle_number`  | `daily_puzzles.puzzle_number` (natural key, not id)       | The display number, not the surrogate UUID, is what gates streak advance. |
| `daily_collective_stats.puzzle_number` | `daily_puzzles.puzzle_number` (natural key, not id)     | Same reasoning as `streaks` — the display number is the join key; soft-deleting a puzzle row is not a workflow we support. |
| `puzzle_round_answers.answer_text`   | denormalized snapshot of bot/decoy text                   | Historical puzzles must stay stable if the source row is retired or soft-deleted. |
| `pre_launch_submissions.ingested_decoy_id` | optional FK — null until the prelaunched row is promoted into the live pool | Two-stage ingest. |

## Indexes worth knowing for query cost

- `bot_candidates_prompt_status_idx (prompt_id, status)` and `decoy_submissions_prompt_status_idx (prompt_id, status)` make the composer's "approved content for this prompt" pickers fast.
- `daily_puzzles_date_idx` and the unique `puzzle_number` cover both date-based and number-based lookups.
- `audit_log_at_idx (at DESC)` makes "recent admin actions" queries cheap; same for `events_kind_at_idx`.
- `users_handle_lower_unique` (partial, on `LOWER(handle)`) prevents case-variant impersonation across the leaderboard.
