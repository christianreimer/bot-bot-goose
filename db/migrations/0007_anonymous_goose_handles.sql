-- +goose Up
-- +goose StatementBegin

-- Anonymous users now get an auto-assigned handle of the form
-- "AnonymousGoose<n>" where <n> is drawn from a dedicated sequence.
-- This replaces the prior "all NULL handles render as 'anonymous'"
-- behavior: every user has a real, stable display name from creation.
--
-- The sequence is INCREMENTed at INSERT time by CreateAnonymousUser in
-- internal/db/users.go. The manual handle-picker in handlePatchHandle
-- reserves the "AnonymousGoose*" prefix so a player can't claim a
-- handle in this namespace and risk collision with the auto-assigner.
CREATE SEQUENCE IF NOT EXISTS anonymous_goose_seq START 1;

-- Backfill all existing live users whose handle is NULL or empty.
-- nextval() is volatile and evaluated per-row, so each row gets a
-- unique number. Order is implementation-defined; uniqueness is what
-- matters. Soft-deleted users are skipped (they're filtered out of
-- leaderboards anyway and may collide with new picks if undeleted).
UPDATE users
   SET handle = 'AnonymousGoose' || nextval('anonymous_goose_seq')
 WHERE (handle IS NULL OR handle = '')
   AND deleted_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- We cannot undo the backfill (the prior handles were NULL, not
-- recoverable from the sequence values). Down only drops the sequence;
-- the assigned handles stay on the rows so the schema rolls back
-- without data loss.
DROP SEQUENCE IF EXISTS anonymous_goose_seq;

-- +goose StatementEnd
