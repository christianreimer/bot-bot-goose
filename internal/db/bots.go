package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Bot is one row from bot_candidates joined with prompt text and archetype slug.
// Mirrors the shape of Decoy so the admin CLI can render symmetrical output.
type Bot struct {
	ID            uuid.UUID
	PromptID      uuid.UUID
	PromptText    string
	ArchetypeID   uuid.UUID
	ArchetypeSlug string
	Text          string
	Status        string
	LLMModel      *string
	GeneratorRun  *uuid.UUID
	CreatedAt     time.Time
}

// BotListOpts filters and bounds a bot listing.
type BotListOpts struct {
	Status      *string    // pending|approved|rejected|retired
	PromptID    *uuid.UUID
	ArchetypeID *uuid.UUID
	Since       *time.Time // created_at >= since
	Until       *time.Time // created_at <= until
	Limit       int
	Offset      int
}

// ListBots returns bot candidates joined with prompt text + archetype slug,
// ordered by created_at DESC.
func (d *DB) ListBots(ctx context.Context, opts BotListOpts) ([]Bot, error) {
	q := `
		SELECT bc.id, bc.prompt_id, p.text, bc.archetype_id, a.slug, bc.text,
		       bc.status, bc.llm_model, bc.generator_run_id, bc.created_at
		  FROM bot_candidates bc
		  JOIN prompts p    ON p.id = bc.prompt_id
		  JOIN archetypes a ON a.id = bc.archetype_id
		 WHERE 1=1`
	args := []any{}
	if opts.Status != nil {
		args = append(args, *opts.Status)
		q += fmt.Sprintf(" AND bc.status = $%d::moderation_status", len(args))
	}
	if opts.PromptID != nil {
		args = append(args, *opts.PromptID)
		q += fmt.Sprintf(" AND bc.prompt_id = $%d", len(args))
	}
	if opts.ArchetypeID != nil {
		args = append(args, *opts.ArchetypeID)
		q += fmt.Sprintf(" AND bc.archetype_id = $%d", len(args))
	}
	if opts.Since != nil {
		args = append(args, *opts.Since)
		q += fmt.Sprintf(" AND bc.created_at >= $%d", len(args))
	}
	if opts.Until != nil {
		args = append(args, *opts.Until)
		q += fmt.Sprintf(" AND bc.created_at <= $%d", len(args))
	}
	q += " ORDER BY bc.created_at DESC"
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
	var out []Bot
	for rows.Next() {
		var b Bot
		if err := rows.Scan(
			&b.ID, &b.PromptID, &b.PromptText, &b.ArchetypeID, &b.ArchetypeSlug,
			&b.Text, &b.Status, &b.LLMModel, &b.GeneratorRun, &b.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BotByID returns one bot candidate or ErrNotFound.
func (d *DB) BotByID(ctx context.Context, id uuid.UUID) (*Bot, error) {
	const q = `
		SELECT bc.id, bc.prompt_id, p.text, bc.archetype_id, a.slug, bc.text,
		       bc.status, bc.llm_model, bc.generator_run_id, bc.created_at
		  FROM bot_candidates bc
		  JOIN prompts p    ON p.id = bc.prompt_id
		  JOIN archetypes a ON a.id = bc.archetype_id
		 WHERE bc.id = $1`
	row := d.QueryRow(ctx, q, id)
	b := &Bot{}
	err := row.Scan(
		&b.ID, &b.PromptID, &b.PromptText, &b.ArchetypeID, &b.ArchetypeSlug,
		&b.Text, &b.Status, &b.LLMModel, &b.GeneratorRun, &b.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return b, nil
}

// ReviewBot transitions a bot_candidate's status and records the moderation
// paper trail in a single transaction. Mirrors ReviewDecoy exactly, but for
// the bot side of the content pool. `decision` must be one of
// approved|rejected|retired.
func (d *DB) ReviewBot(ctx context.Context, botID, reviewerID uuid.UUID, decision, note string) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tag, err := tx.Exec(ctx,
		`UPDATE bot_candidates
		    SET status = $1::moderation_status
		  WHERE id = $2`,
		decision, botID,
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
		VALUES ('bot_candidate', $1, $2, $3::moderation_status, $4)
	`, botID, reviewerID, decision, notePtr); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_log
		    (actor_user_id, action, target_kind, target_id, payload)
		VALUES ($1, 'bot_review', 'bot_candidate', $2,
		        jsonb_build_object('decision', $3::text, 'note', $4::text))
	`, reviewerID, botID, decision, notePtr); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
