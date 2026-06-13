package db

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// StatOverview is the snapshot bbg-admin stats overview returns: a single
// envelope mixing time-window counts with all-time totals. Fields are named
// after what they answer, not the column they came from.
type StatOverview struct {
	Since                time.Time
	WindowDays           int
	ActiveDevices        int // distinct user_id firing play_started in window
	PlaysStarted         int // events kind='play_started' in window
	PlaysCompleted       int // plays.completed_at IS NOT NULL AND completed_at >= since
	AvgScore             *float64
	DecoysSubmittedTotal int // submitted_at >= since
	DecoysApproved       int // status='approved' submitted_at >= since
	DecoysPending        int // status='pending'  submitted_at >= since
	DecoysRejected       int // status='rejected' submitted_at >= since
	HarvestSubmitted     int // consent_at >= since
	HarvestIngested      int // ingested_decoy_id IS NOT NULL, consent_at >= since
	HarvestRejected      int // rejected_at IS NOT NULL, consent_at >= since
	HarvestUniqueDevices int // distinct user_id in window
	// All-time pool inventory (NOT windowed — what's currently live):
	ApprovedDecoyPool int
	ApprovedBotPool   int
	PendingDecoyPool  int
	PendingBotPool    int
	PendingHarvest    int // ingested IS NULL AND rejected IS NULL
}

