-- +goose Up
-- +goose StatementBegin

-- Block handle impersonation across case variants. Without this, user A
-- claiming "Alice" doesn't prevent user B from claiming "alice" — both
-- show as different rows but read identically on the leaderboard, and
-- the lowercased reserved-handle list is bypassable ("aDmIn" trivially
-- evades a literal lookup of "admin").
--
-- The existing UNIQUE on `handle` stays as a safety belt for exact-case
-- collisions; this partial functional index covers the rest.
CREATE UNIQUE INDEX users_handle_lower_unique
    ON users (LOWER(handle))
    WHERE deleted_at IS NULL AND handle IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS users_handle_lower_unique;
-- +goose StatementEnd
