package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/content"
	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/google/uuid"
)

// importDoc is the on-disk content format. Designed to be hand-authorable:
//   - prompts are inlined per round (not a separate table) so a reviewer can
//     read top-to-bottom without cross-referencing.
//   - "approved" status is implicit for everything in the file. Pending content
//     belongs in the moderation queue, not in version-controlled imports.
//   - puzzles MUST match their mode's recipe:
//       find_the_bot:   1 bot + 3 decoys
//       find_the_human: 3 bots + 1 decoy
type importDoc struct {
	Puzzles []importPuzzle  `json:"puzzles"`
	Prompts []importPrompt  `json:"prompts"` // bare prompts (no decoys/bots) — used for the harvest campaign seed
}

// importPrompt is a bare prompt entry. No status; prompts have no moderation
// state of their own (they're just questions). Theme is optional metadata.
type importPrompt struct {
	Text  string `json:"text"`
	Theme string `json:"theme,omitempty"`
}

type importPuzzle struct {
	PuzzleNumber int             `json:"puzzle_number"`
	Date         string          `json:"date"` // YYYY-MM-DD UTC
	Mode         string          `json:"mode"` // find_the_bot | find_the_human
	Theme        string          `json:"theme,omitempty"`
	Rounds       []importRound   `json:"rounds"`
}

type importRound struct {
	Prompt string       `json:"prompt"`
	Bots   []importBot  `json:"bots"`
	Decoys []string     `json:"decoys"`
}

type importBot struct {
	Archetype string `json:"archetype"` // slug from internal/content
	Text      string `json:"text"`
}

func runImport(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	dbURL := fs.String("db", envDefault("BBG_DB_URL", "postgres://bbg:bbg@localhost:5432/bbg?sslmode=disable"), "db url")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: bbg-admin import <file.json>")
	}
	path := fs.Arg(0)

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var doc importDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	d, err := db.Open(ctx, *dbURL)
	if err != nil {
		return err
	}
	defer d.Close()

	// Make sure the archetype roster exists (idempotent — seed does the same).
	arche := map[string]uuid.UUID{}
	for _, a := range content.StarterRoster {
		id, err := d.UpsertArchetype(ctx, a.Slug, a.Name, a.Tell, a.Difficulty)
		if err != nil {
			return fmt.Errorf("archetype %s: %w", a.Slug, err)
		}
		arche[a.Slug] = id
	}

	// Bare prompts. Idempotent — UpsertPrompt dedupes on exact text.
	for _, p := range doc.Prompts {
		if p.Text == "" {
			continue
		}
		if _, err := d.UpsertPrompt(ctx, p.Text); err != nil {
			return fmt.Errorf("prompt %q: %w", p.Text, err)
		}
	}

	for _, p := range doc.Puzzles {
		if err := importOnePuzzle(ctx, d, log, arche, p); err != nil {
			return fmt.Errorf("puzzle %d: %w", p.PuzzleNumber, err)
		}
	}
	log.Info("import complete", "puzzles", len(doc.Puzzles), "prompts", len(doc.Prompts))
	return nil
}

func importOnePuzzle(ctx context.Context, d *db.DB, log *slog.Logger, arche map[string]uuid.UUID, p importPuzzle) error {
	if p.Mode != "find_the_bot" && p.Mode != "find_the_human" {
		return fmt.Errorf("bad mode %q", p.Mode)
	}
	if len(p.Rounds) != 3 {
		return fmt.Errorf("want 3 rounds, got %d", len(p.Rounds))
	}
	date, err := time.Parse("2006-01-02", p.Date)
	if err != nil {
		return fmt.Errorf("bad date: %w", err)
	}
	var theme *string
	if p.Theme != "" {
		theme = &p.Theme
	}

	puzzleID, err := d.InsertDailyPuzzle(ctx, int32(p.PuzzleNumber), date, db.Mode(p.Mode), theme)
	if err != nil {
		return fmt.Errorf("insert puzzle: %w", err)
	}

	targetKind := "bot"
	if p.Mode == "find_the_human" {
		targetKind = "human"
	}

	for ri, r := range p.Rounds {
		if err := validateRound(p.Mode, ri, r); err != nil {
			return err
		}
		promptID, err := d.UpsertPrompt(ctx, r.Prompt)
		if err != nil {
			return fmt.Errorf("round %d prompt: %w", ri, err)
		}
		roundID, err := d.InsertPuzzleRound(ctx, puzzleID, int16(ri), promptID, targetKind, 1)
		if err != nil {
			return fmt.Errorf("round %d insert: %w", ri, err)
		}

		var answers []db.Answer

		for bi, b := range r.Bots {
			archID, ok := arche[b.Archetype]
			if !ok {
				return fmt.Errorf("round %d bot %d: unknown archetype %q", ri, bi, b.Archetype)
			}
			botID, err := d.InsertBotCandidate(ctx, promptID, archID, b.Text, "approved")
			if err != nil {
				return fmt.Errorf("round %d bot %d insert: %w", ri, bi, err)
			}
			answers = append(answers, db.Answer{
				ContentKind:    db.ContentBot,
				BotCandidateID: &botID,
				AnswerText:     b.Text,
			})
		}

		for di, text := range r.Decoys {
			decoyID, err := d.InsertDecoy(ctx, promptID, nil, text, "approved")
			if err != nil {
				return fmt.Errorf("round %d decoy %d insert: %w", ri, di, err)
			}
			answers = append(answers, db.Answer{
				ContentKind: db.ContentDecoy,
				DecoyID:     &decoyID,
				AnswerText:  text,
			})
		}

		if err := d.ReplaceRoundAnswers(ctx, roundID, answers); err != nil {
			return fmt.Errorf("round %d replace: %w", ri, err)
		}
	}
	log.Info("imported puzzle", "n", p.PuzzleNumber, "date", p.Date, "mode", p.Mode)
	return nil
}

func validateRound(mode string, idx int, r importRound) error {
	if mode == "find_the_bot" {
		if len(r.Bots) != 1 || len(r.Decoys) != 3 {
			return fmt.Errorf("round %d: find_the_bot wants 1 bot + 3 decoys, got %d/%d", idx, len(r.Bots), len(r.Decoys))
		}
	} else {
		if len(r.Bots) != 3 || len(r.Decoys) != 1 {
			return fmt.Errorf("round %d: find_the_human wants 3 bots + 1 decoy, got %d/%d", idx, len(r.Bots), len(r.Decoys))
		}
	}
	if len(r.Prompt) == 0 {
		return fmt.Errorf("round %d: prompt empty", idx)
	}
	for bi, b := range r.Bots {
		if b.Archetype == "" || b.Text == "" {
			return fmt.Errorf("round %d bot %d: archetype+text required", idx, bi)
		}
	}
	for di, t := range r.Decoys {
		if t == "" {
			return fmt.Errorf("round %d decoy %d: empty text", idx, di)
		}
	}
	return nil
}
