package db

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CreateAnonymousUser inserts a user with no email/handle. Returns the new id.
func (d *DB) CreateAnonymousUser(ctx context.Context) (uuid.UUID, error) {
	var id uuid.UUID
	err := d.QueryRow(ctx, `INSERT INTO users DEFAULT VALUES RETURNING id`).Scan(&id)
	return id, err
}

// InsertDeviceCookie binds a hashed cookie to a user. cookieHash is the
// SHA-256 of the cleartext token — we never store the cleartext.
func (d *DB) InsertDeviceCookie(ctx context.Context, userID uuid.UUID, cookieHash []byte, ua string) error {
	_, err := d.Exec(ctx, `
		INSERT INTO device_cookies (user_id, cookie_hash, ua)
		VALUES ($1, $2, $3)
		ON CONFLICT (cookie_hash) DO UPDATE
		   SET last_seen_at = NOW(), ua = EXCLUDED.ua
	`, userID, cookieHash, ua)
	return err
}

// UserByCookieHash resolves a hashed device cookie to its user. Returns
// ErrNotFound if no row matches.
func (d *DB) UserByCookieHash(ctx context.Context, cookieHash []byte) (*User, error) {
	const q = `
		SELECT u.id, u.handle, u.email, u.role, u.spotter_elo, u.created_at
		  FROM device_cookies dc
		  JOIN users u ON u.id = dc.user_id
		 WHERE dc.cookie_hash = $1 AND u.deleted_at IS NULL
		 LIMIT 1
	`
	row := d.QueryRow(ctx, q, cookieHash)
	u := &User{}
	if err := row.Scan(&u.ID, &u.Handle, &u.Email, &u.Role, &u.SpotterELO, &u.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return u, nil
}

// SetUserRole updates role and writes an audit_log entry. Used by admin tooling.
func (d *DB) SetUserRole(ctx context.Context, actorID *uuid.UUID, email, role string) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var id uuid.UUID
	err = tx.QueryRow(ctx,
		`UPDATE users SET role = $1 WHERE email = $2 RETURNING id`,
		role, email,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_log (actor_user_id, action, target_kind, target_id, payload)
		VALUES ($1, 'role_change', 'user', $2, jsonb_build_object('role', $3::text))
	`, actorID, id, role)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
