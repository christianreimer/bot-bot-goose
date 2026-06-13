-- +goose Up
-- +goose StatementBegin

-- Bot Bot Goose is reverting from dual-mode (find_the_bot + find_the_human)
-- back to a single mode (find_the_bot). The "find the human" variant added
-- complexity across the schema, game math, leaderboard math, share renderers,
-- intro modal, footer copy, and admin tooling, with no comparable upside.
-- Game is not yet live, so this migration is destructive: any data tied to
-- find_the_human is deleted, then the schema scaffolding is removed.

-- 1. Delete plays + cascades for any find_the_human puzzle. plays.daily_puzzle_id
--    has no ON DELETE CASCADE, so we delete plays first so the puzzle DELETE
--    can proceed without an FK violation.
DELETE FROM plays
 WHERE daily_puzzle_id IN (
   SELECT id FROM daily_puzzles WHERE mode = 'find_the_human'
 );

-- 2. Delete find_the_human puzzles. puzzle_rounds + puzzle_round_answers
--    cascade via ON DELETE CASCADE on daily_puzzles.
DELETE FROM daily_puzzles WHERE mode = 'find_the_human';

-- 3. Delete stats rows that were keyed on the human mode.
DELETE FROM decoy_daily_stats     WHERE mode = 'find_the_human';
DELETE FROM archetype_daily_stats WHERE mode = 'find_the_human';

-- 4. Drop the mode column from daily_puzzles (no longer a meaningful axis).
ALTER TABLE daily_puzzles DROP COLUMN mode;

-- 5. Strip mode from the stats tables' primary key. Postgres can't ALTER a
--    composite PK in place, so drop and recreate.
ALTER TABLE decoy_daily_stats DROP CONSTRAINT decoy_daily_stats_pkey;
ALTER TABLE decoy_daily_stats DROP COLUMN mode;
ALTER TABLE decoy_daily_stats ADD PRIMARY KEY (decoy_id, stat_date);

ALTER TABLE archetype_daily_stats DROP CONSTRAINT archetype_daily_stats_pkey;
ALTER TABLE archetype_daily_stats DROP COLUMN mode;
ALTER TABLE archetype_daily_stats ADD PRIMARY KEY (archetype_id, stat_date);

-- 6. Every puzzle round now targets the bot. The target_kind column was the
--    per-round flag that picked between bot vs human; in single mode it's a
--    constant.
ALTER TABLE puzzle_rounds DROP COLUMN target_kind;

-- 7. With no more references, the enum type goes away.
DROP TYPE puzzle_mode;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Recreate the schema scaffolding. Deleted rows do NOT come back; the down
-- migration is best-effort for structure only.

CREATE TYPE puzzle_mode AS ENUM ('find_the_bot', 'find_the_human');

ALTER TABLE daily_puzzles ADD COLUMN mode puzzle_mode NOT NULL DEFAULT 'find_the_bot';

ALTER TABLE decoy_daily_stats DROP CONSTRAINT decoy_daily_stats_pkey;
ALTER TABLE decoy_daily_stats ADD COLUMN mode puzzle_mode NOT NULL DEFAULT 'find_the_bot';
ALTER TABLE decoy_daily_stats ADD PRIMARY KEY (decoy_id, stat_date, mode);

ALTER TABLE archetype_daily_stats DROP CONSTRAINT archetype_daily_stats_pkey;
ALTER TABLE archetype_daily_stats ADD COLUMN mode puzzle_mode NOT NULL DEFAULT 'find_the_bot';
ALTER TABLE archetype_daily_stats ADD PRIMARY KEY (archetype_id, stat_date, mode);

ALTER TABLE puzzle_rounds ADD COLUMN target_kind TEXT NOT NULL DEFAULT 'bot'
  CHECK (target_kind IN ('bot','human'));

-- +goose StatementEnd
