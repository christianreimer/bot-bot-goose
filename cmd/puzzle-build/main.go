// bbg-puzzle-build — composes the next daily puzzle. Idempotent on
// puzzle_number; safe to re-run.
//
// The 12:00 UTC cron points at this; the 22:00 UTC alarm cron points at
// `bbg-puzzle-build --check` (see TODO at bottom).
//
// v1 scope: pick a mode per the rotation policy and assemble rounds from
// approved content. Full bandit (slot E/P/B sampling) lands with build-order
// step 9 of the plan; this stub uses uniform random selection but keeps the
// rotation policy + author-exclusion contract in place so it can be replaced
// without touching the command-line interface.
package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/google/uuid"
)

func main() {
	fs := flag.NewFlagSet("puzzle-build", flag.ExitOnError)
	dateStr := fs.String("date", "", "puzzle date in YYYY-MM-DD, defaults to tomorrow UTC")
	modeFlag := fs.String("mode", "", "force mode: find_the_bot | find_the_human (default rotates)")
	dbURL := fs.String("db", envDefault("BBG_DB_URL", "postgres://bbg:bbg@localhost:5432/bbg?sslmode=disable"), "db url")
	check := fs.Bool("check", false, "alarm mode: exit 1 if today+1 puzzle is missing")
	_ = fs.Parse(os.Args[1:])

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := context.Background()

	d, err := db.Open(ctx, *dbURL)
	if err != nil {
		log.Error("open db", "err", err)
		os.Exit(1)
	}
	defer d.Close()

	if *check {
		runCheck(ctx, d, log)
		return
	}

	date := tomorrowUTC()
	if *dateStr != "" {
		t, err := time.Parse("2006-01-02", *dateStr)
		if err != nil {
			log.Error("parse date", "err", err)
			os.Exit(2)
		}
		date = t
	}
	if err := runBuild(ctx, d, log, date, db.Mode(*modeFlag)); err != nil {
		log.Error("build", "err", err)
		os.Exit(1)
	}
}

