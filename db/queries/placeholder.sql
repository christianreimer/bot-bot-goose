-- This directory is the future home of sqlc query files. The codebase currently
-- uses hand-rolled pgx queries in internal/db/; the sqlc adoption PR will
-- replace them with one file per domain (puzzles.sql, plays.sql, decoys.sql,
-- users.sql, leaderboard.sql, moderation.sql) matching the function names in
-- internal/db/.
--
-- Until then this placeholder exists so `make sqlc` doesn't fail on a missing
-- queries directory.

-- name: Placeholder :one
SELECT 1 AS one;
