package db

import (
	"context"
	"errors"
	"net/netip"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// HarvestPrompt is one card in the deck served by GET /harvest. Just id + text.
type HarvestPrompt struct {
	ID   uuid.UUID
	Text string
}

// HarvestDeck returns up to `limit` prompts that:
//
//   - are not retired,
//   - have FEWER THAN 5 rows in pre_launch_submissions (under-supplied),
//   - have no row from this device in pre_launch_submissions (don't repeat).
//
// Order is random so concurrent harvesters don't all hit the same prompts.
// At v1 scale the CTE is cheap; the index added in 0005 keeps the count
// query indexed.
func (d *DB) HarvestDeck(ctx context.Context, userID uuid.UUID, limit int) ([]HarvestPrompt, error) {
	const q = `
		WITH counts AS (
		    SELECT prompt_id, count(*) AS n
		      FROM pre_launch_submissions
		     GROUP BY prompt_id
		)
		SELECT p.id, p.text
		  FROM prompts p
		  LEFT JOIN counts c ON c.prompt_id = p.id
		 WHERE p.retired_at IS NULL
		   AND COALESCE(c.n, 0) < 5
		   AND NOT EXISTS (
		       SELECT 1 FROM pre_launch_submissions ps
		        WHERE ps.prompt_id = p.id AND ps.user_id = $1
		   )
		 ORDER BY random()
		 LIMIT $2
	`
	rows, err := d.Query(ctx, q, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HarvestPrompt
	for rows.Next() {
		var p HarvestPrompt
		if err := rows.Scan(&p.ID, &p.Text); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// InsertHarvestSubmission writes a row in pre_launch_submissions for the
// anonymous device. email stays NULL (Reddit traffic doesn't carry one),
// ingested_decoy_id stays NULL (auto-promotion is intentionally forbidden).
// Returns ErrHarvestAlreadySubmitted on the partial-unique-index violation
// so the handler can map cleanly to a 409 {code: "already_submitted"}.
func (d *DB) InsertHarvestSubmission(ctx context.Context, userID, promptID uuid.UUID, text, requestedIP string) (uuid.UUID, error) {
	var inet any
	if addr, err := netip.ParseAddr(requestedIP); err == nil {
		inet = addr.String()
	}
	var id uuid.UUID
	err := d.QueryRow(ctx, `
		INSERT INTO pre_launch_submissions (prompt_id, user_id, text, requested_ip)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, promptID, userID, text, inet).Scan(&id)
	if err != nil && isUniqueViolation(err) {
		return uuid.Nil, ErrHarvestAlreadySubmitted
	}
	return id, err
}

// HarvestSubmissionForUserAndPrompt is the symmetric counterpart of
// DecoyForUserAndPrompt — used by the handler's TOCTOU recheck after an
// insert race so the user gets a clean "already_submitted" response rather
// than a raw pg error.
func (d *DB) HarvestSubmissionForUserAndPrompt(ctx context.Context, userID, promptID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := d.QueryRow(ctx, `
		SELECT id FROM pre_launch_submissions
		 WHERE user_id = $1 AND prompt_id = $2
		 LIMIT 1
	`, userID, promptID).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrNotFound
		}
		return uuid.Nil, err
	}
	return id, nil
}

// ErrHarvestAlreadySubmitted is the sentinel for "this device already
// submitted for this prompt." Maps to the 409 already_submitted code.
var ErrHarvestAlreadySubmitted = errors.New("harvest submission already exists for this user+prompt")