func tomorrowUTC() time.Time {
	t := time.Now().UTC().Add(24 * time.Hour)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// runBuild composes the puzzle for `date`. Idempotent: re-running for the same
// (puzzle_number → date mapping) overwrites the same row.
func runBuild(ctx context.Context, d *db.DB, log *slog.Logger, date time.Time, forcedMode db.Mode) error {
	// Pick or force the mode.
	mode := forcedMode
	if mode == "" {
		mode = pickMode(date)
	}

	// Decide on the puzzle_number. v1 uses sequential numbering starting at 1;
	// the seed inserts #001. For dates after #001 we pick the next number.
	n, err := d.NextPuzzleNumber(ctx)
	if err != nil {
		return err
	}
	log.Info("building puzzle", "n", n, "date", date.Format(time.DateOnly), "mode", mode)

	puzzleID, err := d.InsertDailyPuzzle(ctx, n, date, mode, nil)
	if err != nil {
		return err
	}

	// Need three prompts. We pick the three least-recently-used approved
	// prompts. For v1 we use uniform random over all non-retired prompts.
	prompts, err := selectPrompts(ctx, d, 3)
	if err != nil {
		return err
	}
	if len(prompts) < 3 {
		return fmt.Errorf("need 3 prompts, have %d", len(prompts))
	}

	for i, promptID := range prompts {
		targetKind := "bot"
		if mode == db.ModeFindHuman {
			targetKind = "human"
		}
		roundID, err := d.InsertPuzzleRound(ctx, puzzleID, int16(i), promptID, targetKind, 1)
		if err != nil {
			return err
		}
		answers, err := composeRoundAnswers(ctx, d, promptID, mode)
		if err != nil {
			return fmt.Errorf("compose round %d: %w", i, err)
		}
		if err := d.ReplaceRoundAnswers(ctx, roundID, answers); err != nil {
			return err
		}
	}
	log.Info("puzzle built", "n", n)
	return nil
}

// composeRoundAnswers picks the 4 answers for one round, mode-aware.
//
// TODO(step-9): replace the uniform random pickers with the slot E/P/B bandit
// from internal/puzzle/bandit. The shape of this function — 1 bot + 3 decoys
// for find_the_bot, 3 bots + 1 decoy for find_the_human — is the stable
// contract.
func composeRoundAnswers(ctx context.Context, d *db.DB, promptID uuid.UUID, mode db.Mode) ([]db.Answer, error) {
	if mode == db.ModeFindHuman {
		bots, err := pickApprovedBots(ctx, d, promptID, 3)
		if err != nil {
			return nil, err
		}
		decoy, err := pickApprovedDecoys(ctx, d, promptID, 1)
		if err != nil {
			return nil, err
		}
		if len(bots) < 3 || len(decoy) < 1 {
			return nil, errors.New("not enough approved content for find_the_human round")
		}
		out := make([]db.Answer, 0, 4)
		for _, b := range bots {
			b := b
			out = append(out, db.Answer{ContentKind: db.ContentBot, BotCandidateID: &b.id, AnswerText: b.text})
		}
		out = append(out, db.Answer{ContentKind: db.ContentDecoy, DecoyID: &decoy[0].id, AnswerText: decoy[0].text})
		return out, nil
	}
	// default: find_the_bot — 1 bot + 3 decoys.
	bots, err := pickApprovedBots(ctx, d, promptID, 1)
	if err != nil {
		return nil, err
	}
	decoys, err := pickApprovedDecoys(ctx, d, promptID, 3)
	if err != nil {
		return nil, err
	}
	if len(bots) < 1 || len(decoys) < 3 {
		return nil, errors.New("not enough approved content for find_the_bot round")
	}
	out := []db.Answer{
		{ContentKind: db.ContentBot, BotCandidateID: &bots[0].id, AnswerText: bots[0].text},
	}
	for _, dc := range decoys {
		dc := dc
		out = append(out, db.Answer{ContentKind: db.ContentDecoy, DecoyID: &dc.id, AnswerText: dc.text})
	}
	return out, nil
}

// pickMode implements the mode-rotation policy in the plan: roughly 5
// find_the_bot per 1 find_the_human (~14% inverted). The decision is seeded
// by date to keep idempotent re-runs deterministic.
func pickMode(date time.Time) db.Mode {
	// 6-day cycle: 0..4 = find_the_bot, 5 = find_the_human. Anti-streak
	// enforcement (never 3 consecutive find_the_human) is naturally satisfied
	// by this cadence.
	dayNum := date.Unix() / 86400
	if dayNum%6 == 5 {
		return db.ModeFindHuman
	}
	return db.ModeFindBot
}

// runCheck is the 22:00 UTC alarm: exits non-zero if today+1's puzzle is
// missing. Exit code feeds into the on-call alarm.
func runCheck(ctx context.Context, d *db.DB, log *slog.Logger) {
	target := tomorrowUTC()
	var n int
	err := d.QueryRow(ctx, `SELECT count(*) FROM daily_puzzles WHERE puzzle_date = $1`, target).Scan(&n)
	if err != nil {
		log.Error("check query", "err", err)
		os.Exit(2)
	}
	if n == 0 {
		log.Error("no puzzle for tomorrow", "date", target.Format(time.DateOnly))
		os.Exit(1)
	}
	log.Info("tomorrow's puzzle present", "date", target.Format(time.DateOnly))
}

// ---------------------------------------------------------------------------
// thin pickers — replace with proper bandit later. Author exclusion at serve
// time happens in internal/httpx; here we exclude retired/non-approved only.
// ---------------------------------------------------------------------------

type contentRow struct {
	id   uuid.UUID
	text string
}

func selectPrompts(ctx context.Context, d *db.DB, n int) ([]uuid.UUID, error) {
	rows, err := d.Query(ctx, `
		SELECT id FROM prompts
		 WHERE retired_at IS NULL
		 ORDER BY random()
		 LIMIT $1
	`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]uuid.UUID, 0, n)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func pickApprovedBots(ctx context.Context, d *db.DB, promptID uuid.UUID, n int) ([]contentRow, error) {
	rows, err := d.Query(ctx, `
		SELECT id, text FROM bot_candidates
		 WHERE prompt_id = $1 AND status = 'approved'
		 ORDER BY random()
		 LIMIT $2
	`, promptID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contentRow, 0, n)
	for rows.Next() {
		var c contentRow
		if err := rows.Scan(&c.id, &c.text); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func pickApprovedDecoys(ctx context.Context, d *db.DB, promptID uuid.UUID, n int) ([]contentRow, error) {
	rows, err := d.Query(ctx, `
		SELECT id, text FROM decoy_submissions
		 WHERE prompt_id = $1 AND status = 'approved' AND deleted_at IS NULL
		 ORDER BY random()
		 LIMIT $2
	`, promptID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]contentRow, 0, n)
	for rows.Next() {
		var c contentRow
		if err := rows.Scan(&c.id, &c.text); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// cryptoRand is unused here but kept around for the bandit's Thompson sampling
// when step-9 lands; otherwise go vet complains about unused imports.
func cryptoRandInt(max int64) int64 {
	if max <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(max))
	if err != nil {
		var b [8]byte
		_, _ = rand.Read(b[:])
		return int64(binary.BigEndian.Uint64(b[:])) % max
	}
	return n.Int64()
}

var _ = cryptoRandInt // referenced by future bandit code
