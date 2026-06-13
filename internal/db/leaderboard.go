package db

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DecoyAggregate is the per-user, mode-split lifetime decoy total — the
// input to the leaderboard rollup.
type DecoyAggregate struct {
	UserID        uuid.UUID
	BotImp        int64
	BotPicked     int64
	HumanImp      int64
	HumanPicked   int64
}

// AggregateDecoyStats walks decoy_daily_stats + decoy_submissions and yields
// one row per author with their mode-split totals. Soft-deleted decoys are
// excluded; system-seeded decoys (user_id IS NULL) are too — they don't
// belong on a leaderboard.
func (d *DB) AggregateDecoyStats(ctx context.Context) ([]DecoyAggregate, error) {
	rows, err := d.Query(ctx, `
		SELECT ds.user_id,
		       COALESCE(SUM(s.impressions)   FILTER (WHERE s.mode = 'find_the_bot'), 0),
		       COALESCE(SUM(s.picked_as_bot) FILTER (WHERE s.mode = 'find_the_bot'), 0),
		       COALESCE(SUM(s.impressions)   FILTER (WHERE s.mode = 'find_the_human'), 0),
		       COALESCE(SUM(s.picked_as_bot) FILTER (WHERE s.mode = 'find_the_human'), 0)
		  FROM decoy_submissions ds
		  LEFT JOIN decoy_daily_stats s ON s.decoy_id = ds.id
		 WHERE ds.user_id IS NOT NULL AND ds.deleted_at IS NULL
		 GROUP BY ds.user_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DecoyAggregate
	for rows.Next() {
		var a DecoyAggregate
		if err := rows.Scan(&a.UserID, &a.BotImp, &a.BotPicked, &a.HumanImp, &a.HumanPicked); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpsertForgerRanking writes a user's computed ranking row. Used by the rollup.
func (d *DB) UpsertForgerRanking(ctx context.Context, userID uuid.UUID, adjusted float64, totalImp, totalPicked int64, tier string, now time.Time) error {
	_, err := d.Exec(ctx, `
		INSERT INTO forger_rankings
		    (user_id, adjusted_fool_rate, total_impressions, total_picked_as_bot, tier, computed_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id) DO UPDATE
		   SET adjusted_fool_rate = EXCLUDED.adjusted_fool_rate,
		       total_impressions  = EXCLUDED.total_impressions,
		       total_picked_as_bot = EXCLUDED.total_picked_as_bot,
		       tier = EXCLUDED.tier,
		       computed_at = EXCLUDED.computed_at
	`, userID, adjusted, totalImp, totalPicked, tier, now)
	return err
}

// ForgerLeaderboardRow is one row on the public leaderboard.
type ForgerLeaderboardRow struct {
	Rank             int
	UserID           uuid.UUID
	Handle           string
	AdjustedFoolRate float64
	Tier             string
	TotalImpressions int64
	TotalPickedAsBot int64
}

// TopForgers returns the top n forgers above the impression-eligibility gate.
// `gate` is leaderboard.MinImpressionsEligible (passed in so the db package
// stays free of leaderboard imports).
func (d *DB) TopForgers(ctx context.Context, n int, gate int64) ([]ForgerLeaderboardRow, error) {
	rows, err := d.Query(ctx, `
		SELECT u.id,
		       CASE WHEN u.handle IS NULL OR u.handle = ''
		            THEN 'anonymous' ELSE u.handle END,
		       fr.adjusted_fool_rate,
		       fr.tier,
		       fr.total_impressions,
		       fr.total_picked_as_bot
		  FROM forger_rankings fr
		  JOIN users u ON u.id = fr.user_id
		 WHERE fr.total_impressions >= $2 AND u.deleted_at IS NULL
		 ORDER BY fr.adjusted_fool_rate DESC, fr.total_impressions DESC
		 LIMIT $1
	`, n, gate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ForgerLeaderboardRow
	rank := 1
	for rows.Next() {
		var r ForgerLeaderboardRow
		if err := rows.Scan(&r.UserID, &r.Handle, &r.AdjustedFoolRate, &r.Tier, &r.TotalImpressions, &r.TotalPickedAsBot); err != nil {
			return nil, err
		}
		r.Rank = rank
		rank++
		out = append(out, r)
	}
	return out, rows.Err()
}

// EligibleForgerCount tells the user what they're competing against. Used
// in the "Rank #4 of 1,208 forgers" copy from §4.
func (d *DB) EligibleForgerCount(ctx context.Context, gate int64) (int, error) {
	var n int
	err := d.QueryRow(ctx, `
		SELECT count(*) FROM forger_rankings WHERE total_impressions >= $1
	`, gate).Scan(&n)
	return n, err
}

// SpotterLeaderboardRow ranks players by best Bot-Dar % over completed plays.
// We use avg(score_pct) over the last 30 days, with a min-plays gate, until
// spotter_elo gets a real implementation in step 12 of the plan.
type SpotterLeaderboardRow struct {
	Rank      int
	UserID    uuid.UUID
	Handle    string
	AvgScore  float64
	Plays     int
}

func (d *DB) TopSpotters(ctx context.Context, n int, minPlays int) ([]SpotterLeaderboardRow, error) {
	rows, err := d.Query(ctx, `
		SELECT u.id,
		       CASE WHEN u.handle IS NULL OR u.handle = ''
		            THEN 'anonymous' ELSE u.handle END,
		       AVG(p.score_pct)::float8 AS avg_score,
		       count(*) AS plays
		  FROM plays p
		  JOIN users u ON u.id = p.user_id
		 WHERE p.completed_at IS NOT NULL AND p.score_pct IS NOT NULL AND u.deleted_at IS NULL
		 GROUP BY u.id, u.handle
		 HAVING count(*) >= $2
		 ORDER BY avg_score DESC, plays DESC
		 LIMIT $1
	`, n, minPlays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpotterLeaderboardRow
	rank := 1
	for rows.Next() {
		var r SpotterLeaderboardRow
		if err := rows.Scan(&r.UserID, &r.Handle, &r.AvgScore, &r.Plays); err != nil {
			return nil, err
		}
		r.Rank = rank
		rank++
		out = append(out, r)
	}
	return out, rows.Err()
}

// UserDecoy is one row of "my decoys" on the /me page.
type UserDecoy struct {
	ID              uuid.UUID
	PromptText      string
	Text            string
	Status          string
	BotImp          int64
	BotPicked       int64
	HumanImp        int64
	HumanPicked     int64
	SubmittedAt     time.Time
}

func (d *DB) UserDecoys(ctx context.Context, userID uuid.UUID) ([]UserDecoy, error) {
	rows, err := d.Query(ctx, `
		SELECT ds.id, p.text, ds.text, ds.status,
		       COALESCE(SUM(s.impressions)   FILTER (WHERE s.mode = 'find_the_bot'), 0),
		       COALESCE(SUM(s.picked_as_bot) FILTER (WHERE s.mode = 'find_the_bot'), 0),
		       COALESCE(SUM(s.impressions)   FILTER (WHERE s.mode = 'find_the_human'), 0),
		       COALESCE(SUM(s.picked_as_bot) FILTER (WHERE s.mode = 'find_the_human'), 0),
		       ds.submitted_at
		  FROM decoy_submissions ds
		  JOIN prompts p ON p.id = ds.prompt_id
		  LEFT JOIN decoy_daily_stats s ON s.decoy_id = ds.id
		 WHERE ds.user_id = $1 AND ds.deleted_at IS NULL
		 GROUP BY ds.id, p.text, ds.text, ds.status, ds.submitted_at
		 ORDER BY ds.submitted_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserDecoy
	for rows.Next() {
		var d UserDecoy
		if err := rows.Scan(&d.ID, &d.PromptText, &d.Text, &d.Status, &d.BotImp, &d.BotPicked, &d.HumanImp, &d.HumanPicked, &d.SubmittedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PublicDecoy is the data behind /d/<short> — the standalone share page.
// Author handle is included so the page can credit them (opt-in via handle
// having been set; falls back to "anonymous" otherwise).
type PublicDecoy struct {
	ID            uuid.UUID
	Text          string
	PromptText    string
	Status        string
	BotImp        int64
	BotPicked     int64
	HumanImp      int64
	HumanPicked   int64
	AuthorUserID  *uuid.UUID
	AuthorHandle  string // empty if anonymous / no user
}

// DecoyByShortID resolves a /d/<short> URL to its decoy. The short is the
// 12-hex-char prefix of the UUID; we match by prefix-LIKE. TODO at scale:
// promote `short_id` to a stored column with its own index.
func (d *DB) DecoyByShortID(ctx context.Context, short string) (*PublicDecoy, error) {
	const q = `
		SELECT ds.id, ds.text, p.text, ds.status,
		       COALESCE(SUM(s.impressions)   FILTER (WHERE s.mode = 'find_the_bot'), 0),
		       COALESCE(SUM(s.picked_as_bot) FILTER (WHERE s.mode = 'find_the_bot'), 0),
		       COALESCE(SUM(s.impressions)   FILTER (WHERE s.mode = 'find_the_human'), 0),
		       COALESCE(SUM(s.picked_as_bot) FILTER (WHERE s.mode = 'find_the_human'), 0),
		       ds.user_id,
		       CASE WHEN u.handle IS NULL OR u.handle = ''
		            THEN '' ELSE u.handle END
		  FROM decoy_submissions ds
		  JOIN prompts p ON p.id = ds.prompt_id
		  LEFT JOIN users u ON u.id = ds.user_id
		  LEFT JOIN decoy_daily_stats s ON s.decoy_id = ds.id
		 WHERE replace(ds.id::text, '-', '') LIKE $1 || '%'
		   AND ds.deleted_at IS NULL
		 GROUP BY ds.id, ds.text, p.text, ds.status, ds.user_id, u.handle
		 LIMIT 1
	`
	row := d.QueryRow(ctx, q, short)
	pd := &PublicDecoy{}
	if err := row.Scan(&pd.ID, &pd.Text, &pd.PromptText, &pd.Status,
		&pd.BotImp, &pd.BotPicked, &pd.HumanImp, &pd.HumanPicked,
		&pd.AuthorUserID, &pd.AuthorHandle); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return pd, nil
}

// SpotterRankingFor returns the user's row on the spotter leaderboard:
// rank (1-based among users with ≥ minPlays), avg Bot-Dar, and play
// count. Mirrors ForgerRankingFor in shape so /me can render both
// standings with parallel data flow. Returns ErrNotFound only when the
// user has zero completed scored plays; sub-gate users still get a row
// back with Plays > 0 and Rank = 0 so the template can show
// "X plays, need Y to rank."
func (d *DB) SpotterRankingFor(ctx context.Context, userID uuid.UUID, minPlays int) (*SpotterLeaderboardRow, error) {
	var r SpotterLeaderboardRow
	err := d.QueryRow(ctx, `
		WITH stats AS (
		    SELECT u.id AS user_id,
		           CASE WHEN u.handle IS NULL OR u.handle = ''
		                THEN 'anonymous' ELSE u.handle END AS handle,
		           AVG(p.score_pct)::float8 AS avg_score,
		           count(*) AS plays
		      FROM plays p
		      JOIN users u ON u.id = p.user_id
		     WHERE p.completed_at IS NOT NULL AND p.score_pct IS NOT NULL
		       AND u.deleted_at IS NULL
		     GROUP BY u.id, u.handle
		),
		ranked AS (
		    SELECT user_id, handle, avg_score, plays,
		           CASE WHEN plays >= $2 THEN
		                row_number() OVER (
		                    PARTITION BY (plays >= $2)
		                    ORDER BY avg_score DESC, plays DESC
		                )
		           ELSE 0 END AS rk
		      FROM stats
		)
		SELECT handle, avg_score, plays, rk
		  FROM ranked
		 WHERE user_id = $1
	`, userID, minPlays).Scan(&r.Handle, &r.AvgScore, &r.Plays, &r.Rank)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.UserID = userID
	return &r, nil
}

// EligibleSpotterCount tells the user what they're competing against on
// the spotter board. Mirrors EligibleForgerCount.
func (d *DB) EligibleSpotterCount(ctx context.Context, minPlays int) (int, error) {
	var n int
	err := d.QueryRow(ctx, `
		SELECT count(*) FROM (
		    SELECT p.user_id
		      FROM plays p
		      JOIN users u ON u.id = p.user_id
		     WHERE p.completed_at IS NOT NULL AND p.score_pct IS NOT NULL
		       AND u.deleted_at IS NULL
		     GROUP BY p.user_id
		    HAVING count(*) >= $1
		) t
	`, minPlays).Scan(&n)
	return n, err
}

// ForgerRankingFor returns the user's current row + rank (1-based, among
// eligible forgers). rank=0 means "not on the board yet (under min impressions)".
func (d *DB) ForgerRankingFor(ctx context.Context, userID uuid.UUID, gate int64) (*ForgerLeaderboardRow, error) {
	var r ForgerLeaderboardRow
	err := d.QueryRow(ctx, `
		WITH ranked AS (
		    SELECT user_id, adjusted_fool_rate, tier, total_impressions, total_picked_as_bot,
		           CASE WHEN total_impressions >= $2 THEN
		                row_number() OVER (
		                    PARTITION BY (total_impressions >= $2)
		                    ORDER BY adjusted_fool_rate DESC, total_impressions DESC
		                )
		           ELSE 0 END AS rk
		      FROM forger_rankings
		)
		SELECT CASE WHEN u.handle IS NULL OR u.handle = ''
		            THEN 'anonymous' ELSE u.handle END,
		       ranked.adjusted_fool_rate, ranked.tier,
		       ranked.total_impressions, ranked.total_picked_as_bot, ranked.rk
		  FROM ranked
		  JOIN users u ON u.id = ranked.user_id
		 WHERE ranked.user_id = $1
	`, userID, gate).Scan(&r.Handle, &r.AdjustedFoolRate, &r.Tier, &r.TotalImpressions, &r.TotalPickedAsBot, &r.Rank)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.UserID = userID
	return &r, nil
}
