package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/content"
	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/google/uuid"
)

// seedRound is the in-Go mirror of the prototype's rounds. answers[goose] is
// the bot, all others are decoys. The seed promotes prototype #001 only;
// additional prototype puzzles live in the test fixtures.
type seedRound struct {
	prompt        string
	answers       []string
	gooseSlot     int
	archetypeSlug string // intentionally one per round to exercise the roster
}

var seedPuzzle = []seedRound{
	{
		prompt: "What's the worst advice you've ever been given?",
		answers: []string{
			"my uncle told me to dump my savings into beanie babies in 2003. man still has a tub in his garage labeled 'retirement'",
			"'just be yourself' before a job interview. myself does not want the job. myself wants to be asleep.",
			"Someone once told me to never accept criticism. Looking back, I realize that learning to embrace constructive feedback has been essential for both personal and professional growth.",
			"a teacher said don't rely on a calculator, i won't always have one in my pocket. typing this on my phone.",
		},
		gooseSlot:     2,
		archetypeSlug: "lecturer",
	},
	{
		prompt: "Describe your morning routine in one sentence.",
		answers: []string{
			"I begin each morning with a glass of water, a few minutes of mindfulness, and a healthy breakfast to set a positive tone for the day ahead.",
			"alarm at 6, snooze until 6:54, panic, leave.",
			"coffee before words. words are not safe before coffee.",
			"let the dog out, let the dog in, let the dog out again, cry a little, work.",
		},
		gooseSlot:     0,
		archetypeSlug: "sunbeam",
	},
	{
		prompt: "What would you do with one extra hour every day?",
		answers: []string{
			"absolutely nothing and i would defend that hour with my life",
			"probably just scroll and then feel bad about it tbh",
			"learn bass. i've said this for 9 years. i will not learn bass.",
			"I would dedicate that time to reading, exercising, and connecting with loved ones, as these activities contribute to a balanced and fulfilling life.",
		},
		gooseSlot:     3,
		archetypeSlug: "lister",
	},
}

func runSeed(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	dbURL := fs.String("db", envDefault("BBG_DB_URL", "postgres://bbg:bbg@localhost:5432/bbg?sslmode=disable"), "db url")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	d, err := db.Open(ctx, *dbURL)
	if err != nil {
		return err
	}
	defer d.Close()

	// 1. Archetypes — seed the starter roster (idempotent).
	arche := map[string]uuid.UUID{}
	for _, a := range content.StarterRoster {
		id, err := d.UpsertArchetype(ctx, a.Slug, a.Name, a.Tell, a.Difficulty)
		if err != nil {
			return fmt.Errorf("archetype %s: %w", a.Slug, err)
		}
		arche[a.Slug] = id
	}
	log.Info("seeded archetypes", "n", len(arche))

	// 2. Puzzle #001 — prototype content.
	puzzleID, err := d.InsertDailyPuzzle(ctx, 1, time.Now().UTC().Truncate(24*time.Hour), nil)
	if err != nil {
		return fmt.Errorf("insert puzzle: %w", err)
	}

	for idx, sr := range seedPuzzle {
		promptID, err := d.UpsertPrompt(ctx, sr.prompt)
		if err != nil {
			return fmt.Errorf("prompt: %w", err)
		}
		roundID, err := d.InsertPuzzleRound(ctx, puzzleID, int16(idx), promptID, 1)
		if err != nil {
			return fmt.Errorf("round: %w", err)
		}

		archetypeID := arche[sr.archetypeSlug]
		// Insert bot candidate (approved).
		botID, err := d.InsertBotCandidate(ctx, promptID, archetypeID, sr.answers[sr.gooseSlot], "approved")
		if err != nil {
			return fmt.Errorf("bot candidate: %w", err)
		}

		// Insert decoys (approved, no author for seed).
		var decoyIDs []uuid.UUID
		for i, ans := range sr.answers {
			if i == sr.gooseSlot {
				continue
			}
			id, err := d.InsertDecoy(ctx, promptID, nil, ans, "approved")
			if err != nil {
				return fmt.Errorf("decoy: %w", err)
			}
			decoyIDs = append(decoyIDs, id)
		}

		// Assemble round answers — order does not matter; canonical ordering
		// is by uuid (and the slot_permutation rerolls it per play anyway).
		answers := []db.Answer{
			{ContentKind: db.ContentBot, BotCandidateID: &botID, AnswerText: sr.answers[sr.gooseSlot]},
		}
		di := 0
		for i, ans := range sr.answers {
			if i == sr.gooseSlot {
				continue
			}
			decoyID := decoyIDs[di]
			di++
			answers = append(answers, db.Answer{ContentKind: db.ContentDecoy, DecoyID: &decoyID, AnswerText: ans})
		}
		if err := d.ReplaceRoundAnswers(ctx, roundID, answers); err != nil {
			return fmt.Errorf("replace round answers: %w", err)
		}
	}
	log.Info("seeded puzzle #001", "rounds", len(seedPuzzle))
	return nil
}
