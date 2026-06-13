// bbg-og-render — renders the OG share image for a puzzle.
//
// v1 stub: in production this would drive headless Chromium (chromedp) or
// `imaginary` to render web/templates/og/card.html → PNG and either upload to
// S3 or cache locally. For now it prints what it WOULD render so the wiring
// downstream of the share flow can be tested without a binary dependency.
//
// TODO(step-5): swap in chromedp + a real /share/:n/:hash.png handler.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/christianreimer/bot-bot-goose/internal/game"
	"github.com/christianreimer/bot-bot-goose/internal/share"
)

func main() {
	fs := flag.NewFlagSet("og-render", flag.ExitOnError)
	puzzleN := fs.Int("puzzle", 0, "puzzle_number to render")
	dbURL := fs.String("db", envDefault("BBG_DB_URL", "postgres://bbg:bbg@localhost:5432/bbg?sslmode=disable"), "db url")
	_ = fs.Parse(os.Args[1:])

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if *puzzleN <= 0 {
		log.Error("--puzzle required")
		os.Exit(2)
	}

	d, err := db.Open(ctx, *dbURL)
	if err != nil {
		log.Error("open db", "err", err)
		os.Exit(1)
	}
	defer d.Close()

	p, err := d.PuzzleByNumber(ctx, int32(*puzzleN))
	if err != nil {
		log.Error("puzzle missing", "err", err)
		os.Exit(1)
	}

	// v1 stub: print a generic share card. A real OG image would draw the
	// grid + brand mark on a 1200x630 canvas. Sample outcomes (mixed) so
	// the layout is visible even before any plays exist.
	sample := []game.Outcome{game.Green, game.Yellow, game.Red}
	card := share.Card(p.PuzzleNumber, sample, 0, -1, "botbotgoose.fun")
	fmt.Println(card)
	log.Info("stub render", "puzzle", p.PuzzleNumber)
}

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
