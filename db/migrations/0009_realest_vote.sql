-- +goose Up
-- +goose StatementBegin

-- Replaces "fool rate" as the ranked forger metric with "most human" voting.
-- After each round's reveal, the player optionally casts a "which felt most
-- human?" vote among the 3 human decoys. The bot/goose card is not votable.
--
-- The fool track (impressions + picked_as_bot) is left intact and becomes
-- display-only flavor copy. The new realest track runs in parallel and is
-- the new primary ranking on the forger leaderboard.
--
-- Accounting (per cast vote — skipped votes record nothing):
--   +1 realest_impressions to each of the 3 human decoys shown in the round
--   +1 realest_votes to the chosen decoy
-- Re-voting on the same round moves the vote (impressions unchanged).

-- 1. Record the player's per-round vote so a re-vote can move the count.
ALTER TABLE play_rounds
  ADD COLUMN realest_decoy_id UUID REFERENCES decoy_submissions(id) ON DELETE SET NULL;

-- 2. Parallel counters on the daily-stats roll. Defaults match the existing
--    fool columns so backfill is a no-op.
ALTER TABLE decoy_daily_stats
  ADD COLUMN realest_impressions INT NOT NULL DEFAULT 0,
  ADD COLUMN realest_votes       INT NOT NULL DEFAULT 0;

-- 3. forger_rankings carries the new adjusted rate + beyond-chance points
--    alongside the existing fool columns. The nightly rollup populates both;
--    the leaderboard now orders by adjusted_realest_rate.
ALTER TABLE forger_rankings
  ADD COLUMN adjusted_realest_rate     NUMERIC NOT NULL DEFAULT 0.3333,
  ADD COLUMN realest_total_impressions INT     NOT NULL DEFAULT 0,
  ADD COLUMN realest_total_votes       INT     NOT NULL DEFAULT 0,
  ADD COLUMN realest_beyond_chance     INT     NOT NULL DEFAULT 0;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE forger_rankings
  DROP COLUMN realest_beyond_chance,
  DROP COLUMN realest_total_votes,
  DROP COLUMN realest_total_impressions,
  DROP COLUMN adjusted_realest_rate;

ALTER TABLE decoy_daily_stats
  DROP COLUMN realest_votes,
  DROP COLUMN realest_impressions;

ALTER TABLE play_rounds
  DROP COLUMN realest_decoy_id;

-- +goose StatementEnd
