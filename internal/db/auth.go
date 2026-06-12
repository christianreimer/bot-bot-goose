package db

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// InsertMagicLink records a freshly-issued magic-link token. The table
// holds ONLY sha256(token) plus its expiry — the email is sealed inside
// the token under HMAC (see internal/auth.IssueMagicToken) and never
// stored in any row.
func (d *DB) InsertMagicLink(ctx context.Context, tokenHash []byte, expiresAt time.Time, requestedIP string) error {
	var inet any
	if addr, err := netip.ParseAddr(requestedIP); err == nil {
		inet = addr.String()
	}
	_, err := d.Exec(ctx, `
		INSERT INTO magic_links (token_hash, expires_at, requested_ip)
		VALUES ($1, $2, $3)
	`, tokenHash, expiresAt, inet)
	return err
}

// ConsumeMagicLink validates and marks a token consumed. The handler will
// have already recovered the email from the signed token; this call just
// enforces one-time-use + expiry. Returns ErrNotFound for any unusable
// token; callers must NOT differentiate (expired vs unknown vs consumed)
// in the user-facing response.
func (d *DB) ConsumeMagicLink(ctx context.Context, tokenHash []byte, now time.Time) error {
	ct, err := d.Exec(ctx, `
		UPDATE magic_links
		   SET consumed_at = $2
		 WHERE token_hash = $1
		   AND consumed_at IS NULL
		   AND expires_at > $2
	`, tokenHash, now)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UserByEmail returns the user bound to the given email, or ErrNotFound.
func (d *DB) UserByEmail(ctx context.Context, email string) (*User, error) {
	const q = `
		SELECT id, handle, email, role, spotter_elo, display_anonymous, created_at
		  FROM users
		 WHERE email = $1 AND deleted_at IS NULL
		 LIMIT 1
	`
	u := &User{}
	if err := d.QueryRow(ctx, q, email).Scan(&u.ID, &u.Handle, &u.Email, &u.Role, &u.SpotterELO, &u.DisplayAnonymous, &u.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return u, nil
}

// MergeUsers moves every owned row from `current` to `target` and deletes
// `current`. Used by the magic-link consume handler when a player who has
// been playing anonymously signs in to an existing email-bound user.
//
// This MUST run in a single transaction — a partial merge would leave
// orphaned plays + a phantom anonymous user the leaderboard would still
// count.
//
// The streaks and forger_rankings tables have user_id as PRIMARY KEY, so
// the move has to handle conflicts: prefer the target's existing row,
// drop the current's. Plays and decoys can collide too (the unique index
// `decoy_submissions_unique_per_user_prompt` for example) — same story.
func (d *DB) MergeUsers(ctx context.Context, current, target uuid.UUID, now time.Time) error {
	if current == target {
		return nil
	}
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// device_cookies: many-to-one with user_id; safe to UPDATE.
	if _, err := tx.Exec(ctx, `
		UPDATE device_cookies SET user_id = $1 WHERE user_id = $2
	`, target, current); err != nil {
		return err
	}

	// plays: UNIQUE (user_id, daily_puzzle_id). If the target already played
	// this puzzle, drop the current user's row (their attempt) — the target
	// is the canonical identity now. This is an unavoidable lossy step;
	// played-once-per-day is a design rule.
	if _, err := tx.Exec(ctx, `
		DELETE FROM plays p_current
		 USING plays p_target
		 WHERE p_current.user_id = $1
		   AND p_target.user_id = $2
		   AND p_current.daily_puzzle_id = p_target.daily_puzzle_id
	`, current, target); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE plays SET user_id = $1 WHERE user_id = $2
	`, target, current); err != nil {
		return err
	}

	// decoy_submissions: similar — unique on (user_id, prompt_id). Prefer
	// the target's existing decoy; drop the duplicate from current.
	if _, err := tx.Exec(ctx, `
		DELETE FROM decoy_submissions ds_current
		 USING decoy_submissions ds_target
		 WHERE ds_current.user_id = $1
		   AND ds_target.user_id = $2
		   AND ds_current.prompt_id = ds_target.prompt_id
		   AND ds_current.deleted_at IS NULL
		   AND ds_target.deleted_at IS NULL
	`, current, target); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE decoy_submissions SET user_id = $1 WHERE user_id = $2
	`, target, current); err != nil {
		return err
	}

	// streaks: PRIMARY KEY user_id. Keep whichever has the higher current.
	if _, err := tx.Exec(ctx, `
		WITH cur AS (SELECT * FROM streaks WHERE user_id = $1)
		INSERT INTO streaks (user_id, current, longest, last_played_puzzle_number)
		SELECT $2, current, longest, last_played_puzzle_number FROM cur
		ON CONFLICT (user_id) DO UPDATE
		   SET current  = GREATEST(streaks.current,  EXCLUDED.current),
		       longest  = GREATEST(streaks.longest,  EXCLUDED.longest),
		       last_played_puzzle_number = GREATEST(streaks.last_played_puzzle_number, EXCLUDED.last_played_puzzle_number)
	`, current, target); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM streaks WHERE user_id = $1`, current); err != nil {
		return err
	}

	// forger_rankings: PRIMARY KEY user_id. Will be re-derived by the
	// nightly rollup; drop the current row, let the target stand. The next
	// `make rollup` will fold in the merged plays/decoys.
	if _, err := tx.Exec(ctx, `DELETE FROM forger_rankings WHERE user_id = $1`, current); err != nil {
		return err
	}

	// moderation_reviews: link reviewers performed under the anonymous user
	// to the target so the audit trail follows the human.
	if _, err := tx.Exec(ctx, `
		UPDATE moderation_reviews SET reviewer_user_id = $1 WHERE reviewer_user_id = $2
	`, target, current); err != nil {
		return err
	}

	// audit_log + events: best-effort move.
	if _, err := tx.Exec(ctx, `
		UPDATE audit_log SET actor_user_id = $1 WHERE actor_user_id = $2
	`, target, current); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE events SET user_id = $1 WHERE user_id = $2
	`, target, current); err != nil {
		return err
	}

	// push_subscriptions + email_reminders: similar, target wins on conflict.
	if _, err := tx.Exec(ctx, `
		UPDATE push_subscriptions SET user_id = $1
		 WHERE user_id = $2
		   AND endpoint NOT IN (SELECT endpoint FROM push_subscriptions WHERE user_id = $1)
	`, target, current); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM push_subscriptions WHERE user_id = $1`, current); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM email_reminders WHERE user_id = $1`, current); err != nil {
		return err
	}

	// Audit the merge before we lose the current user_id.
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_log (actor_user_id, action, target_kind, target_id, payload)
		VALUES ($1, 'user_merge', 'user', $1,
		        jsonb_build_object('merged_from', $2::text, 'at', $3::text))
	`, target, current.String(), now.Format(time.RFC3339)); err != nil {
		return err
	}

	// Finally, delete the empty anonymous user.
	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, current); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// SetEmailOnUser binds an email to an existing user (the "promotion" path
// when no email-bound user existed yet). Sets email_verified_at to NOW().
func (d *DB) SetEmailOnUser(ctx context.Context, userID uuid.UUID, email string, now time.Time) error {
	_, err := d.Exec(ctx, `
		UPDATE users
		   SET email = $2, email_verified_at = $3
		 WHERE id = $1 AND deleted_at IS NULL
	`, userID, email, now)
	return err
}

// SetHandle sets the user's display handle. Returns a sentinel error
// HandleTaken on uniqueness violation so the handler can return a clean
// 409 instead of leaking the raw DB error.
var ErrHandleTaken = errors.New("handle already taken")

func (d *DB) SetHandle(ctx context.Context, userID uuid.UUID, handle string) error {
	_, err := d.Exec(ctx, `UPDATE users SET handle = $2 WHERE id = $1`, userID, handle)
	if err != nil && isUniqueViolation(err) {
		return ErrHandleTaken
	}
	return err
}

// SetDisplayAnonymous toggles the "one-tap anonymous" flag (design §12).
func (d *DB) SetDisplayAnonymous(ctx context.Context, userID uuid.UUID, anon bool) error {
	_, err := d.Exec(ctx, `UPDATE users SET display_anonymous = $2 WHERE id = $1`, userID, anon)
	return err
}

// isUniqueViolation returns true if err is a pgx unique-constraint error.
// We pattern-match on the SQLSTATE code (23505) instead of importing
// pgxconn so the dep doesn't bleed into callers.
func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}
