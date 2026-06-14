-- +goose Up
-- +goose StatementBegin

-- Persist the per-play OG PNG so handleResultShareOG can serve bytes from a
-- single SELECT instead of an ~80ms inline render. Populated in CompletePlay;
-- handlers that hit a NULL column (historic plays, or any path where the
-- write was skipped) fall back to rendering on demand.
--
-- Sizing: ~30–80KB per completed play. The launch-capacity plan flags a
-- pruning job for og_png on plays older than 30 days as a future task —
-- it's not a launch-day concern.

ALTER TABLE plays ADD COLUMN IF NOT EXISTS og_png BYTEA;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE plays DROP COLUMN IF EXISTS og_png;

-- +goose StatementEnd
