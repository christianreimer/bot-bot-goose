-- +goose Up
-- +goose StatementBegin

-- pre_launch_submissions previously had two terminal states reachable from
-- the harvest reviewer: still-pending (ingested_decoy_id IS NULL) and
-- ingested (ingested_decoy_id set). Rejection was destructive — a SQL
-- DELETE that lost the original text and the IP needed for spam triage.
--
-- This migration adds a third terminal state: soft-reject via rejected_at.
-- Once set, the row is excluded from the harvest deck's "under-supplied"
-- counter (so the rejecting reviewer's intent isn't undone by the prompt
-- re-surfacing in the deck), but the row stays around for audit and for
-- the harvest stats query.

ALTER TABLE pre_launch_submissions
    ADD COLUMN rejected_at TIMESTAMPTZ NULL;

-- The harvest deck counter only cares about live (non-rejected) rows; a
-- partial index keeps that filter cheap as the rejected pile grows.
CREATE INDEX pre_launch_live_prompt_idx
    ON pre_launch_submissions (prompt_id)
    WHERE rejected_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS pre_launch_live_prompt_idx;
ALTER TABLE pre_launch_submissions DROP COLUMN IF EXISTS rejected_at;
-- +goose StatementEnd
