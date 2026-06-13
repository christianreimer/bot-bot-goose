// bbg-puzzle-build — composes the next daily puzzle. Idempotent on
// puzzle_number; safe to re-run.
//
// The 12:00 UTC cron points at this; the 22:00 UTC alarm cron points at
// `bbg-puzzle-build --check`.
//
// The actual composition (pickers, round shape) lives in internal/puzzle
// so both this cron and `bbg-admin puzzle compose|schedule` produce
// identical puzzles. Every round is 1 bot + 3 decoys.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/christianreimer/bot-bot-goose/internal/puzzle"
)

func main() {
	fs := flag.NewFlagSet("puzzle-build", flag.ExitOnError)
	dateStr := fs.String("date", "", "puzzle date in YYYY-MM-DD, defaults to tomorrow UTC")
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
	if err := runBuild(ctx, d, log, date); err != nil {
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

// runBuild composes the puzzle for `date`. Idempotent on puzzle_number.
func runBuild(ctx context.Context, d *db.DB, log *slog.Logger, date time.Time) error {
	n, err := d.NextPuzzleNumber(ctx)
	if err != nil {
		return err
	}
	log.Info("building puzzle", "n", n, "date", date.Format(time.DateOnly))

	puzzleID, err := d.InsertDailyPuzzle(ctx, n, date, nil)
	if err != nil {
		return err
	}

	prompts, err := puzzle.SelectPrompts(ctx, d, 3)
	if err != nil {
		return err
	}
	if len(prompts) < 3 {
		return fmt.Errorf("need 3 prompts, have %d", len(prompts))
	}

	for i, promptID := range prompts {
		roundID, err := d.InsertPuzzleRound(ctx, puzzleID, int16(i), promptID, 1)
		if err != nil {
			return err
		}
		answers, err := puzzle.ComposeRoundAnswers(ctx, d, promptID)
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

// runCheck is the 22:00 UTC alarm: exits non-zero if tomorrow's puzzle is
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
