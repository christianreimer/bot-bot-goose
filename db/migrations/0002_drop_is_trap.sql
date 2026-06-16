-- +goose Up
-- +goose StatementBegin

-- Drop the is_trap boolean from decoy_submissions and puzzle_round_answers.
-- The "trap decoy" feature (curated human answers tagged as bot-looking,
-- intended to feed an adaptive-difficulty composer) was on the backlog
-- but no gameplay logic ever consumed the flag. Removing it cleans up
-- the schema and removes a confusing reviewer choice (approve vs. trap)
-- whose effect was identical in the live game.
--
-- The IF EXISTS guard makes this a no-op for fresh databases that ran
-- the updated 0001_init.sql (where the column is already absent).

ALTER TABLE decoy_submissions     DROP COLUMN IF EXISTS is_trap;
ALTER TABLE puzzle_round_answers  DROP COLUMN IF EXISTS is_trap;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE decoy_submissions     ADD COLUMN is_trap BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE puzzle_round_answers  ADD COLUMN is_trap BOOLEAN NOT NULL DEFAULT false;

-- +goose StatementEnd
