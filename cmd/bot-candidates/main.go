// bbg-bot-candidates — offline LLM-driven bot-candidate generator.
//
// Per the plan: never call the LLM on the live request path. This command
// generates N candidates per archetype for a prompt and inserts them into
// bot_candidates with status='pending' for human curation in the admin queue.
//
// v1: contains the Anthropic Messages API call wired up, but the API key
// is required at run time. Without it, the command prints the prompt it
// would send and exits — useful for review.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/content"
	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/google/uuid"
)

func main() {
	fs := flag.NewFlagSet("bot-candidates", flag.ExitOnError)
	promptText := fs.String("prompt", "", "the prompt to generate against (exact text)")
	n := fs.Int("n", 4, "candidates per archetype")
	model := fs.String("model", envDefault("BBG_ANTHROPIC_MODEL", "claude-opus-4-7"), "Anthropic model id")
	dbURL := fs.String("db", envDefault("BBG_DB_URL", "postgres://bbg:bbg@localhost:5432/bbg?sslmode=disable"), "db url")
	dryRun := fs.Bool("dry", false, "print prompts, don't call the API or write")
	_ = fs.Parse(os.Args[1:])

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := context.Background()

	if *promptText == "" {
		log.Error("--prompt required")
		os.Exit(2)
	}

	d, err := db.Open(ctx, *dbURL)
	if err != nil {
		log.Error("open db", "err", err)
		os.Exit(1)
	}
	defer d.Close()

	promptID, err := d.UpsertPrompt(ctx, *promptText)
	if err != nil {
		log.Error("upsert prompt", "err", err)
		os.Exit(1)
	}

	apiKey := os.Getenv("BBG_ANTHROPIC_API_KEY")
	runID := uuid.New()
	for _, a := range content.StarterRoster {
		archID, err := d.UpsertArchetype(ctx, a.Slug, a.Name, a.Tell, a.Difficulty)
		if err != nil {
			log.Error("archetype", "err", err)
			os.Exit(1)
		}
		sys, user := promptForArchetype(a, *promptText, *n)
		if *dryRun || apiKey == "" {
			log.Info("DRY (no API call)", "archetype", a.Slug)
			fmt.Println("--- system ---")
			fmt.Println(sys)
			fmt.Println("--- user ---")
			fmt.Println(user)
			fmt.Println()
			continue
		}

		texts, err := callAnthropic(ctx, apiKey, *model, sys, user)
		if err != nil {
			log.Error("anthropic call", "archetype", a.Slug, "err", err)
			continue
		}
		for _, t := range texts {
			if strings.TrimSpace(t) == "" {
				continue
			}
			if _, err := d.InsertBotCandidate(ctx, promptID, archID, t, "pending"); err != nil {
				log.Error("insert candidate", "err", err)
			}
		}
		log.Info("generated", "archetype", a.Slug, "n", len(texts))
	}
	log.Info("done", "run_id", runID)
}

// promptForArchetype produces a (system, user) prompt pair instructing the
// model to emit `n` JSON-line answers in that archetype's voice. We
// intentionally do NOT show the model the human pool — its job is to be a
// recognizable archetype, not a Mirror (yet).
func promptForArchetype(a content.Archetype, promptText string, n int) (system, user string) {
	system = strings.Join([]string{
		"You are generating bot answers for a daily game where players spot AI-written answers among real human ones.",
		"You write in the voice of an archetype with a recognizable 'tell'.",
		"Stay in voice. Do not break character. Each answer must be plausible on its own — a player will see four answers (one yours, three humans) and tap the one they think is AI.",
		"Output exactly the requested number of answers as a JSON array of strings. No other text.",
	}, "\n")
	user = fmt.Sprintf("Archetype: %s\nTell: %s\nDifficulty: %d/5\n\nPrompt: %s\n\nGenerate %d answers in this archetype's voice, as a JSON array.",
		a.Name, a.Tell, a.Difficulty, promptText, n)
	return
}

// callAnthropic hits the Messages API and expects a JSON array of strings in
// the response. We don't use the official SDK to avoid an extra dep just for
// this offline tool.
func callAnthropic(ctx context.Context, apiKey, model, system, user string) ([]string, error) {
	body := map[string]any{
		"model":      model,
		"max_tokens": 1024,
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": user}},
	}
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(buf))
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("anthropic %d: %s", resp.StatusCode, string(raw))
	}

	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	var text string
	for _, p := range parsed.Content {
		if p.Type == "text" {
			text += p.Text
		}
	}
	var arr []string
	if err := json.Unmarshal([]byte(text), &arr); err != nil {
		return nil, fmt.Errorf("model didn't return a JSON array: %w (got %q)", err, text)
	}
	return arr, nil
}

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
