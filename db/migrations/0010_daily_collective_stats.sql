-- +goose Up
-- +goose StatementBegin

-- Collective "yesterday" scoreboard. The result page and shared text card
-- both want to surface a flat, dry stat: "Yesterday, humans caught the goose
-- X%." We freeze the number per-puzzle so it's:
--   - identical for every player who sees it,
--   - stable across the day (no drifting partial-day numbers),
--   - screenshot-proof.
--
-- Population: the nightly rollup computes round(AVG(score_pct)) over the
-- completed plays of the most recent puzzle whose puzzle_date < current UTC
-- date and upserts one row keyed by puzzle_number. The application layer
-- enforces the floor on total_plays (skipped when too few plays to be
-- meaningful) before reading; we store the raw count here so the rule can
-- be tuned without a schema change.

CREATE TABLE daily_collective_stats (
    puzzle_number   INT          PRIMARY KEY,
    stat_date       DATE         NOT NULL,
    catch_pct       INT          NOT NULL,   -- 0..100, round(AVG(score_pct))
    total_plays     INT          NOT NULL,
    computed_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS daily_collective_stats;

-- +goose StatementEnd
