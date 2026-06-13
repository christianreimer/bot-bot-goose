package db

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DecoyAggregate is the per-user lifetime decoy total — the input to the
// leaderboard rollup. Two tracks live side-by-side: the fool track
// (Impressions / PickedAsBot, kept for display flavor only) and the realest
// track (RealestImpressions / RealestVotes, used for ranking).
type DecoyAggregate struct {
	UserID             uuid.UUID
	Impressions        int64
	PickedAsBot        int64
	RealestImpressions int64
	RealestVotes       int64
}

// AggregateDecoyStats walks decoy_daily_stats + decoy_submissions and yields
// one row per author with their totals. Soft-deleted decoys are excluded;
// system-seeded decoys (user_id IS NULL) are too — they don't belong on a
// leaderboard.
func (d *DB) AggregateDecoyStats(ctx context.Context) ([]DecoyAggregate, error) {
	rows, err := d.Query(ctx, `
		SELECT ds.user_id,
		       COALESCE(SUM(s.impressions),         0),
		       COALESCE(SUM(s.picked_as_bot),       0),
		       COALESCE(SUM(s.realest_impressions), 0),
		       COALESCE(SUM(s.realest_votes),       0)
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
		if err := rows.Scan(&a.UserID, &a.Impressions, &a.PickedAsBot, &a.RealestImpressions, &a.RealestVotes); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ForgerRankingUpsert carries everything the rollup writes per user. Two
// rates live side-by-side; the leaderboard orders by adjusted_realest_rate
// (the new primary), while adjusted_fool_rate stays as display-only flavor.
type ForgerRankingUpsert struct {
	UserID                  uuid.UUID
	AdjustedFoolRate        float64
	TotalImpressions        int64
	TotalPickedAsBot        int64
	AdjustedRealestRate     float64
	RealestTotalImpressions int64
	RealestTotalVotes       int64
	RealestBeyondChance     int
	Tier                    string
	ComputedAt              time.Time
}

// UpsertForgerRanking writes a user's computed ranking row. Used by the rollup.
func (d *DB) UpsertForgerRanking(ctx context.Context, r ForgerRankingUpsert) error {
	_, err := d.Exec(ctx, `
		INSERT INTO forger_rankings
		    (user_id, adjusted_fool_rate, total_impressions, total_picked_as_bot,
		     adjusted_realest_rate, realest_total_impressions, realest_total_votes, realest_beyond_chance,
		     tier, computed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (user_id) DO UPDATE
		   SET adjusted_fool_rate         = EXCLUDED.adjusted_fool_rate,
		       total_impressions          = EXCLUDED.total_impressions,
		       total_picked_as_bot        = EXCLUDED.total_picked_as_bot,
		       adjusted_realest_rate      = EXCLUDED.adjusted_realest_rate,
		       realest_total_impressions  = EXCLUDED.realest_total_impressions,
		       realest_total_votes        = EXCLUDED.realest_total_votes,
		       realest_beyond_chance      = EXCLUDED.realest_beyond_chance,
		       tier                       = EXCLUDED.tier,
		       computed_at                = EXCLUDED.computed_at
	`, r.UserID,
		r.AdjustedFoolRate, r.TotalImpressions, r.TotalPickedAsBot,
		r.AdjustedRealestRate, r.RealestTotalImpressions, r.RealestTotalVotes, r.RealestBeyondChance,
		r.Tier, r.ComputedAt)
	return err
}

// ForgerLeaderboardRow is one row on the public leaderboard. AdjustedRealestRate
// is the ranked metric; AdjustedFoolRate stays as flavor.
type ForgerLeaderboardRow struct {
	Rank                    int
	UserID                  uuid.UUID
	Handle                  string
	AdjustedRealestRate     float64
	RealestTotalImpressions int64
	RealestTotalVotes       int64
	RealestBeyondChance     int
	AdjustedFoolRate        float64
	TotalImpressions        int64
	TotalPickedAsBot        int64
	Tier                    string
}

// TopForgers returns the top n forgers above the realest-impression
// eligibility gate. The board is ordered by the realest track now; the
// fool columns ride along for display-only flavor. `gate` is
// leaderboard.MinImpressionsEligible (passed in so the db package stays
// free of leaderboard imports). The gate applies to realest impressions
// because that's what the ranking depends on.
func (d *DB) TopForgers(ctx context.Context, n int, gate int64) ([]ForgerLeaderboardRow, error) {
	rows, err := d.Query(ctx, `
		SELECT u.id,
		       CASE WHEN u.handle IS NULL OR u.handle = ''
		            THEN 'anonymous' ELSE u.handle END,
		       fr.adjusted_realest_rate,
		       fr.realest_total_impressions,
		       fr.realest_total_votes,
		       fr.realest_beyond_chance,
		       fr.adjusted_fool_rate,
		       fr.total_impressions,
		       fr.total_picked_as_bot,
		       fr.tier
		  FROM forger_rankings fr
		  JOIN users u ON u.id = fr.user_id
		 WHERE fr.realest_total_impressions >= $2 AND u.deleted_at IS NULL
		 ORDER BY fr.adjusted_realest_rate DESC, fr.realest_total_impressions DESC
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
		if err := rows.Scan(&r.UserID, &r.Handle,
			&r.AdjustedRealestRate, &r.RealestTotalImpressions, &r.RealestTotalVotes, &r.RealestBeyondChance,
			&r.AdjustedFoolRate, &r.TotalImpressions, &r.TotalPickedAsBot,
			&r.Tier); err != nil {
			return nil, err
		}
		r.Rank = rank
		rank++
		out = append(out, r)
	}
	return out, rows.Err()
}

// EligibleForgerCount tells the user what they're competing against. Used
// in the "Rank #4 of 1,208 forgers" copy from §4. Gates on the realest
// track because that's what the leaderboard ranks on.
func (d *DB) EligibleForgerCount(ctx context.Context, gate int64) (int, error) {
	var n int
	err := d.QueryRow(ctx, `
		SELECT count(*) FROM forger_rankings WHERE realest_total_impressions >= $1
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

// UserDecoy is one row of "my decoys" on the /me page. Realest fields are
// the primary signal; Impressions/PickedAsBot are kept as display-only flavor.
type UserDecoy struct {
	ID                 uuid.UUID
	PromptText         string
	Text               string
	Status             string
	Impressions        int64
	PickedAsBot        int64
	RealestImpressions int64
	RealestVotes       int64
	SubmittedAt        time.Time
}

func (d *DB) UserDecoys(ctx context.Context, userID uuid.UUID) ([]UserDecoy, error) {
	rows, err := d.Query(ctx, `
		SELECT ds.id, p.text, ds.text, ds.status,
		       COALESCE(SUM(s.impressions),         0),
		       COALESCE(SUM(s.picked_as_bot),       0),
		       COALESCE(SUM(s.realest_impressions), 0),
		       COALESCE(SUM(s.realest_votes),       0),
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
		if err := rows.Scan(&d.ID, &d.PromptText, &d.Text, &d.Status,
			&d.Impressions, &d.PickedAsBot,
			&d.RealestImpressions, &d.RealestVotes,
			&d.SubmittedAt); err != nil {
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
	ID                 uuid.UUID
	Text               string
	PromptText         string
	Status             string
	Impressions        int64
	PickedAsBot        int64
	RealestImpressions int64
	RealestVotes       int64
	AuthorUserID       *uuid.UUID
	AuthorHandle       string // empty if anonymous / no user
}

// DecoyByShortID resolves a /d/<short> URL to its decoy. The short is the
// 12-hex-char prefix of the UUID; we match by prefix-LIKE. TODO at scale:
// promote `short_id` to a stored column with its own index.
func (d *DB) DecoyByShortID(ctx context.Context, short string) (*PublicDecoy, error) {
	const q = `
		SELECT ds.id, ds.text, p.text, ds.status,
		       COALESCE(SUM(s.impressions),         0),
		       COALESCE(SUM(s.picked_as_bot),       0),
		       COALESCE(SUM(s.realest_impressions), 0),
		       COALESCE(SUM(s.realest_votes),       0),
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
		&pd.Impressions, &pd.PickedAsBot,
		&pd.RealestImpressions, &pd.RealestVotes,
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
// Ranks on adjusted_realest_rate now; fool columns ride along as flavor.
func (d *DB) ForgerRankingFor(ctx context.Context, userID uuid.UUID, gate int64) (*ForgerLeaderboardRow, error) {
	var r ForgerLeaderboardRow
	err := d.QueryRow(ctx, `
		WITH ranked AS (
		    SELECT user_id,
		           adjusted_realest_rate, realest_total_impressions, realest_total_votes, realest_beyond_chance,
		           adjusted_fool_rate, total_impressions, total_picked_as_bot,
		           tier,
		           CASE WHEN realest_total_impressions >= $2 THEN
		                row_number() OVER (
		                    PARTITION BY (realest_total_impressions >= $2)
		                    ORDER BY adjusted_realest_rate DESC, realest_total_impressions DESC
		                )
		           ELSE 0 END AS rk
		      FROM forger_rankings
		)
		SELECT CASE WHEN u.handle IS NULL OR u.handle = ''
		            THEN 'anonymous' ELSE u.handle END,
		       ranked.adjusted_realest_rate, ranked.realest_total_impressions, ranked.realest_total_votes, ranked.realest_beyond_chance,
		       ranked.adjusted_fool_rate, ranked.total_impressions, ranked.total_picked_as_bot,
		       ranked.tier, ranked.rk
		  FROM ranked
		  JOIN users u ON u.id = ranked.user_id
		 WHERE ranked.user_id = $1
	`, userID, gate).Scan(&r.Handle,
		&r.AdjustedRealestRate, &r.RealestTotalImpressions, &r.RealestTotalVotes, &r.RealestBeyondChance,
		&r.AdjustedFoolRate, &r.TotalImpressions, &r.TotalPickedAsBot,
		&r.Tier, &r.Rank)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.UserID = userID
	return &r, nil
}

// CastRealestVoteResult names the outcome of a vote so the API can return
// a structured response.
type CastRealestVoteResult struct {
	Recorded   bool      // true on first vote, false on re-vote
	PreviousID uuid.UUID // zero value when this is the first vote
}

// CastRealestVote records (or re-records) the player's "felt most human"
// vote for a round. Inside one transaction:
//   - looks up the round's prior realest_decoy_id
//   - writes the new decoy id to play_rounds
//   - on first vote: +1 realest_impressions for each of the 3 human decoys
//     in the round, +1 realest_votes for the chosen one
//   - on re-vote: shifts 1 realest_vote from the prior decoy to the new one
//     (impressions stay put — the round was only ever "shown" once)
//
// The handler is responsible for validating that newDecoyID is one of the
// three human decoys actually shown in this round. The DB layer trusts the
// caller on identity.
func (d *DB) CastRealestVote(ctx context.Context, playRoundID, newDecoyID uuid.UUID) (CastRealestVoteResult, error) {
	tx, err := d.Begin(ctx)
	if err != nil {
		return CastRealestVoteResult{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var prev *uuid.UUID
	if err := tx.QueryRow(ctx,
		`SELECT realest_decoy_id FROM play_rounds WHERE id = $1`, playRoundID,
	).Scan(&prev); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CastRealestVoteResult{}, ErrNotFound
		}
		return CastRealestVoteResult{}, err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE play_rounds SET realest_decoy_id = $2 WHERE id = $1`,
		playRoundID, newDecoyID,
	); err != nil {
		return CastRealestVoteResult{}, err
	}

	if prev == nil {
		// First vote on this round. +1 realest_impressions to every decoy
		// shown in the round; +1 realest_votes to the chosen one.
		if _, err := tx.Exec(ctx, `
			INSERT INTO decoy_daily_stats (decoy_id, stat_date, realest_impressions, realest_votes)
			SELECT pra.decoy_id,
			       CURRENT_DATE,
			       1,
			       CASE WHEN pra.decoy_id = $2 THEN 1 ELSE 0 END
			  FROM play_rounds pr
			  JOIN plays p              ON p.id = pr.play_id
			  JOIN puzzle_rounds qr     ON qr.daily_puzzle_id = p.daily_puzzle_id
			                            AND qr.round_index    = pr.round_index
			  JOIN puzzle_round_answers pra ON pra.round_id = qr.id
			 WHERE pr.id = $1 AND pra.content_kind = 'decoy'
			ON CONFLICT (decoy_id, stat_date) DO UPDATE
			   SET realest_impressions = decoy_daily_stats.realest_impressions + EXCLUDED.realest_impressions,
			       realest_votes       = decoy_daily_stats.realest_votes       + EXCLUDED.realest_votes
		`, playRoundID, newDecoyID); err != nil {
			return CastRealestVoteResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return CastRealestVoteResult{}, err
		}
		return CastRealestVoteResult{Recorded: true}, nil
	}

	// Re-vote: same decoy → no-op.
	if *prev == newDecoyID {
		if err := tx.Commit(ctx); err != nil {
			return CastRealestVoteResult{}, err
		}
		return CastRealestVoteResult{Recorded: false, PreviousID: *prev}, nil
	}

	// Move 1 realest_vote from prev → new. Impressions unchanged.
	if _, err := tx.Exec(ctx, `
		UPDATE decoy_daily_stats
		   SET realest_votes = GREATEST(realest_votes - 1, 0)
		 WHERE decoy_id = $1
		   AND stat_date = (SELECT MAX(stat_date) FROM decoy_daily_stats WHERE decoy_id = $1)
	`, *prev); err != nil {
		return CastRealestVoteResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO decoy_daily_stats (decoy_id, stat_date, realest_impressions, realest_votes)
		VALUES ($1, CURRENT_DATE, 0, 1)
		ON CONFLICT (decoy_id, stat_date) DO UPDATE
		   SET realest_votes = decoy_daily_stats.realest_votes + 1
	`, newDecoyID); err != nil {
		return CastRealestVoteResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return CastRealestVoteResult{}, err
	}
	return CastRealestVoteResult{Recorded: false, PreviousID: *prev}, nil
}
