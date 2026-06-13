package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Decoy is a user-submitted answer pulled from decoy_submissions.
type Decoy struct {
	ID              uuid.UUID
	PromptID        uuid.UUID
	PromptText      string
	UserID          *uuid.UUID
	Text            string
	Status          string
	IsTrap          bool
	AIDetectorScore *float64
	SubmittedAt     time.Time
	DeletedAt       *time.Time
}

// DecoyListOpts filters and bounds a decoy listing.
type DecoyListOpts struct {
	Status         *string    // pending|approved|rejected|retired
	PromptID       *uuid.UUID
	UserID         *uuid.UUID
	Since          *time.Time // submitted_at >= since
	Until          *time.Time // submitted_at <= until
	IncludeDeleted bool
	Limit          int // 0 = no limit (caller should always set one)
	Offset         int
}

// ListDecoys returns decoys joined with prompt text, ordered by submitted_at DESC.
func (d *DB) ListDecoys(ctx context.Context, opts DecoyListOpts) ([]Decoy, error) {
	q := `
		SELECT ds.id, ds.prompt_id, p.text, ds.user_id, ds.text, ds.status,
		       ds.is_trap, ds.ai_detector_score, ds.submitted_at, ds.deleted_at
		  FROM decoy_submissions ds
		  JOIN prompts p ON p.id = ds.prompt_id
		 WHERE 1=1`
	args := []any{}
	if !opts.IncludeDeleted {
		q += " AND ds.deleted_at IS NULL"
	}
	if opts.Status != nil {
		args = append(args, *opts.Status)
		q += fmt.Sprintf(" AND ds.status = $%d::moderation_status", len(args))
	}
	if opts.PromptID != nil {
		args = append(args, *opts.PromptID)
		q += fmt.Sprintf(" AND ds.prompt_id = $%d", len(args))
	}
	if opts.UserID != nil {
		args = append(args, *opts.UserID)
		q += fmt.Sprintf(" AND ds.user_id = $%d", len(args))
	}
	if opts.Since != nil {
		args = append(args, *opts.Since)
		q += fmt.Sprintf(" AND ds.submitted_at >= $%d", len(args))
	}
	if opts.Until != nil {
		args = append(args, *opts.Until)
		q += fmt.Sprintf(" AND ds.submitted_at <= $%d", len(args))
	}
	q += " ORDER BY ds.submitted_at DESC"
	if opts.Limit > 0 {
		args = append(args, opts.Limit)
		q += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	if opts.Offset > 0 {
		args = append(args, opts.Offset)
		q += fmt.Sprintf(" OFFSET $%d", len(args))
	}
	rows, err := d.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Decoy
	for rows.Next() {
		var dc Decoy
		if err := rows.Scan(
			&dc.ID, &dc.PromptID, &dc.PromptText, &dc.UserID, &dc.Text, &dc.Status,
			&dc.IsTrap, &dc.AIDetectorScore, &dc.SubmittedAt, &dc.DeletedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, dc)
	}
	return out, rows.Err()
}

// DecoyByID returns one decoy with its prompt text, or ErrNotFound.
func (d *DB) DecoyByID(ctx context.Context, id uuid.UUID) (*Decoy, error) {
	const q = `
		SELECT ds.id, ds.prompt_id, p.text, ds.user_id, ds.text, ds.status,
		       ds.is_trap, ds.ai_detector_score, ds.submitted_at, ds.deleted_at
		  FROM decoy_submissions ds
		  JOIN prompts p ON p.id = ds.prompt_id
		 WHERE ds.id = $1`
	row := d.QueryRow(ctx, q, id)
	dc := &Decoy{}
	err := row.Scan(
		&dc.ID, &dc.PromptID, &dc.PromptText, &dc.UserID, &dc.Text, &dc.Status,
		&dc.IsTrap, &dc.AIDetectorScore, &dc.SubmittedAt, &dc.DeletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return dc, nil
}

// ReviewDecoy transitions a decoy's status and records the moderation paper
// trail in a single transaction:
//   1. UPDATE decoy_submissions.status
//   2. INSERT moderation_reviews row
//   3. INSERT audit_log row
//
// `decision` must be one of approved|rejected|retired (pending is a no-op here
// — the CLI rejects it before calling). `reviewerID` is required because
// moderation_reviews.reviewer_user_id is NOT NULL.
func (d *DB) ReviewDecoy(ctx context.Context, decoyID, reviewerID uuid.UUID, decision, note string) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tag, err := tx.Exec(ctx,
		`UPDATE decoy_submissions
		    SET status = $1::moderation_status
		  WHERE id = $2 AND deleted_at IS NULL`,
		decision, decoyID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	var notePtr *string
	if note != "" {
		notePtr = &note
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO moderation_reviews
		    (target_kind, target_id, reviewer_user_id, decision, note)
		VALUES ('decoy_submission', $1, $2, $3::moderation_status, $4)
	`, decoyID, reviewerID, decision, notePtr); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_log
		    (actor_user_id, action, target_kind, target_id, payload)
		VALUES ($1, 'decoy_review', 'decoy_submission', $2,
		        jsonb_build_object('decision', $3::text, 'note', $4::text))
	`, reviewerID, decoyID, decision, notePtr); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
