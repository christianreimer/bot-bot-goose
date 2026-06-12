// bbg-admin — one-shot admin tasks. Subcommands keep ops scripts small.
//
//	bbg-admin seed                           # insert prototype content as puzzle #001
//	bbg-admin promote --email=... --role=... # role change with audit log
//	bbg-admin vapid-gen                      # print a fresh VAPID key pair
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/christianreimer/bot-bot-goose/internal/db"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	os.Args = append([]string{os.Args[0]}, os.Args[2:]...)

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	switch cmd {
	case "seed":
		mustRun(log, runSeed)
	case "promote":
		mustRun(log, runPromote)
	case "vapid-gen":
		mustRun(log, runVAPIDGen)
	case "import":
		mustRun(log, runImport)
	case "rollup":
		mustRun(log, runRollup)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: bbg-admin <subcommand> [flags]
  seed       Insert prototype puzzle #001 as approved content.
  promote    Set a user's role; writes an audit_log entry.
             --email=user@example.com --role=reviewer
  vapid-gen  Print a fresh VAPID key pair for BBG_VAPID_*.
  import     Load a content JSON file (prompts + bots + decoys + puzzles).
             bbg-admin import path/to/content.json
  rollup     Recompute forger_rankings from decoy_daily_stats. Nightly cron target.`)
}

func mustRun(log *slog.Logger, fn func(context.Context, *slog.Logger) error) {
	ctx := context.Background()
	if err := fn(ctx, log); err != nil {
		log.Error("admin", "err", err)
		os.Exit(1)
	}
}

func runPromote(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("promote", flag.ExitOnError)
	email := fs.String("email", "", "user email")
	role := fs.String("role", "", "role to set (player|reviewer|admin)")
	dbURL := fs.String("db", envDefault("BBG_DB_URL", "postgres://bbg:bbg@localhost:5432/bbg?sslmode=disable"), "db url")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *email == "" || *role == "" {
		return fmt.Errorf("usage: bbg-admin promote --email=... --role=...")
	}
	d, err := db.Open(ctx, *dbURL)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.SetUserRole(ctx, nil, *email, *role); err != nil {
		return err
	}
	log.Info("role updated", "email", *email, "role", *role)
	return nil
}

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
