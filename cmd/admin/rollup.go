package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/christianreimer/bot-bot-goose/internal/collective"
	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/christianreimer/bot-bot-goose/internal/leaderboard"
)

// runRollup recomputes forger_rankings from decoy_daily_stats. Nightly cron
// target; can also be run on demand to refresh the /leaderboard/originals page.
func runRollup(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("rollup", flag.ExitOnError)
	dbURL := fs.String("db", envDefault("BBG_DB_URL", "postgres://bbg:bbg@localhost:5432/bbg?sslmode=disable"), "db url")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	d, err := db.Open(ctx, *dbURL)
	if err != nil {
		return err
	}
	defer d.Close()
	n, err := leaderboard.Rollup(ctx, d)
	if err != nil {
		return err
	}
	log.Info("forger rollup complete", "authors_updated", n)

	// Freeze yesterday's collective catch rate so the result page + share
	// card surface a stable, identical-for-everyone number all day.
	wrote, err := collective.Rollup(ctx, d)
	if err != nil {
		return err
	}
	log.Info("collective rollup complete", "stat_written", wrote)
	return nil
}
