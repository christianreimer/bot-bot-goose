// bbg-admin — admin and content-management subcommands.
//
// One-shots:
//
//	bbg-admin seed                           # insert prototype content as puzzle #001
//	bbg-admin promote --email=... --role=... # role change with audit log
//	bbg-admin vapid-gen                      # print a fresh VAPID key pair
//	bbg-admin import path/to/content.json    # bulk import prompts/bots/decoys/puzzles
//	bbg-admin rollup                         # nightly leaderboard rollup
//
// Content-management groups (agent-operable — JSON stdout, --table opt-in):
//
//	bbg-admin puzzle  <list|show|create|compose|edit|set-round|set-answer|delete|replace|schedule>
//	bbg-admin decoy   <list|show|review|bulk-review>
//	bbg-admin bot     <list|show|review|bulk-review>
//	bbg-admin prompt  <list|show|create|edit|retire|delete|supply>
//	bbg-admin prelaunch <list|show|review|bulk-review|prompts>
//	bbg-admin stats   <overview|players|decoys|prelaunch>
//
// See cmd/admin/README.md for the JSON shapes and the DigitalOcean connection
// recipe.
package main

import (
	"context"
	"errors"
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

	// Wrap stderr so any password that slips through a log/error message gets
	// redacted before hitting the terminal.
	log := slog.New(slog.NewTextHandler(safeIOWriter{w: os.Stderr}, &slog.HandlerOptions{Level: slog.LevelInfo}))

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
	case "puzzle":
		mustRun(log, runPuzzle)
	case "decoy":
		mustRun(log, runDecoy)
	case "prompt":
		mustRun(log, runPrompt)
	case "prelaunch":
		mustRun(log, runPrelaunch)
	case "bot":
		mustRun(log, runBot)
	case "stats":
		mustRun(log, runStats)
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
             bbg-admin import [--dry-run] path/to/content.json
  rollup     Recompute forger_rankings from decoy_daily_stats. Nightly cron target.
  puzzle     Manage daily puzzles (list/show/create/compose/edit/set-round/set-answer/delete/replace/schedule).
  decoy      Manage user-submitted answers (list/show/review/bulk-review).
  bot        Manage LLM-generated bot candidates (list/show/review/bulk-review).
  prompt     Manage prompts (list/show/create/edit/retire/delete/supply).
  prelaunch    Review /prelaunch submissions in pre_launch_submissions (list/show/review/bulk-review/prompts).
  stats      Usage reporting (overview/players/decoys/prelaunch).

See cmd/admin/README.md for JSON output shapes and the DigitalOcean connection recipe.`)
}

func mustRun(log *slog.Logger, fn func(context.Context, *slog.Logger) error) {
	ctx := context.Background()
	err := fn(ctx, log)
	if err == nil {
		return
	}
	// errorEmitted means the runner already wrote a JSON envelope to stderr —
	// don't double-log; just exit non-zero.
	if errors.Is(err, errorEmitted) {
		os.Exit(1)
	}
	log.Error("admin", "err", err)
	os.Exit(1)
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
