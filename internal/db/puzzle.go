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

// ExistingDecoy is the data the result page needs to tell a user "you've
// already planted a decoy for this prompt" instead of showing the form.
type ExistingDecoy struct {
	ID     uuid.UUID
	Text   string
	Status string
}

// DecoyForUserAndPrompt returns the user's existing non-deleted decoy for
// the given prompt, if any. ErrNotFound means they haven't submitted yet
// and the form should render normally.
func (d *DB) DecoyForUserAndPrompt(ctx context.Context, userID, promptID uuid.UUID) (*ExistingDecoy, error) {
	const q = `
		SELECT id, text, status
		  FROM decoy_submissions
		 WHERE user_id = $1 AND prompt_id = $2 AND deleted_at IS NULL
		 LIMIT 1
	`
	row := d.QueryRow(ctx, q, userID, promptID)
	out := &ExistingDecoy{}
	if err := row.Scan(&out.ID, &out.Text, &out.Status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return out, nil
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

// ErrHasPlays is returned by destructive puzzle ops when at least one play
// row exists. Mutating an answered puzzle would corrupt historical results
// (the slot_permutation refers to canonical answer ordinals at play time).
var ErrHasPlays = errors.New("puzzle has plays; refuse to mutate")

// ErrReferenced is returned by `prompt delete` when puzzle_rounds reference it.
// Caller should suggest `prompt retire` instead.
var ErrReferenced = errors.New("row is referenced by another table")

// PuzzleListOpts filters and bounds a daily-puzzles listing.
type PuzzleListOpts struct {
	From        *time.Time // inclusive; nil means no lower bound
	To          *time.Time // inclusive; nil means no upper bound
	IncludePast bool       // if false, From defaults to today UTC
	Limit       int        // 0 means no limit
}

// ListDailyPuzzles returns puzzles ordered by puzzle_date ASC (upcoming first).
func (d *DB) ListDailyPuzzles(ctx context.Context, opts PuzzleListOpts) ([]DailyPuzzle, error) {
	q := `SELECT id, puzzle_number, puzzle_date, mode, frozen_at, theme
	        FROM daily_puzzles WHERE 1=1`
	args := []any{}
	if opts.From != nil {
		args = append(args, *opts.From)
		q += fmt.Sprintf(" AND puzzle_date >= $%d", len(args))
	} else if !opts.IncludePast {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		args = append(args, today)
		q += fmt.Sprintf(" AND puzzle_date >= $%d", len(args))
	}
	if opts.To != nil {
		args = append(args, *opts.To)
		q += fmt.Sprintf(" AND puzzle_date <= $%d", len(args))
	}
	q += " ORDER BY puzzle_date ASC, puzzle_number ASC"
	if opts.Limit > 0 {
		args = append(args, opts.Limit)
		q += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	rows, err := d.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DailyPuzzle
	for rows.Next() {
		var p DailyPuzzle
		if err := rows.Scan(&p.ID, &p.PuzzleNumber, &p.PuzzleDate, &p.Mode, &p.FrozenAt, &p.Theme); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PuzzleByDate finds the puzzle for an exact date. Useful for `--date` lookups.
func (d *DB) PuzzleByDate(ctx context.Context, date time.Time) (*DailyPuzzle, error) {
	const q = `SELECT id, puzzle_number, puzzle_date, mode, frozen_at, theme
	             FROM daily_puzzles WHERE puzzle_date = $1 LIMIT 1`
	row := d.QueryRow(ctx, q, date)
	p := &DailyPuzzle{}
	if err := row.Scan(&p.ID, &p.PuzzleNumber, &p.PuzzleDate, &p.Mode, &p.FrozenAt, &p.Theme); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// PuzzleHasPlays reports whether any plays row references the puzzle.
func (d *DB) PuzzleHasPlays(ctx context.Context, puzzleID uuid.UUID) (bool, error) {
	var n int
	err := d.QueryRow(ctx, `SELECT count(*) FROM plays WHERE daily_puzzle_id = $1`, puzzleID).Scan(&n)
	return n > 0, err
}

// UpdateDailyPuzzle patches mutable fields on an unplayed puzzle. Nil pointers
// leave the field untouched. Returns ErrHasPlays if any play exists.
func (d *DB) UpdateDailyPuzzle(ctx context.Context, n int32, mode *Mode, theme *string, date *time.Time) error {
	p, err := d.PuzzleByNumber(ctx, n)
	if err != nil {
		return err
	}
	played, err := d.PuzzleHasPlays(ctx, p.ID)
	if err != nil {
		return err
	}
	if played {
		return ErrHasPlays
	}
	if mode == nil && theme == nil && date == nil {
		return nil
	}
	// COALESCE keeps existing values when the corresponding arg is nil.
	var modeArg any
	if mode != nil {
		modeArg = string(*mode)
	}
	_, err = d.Exec(ctx, `
		UPDATE daily_puzzles
		   SET mode  = COALESCE($2::puzzle_mode, mode),
		       theme = COALESCE($3, theme),
		       puzzle_date = COALESCE($4::date, puzzle_date)
		 WHERE puzzle_number = $1
	`, n, modeArg, theme, date)
	return err
}

// DeleteDailyPuzzle removes a puzzle (cascade drops its rounds + answers).
// Refuses if any play references it.
func (d *DB) DeleteDailyPuzzle(ctx context.Context, n int32) error {
	p, err := d.PuzzleByNumber(ctx, n)
	if err != nil {
		return err
	}
	played, err := d.PuzzleHasPlays(ctx, p.ID)
	if err != nil {
		return err
	}
	if played {
		return ErrHasPlays
	}
	_, err = d.Exec(ctx, `DELETE FROM daily_puzzles WHERE puzzle_number = $1`, n)
	return err
}

// OverrideAnswerText replaces the denormalized text snapshot for the answer at
// canonical position `slot` (0..3) within a round. Canonical order is by id
// ASC — same order AnswersForRound returns. Refuses if the puzzle has plays.
func (d *DB) OverrideAnswerText(ctx context.Context, puzzleID, roundID uuid.UUID, slot int, text string) error {
	played, err := d.PuzzleHasPlays(ctx, puzzleID)
	if err != nil {
		return err
	}
	if played {
		return ErrHasPlays
	}
	rows, err := d.Query(ctx, `SELECT id FROM puzzle_round_answers WHERE round_id = $1 ORDER BY id`, roundID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if slot < 0 || slot >= len(ids) {
		return fmt.Errorf("slot %d out of range [0,%d)", slot, len(ids))
	}
	_, err = d.Exec(ctx, `UPDATE puzzle_round_answers SET answer_text = $1 WHERE id = $2`, text, ids[slot])
	return err
}

// OverrideAnswerContent swaps the underlying content at canonical slot `slot`
// in `roundID` to a new bot_candidate or decoy_submission. Exactly one of
// botID / decoyID must be non-nil. The row's content_kind, FK, author, and
// text snapshot are all updated to match the new source. Refuses on has_plays
// (would corrupt the slot_permutation of every prior play).
//
// The caller is responsible for verifying that the new source is approved and
// belongs to the round's prompt — puzzle.PickBotsByIDs / PickDecoysByIDs are
// the canonical validators.
func (d *DB) OverrideAnswerContent(ctx context.Context, puzzleID, roundID uuid.UUID, slot int, botID, decoyID *uuid.UUID) error {
	if (botID == nil) == (decoyID == nil) {
		return fmt.Errorf("exactly one of botID / decoyID must be set")
	}
	played, err := d.PuzzleHasPlays(ctx, puzzleID)
	if err != nil {
		return err
	}
	if played {
		return ErrHasPlays
	}
	rows, err := d.Query(ctx, `SELECT id FROM puzzle_round_answers WHERE round_id = $1 ORDER BY id`, roundID)
	if err != nil {
		return err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if slot < 0 || slot >= len(ids) {
		return fmt.Errorf("slot %d out of range [0,%d)", slot, len(ids))
	}

	var (
		contentKind  string
		text         string
		authorUserID *uuid.UUID
	)
	if botID != nil {
		contentKind = string(ContentBot)
		if err := d.QueryRow(ctx, `SELECT text FROM bot_candidates WHERE id = $1`, *botID).Scan(&text); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
	} else {
		contentKind = string(ContentDecoy)
		if err := d.QueryRow(ctx, `SELECT text, user_id FROM decoy_submissions WHERE id = $1 AND deleted_at IS NULL`, *decoyID).Scan(&text, &authorUserID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
	}

	_, err = d.Exec(ctx, `
		UPDATE puzzle_round_answers
		   SET content_kind     = $1,
		       bot_candidate_id = $2,
		       decoy_id         = $3,
		       author_user_id   = $4,
		       answer_text      = $5
		 WHERE id = $6
	`, contentKind, botID, decoyID, authorUserID, text, ids[slot])
	return err
}

// --- prompts -----------------------------------------------------------------

type Prompt struct {
	ID         uuid.UUID
	Text       string
	Theme      *string
	RetiredAt  *time.Time
	CreatedAt  time.Time
}

// InsertPrompt creates a prompt with optional theme. Returns the new id.
// Unlike UpsertPrompt, this does not deduplicate.
func (d *DB) InsertPrompt(ctx context.Context, text string, theme *string) (uuid.UUID, error) {
	var id uuid.UUID
	err := d.QueryRow(ctx,
		`INSERT INTO prompts (text, theme) VALUES ($1, $2) RETURNING id`,
		text, theme,
	).Scan(&id)
	return id, err
}

// PromptByID loads a prompt or returns ErrNotFound.
func (d *DB) PromptByID(ctx context.Context, id uuid.UUID) (*Prompt, error) {
	const q = `SELECT id, text, theme, retired_at, created_at FROM prompts WHERE id = $1`
	row := d.QueryRow(ctx, q, id)
	p := &Prompt{}
	if err := row.Scan(&p.ID, &p.Text, &p.Theme, &p.RetiredAt, &p.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// ListPrompts returns prompts; excludes retired by default.
func (d *DB) ListPrompts(ctx context.Context, includeRetired bool, theme *string, limit int) ([]Prompt, error) {
	q := `SELECT id, text, theme, retired_at, created_at FROM prompts WHERE 1=1`
	args := []any{}
	if !includeRetired {
		q += " AND retired_at IS NULL"
	}
	if theme != nil {
		args = append(args, *theme)
		q += fmt.Sprintf(" AND theme = $%d", len(args))
	}
	q += " ORDER BY created_at DESC"
	if limit > 0 {
		args = append(args, limit)
		q += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	rows, err := d.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Prompt
	for rows.Next() {
		var p Prompt
		if err := rows.Scan(&p.ID, &p.Text, &p.Theme, &p.RetiredAt, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdatePrompt patches mutable fields. Nil leaves the field unchanged.
func (d *DB) UpdatePrompt(ctx context.Context, id uuid.UUID, text *string, theme *string) error {
	_, err := d.Exec(ctx, `
		UPDATE prompts
		   SET text  = COALESCE($2, text),
		       theme = COALESCE($3, theme)
		 WHERE id = $1
	`, id, text, theme)
	return err
}

// RetirePrompt sets retired_at = NOW() if not already retired.
func (d *DB) RetirePrompt(ctx context.Context, id uuid.UUID) error {
	tag, err := d.Exec(ctx, `UPDATE prompts SET retired_at = NOW() WHERE id = $1 AND retired_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// either not found or already retired — distinguish.
		if _, err := d.PromptByID(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// DeletePrompt hard-deletes a prompt. Refuses with ErrReferenced if any
// puzzle_rounds reference it.
func (d *DB) DeletePrompt(ctx context.Context, id uuid.UUID) error {
	var refs int
	if err := d.QueryRow(ctx, `SELECT count(*) FROM puzzle_rounds WHERE prompt_id = $1`, id).Scan(&refs); err != nil {
		return err
	}
	if refs > 0 {
		return ErrReferenced
	}
	tag, err := d.Exec(ctx, `DELETE FROM prompts WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// PromptSupplyRollup is the per-prompt content-readiness snapshot used by the
// `bbg-admin prompt supply` verb. ApprovedBots / ApprovedDecoys count live
// rows in the moderated pool; PendingDecoys counts un-decided harvest rows;
// UsedInPuzzles lists every puzzle_number the prompt currently appears in.
type PromptSupplyRollup struct {
	PromptID       uuid.UUID
	PromptText     string
	ApprovedBots   int
	ApprovedDecoys int
	PendingDecoys  int
	UsedInPuzzles  []int32
}

// PromptSupplyCounts returns one rollup per non-retired prompt. Used to spot
// which prompts are ready for a given mode (find_the_bot needs ≥4 decoys;
// find_the_human needs ≥3 bots + ≥1 decoy) and which are already burned by
// the upcoming schedule.
func (d *DB) PromptSupplyCounts(ctx context.Context) ([]PromptSupplyRollup, error) {
	const q = `
		SELECT
		  p.id,
		  p.text,
		  (SELECT COUNT(*) FROM bot_candidates b
		    WHERE b.prompt_id = p.id AND b.status = 'approved')                AS approved_bots,
		  (SELECT COUNT(*) FROM decoy_submissions ds
		    WHERE ds.prompt_id = p.id AND ds.status = 'approved'
		      AND ds.deleted_at IS NULL)                                       AS approved_decoys,
		  (SELECT COUNT(*) FROM pre_launch_submissions pls
		    WHERE pls.prompt_id = p.id
		      AND pls.ingested_decoy_id IS NULL
		      AND pls.rejected_at IS NULL)                                     AS pending_decoys,
		  COALESCE(ARRAY(
		    SELECT dp.puzzle_number
		      FROM puzzle_rounds pr
		      JOIN daily_puzzles dp ON dp.id = pr.daily_puzzle_id
		     WHERE pr.prompt_id = p.id
		     ORDER BY dp.puzzle_number
		  ), '{}'::int[])                                                      AS used_in_puzzles
		FROM prompts p
		WHERE p.retired_at IS NULL
		ORDER BY p.text`
	rows, err := d.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PromptSupplyRollup
	for rows.Next() {
		var r PromptSupplyRollup
		if err := rows.Scan(
			&r.PromptID, &r.PromptText,
			&r.ApprovedBots, &r.ApprovedDecoys, &r.PendingDecoys, &r.UsedInPuzzles,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PuzzlePlayCount returns how many plays reference the puzzle.
func (d *DB) PuzzlePlayCount(ctx context.Context, puzzleID uuid.UUID) (int, error) {
	var n int
	err := d.QueryRow(ctx, `SELECT COUNT(*) FROM plays WHERE daily_puzzle_id = $1`, puzzleID).Scan(&n)
	return n, err
}

// FirstRoundPromptText returns the round-0 prompt text for a puzzle. Returns
// ErrNotFound when the puzzle has no rounds yet (newly created, not composed).
func (d *DB) FirstRoundPromptText(ctx context.Context, puzzleID uuid.UUID) (string, error) {
	var text string
	err := d.QueryRow(ctx, `
		SELECT p.text
		  FROM puzzle_rounds pr
		  JOIN prompts p ON p.id = pr.prompt_id
		 WHERE pr.daily_puzzle_id = $1
		 ORDER BY pr.round_index
		 LIMIT 1
	`, puzzleID).Scan(&text)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return text, nil
}

// DeleteDailyPuzzleAndPlays force-removes a puzzle even when plays reference
// it. Plays (and their cascading play_rounds + play_guesses) are deleted
// first, then the puzzle row itself (cascade drops rounds + answers). The
// whole thing runs in a single transaction; returns the number of plays
// that were destroyed so the caller can echo it back to the operator.
//
// This is the destructive variant of DeleteDailyPuzzle — use only when the
// operator has explicitly accepted the loss of play data (e.g. via the
// `puzzle replace` verb, which wraps this delete with an immediate import).
func (d *DB) DeleteDailyPuzzleAndPlays(ctx context.Context, n int32) (playsDeleted int, err error) {
	tx, err := d.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var puzzleID uuid.UUID
	if err := tx.QueryRow(ctx,
		`SELECT id FROM daily_puzzles WHERE puzzle_number = $1`, n,
	).Scan(&puzzleID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM plays WHERE daily_puzzle_id = $1`, puzzleID)
	if err != nil {
		return 0, err
	}
	playsDeleted = int(tag.RowsAffected())
	if _, err := tx.Exec(ctx, `DELETE FROM daily_puzzles WHERE puzzle_number = $1`, n); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return playsDeleted, nil
}

// UserIDByEmail resolves a user by email, used to map --reviewer-email flags
// to moderation_reviews.reviewer_user_id (which is NOT NULL).
func (d *DB) UserIDByEmail(ctx context.Context, email string) (uuid.UUID, error) {
	var id uuid.UUID
	err := d.QueryRow(ctx, `SELECT id FROM users WHERE email = $1 AND deleted_at IS NULL`, email).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrNotFound
		}
		return uuid.Nil, err
	}
	return id, nil
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
