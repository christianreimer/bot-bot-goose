// Package llm is a thin wrapper around the Anthropic Messages API tuned
// for generating one bot line at a time for the prelaunch review TUI.
//
// The offline cmd/bot-candidates generator owns the batch path (N
// candidates per archetype, written as 'pending' for later moderation).
// This package owns the single-shot path: reviewer presses `g`, we call
// the API, return one answer, the reviewer accepts or regenerates.
//
// Anthropic SDK is intentionally not used — one HTTP call doesn't earn a
// dep, and the request shape is stable.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// DefaultModel is the model id used when none is supplied. Tracks the
// project's current Anthropic default.
const DefaultModel = "claude-opus-4-7"

const anthropicURL = "https://api.anthropic.com/v1/messages"

// Client is a single-purpose wrapper around the Anthropic Messages API.
// Reuse one across many calls — the underlying http.Client pools
// connections.
type Client struct {
	apiKey string
	model  string
	httpDo func(*http.Request) (*http.Response, error)
}

// New returns a client for the given API key + model. Empty model →
// DefaultModel. 60s timeout matches the offline generator; bot lines are
// short so this is generous.
func New(apiKey, model string) *Client {
	if model == "" {
		model = DefaultModel
	}
	h := &http.Client{Timeout: 60 * time.Second}
	return &Client{apiKey: apiKey, model: model, httpDo: h.Do}
}

// NewFromEnv reads BBG_ANTHROPIC_API_KEY (required) and
// BBG_ANTHROPIC_MODEL (optional). Returns an error if the key is unset
// so callers can surface a clear message instead of erroring at first
// call time.
func NewFromEnv() (*Client, error) {
	key := os.Getenv("BBG_ANTHROPIC_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("BBG_ANTHROPIC_API_KEY is not set")
	}
	return New(key, os.Getenv("BBG_ANTHROPIC_MODEL")), nil
}

// Model returns the model id this client will call. Useful for echoing
// onto bot_candidates.llm_model so historical puzzles record which
// model produced each line.
func (c *Client) Model() string { return c.model }

// BotLineRequest describes one bot-line generation. The archetype fields
// shape voice; an empty ArchetypeName means "no archetype hinting."
type BotLineRequest struct {
	Prompt              string
	ArchetypeName       string
	ArchetypeTell       string
	ArchetypeDifficulty int16
	// HumanLines, if non-empty, gives the model the actual approved
	// human answers it has to hide among. Useful for high-difficulty
	// archetypes (the "Mirror") where blending is the whole point.
	HumanLines []string
}

// BotLineResult is the model's answer plus the model id used (echoed for
// the bot_candidates.llm_model column).
type BotLineResult struct {
	Text  string
	Model string
}

// GenerateBotLine asks the model for one answer in the requested
// archetype's voice. The model is instructed to wrap the answer in
// <answer>...</answer> tags so any preamble is easy to strip.
func (c *Client) GenerateBotLine(ctx context.Context, req BotLineRequest) (*BotLineResult, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("anthropic api key is empty")
	}
	system, user := buildBotLinePrompt(req)
	body := map[string]any{
		"model":      c.model,
		"max_tokens": 1024,
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": user}},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", anthropicURL, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("content-type", "application/json")

	resp, err := c.httpDo(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("anthropic %d: %s", resp.StatusCode, snippet(string(raw), 400))
	}
	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal anthropic response: %w", err)
	}
	var text strings.Builder
	for _, p := range parsed.Content {
		if p.Type == "text" {
			text.WriteString(p.Text)
		}
	}
	answer := extractAnswer(text.String())
	if answer == "" {
		return nil, fmt.Errorf("model returned no answer (raw: %q)", snippet(text.String(), 200))
	}
	return &BotLineResult{Text: answer, Model: c.model}, nil
}

var answerTagRE = regexp.MustCompile(`(?s)<answer>(.*?)</answer>`)

// extractAnswer pulls the first <answer>...</answer> block out of the
// model's text. If no tags are found, falls back to the whole text
// trimmed — defensive against models that ignore the wrapping
// instruction.
func extractAnswer(s string) string {
	if m := answerTagRE.FindStringSubmatch(s); len(m) > 0 {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(s)
}

func buildBotLinePrompt(req BotLineRequest) (system, user string) {
	system = strings.Join([]string{
		"You are generating ONE answer for a daily game where players try to spot the AI-written answer hiding among three real human answers.",
		"Players see a row of 4 answers (yours + 3 humans). They tap the one they think is AI. Your job is to write one that blends in or has the archetype's specific tell, depending on difficulty.",
		"Stay in voice. Keep the answer short — one or two sentences, casual register, lowercase-leaning, no preamble.",
		"Wrap your final answer in <answer>...</answer> tags. Nothing else outside the tags.",
	}, "\n")
	var ub strings.Builder
	if req.ArchetypeName != "" {
		fmt.Fprintf(&ub, "Archetype: %s\n", req.ArchetypeName)
	}
	if req.ArchetypeTell != "" {
		fmt.Fprintf(&ub, "Tell: %s\n", req.ArchetypeTell)
	}
	if req.ArchetypeDifficulty > 0 {
		fmt.Fprintf(&ub, "Difficulty: %d/5\n", req.ArchetypeDifficulty)
	}
	fmt.Fprintf(&ub, "\nPrompt: %s\n", req.Prompt)
	if len(req.HumanLines) > 0 {
		ub.WriteString("\nThe three real human answers you must hide among (for tone reference only — do not copy phrasing):\n")
		for i, h := range req.HumanLines {
			fmt.Fprintf(&ub, "%d. %s\n", i+1, h)
		}
	}
	ub.WriteString("\nWrite ONE answer in this voice. Use <answer>...</answer>.")
	return system, ub.String()
}

func snippet(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
