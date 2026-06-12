package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LatestPuzzle returns the puzzle that should be served right now (highest
// puzzle_number whose puzzle_date <= today UTC).
func (d *DB) LatestPuzzle(ctx context.Context, asOf time.Time) (*DailyPuzzle, error) {
	const q = `
		SELECT id, puzzle_number, puzzle_date, mode, frozen_at, theme
		  FROM daily_puzzles
		 WHERE puzzle_date <= $1::date
		 ORDER BY puzzle_number DESC
		 LIMIT 1
	`
	row := d.QueryRow(ctx, q, asOf)
	p := &DailyPuzzle{}
	if err := row.Scan(&p.ID, &p.PuzzleNumber, &p.PuzzleDate, &p.Mode, &p.FrozenAt, &p.Theme); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// PuzzleByID loads a puzzle by uuid. Used in handlers that already have the
// playRow on hand and only need to look up the puzzle_number for share URLs.
func (d *DB) PuzzleByID(ctx context.Context, id uuid.UUID) (*DailyPuzzle, error) {
	const q = `
		SELECT id, puzzle_number, puzzle_date, mode, frozen_at, theme
		  FROM daily_puzzles WHERE id = $1
	`
	p := &DailyPuzzle{}
	row := d.QueryRow(ctx, q, id)
	if err := row.Scan(&p.ID, &p.PuzzleNumber, &p.PuzzleDate, &p.Mode, &p.FrozenAt, &p.Theme); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// PuzzleByNumber loads a specific puzzle by its monotonic number.
func (d *DB) PuzzleByNumber(ctx context.Context, n int32) (*DailyPuzzle, error) {
	const q = `
		SELECT id, puzzle_number, puzzle_date, mode, frozen_at, theme
		  FROM daily_puzzles
		 WHERE puzzle_number = $1
	`
	row := d.QueryRow(ctx, q, n)
	p := &DailyPuzzle{}
	if err := row.Scan(&p.ID, &p.PuzzleNumber, &p.PuzzleDate, &p.Mode, &p.FrozenAt, &p.Theme); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// Rounds returns the puzzle's rounds in order, each with the resolved prompt text.
func (d *DB) Rounds(ctx context.Context, puzzleID uuid.UUID) ([]PuzzleRound, error) {
	const q = `
		SELECT r.id, r.daily_puzzle_id, r.round_index, r.prompt_id, p.text,
		       r.target_kind, r.target_count
		  FROM puzzle_rounds r
		  JOIN prompts p ON p.id = r.prompt_id
		 WHERE r.daily_puzzle_id = $1
		 ORDER BY r.round_index
	`
	rows, err := d.Query(ctx, q, puzzleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PuzzleRound
	for rows.Next() {
		var r PuzzleRound
		if err := rows.Scan(&r.ID, &r.DailyPuzzleID, &r.RoundIndex, &r.PromptID, &r.PromptText, &r.TargetKind, &r.TargetCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AnswersForRound returns answers in a deterministic canonical order (by id).
// This is the order slot_permutation indexes into.
func (d *DB) AnswersForRound(ctx context.Context, roundID uuid.UUID) ([]Answer, error) {
	const q = `
		SELECT id, round_id, content_kind, bot_candidate_id, decoy_id,
		       is_trap, author_user_id, answer_text
		  FROM puzzle_round_answers
		 WHERE round_id = $1
		 ORDER BY id
	`
	rows, err := d.Query(ctx, q, roundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Answer
	for rows.Next() {
		var a Answer
		if err := rows.Scan(&a.ID, &a.RoundID, &a.ContentKind, &a.BotCandidateID, &a.DecoyID, &a.IsTrap, &a.AuthorUserID, &a.AnswerText); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// InsertDailyPuzzle creates the puzzle row. Idempotent on puzzle_number.
func (d *DB) InsertDailyPuzzle(ctx context.Context, n int32, date time.Time, mode Mode, theme *string) (uuid.UUID, error) {
	var id uuid.UUID
	err := d.QueryRow(ctx, `
		INSERT INTO daily_puzzles (puzzle_number, puzzle_date, mode, theme)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (puzzle_number) DO UPDATE
		   SET puzzle_date = EXCLUDED.puzzle_date,
		       mode = EXCLUDED.mode,
		       theme = EXCLUDED.theme
		RETURNING id
	`, n, date, string(mode), theme).Scan(&id)
	return id, err
}

// NextPuzzleNumber returns max(puzzle_number)+1, or 1 if the table is empty.
func (d *DB) NextPuzzleNumber(ctx context.Context) (int32, error) {
	var n *int32
	if err := d.QueryRow(ctx, `SELECT max(puzzle_number) FROM daily_puzzles`).Scan(&n); err != nil {
		return 0, fmt.Errorf("max puzzle_number: %w", err)
	}
	if n == nil {
		return 1, nil
	}
	return *n + 1, nil
}

// NextSolicitPrompt picks the prompt to ask a player to write a decoy for,
// given they just finished puzzle `currentNumber`. Preference order:
//
//  1. The first prompt of puzzle (currentNumber + 1), if it exists — keeps the
//     "your words could be in tomorrow's puzzle" narrative concrete.
//  2. Otherwise, any non-retired prompt picked at random.
//
// In v1 this is the prompt shown on the result-page decoy form. A submitted
// decoy goes to moderation regardless; the "for tomorrow's puzzle" framing is
// motivational copy, not a hard scheduling guarantee.
func (d *DB) NextSolicitPrompt(ctx context.Context, currentNumber int32) (uuid.UUID, string, error) {
	const next = `
		SELECT p.id, p.text
		  FROM daily_puzzles dp
		  JOIN puzzle_rounds pr ON pr.daily_puzzle_id = dp.id AND pr.round_index = 0
		  JOIN prompts p ON p.id = pr.prompt_id
		 WHERE dp.puzzle_number = $1
	`
	var id uuid.UUID
	var text string
	err := d.QueryRow(ctx, next, currentNumber+1).Scan(&id, &text)
	if err == nil {
		return id, text, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", err
	}
	// Fallback: any non-retired prompt.
	err = d.QueryRow(ctx, `
		SELECT id, text FROM prompts
		 WHERE retired_at IS NULL
		 ORDER BY random()
		 LIMIT 1
	`).Scan(&id, &text)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, "", ErrNotFound
		}
		return uuid.Nil, "", err
	}
	return id, text, nil
}

// UpsertPrompt creates-or-returns a prompt by exact text. Used by the seed.
func (d *DB) UpsertPrompt(ctx context.Context, text string) (uuid.UUID, error) {
	var id uuid.UUID
	err := d.QueryRow(ctx, `
		WITH ins AS (
		    INSERT INTO prompts (text)
		    VALUES ($1)
		    ON CONFLICT DO NOTHING
		    RETURNING id
		)
		SELECT id FROM ins
		UNION ALL
		SELECT id FROM prompts WHERE text = $1
		LIMIT 1
	`, text).Scan(&id)
	return id, err
}

// InsertPuzzleRound inserts a round; idempotent on (daily_puzzle_id, round_index).
func (d *DB) InsertPuzzleRound(ctx context.Context, puzzleID uuid.UUID, idx int16, promptID uuid.UUID, targetKind string, targetCount int16) (uuid.UUID, error) {
	var id uuid.UUID
	err := d.QueryRow(ctx, `
		INSERT INTO puzzle_rounds (daily_puzzle_id, round_index, prompt_id, target_kind, target_count)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (daily_puzzle_id, round_index) DO UPDATE
		   SET prompt_id = EXCLUDED.prompt_id,
		       target_kind = EXCLUDED.target_kind,
		       target_count = EXCLUDED.target_count
		RETURNING id
	`, puzzleID, idx, promptID, targetKind, targetCount).Scan(&id)
	return id, err
}

// ReplaceRoundAnswers wipes and rewrites a round's answers in one tx. Used
// by the composer to keep idempotency clean.
func (d *DB) ReplaceRoundAnswers(ctx context.Context, roundID uuid.UUID, answers []Answer) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `DELETE FROM puzzle_round_answers WHERE round_id = $1`, roundID); err != nil {
		return err
	}
	for _, a := range answers {
		_, err := tx.Exec(ctx, `
			INSERT INTO puzzle_round_answers
			    (round_id, content_kind, bot_candidate_id, decoy_id, is_trap, author_user_id, answer_text)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, roundID, string(a.ContentKind), a.BotCandidateID, a.DecoyID, a.IsTrap, a.AuthorUserID, a.AnswerText)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// InsertBotCandidate adds one approved bot candidate. Used by seed + admin
// approval (the live generator inserts as 'pending').
func (d *DB) InsertBotCandidate(ctx context.Context, promptID, archetypeID uuid.UUID, text, status string) (uuid.UUID, error) {
	var id uuid.UUID
	err := d.QueryRow(ctx, `
		INSERT INTO bot_candidates (prompt_id, archetype_id, text, status)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, promptID, archetypeID, text, status).Scan(&id)
	return id, err
}

// InsertDecoy inserts a (typically pending) decoy. user_id may be nil for seed content.
func (d *DB) InsertDecoy(ctx context.Context, promptID uuid.UUID, userID *uuid.UUID, text, status string) (uuid.UUID, error) {
	var id uuid.UUID
	err := d.QueryRow(ctx, `
		INSERT INTO decoy_submissions (prompt_id, user_id, text, status)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, promptID, userID, text, status).Scan(&id)
	return id, err
}

// UpsertArchetype creates-or-updates an archetype by slug.
func (d *DB) UpsertArchetype(ctx context.Context, slug, name, tell string, difficulty int16) (uuid.UUID, error) {
	var id uuid.UUID
	err := d.QueryRow(ctx, `
		INSERT INTO archetypes (slug, name, tell, difficulty)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (slug) DO UPDATE
		   SET name = EXCLUDED.name, tell = EXCLUDED.tell, difficulty = EXCLUDED.difficulty
		RETURNING id
	`, slug, name, tell, difficulty).Scan(&id)
	return id, err
}
