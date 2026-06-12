-- +goose Up
-- +goose StatementBegin

-- Drop the email cleartext from magic_links. The token itself now carries
-- the email under HMAC (see internal/auth.IssueMagicToken). The table now
-- holds only sha256(token) — enough to enforce one-time-use + expiry, but
-- not enough to recover the recipient address from a DB dump.
--
-- Outstanding unconsumed tokens at the moment of migration become
-- unusable (the new code path expects email-in-token, the old rows have
-- only token_hash). 15 minutes after deploy the table is consistent
-- again — every old row has expired or been consumed.

DROP INDEX IF EXISTS magic_links_email_idx;
ALTER TABLE magic_links DROP COLUMN email;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Reversible structurally, but the data is lost — we never had it.
ALTER TABLE magic_links ADD COLUMN email CITEXT;
CREATE INDEX magic_links_email_idx ON magic_links (email) WHERE consumed_at IS NULL;

-- +goose StatementEnd