// StatOverview computes the snapshot in one tx-free pass. Each query is cheap
// at v1 scale; if we need to speed this up later, a single big CTE-shaped
// query is the obvious next move.
func (d *DB) StatOverview(ctx context.Context, since time.Time) (*StatOverview, error) {
	s := &StatOverview{Since: since.UTC()}
	s.WindowDays = int(time.Since(since).Hours()/24) + 1

	if err := d.QueryRow(ctx, `
		SELECT count(DISTINCT user_id) FROM events
		 WHERE kind = 'play_started' AND at >= $1 AND user_id IS NOT NULL
	`, since).Scan(&s.ActiveDevices); err != nil {
		return nil, err
	}
	if err := d.QueryRow(ctx, `
		SELECT count(*) FROM events
		 WHERE kind = 'play_started' AND at >= $1
	`, since).Scan(&s.PlaysStarted); err != nil {
		return nil, err
	}
	if err := d.QueryRow(ctx, `
		SELECT count(*), avg(score_pct)::float8
		  FROM plays
		 WHERE completed_at IS NOT NULL AND completed_at >= $1
	`, since).Scan(&s.PlaysCompleted, &s.AvgScore); err != nil {
		return nil, err
	}
	if err := d.QueryRow(ctx, `
		SELECT
		  count(*),
		  count(*) FILTER (WHERE status = 'approved'),
		  count(*) FILTER (WHERE status = 'pending'),
		  count(*) FILTER (WHERE status = 'rejected')
		FROM decoy_submissions
		WHERE submitted_at >= $1 AND deleted_at IS NULL
	`, since).Scan(&s.DecoysSubmittedTotal, &s.DecoysApproved, &s.DecoysPending, &s.DecoysRejected); err != nil {
		return nil, err
	}
	if err := d.QueryRow(ctx, `
		SELECT
		  count(*),
		  count(*) FILTER (WHERE ingested_decoy_id IS NOT NULL),
		  count(*) FILTER (WHERE rejected_at IS NOT NULL),
		  count(DISTINCT user_id)
		FROM pre_launch_submissions
		WHERE consent_at >= $1
	`, since).Scan(&s.HarvestSubmitted, &s.HarvestIngested, &s.HarvestRejected, &s.HarvestUniqueDevices); err != nil {
		return nil, err
	}
	if err := d.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM decoy_submissions WHERE status='approved' AND deleted_at IS NULL),
		  (SELECT count(*) FROM bot_candidates    WHERE status='approved'),
		  (SELECT count(*) FROM decoy_submissions WHERE status='pending'  AND deleted_at IS NULL),
		  (SELECT count(*) FROM bot_candidates    WHERE status='pending'),
		  (SELECT count(*) FROM pre_launch_submissions WHERE ingested_decoy_id IS NULL AND rejected_at IS NULL)
	`).Scan(&s.ApprovedDecoyPool, &s.ApprovedBotPool, &s.PendingDecoyPool, &s.PendingBotPool, &s.PendingHarvest); err != nil {
		return nil, err
	}
	return s, nil
}

// PlayersByDay is one row of the daily-players time series.
type PlayersByDay struct {
	Day            time.Time
	ActiveDevices  int
	PlaysStarted   int
	PlaysCompleted int
	AvgScore       *float64
}

// PlayersByDay returns one row per day in [since, now], oldest first.
func (d *DB) PlayersByDay(ctx context.Context, since time.Time) ([]PlayersByDay, error) {
	const q = `
		WITH days AS (
		    SELECT generate_series(date_trunc('day', $1::timestamptz),
		                           date_trunc('day', now()),
		                           interval '1 day')::date AS day
		),
		starts AS (
		    SELECT date_trunc('day', at)::date AS day,
		           count(*)                    AS started,
		           count(DISTINCT user_id) FILTER (WHERE user_id IS NOT NULL) AS active
		      FROM events
		     WHERE kind = 'play_started' AND at >= $1
		     GROUP BY 1
		),
		completes AS (
		    SELECT date_trunc('day', completed_at)::date AS day,
		           count(*)                              AS completed,
		           avg(score_pct)::float8                AS avg_score
		      FROM plays
		     WHERE completed_at IS NOT NULL AND completed_at >= $1
		     GROUP BY 1
		)
		SELECT d.day,
		       COALESCE(s.active, 0), COALESCE(s.started, 0),
		       COALESCE(c.completed, 0), c.avg_score
		  FROM days d
		  LEFT JOIN starts    s ON s.day = d.day
		  LEFT JOIN completes c ON c.day = d.day
		 ORDER BY d.day
	`
	rows, err := d.Query(ctx, q, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlayersByDay
	for rows.Next() {
		var r PlayersByDay
		if err := rows.Scan(&r.Day, &r.ActiveDevices, &r.PlaysStarted, &r.PlaysCompleted, &r.AvgScore); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DecoysByDay is one row of the daily-decoy-submissions time series.
type DecoysByDay struct {
	Day       time.Time
	Submitted int
	Approved  int
	Pending   int
	Rejected  int
}

// DecoysByDay returns one row per day in [since, now], oldest first.
// Counts decoy_submissions rows that landed (submitted_at) on each day,
// bucketed by their current status — so a row that was submitted on day X
// and approved later shows up on day X under "approved" once reviewed.
func (d *DB) DecoysByDay(ctx context.Context, since time.Time) ([]DecoysByDay, error) {
	const q = `
		WITH days AS (
		    SELECT generate_series(date_trunc('day', $1::timestamptz),
		                           date_trunc('day', now()),
		                           interval '1 day')::date AS day
		),
		grouped AS (
		    SELECT date_trunc('day', submitted_at)::date AS day,
		           count(*)                              AS submitted,
		           count(*) FILTER (WHERE status='approved') AS approved,
		           count(*) FILTER (WHERE status='pending')  AS pending,
		           count(*) FILTER (WHERE status='rejected') AS rejected
		      FROM decoy_submissions
		     WHERE submitted_at >= $1 AND deleted_at IS NULL
		     GROUP BY 1
		)
		SELECT d.day,
		       COALESCE(g.submitted, 0), COALESCE(g.approved, 0),
		       COALESCE(g.pending, 0), COALESCE(g.rejected, 0)
		  FROM days d
		  LEFT JOIN grouped g ON g.day = d.day
		 ORDER BY d.day
	`
	rows, err := d.Query(ctx, q, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DecoysByDay
	for rows.Next() {
		var r DecoysByDay
		if err := rows.Scan(&r.Day, &r.Submitted, &r.Approved, &r.Pending, &r.Rejected); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// HarvestByDay is one row of the daily-harvest time series.
type HarvestByDay struct {
	Day            time.Time
	Submitted      int
	Ingested       int
	Rejected       int
	StillPending   int // submitted on this day, still in pending state
	UniqueDevices  int
}

// HarvestByDay returns one row per day in [since, now], oldest first.
func (d *DB) HarvestByDay(ctx context.Context, since time.Time) ([]HarvestByDay, error) {
	const q = `
		WITH days AS (
		    SELECT generate_series(date_trunc('day', $1::timestamptz),
		                           date_trunc('day', now()),
		                           interval '1 day')::date AS day
		),
		grouped AS (
		    SELECT date_trunc('day', consent_at)::date AS day,
		           count(*)                            AS submitted,
		           count(*) FILTER (WHERE ingested_decoy_id IS NOT NULL) AS ingested,
		           count(*) FILTER (WHERE rejected_at      IS NOT NULL) AS rejected,
		           count(*) FILTER (WHERE ingested_decoy_id IS NULL
		                              AND rejected_at      IS NULL)     AS still_pending,
		           count(DISTINCT user_id)                               AS unique_devices
		      FROM pre_launch_submissions
		     WHERE consent_at >= $1
		     GROUP BY 1
		)
		SELECT d.day,
		       COALESCE(g.submitted, 0), COALESCE(g.ingested, 0),
		       COALESCE(g.rejected, 0), COALESCE(g.still_pending, 0),
		       COALESCE(g.unique_devices, 0)
		  FROM days d
		  LEFT JOIN grouped g ON g.day = d.day
		 ORDER BY d.day
	`
	rows, err := d.Query(ctx, q, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HarvestByDay
	for rows.Next() {
		var r HarvestByDay
		if err := rows.Scan(&r.Day, &r.Submitted, &r.Ingested, &r.Rejected, &r.StillPending, &r.UniqueDevices); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TopDecoyByFoolRate is one row of the "best human decoys" leaderboard slice
// used by `stats decoys --top`.
type TopDecoyByFoolRate struct {
	DecoyID     uuid.UUID
	Text        string
	PromptText  string
	Impressions int
	PickedAsBot int
	FoolRate    *float64
}

// TopDecoysByFoolRate aggregates decoy_daily_stats over [since, now] and
// returns the highest-fool-rate decoys. min_impressions filters out
// low-volume noise.
func (d *DB) TopDecoysByFoolRate(ctx context.Context, since time.Time, limit, minImpressions int) ([]TopDecoyByFoolRate, error) {
	const q = `
		SELECT ds.id, ds.text, p.text,
		       sum(s.impressions)::int  AS impressions,
		       sum(s.picked_as_bot)::int AS picked,
		       (sum(s.picked_as_bot)::numeric / NULLIF(sum(s.impressions), 0))::float8 AS fool_rate
		  FROM decoy_daily_stats s
		  JOIN decoy_submissions ds ON ds.id = s.decoy_id
		  JOIN prompts p            ON p.id = ds.prompt_id
		 WHERE s.stat_date >= $1
		 GROUP BY ds.id, ds.text, p.text
		HAVING sum(s.impressions) >= $2
		 ORDER BY fool_rate DESC NULLS LAST
		 LIMIT $3
	`
	rows, err := d.Query(ctx, q, since, minImpressions, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopDecoyByFoolRate
	for rows.Next() {
		var r TopDecoyByFoolRate
		if err := rows.Scan(&r.DecoyID, &r.Text, &r.PromptText, &r.Impressions, &r.PickedAsBot, &r.FoolRate); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
