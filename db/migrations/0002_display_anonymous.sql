-- +goose Up
-- +goose StatementBegin

-- One-tap anonymous (design doc §12). When TRUE, the user keeps their
-- handle for the day they change their mind but renders as "anonymous"
-- on the leaderboard, /d/<short>, and /r/<short>.
ALTER TABLE users ADD COLUMN display_anonymous BOOLEAN NOT NULL DEFAULT false;

-- Index magic_links.email for the rate-limit + lookup paths.
CREATE INDEX magic_links_email_idx ON magic_links (email) WHERE consumed_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS magic_links_email_idx;
ALTER TABLE users DROP COLUMN IF EXISTS display_anonymous;
-- +goose StatementEnd
