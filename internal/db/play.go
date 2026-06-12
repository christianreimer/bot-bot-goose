package db

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CreateOrGetPlay returns the active play row for (user, puzzle), creating
// one if missing. The hmacSecret is only set on creation; returned secret is
// the one bound to whichever row wins the upsert (existing or new).
func (d *DB) CreateOrGetPlay(ctx context.Context, userID, puzzleID uuid.UUID, hmacSecret []byte) (*Play, bool, error) {
	tx, err := d.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Try to insert. If it conflicts, fetch.
	var p Play
	created := false
	err = tx.QueryRow(ctx, `
		INSERT INTO plays (user_id, daily_puzzle_id, hmac_secret)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, daily_puzzle_id) DO NOTHING
		RETURNING id, user_id, daily_puzzle_id, started_at, completed_at, score_pct, hmac_secret
	`, userID, puzzleID, hmacSecret).Scan(
		&p.ID, &p.UserID, &p.DailyPuzzleID, &p.StartedAt, &p.CompletedAt, &p.ScorePct, &p.HMACSecret,
	)
	switch {
	case err == nil:
		created = true
	case errors.Is(err, pgx.ErrNoRows):
		err = tx.QueryRow(ctx, `
			SELECT id, user_id, daily_puzzle_id, started_at, completed_at, score_pct, hmac_secret
			  FROM plays WHERE user_id = $1 AND daily_puzzle_id = $2
		`, userID, puzzleID).Scan(
			&p.ID, &p.UserID, &p.DailyPuzzleID, &p.StartedAt, &p.CompletedAt, &p.ScorePct, &p.HMACSecret,
		)
		if err != nil {
			return nil, false, err
		}
	default:
		return nil, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &p, created, nil
}

// PublicPlay is the data behind /r/<short> — the public result share page.
// Only completed plays are reachable; in-progress plays would leak state.
type PublicPlay struct {
	PlayID         uuid.UUID
	UserID         uuid.UUID
	AuthorHandle   string
	PuzzleNumber   int32
	Mode           Mode
	Outcomes       []Outcome
	ScorePct       int
	Streak         int
	CompletedAt    time.Time
}

// PlayByShortID resolves a /r/<short> URL to its public result bundle. Only
// completed plays match; an in-progress play won't be found.
func (d *DB) PlayByShortID(ctx context.Context, short string) (*PublicPlay, error) {
	row := d.QueryRow(ctx, `
		SELECT p.id, p.user_id,
		       CASE WHEN u.display_anonymous OR u.handle IS NULL OR u.handle = ''
		            THEN '' ELSE u.handle END,
		       dp.puzzle_number, dp.mode,
		       p.score_pct, p.completed_at,
		       COALESCE(s.current, 0)
		  FROM plays p
		  JOIN daily_puzzles dp ON dp.id = p.daily_puzzle_id
		  LEFT JOIN users u ON u.id = p.user_id
		  LEFT JOIN streaks s ON s.user_id = p.user_id
		 WHERE replace(p.id::text, '-', '') LIKE $1 || '%'
		   AND p.completed_at IS NOT NULL
		 LIMIT 1
	`, short)
	pp := &PublicPlay{}
	var scorePct *int16
	if err := row.Scan(&pp.PlayID, &pp.UserID, &pp.AuthorHandle, &pp.PuzzleNumber, &pp.Mode,
		&scorePct, &pp.CompletedAt, &pp.Streak); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if scorePct != nil {
		pp.ScorePct = int(*scorePct)
	}
	outs, err := d.LastOutcomes(ctx, pp.PlayID)
	if err != nil {
		return nil, err
	}
	pp.Outcomes = outs
	if pp.AuthorHandle == "" {
		pp.AuthorHandle = "anonymous"
	}
	return pp, nil
}

// PlayByUserAndPuzzle returns the unique play row for (user, puzzle) if it
// exists. Used to derive the /r/<short> share URL on the result page render.
func (d *DB) PlayByUserAndPuzzle(ctx context.Context, userID, puzzleID uuid.UUID) (*Play, error) {
	var p Play
	err := d.QueryRow(ctx, `
		SELECT id, user_id, daily_puzzle_id, started_at, completed_at, score_pct, hmac_secret
		  FROM plays WHERE user_id = $1 AND daily_puzzle_id = $2
	`, userID, puzzleID).Scan(&p.ID, &p.UserID, &p.DailyPuzzleID, &p.StartedAt, &p.CompletedAt, &p.ScorePct, &p.HMACSecret)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

// PlayByID loads a play row. Used by the guess endpoint to recover hmac_secret.
func (d *DB) PlayByID(ctx context.Context, id uuid.UUID) (*Play, error) {
	var p Play
	err := d.QueryRow(ctx, `
		SELECT id, user_id, daily_puzzle_id, started_at, completed_at, score_pct, hmac_secret
		  FROM plays WHERE id = $1
	`, id).Scan(&p.ID, &p.UserID, &p.DailyPuzzleID, &p.StartedAt, &p.CompletedAt, &p.ScorePct, &p.HMACSecret)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

// UpsertPlayRound creates or returns the play_round row for (play, index).
// On creation, slot_permutation is recorded. On a re-fetch we return what's
// already there. Returns (row, created).
func (d *DB) UpsertPlayRound(ctx context.Context, playID uuid.UUID, idx int16, perm []int16) (*PlayRound, bool, error) {
	tx, err := d.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	pr := &PlayRound{}
	created := false
	err = tx.QueryRow(ctx, `
		INSERT INTO play_rounds (play_id, round_index, slot_permutation)
		VALUES ($1, $2, $3)
		ON CONFLICT (play_id, round_index) DO NOTHING
		RETURNING id, play_id, round_index, slot_permutation, hint_used, removed_slot, started_at, committed_at
	`, playID, idx, perm).Scan(
		&pr.ID, &pr.PlayID, &pr.RoundIndex, &pr.SlotPermutation, &pr.HintUsed, &pr.RemovedSlot, &pr.StartedAt, &pr.CommittedAt,
	)
	switch {
	case err == nil:
		created = true
	case errors.Is(err, pgx.ErrNoRows):
		err = tx.QueryRow(ctx, `
			SELECT id, play_id, round_index, slot_permutation, hint_used, removed_slot, started_at, committed_at
			  FROM play_rounds WHERE play_id = $1 AND round_index = $2
		`, playID, idx).Scan(
			&pr.ID, &pr.PlayID, &pr.RoundIndex, &pr.SlotPermutation, &pr.HintUsed, &pr.RemovedSlot, &pr.StartedAt, &pr.CommittedAt,
		)
		if err != nil {
			return nil, false, err
		}
	default:
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return pr, created, nil
}

// PlayRoundByIndex fetches an existing play_round (no insert).
func (d *DB) PlayRoundByIndex(ctx context.Context, playID uuid.UUID, idx int16) (*PlayRound, error) {
	pr := &PlayRound{}
	err := d.QueryRow(ctx, `
		SELECT id, play_id, round_index, slot_permutation, hint_used, removed_slot, started_at, committed_at
		  FROM play_rounds WHERE play_id = $1 AND round_index = $2
	`, playID, idx).Scan(&pr.ID, &pr.PlayID, &pr.RoundIndex, &pr.SlotPermutation, &pr.HintUsed, &pr.RemovedSlot, &pr.StartedAt, &pr.CommittedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return pr, nil
}

// PriorRoundsAllCommitted returns true if every round_index < idx has a
// committed_at set. Used to enforce in-order play.
func (d *DB) PriorRoundsAllCommitted(ctx context.Context, playID uuid.UUID, idx int16) (bool, error) {
	var pending int
	err := d.QueryRow(ctx, `
		SELECT count(*) FROM play_rounds
		 WHERE play_id = $1 AND round_index < $2 AND committed_at IS NULL
	`, playID, idx).Scan(&pending)
	return pending == 0, err
}

// MarkHint sets hint_used + removed_slot atomically. Fails on already-committed.
func (d *DB) MarkHint(ctx context.Context, playRoundID uuid.UUID, removedSlot int16) error {
	ct, err := d.Exec(ctx, `
		UPDATE play_rounds
		   SET hint_used = true, removed_slot = $2
		 WHERE id = $1 AND committed_at IS NULL AND hint_used = false
	`, playRoundID, removedSlot)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return errors.New("hint not applicable")
	}
	return nil
}

// CommitGuess inserts the guess row, marks the play_round committed, and, if
// it's the last round, finalizes the play with score_pct. All in one tx.
func (d *DB) CommitGuess(ctx context.Context, playRoundID uuid.UUID, slot int16, outcome Outcome) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	ct, err := tx.Exec(ctx, `
		UPDATE play_rounds SET committed_at = NOW()
		 WHERE id = $1 AND committed_at IS NULL
	`, playRoundID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return errors.New("round already committed")
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO play_guesses (play_round_id, slot, outcome) VALUES ($1, $2, $3)
	`, playRoundID, slot, string(outcome))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CompletePlay sets the final score_pct + completed_at, updates the streak,
// and bumps decoy_daily_stats for the impressions+accusations this play
// generated. Wrapped in a single tx so a partial commit can't leave the
// leaderboard half-updated.
func (d *DB) CompletePlay(ctx context.Context, playID uuid.UUID, scorePct int16, now time.Time) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var userID uuid.UUID
	var puzzleID uuid.UUID
	if err := tx.QueryRow(ctx, `
		UPDATE plays
		   SET completed_at = $2, score_pct = $3
		 WHERE id = $1 AND completed_at IS NULL
		 RETURNING user_id, daily_puzzle_id
	`, playID, now, scorePct).Scan(&userID, &puzzleID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("play already completed or missing")
		}
		return err
	}

	// Streak update. Look up the puzzle number.
	var puzzleNumber int32
	var mode Mode
	if err := tx.QueryRow(ctx, `
		SELECT puzzle_number, mode FROM daily_puzzles WHERE id = $1
	`, puzzleID).Scan(&puzzleNumber, &mode); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO streaks (user_id, current, longest, last_played_puzzle_number)
		VALUES ($1, 1, 1, $2)
		ON CONFLICT (user_id) DO UPDATE
		   SET current = CASE
		           WHEN streaks.last_played_puzzle_number = EXCLUDED.last_played_puzzle_number - 1
		           THEN streaks.current + 1 ELSE 1
		       END,
		       longest = GREATEST(streaks.longest,
		           CASE WHEN streaks.last_played_puzzle_number = EXCLUDED.last_played_puzzle_number - 1
		                THEN streaks.current + 1 ELSE 1 END),
		       last_played_puzzle_number = EXCLUDED.last_played_puzzle_number
	`, userID, puzzleNumber)
	if err != nil {
		return err
	}

	// Decoy stats rollup. For each decoy answer in this play, increment
	// impressions; if the player guessed that slot (i.e. picked_as_bot), bump
	// the accusation counter for the mode.
	//
	// The 'picked_as_bot' interpretation per the plan:
	//   find_the_bot:  player tapping a decoy = accusation (counts up).
	//   find_the_human: player NOT picking the decoy = the human-author wins
	//     (the decoy fooled them into thinking it was a bot). In this mode
	//     'picked_as_bot' increments when the player's guess slot != decoy slot.
	_, err = tx.Exec(ctx, `
		WITH play_decoys AS (
		    SELECT pra.decoy_id, pr.id AS play_round_id, pr.slot_permutation, pra.id AS answer_id
		      FROM play_rounds pr
		      JOIN plays p ON p.id = pr.play_id
		      JOIN puzzle_rounds qr ON qr.daily_puzzle_id = p.daily_puzzle_id AND qr.round_index = pr.round_index
		      JOIN puzzle_round_answers pra ON pra.round_id = qr.id
		     WHERE p.id = $1 AND pra.content_kind = 'decoy'
		),
		round_meta AS (
		    SELECT pd.decoy_id,
		           pd.play_round_id,
		           pd.answer_id,
		           pd.slot_permutation,
		           (SELECT array_agg(id ORDER BY id) FROM puzzle_round_answers WHERE round_id =
		              (SELECT round_id FROM puzzle_round_answers WHERE id = pd.answer_id)) AS canonical
		      FROM play_decoys pd
		),
		guess_for_round AS (
		    SELECT pg.play_round_id, pg.slot
		      FROM play_guesses pg
		      JOIN play_rounds pr ON pr.id = pg.play_round_id
		      JOIN plays p ON p.id = pr.play_id
		     WHERE p.id = $1
		)
		INSERT INTO decoy_daily_stats (decoy_id, stat_date, mode, impressions, picked_as_bot)
		SELECT rm.decoy_id,
		       CURRENT_DATE,
		       $2::puzzle_mode,
		       1,
		       CASE WHEN $2 = 'find_the_bot' THEN
		            -- accused iff player's slot maps to this decoy answer
		            CASE WHEN rm.canonical[rm.slot_permutation[gfr.slot + 1] + 1] = rm.answer_id THEN 1 ELSE 0 END
		            ELSE
		            -- find_the_human: human-author wins when player did NOT pick the decoy
		            CASE WHEN rm.canonical[rm.slot_permutation[gfr.slot + 1] + 1] = rm.answer_id THEN 0 ELSE 1 END
		       END
		  FROM round_meta rm
		  JOIN guess_for_round gfr ON gfr.play_round_id = rm.play_round_id
		ON CONFLICT (decoy_id, stat_date, mode) DO UPDATE
		   SET impressions = decoy_daily_stats.impressions + EXCLUDED.impressions,
		       picked_as_bot = decoy_daily_stats.picked_as_bot + EXCLUDED.picked_as_bot
	`, playID, string(mode))
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// SubmitDecoy inserts a player-authored decoy in 'pending' status. Returns
// the new id. The unique-per-user-prompt index throws on dupes.
func (d *DB) SubmitDecoy(ctx context.Context, userID, promptID uuid.UUID, text string) (uuid.UUID, error) {
	var id uuid.UUID
	err := d.QueryRow(ctx, `
		INSERT INTO decoy_submissions (prompt_id, user_id, text, status)
		VALUES ($1, $2, $3, 'pending')
		RETURNING id
	`, promptID, userID, text).Scan(&id)
	return id, err
}

// StreakFor reads the streak count for a user.
func (d *DB) StreakFor(ctx context.Context, userID uuid.UUID) (int, error) {
	var n int
	err := d.QueryRow(ctx, `SELECT COALESCE(current, 0) FROM streaks WHERE user_id = $1`, userID).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// LastOutcomes returns the per-round outcomes for a completed play in round order.
func (d *DB) LastOutcomes(ctx context.Context, playID uuid.UUID) ([]Outcome, error) {
	rows, err := d.Query(ctx, `
		SELECT pg.outcome
		  FROM play_guesses pg
		  JOIN play_rounds pr ON pr.id = pg.play_round_id
		 WHERE pr.play_id = $1
		 ORDER BY pr.round_index
	`, playID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Outcome
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, Outcome(s))
	}
	return out, rows.Err()
}
