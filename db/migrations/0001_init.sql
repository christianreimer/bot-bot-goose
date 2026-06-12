-- +goose Up
-- +goose StatementBegin

CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "citext";

CREATE TYPE moderation_status AS ENUM ('pending', 'approved', 'rejected', 'retired');
CREATE TYPE puzzle_mode       AS ENUM ('find_the_bot', 'find_the_human');

-- ---------- identity / sessions -------------------------------------------

CREATE TABLE users (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    handle              TEXT UNIQUE,
    email               CITEXT UNIQUE,
    email_verified_at   TIMESTAMPTZ,
    role                TEXT NOT NULL DEFAULT 'player',
    spotter_elo         NUMERIC NOT NULL DEFAULT 1200,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ
);

CREATE TABLE device_cookies (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    cookie_hash     BYTEA NOT NULL UNIQUE,
    ua              TEXT,
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX device_cookies_user_idx ON device_cookies (user_id);

CREATE TABLE sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    cookie_hash     BYTEA NOT NULL UNIQUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL
);

CREATE TABLE magic_links (
    token_hash      BYTEA PRIMARY KEY,
    email           CITEXT NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    consumed_at     TIMESTAMPTZ,
    requested_ip    INET,
    requested_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ---------- content pool --------------------------------------------------

CREATE TABLE prompts (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    text                TEXT NOT NULL,
    theme               TEXT,
    retired_at          TIMESTAMPTZ,
    created_by_user_id  UUID REFERENCES users(id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE archetypes (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug                TEXT NOT NULL UNIQUE,
    name                TEXT NOT NULL,
    tell                TEXT NOT NULL,
    difficulty          SMALLINT NOT NULL DEFAULT 1,
    prompt_template     TEXT,
    retired_at          TIMESTAMPTZ
);

CREATE TABLE bot_candidates (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    prompt_id           UUID NOT NULL REFERENCES prompts(id),
    archetype_id        UUID NOT NULL REFERENCES archetypes(id),
    text                TEXT NOT NULL,
    llm_model           TEXT,
    generator_run_id    UUID,
    status              moderation_status NOT NULL DEFAULT 'pending',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX bot_candidates_prompt_status_idx ON bot_candidates (prompt_id, status);
CREATE INDEX bot_candidates_archetype_idx     ON bot_candidates (archetype_id);

CREATE TABLE decoy_submissions (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    prompt_id           UUID NOT NULL REFERENCES prompts(id),
    user_id             UUID REFERENCES users(id),
    text                TEXT NOT NULL,
    status              moderation_status NOT NULL DEFAULT 'pending',
    is_trap             BOOLEAN NOT NULL DEFAULT false,
    ai_detector_score   NUMERIC,
    submitted_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ
);
CREATE INDEX decoy_submissions_prompt_status_idx ON decoy_submissions (prompt_id, status);
CREATE INDEX decoy_submissions_user_idx          ON decoy_submissions (user_id);
-- One scored decoy per prompt per user (per the design doc anti-gaming rules).
-- Soft-deleted rows are exempt.
CREATE UNIQUE INDEX decoy_submissions_unique_per_user_prompt
    ON decoy_submissions (user_id, prompt_id)
    WHERE user_id IS NOT NULL AND deleted_at IS NULL;

CREATE TABLE moderation_reviews (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    target_kind         TEXT NOT NULL,           -- 'bot_candidate' | 'decoy_submission' | 'prompt' | future
    target_id           UUID NOT NULL,
    reviewer_user_id    UUID NOT NULL REFERENCES users(id),
    decision            moderation_status NOT NULL,
    note                TEXT,
    reviewed_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX moderation_reviews_target_idx ON moderation_reviews (target_kind, target_id);

CREATE TABLE pre_launch_submissions (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email               CITEXT NOT NULL,
    prompt_id           UUID NOT NULL REFERENCES prompts(id),
    text                TEXT NOT NULL,
    consent_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ingested_decoy_id   UUID REFERENCES decoy_submissions(id)
);

-- ---------- seasons -------------------------------------------------------

CREATE TABLE seasons (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug                    TEXT NOT NULL UNIQUE,
    started_on              DATE NOT NULL,
    ended_on                DATE,
    archetype_roster_json   JSONB
);

-- ---------- daily puzzle --------------------------------------------------

CREATE TABLE daily_puzzles (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    puzzle_number   INT  NOT NULL UNIQUE,
    puzzle_date     DATE NOT NULL,
    mode            puzzle_mode NOT NULL DEFAULT 'find_the_bot',
    frozen_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    theme           TEXT,
    season_id       UUID REFERENCES seasons(id)
);
CREATE INDEX daily_puzzles_date_idx ON daily_puzzles (puzzle_date);

CREATE TABLE puzzle_rounds (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    daily_puzzle_id UUID NOT NULL REFERENCES daily_puzzles(id) ON DELETE CASCADE,
    round_index     SMALLINT NOT NULL,
    prompt_id       UUID NOT NULL REFERENCES prompts(id),
    target_kind     TEXT NOT NULL CHECK (target_kind IN ('bot','human')),
    target_count    SMALLINT NOT NULL DEFAULT 1,
    UNIQUE (daily_puzzle_id, round_index)
);

CREATE TABLE puzzle_round_answers (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    round_id            UUID NOT NULL REFERENCES puzzle_rounds(id) ON DELETE CASCADE,
    content_kind        TEXT NOT NULL CHECK (content_kind IN ('bot','decoy')),
    bot_candidate_id    UUID REFERENCES bot_candidates(id),
    decoy_id            UUID REFERENCES decoy_submissions(id),
    is_trap             BOOLEAN NOT NULL DEFAULT false,
    author_user_id      UUID REFERENCES users(id),
    -- denormalized text snapshot — historical puzzles must remain stable
    -- even if a candidate is retired or a user deletes their decoy.
    answer_text         TEXT NOT NULL,
    -- exactly one of (bot_candidate_id, decoy_id) is set, matching content_kind.
    CHECK ( (content_kind = 'bot'   AND bot_candidate_id IS NOT NULL AND decoy_id IS NULL)
         OR (content_kind = 'decoy' AND decoy_id IS NOT NULL AND bot_candidate_id IS NULL) )
);
CREATE INDEX puzzle_round_answers_round_idx ON puzzle_round_answers (round_id);

-- ---------- play (per user) -----------------------------------------------

CREATE TABLE plays (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    daily_puzzle_id     UUID NOT NULL REFERENCES daily_puzzles(id),
    started_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at        TIMESTAMPTZ,
    score_pct           SMALLINT,
    hmac_secret         BYTEA NOT NULL,
    UNIQUE (user_id, daily_puzzle_id)
);
CREATE INDEX plays_user_idx ON plays (user_id);

CREATE TABLE play_rounds (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    play_id             UUID NOT NULL REFERENCES plays(id) ON DELETE CASCADE,
    round_index         SMALLINT NOT NULL,
    -- slot_permutation[i] = which puzzle_round_answers row (by 0-based ordinal
    -- within the canonical, sorted set for the round) appears at client slot i.
    slot_permutation    SMALLINT[] NOT NULL,
    hint_used           BOOLEAN NOT NULL DEFAULT false,
    removed_slot        SMALLINT,
    started_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    committed_at        TIMESTAMPTZ,
    UNIQUE (play_id, round_index)
);

CREATE TABLE play_guesses (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    play_round_id       UUID NOT NULL REFERENCES play_rounds(id) ON DELETE CASCADE,
    slot                SMALLINT NOT NULL,
    outcome             TEXT NOT NULL CHECK (outcome IN ('green','yellow','red')),
    guessed_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ---------- engagement / scoring -----------------------------------------

CREATE TABLE streaks (
    user_id                     UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    current                     INT NOT NULL DEFAULT 0,
    longest                     INT NOT NULL DEFAULT 0,
    last_played_puzzle_number   INT NOT NULL DEFAULT 0
);

CREATE TABLE decoy_daily_stats (
    decoy_id        UUID NOT NULL REFERENCES decoy_submissions(id) ON DELETE CASCADE,
    stat_date       DATE NOT NULL,
    mode            puzzle_mode NOT NULL,
    impressions     INT NOT NULL DEFAULT 0,
    picked_as_bot   INT NOT NULL DEFAULT 0,
    PRIMARY KEY (decoy_id, stat_date, mode)
);

CREATE TABLE archetype_daily_stats (
    archetype_id    UUID NOT NULL REFERENCES archetypes(id) ON DELETE CASCADE,
    stat_date       DATE NOT NULL,
    mode            puzzle_mode NOT NULL,
    impressions     INT NOT NULL DEFAULT 0,
    catches         INT NOT NULL DEFAULT 0,
    PRIMARY KEY (archetype_id, stat_date, mode)
);

CREATE TABLE forger_rankings (
    user_id             UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    adjusted_fool_rate  NUMERIC NOT NULL DEFAULT 0.25,
    total_impressions   INT NOT NULL DEFAULT 0,
    total_picked_as_bot INT NOT NULL DEFAULT 0,
    tier                TEXT NOT NULL DEFAULT 'Decoy',
    computed_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ---------- push + email --------------------------------------------------

CREATE TABLE push_subscriptions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    endpoint    TEXT NOT NULL,
    p256dh      TEXT NOT NULL,
    auth        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, endpoint)
);

CREATE TABLE email_reminders (
    user_id         UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    opted_in_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_sent_at    TIMESTAMPTZ
);

-- ---------- ops / safety --------------------------------------------------

CREATE TABLE audit_log (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_user_id   UUID REFERENCES users(id),
    action          TEXT NOT NULL,
    target_kind     TEXT,
    target_id       UUID,
    payload         JSONB,
    at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX audit_log_target_idx ON audit_log (target_kind, target_id);
CREATE INDEX audit_log_at_idx     ON audit_log (at DESC);

CREATE TABLE rate_limit_buckets (
    key         TEXT PRIMARY KEY,
    tokens      NUMERIC NOT NULL,
    refilled_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID REFERENCES users(id),
    kind        TEXT NOT NULL,
    payload     JSONB,
    at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX events_kind_at_idx ON events (kind, at DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS rate_limit_buckets;
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS email_reminders;
DROP TABLE IF EXISTS push_subscriptions;
DROP TABLE IF EXISTS forger_rankings;
DROP TABLE IF EXISTS archetype_daily_stats;
DROP TABLE IF EXISTS decoy_daily_stats;
DROP TABLE IF EXISTS streaks;
DROP TABLE IF EXISTS play_guesses;
DROP TABLE IF EXISTS play_rounds;
DROP TABLE IF EXISTS plays;
DROP TABLE IF EXISTS puzzle_round_answers;
DROP TABLE IF EXISTS puzzle_rounds;
DROP TABLE IF EXISTS daily_puzzles;
DROP TABLE IF EXISTS seasons;
DROP TABLE IF EXISTS pre_launch_submissions;
DROP TABLE IF EXISTS moderation_reviews;
DROP TABLE IF EXISTS decoy_submissions;
DROP TABLE IF EXISTS bot_candidates;
DROP TABLE IF EXISTS archetypes;
DROP TABLE IF EXISTS prompts;
DROP TABLE IF EXISTS magic_links;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS device_cookies;
DROP TABLE IF EXISTS users;
DROP TYPE  IF EXISTS puzzle_mode;
DROP TYPE  IF EXISTS moderation_status;
-- +goose StatementEnd
