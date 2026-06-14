-- +goose Up
-- +goose StatementBegin

-- Default user handle renamed from "AnonymousGoose<n>" to "Human<n>".
-- Same shape (prefix + monotonic integer) so the share/leaderboard
-- columns keep their typographic feel — just shorter, plainer.
--
-- The migration:
--   1. mints `human_seq` starting where the old `anonymous_goose_seq`
--      left off so newly-assigned handles don't collide with anything
--      already on a row;
--   2. backfills live "AnonymousGoose<n>" users to "Human<n>" using the
--      same <n>. A NOT EXISTS guard skips the (unlikely) row where a
--      manually-picked "Human<n>" already squats the target — that
--      anonymous user keeps their old handle and can rename via /me;
--   3. drops `anonymous_goose_seq`.
--
-- CreateAnonymousUser in internal/db/users.go is updated in the same
-- commit to INSERT 'Human' || nextval('human_seq').

CREATE SEQUENCE IF NOT EXISTS human_seq START 1;

-- Carry over the counter so handle numbering continues monotonically.
-- pg_sequences exposes last_value as a regular row; the cast guards
-- against the (impossible-in-prod) case where 0007 was never applied.
DO $$
DECLARE
    carry BIGINT;
BEGIN
    SELECT last_value INTO carry
      FROM pg_sequences
     WHERE schemaname = current_schema()
       AND sequencename = 'anonymous_goose_seq';
    IF carry IS NOT NULL THEN
        PERFORM setval('human_seq', carry);
    END IF;
END$$;

-- Backfill. The substring picks off everything after the 14-char
-- "AnonymousGoose" prefix; ::int rejects any malformed legacy row
-- (we'd rather abort the migration than silently rewrite a user's
-- self-picked handle).
UPDATE users u
   SET handle = 'Human' || substring(u.handle FROM 15)::int
 WHERE u.handle LIKE 'AnonymousGoose%'
   AND u.deleted_at IS NULL
   AND NOT EXISTS (
       SELECT 1 FROM users u2
        WHERE u2.id <> u.id
          AND u2.handle = 'Human' || substring(u.handle FROM 15)::int
   );

DROP SEQUENCE IF EXISTS anonymous_goose_seq;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Mirror of Up. The renamed rows are lossy to undo if anyone has
-- since changed their handle, so we only re-rename users still
-- carrying the auto-assigned shape "Human<n>". Anyone who's since
-- picked a custom handle stays unchanged.
CREATE SEQUENCE IF NOT EXISTS anonymous_goose_seq START 1;

DO $$
DECLARE
    carry BIGINT;
BEGIN
    SELECT last_value INTO carry
      FROM pg_sequences
     WHERE schemaname = current_schema()
       AND sequencename = 'human_seq';
    IF carry IS NOT NULL THEN
        PERFORM setval('anonymous_goose_seq', carry);
    END IF;
END$$;

UPDATE users u
   SET handle = 'AnonymousGoose' || substring(u.handle FROM 6)::int
 WHERE u.handle ~ '^Human[0-9]+$'
   AND u.deleted_at IS NULL
   AND NOT EXISTS (
       SELECT 1 FROM users u2
        WHERE u2.id <> u.id
          AND u2.handle = 'AnonymousGoose' || substring(u.handle FROM 6)::int
   );

DROP SEQUENCE IF EXISTS human_seq;

-- +goose StatementEnd
