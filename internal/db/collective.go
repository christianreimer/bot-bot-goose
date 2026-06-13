package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// CollectiveStat is one frozen "yesterday, humans caught X%" row. The number
// is the average per-play Bot-Dar (score_pct) across all completed plays of
// a single past puzzle — the collective catch rate, baked once by the
// nightly rollup and read by the result + share surfaces.
type CollectiveStat struct {
	PuzzleNumber int32
	CatchPct     int
	TotalPlays   int
}

// ComputePreviousPuzzleCatchRate looks up the most recent puzzle whose
// puzzle_date precedes the current UTC date and returns the average
// score_pct across its completed plays. It returns ErrNotFound if no such
// puzzle exists yet (e.g., day 1 of the game). totalPlays is returned
// alongside so the rollup can apply the floor before writing.
func (d *DB) ComputePreviousPuzzleCatchRate(ctx context.Context) (CollectiveStat, error) {
	var s CollectiveStat
	// Same query in one pass: pick the target puzzle, then average score_pct
	// over its completed plays. NULL avg → 0 plays → return ErrNotFound so
	// the caller can skip the write.
	err := d.QueryRow(ctx, `
		WITH target AS (
		    SELECT id, puzzle_number
		      FROM daily_puzzles
		     WHERE puzzle_date < (NOW() AT TIME ZONE 'UTC')::date
		     ORDER BY puzzle_number DESC
		     LIMIT 1
		)
		SELECT target.puzzle_number,
		       COALESCE(ROUND(AVG(p.score_pct))::int, 0) AS catch_pct,
		       COUNT(p.id)::int                          AS total_plays
		  FROM target
		  LEFT JOIN plays p ON p.daily_puzzle_id = target.id
		                   AND p.completed_at IS NOT NULL
		                   AND p.score_pct IS NOT NULL
		 GROUP BY target.puzzle_number
	`).Scan(&s.PuzzleNumber, &s.CatchPct, &s.TotalPlays)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CollectiveStat{}, ErrNotFound
		}
		return CollectiveStat{}, err
	}
	return s, nil
}

// UpsertCollectiveStat freezes a (puzzle_number, catch_pct, total_plays)
// row. Idempotent on puzzle_number so reruns of the rollup are safe.
func (d *DB) UpsertCollectiveStat(ctx context.Context, s CollectiveStat) error {
	_, err := d.Exec(ctx, `
		INSERT INTO daily_collective_stats
		    (puzzle_number, stat_date, catch_pct, total_plays)
		SELECT $1, dp.puzzle_date, $2, $3
		  FROM daily_puzzles dp
		 WHERE dp.puzzle_number = $1
		ON CONFLICT (puzzle_number) DO UPDATE
		   SET catch_pct   = EXCLUDED.catch_pct,
		       total_plays = EXCLUDED.total_plays,
		       computed_at = NOW()
	`, s.PuzzleNumber, s.CatchPct, s.TotalPlays)
	return err
}

// LatestCollectiveStat returns the most recently-frozen row above the
// min-plays floor. ok=false when no qualifying row exists — callers should
// render nothing in that case rather than showing 0%.
func (d *DB) LatestCollectiveStat(ctx context.Context, minPlays int) (CollectiveStat, bool, error) {
	var s CollectiveStat
	err := d.QueryRow(ctx, `
		SELECT puzzle_number, catch_pct, total_plays
		  FROM daily_collective_stats
		 WHERE total_plays >= $1
		 ORDER BY puzzle_number DESC
		 LIMIT 1
	`, minPlays).Scan(&s.PuzzleNumber, &s.CatchPct, &s.TotalPlays)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CollectiveStat{}, false, nil
		}
		return CollectiveStat{}, false, err
	}
	return s, true, nil
}
