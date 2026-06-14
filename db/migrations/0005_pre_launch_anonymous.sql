-- +goose Up
-- +goose StatementBegin

-- pre_launch_submissions was designed in 0001 for the original "give us your
-- email so we can email you when your answer goes live" campaign shape.
-- The Reddit prelaunch variant is anonymous — no email, no sign-in — so:
--
--   1. email is no longer required
--   2. we track the anonymous device-cookie user_id instead, so a returning
--      device can be served only prompts it hasn't answered yet
--   3. requested_ip is mirrored from magic_links for abuse triage
--   4. (user_id, prompt_id) is uniquely indexed when user_id is non-null —
--      this is the schema-level "one per device per prompt" guard, the same
--      shape the regular decoy_submissions table uses.

ALTER TABLE pre_launch_submissions ALTER COLUMN email DROP NOT NULL;

ALTER TABLE pre_launch_submissions
    ADD COLUMN user_id      UUID NULL REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN requested_ip INET NULL;

CREATE UNIQUE INDEX pre_launch_unique_per_user_prompt
    ON pre_launch_submissions (user_id, prompt_id)
    WHERE user_id IS NOT NULL;

-- Helps the PrelaunchDeck picker's count CTE.
CREATE INDEX pre_launch_prompt_idx ON pre_launch_submissions (prompt_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS pre_launch_prompt_idx;
DROP INDEX IF EXISTS pre_launch_unique_per_user_prompt;
ALTER TABLE pre_launch_submissions DROP COLUMN IF EXISTS requested_ip;
ALTER TABLE pre_launch_submissions DROP COLUMN IF EXISTS user_id;
-- Note: reverting the NOT NULL is unsafe if any rows have email IS NULL.
-- The down migration leaves it nullable; only the structural changes revert.
-- +goose StatementEnd
