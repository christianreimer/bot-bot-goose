package db

import (
	"context"
	"errors"
	"net/netip"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PrelaunchPrompt is one card in the deck served by GET /prelaunch. Just id + text.
type PrelaunchPrompt struct {
	ID   uuid.UUID
	Text string
}

// PrelaunchEligiblePool returns every prompt that's not retired AND has fewer
// than 5 live (non-rejected) rows in pre_launch_submissions. Unfiltered by
// user — that filter happens in Go after a cache lookup, see plan §2.7.
//
// The list is small enough (~hundreds at v1) that returning it whole and
// sampling client-side beats running the random+limit in SQL on every
// prelaunch request.
func (d *DB) PrelaunchEligiblePool(ctx context.Context) ([]PrelaunchPrompt, error) {
	const q = `
		WITH counts AS (
		    SELECT prompt_id, count(*) AS n
		      FROM pre_launch_submissions
		     WHERE rejected_at IS NULL
		     GROUP BY prompt_id
		)
		SELECT p.id, p.text
		  FROM prompts p
		  LEFT JOIN counts c ON c.prompt_id = p.id
		 WHERE p.retired_at IS NULL
		   AND COALESCE(c.n, 0) < 5
	`
	rows, err := d.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PrelaunchPrompt
	for rows.Next() {
		var p PrelaunchPrompt
		if err := rows.Scan(&p.ID, &p.Text); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PrelaunchSubmittedPromptIDs returns the prompt IDs this user has already
// submitted for (including rejected rows — a rejected row still counts as
// "this device already answered"). Cheap, indexed scan; cached at the
// caller as a small map.
func (d *DB) PrelaunchSubmittedPromptIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	const q = `SELECT prompt_id FROM pre_launch_submissions WHERE user_id = $1`
	rows, err := d.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// InsertPrelaunchSubmission writes a row in pre_launch_submissions for the
// anonymous device. email stays NULL (Reddit traffic doesn't carry one),
// ingested_decoy_id stays NULL (auto-promotion is intentionally forbidden).
// Returns ErrPrelaunchAlreadySubmitted on the partial-unique-index violation
// so the handler can map cleanly to a 409 {code: "already_submitted"}.
func (d *DB) InsertPrelaunchSubmission(ctx context.Context, userID, promptID uuid.UUID, text, requestedIP string) (uuid.UUID, error) {
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
		return uuid.Nil, ErrPrelaunchAlreadySubmitted
	}
	return id, err
}

// PrelaunchSubmissionForUserAndPrompt is the symmetric counterpart of
// DecoyForUserAndPrompt — used by the handler's TOCTOU recheck after an
// insert race so the user gets a clean "already_submitted" response rather
// than a raw pg error.
func (d *DB) PrelaunchSubmissionForUserAndPrompt(ctx context.Context, userID, promptID uuid.UUID) (uuid.UUID, error) {
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

// ErrPrelaunchAlreadySubmitted is the sentinel for "this device already
// submitted for this prompt." Maps to the 409 already_submitted code.
var ErrPrelaunchAlreadySubmitted = errors.New("prelaunch submission already exists for this user+prompt")
