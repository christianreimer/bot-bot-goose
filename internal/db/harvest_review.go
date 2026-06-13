package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// HarvestSubmission is one row from pre_launch_submissions joined to its prompt.
// The reviewer-facing view: enough to decide keep/toss without a second query.
type HarvestSubmission struct {
	ID             uuid.UUID
	PromptID       uuid.UUID
	PromptText     string
	UserID         *uuid.UUID
	Email          *string
	Text           string
	ConsentAt      time.Time
	IngestedDecoy  *uuid.UUID // non-nil once approved
	RejectedAt     *time.Time // non-nil once soft-rejected
	RequestedIP    *string
}

// HarvestStatus is the derived three-way state used by the reviewer surface.
type HarvestStatus string

const (
	HarvestPending  HarvestStatus = "pending"  // ingested_decoy_id IS NULL AND rejected_at IS NULL
	HarvestApproved HarvestStatus = "approved" // ingested_decoy_id IS NOT NULL
	HarvestRejected HarvestStatus = "rejected" // rejected_at IS NOT NULL
)

// HarvestListOpts filters and bounds a harvest listing.
type HarvestListOpts struct {
	Status   *HarvestStatus
	PromptID *uuid.UUID
	Limit    int
	Offset   int
}

// ListHarvest returns harvest submissions joined with prompt text, newest first.
func (d *DB) ListHarvest(ctx context.Context, opts HarvestListOpts) ([]HarvestSubmission, error) {
	q := `
		SELECT pls.id, pls.prompt_id, p.text, pls.user_id, pls.email, pls.text,
		       pls.consent_at, pls.ingested_decoy_id, pls.rejected_at,
		       host(pls.requested_ip)
		  FROM pre_launch_submissions pls
		  JOIN prompts p ON p.id = pls.prompt_id
		 WHERE 1=1`
	args := []any{}
	if opts.Status != nil {
		switch *opts.Status {
		case HarvestPending:
			q += " AND pls.ingested_decoy_id IS NULL AND pls.rejected_at IS NULL"
		case HarvestApproved:
			q += " AND pls.ingested_decoy_id IS NOT NULL"
		case HarvestRejected:
			q += " AND pls.rejected_at IS NOT NULL"
		default:
			return nil, fmt.Errorf("invalid harvest status %q", *opts.Status)
		}
	}
	if opts.PromptID != nil {
		args = append(args, *opts.PromptID)
		q += fmt.Sprintf(" AND pls.prompt_id = $%d", len(args))
	}
	q += " ORDER BY pls.consent_at DESC"
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
	var out []HarvestSubmission
	for rows.Next() {
		var h HarvestSubmission
		if err := rows.Scan(
			&h.ID, &h.PromptID, &h.PromptText, &h.UserID, &h.Email, &h.Text,
			&h.ConsentAt, &h.IngestedDecoy, &h.RejectedAt, &h.RequestedIP,
		); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// HarvestByID returns one harvest submission or ErrNotFound.
func (d *DB) HarvestByID(ctx context.Context, id uuid.UUID) (*HarvestSubmission, error) {
	const q = `
		SELECT pls.id, pls.prompt_id, p.text, pls.user_id, pls.email, pls.text,
		       pls.consent_at, pls.ingested_decoy_id, pls.rejected_at,
		       host(pls.requested_ip)
		  FROM pre_launch_submissions pls
		  JOIN prompts p ON p.id = pls.prompt_id
		 WHERE pls.id = $1`
	h := &HarvestSubmission{}
	err := d.QueryRow(ctx, q, id).Scan(
		&h.ID, &h.PromptID, &h.PromptText, &h.UserID, &h.Email, &h.Text,
		&h.ConsentAt, &h.IngestedDecoy, &h.RejectedAt, &h.RequestedIP,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return h, nil
}

// HarvestPromptRollup is one row of the prompt-by-prompt harvest dashboard:
// how many live, ingested, and rejected submissions a prompt has accumulated.
// Listed in the order the reviewer cares about: which prompts are most
// undersupplied right now.
type HarvestPromptRollup struct {
	PromptID    uuid.UUID
	PromptText  string
	Pending     int // ingested_decoy_id IS NULL AND rejected_at IS NULL
	Ingested    int // ingested_decoy_id IS NOT NULL
	Rejected    int // rejected_at IS NOT NULL
	ApprovedDec int // count of approved live decoys for this prompt (the live pool)
}

// HarvestPromptCounts returns per-prompt rollups for all non-retired prompts.
// Caller orders/filters in the UI layer.
func (d *DB) HarvestPromptCounts(ctx context.Context) ([]HarvestPromptRollup, error) {
	const q = `
		SELECT p.id, p.text,
		       COUNT(*) FILTER (WHERE pls.ingested_decoy_id IS NULL
		                          AND pls.rejected_at IS NULL)             AS pending,
		       COUNT(*) FILTER (WHERE pls.ingested_decoy_id IS NOT NULL)    AS ingested,
		       COUNT(*) FILTER (WHERE pls.rejected_at IS NOT NULL)          AS rejected,
		       (SELECT COUNT(*) FROM decoy_submissions ds
		         WHERE ds.prompt_id = p.id
		           AND ds.status = 'approved'
		           AND ds.deleted_at IS NULL)                               AS approved_decoys
		  FROM prompts p
		  LEFT JOIN pre_launch_submissions pls ON pls.prompt_id = p.id
		 WHERE p.retired_at IS NULL
		 GROUP BY p.id, p.text
		 ORDER BY pending DESC, p.text`
	rows, err := d.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HarvestPromptRollup
	for rows.Next() {
		var r HarvestPromptRollup
		if err := rows.Scan(
			&r.PromptID, &r.PromptText,
			&r.Pending, &r.Ingested, &r.Rejected, &r.ApprovedDec,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ErrHarvestAlreadyDecided is returned when a reviewer tries to act on a
// submission that's already ingested or already rejected. Callers map this to
// `code: "already_decided"` for agents.
var ErrHarvestAlreadyDecided = errors.New("harvest submission already decided (ingested or rejected)")

// ApproveHarvest ingests one pre_launch_submissions row into decoy_submissions
// as a fresh approved decoy in a single transaction:
//
//  1. SELECT the pre_launch row (and lock it) — refuse if already decided.
//  2. INSERT decoy_submissions (status='approved', author = pls.user_id, is_trap).
//  3. UPDATE pre_launch_submissions.ingested_decoy_id = new decoy id.
//  4. INSERT moderation_reviews (target_kind='pre_launch_submission').
//  5. INSERT audit_log row.
//
// Returns the new decoy id so the caller can echo it back in the success envelope.
func (d *DB) ApproveHarvest(ctx context.Context, harvestID, reviewerID uuid.UUID, isTrap bool, note string) (uuid.UUID, error) {
	tx, err := d.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the row and read everything we need to mint the decoy.
	var (
		promptID      uuid.UUID
		text          string
		userID        *uuid.UUID
		ingestedDecoy *uuid.UUID
		rejectedAt    *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT prompt_id, text, user_id, ingested_decoy_id, rejected_at
		  FROM pre_launch_submissions
		 WHERE id = $1
		 FOR UPDATE
	`, harvestID).Scan(&promptID, &text, &userID, &ingestedDecoy, &rejectedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrNotFound
		}
		return uuid.Nil, err
	}
	if ingestedDecoy != nil || rejectedAt != nil {
		return uuid.Nil, ErrHarvestAlreadyDecided
	}

	var newDecoyID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO decoy_submissions (prompt_id, user_id, text, status, is_trap)
		VALUES ($1, $2, $3, 'approved', $4)
		RETURNING id
	`, promptID, userID, text, isTrap).Scan(&newDecoyID)
	if err != nil {
		return uuid.Nil, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE pre_launch_submissions
		   SET ingested_decoy_id = $1
		 WHERE id = $2
	`, newDecoyID, harvestID); err != nil {
		return uuid.Nil, err
	}

	var notePtr *string
	if note != "" {
		notePtr = &note
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO moderation_reviews
		    (target_kind, target_id, reviewer_user_id, decision, note)
		VALUES ('pre_launch_submission', $1, $2, 'approved'::moderation_status, $3)
	`, harvestID, reviewerID, notePtr); err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_log
		    (actor_user_id, action, target_kind, target_id, payload)
		VALUES ($1, 'harvest_approve', 'pre_launch_submission', $2,
		        jsonb_build_object('decoy_id', $3::text, 'is_trap', $4::bool, 'note', $5::text))
	`, reviewerID, harvestID, newDecoyID.String(), isTrap, notePtr); err != nil {
		return uuid.Nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return newDecoyID, nil
}

// RejectHarvest soft-rejects a harvest submission by setting rejected_at = NOW()
// in a single transaction with the moderation_reviews + audit_log rows.
// Refuses with ErrHarvestAlreadyDecided when the row is already ingested or
// already rejected (no re-reject).
func (d *DB) RejectHarvest(ctx context.Context, harvestID, reviewerID uuid.UUID, note string) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var (
		ingestedDecoy *uuid.UUID
		rejectedAt    *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT ingested_decoy_id, rejected_at
		  FROM pre_launch_submissions
		 WHERE id = $1
		 FOR UPDATE
	`, harvestID).Scan(&ingestedDecoy, &rejectedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if ingestedDecoy != nil || rejectedAt != nil {
		return ErrHarvestAlreadyDecided
	}

	if _, err := tx.Exec(ctx, `
		UPDATE pre_launch_submissions SET rejected_at = NOW() WHERE id = $1
	`, harvestID); err != nil {
		return err
	}

	var notePtr *string
	if note != "" {
		notePtr = &note
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO moderation_reviews
		    (target_kind, target_id, reviewer_user_id, decision, note)
		VALUES ('pre_launch_submission', $1, $2, 'rejected'::moderation_status, $3)
	`, harvestID, reviewerID, notePtr); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_log
		    (actor_user_id, action, target_kind, target_id, payload)
		VALUES ($1, 'harvest_reject', 'pre_launch_submission', $2,
		        jsonb_build_object('note', $3::text))
	`, reviewerID, harvestID, notePtr); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

