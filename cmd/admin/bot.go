package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/google/uuid"
)

func runBot(ctx context.Context, log *slog.Logger) error {
	if len(os.Args) < 2 {
		botUsage()
		os.Exit(2)
	}
	verb := os.Args[1]
	os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
	switch verb {
	case "list":
		return botList(ctx, log)
	case "show":
		return botShow(ctx, log)
	case "review":
		return botReview(ctx, log)
	case "bulk-review":
		return botBulkReview(ctx, log)
	default:
		botUsage()
		os.Exit(2)
	}
	return nil
}

func botUsage() {
	fmt.Fprintln(os.Stderr, `usage: bbg-admin bot <verb> [flags]
  list         List bot_candidates with filters.
  show         Show one bot candidate by id.
  review       Transition status of one bot (approve|reject|retire).
  bulk-review  Apply the same decision to many bot candidates at once.`)
}

// --- list --------------------------------------------------------------------

func botList(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("bot list", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	status := fs.String("status", "", "pending|approved|rejected|retired")
	promptIDStr := fs.String("prompt-id", "", "filter by prompt UUID")
	archIDStr := fs.String("archetype-id", "", "filter by archetype UUID")
	since := fs.String("since", "", "created_at >= YYYY-MM-DD")
	until := fs.String("until", "", "created_at <= YYYY-MM-DD")
	limit := fs.Int("limit", 50, "max rows")
	offset := fs.Int("offset", 0, "skip rows")
	asTable := fs.Bool("table", false, "human-readable table instead of JSON")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *status != "" && !validStatus(*status) {
		return emitError("invalid", "invalid --status (want pending|approved|rejected|retired)", nil)
	}
	opts := db.BotListOpts{Limit: *limit, Offset: *offset}
	if *status != "" {
		opts.Status = status
	}
	if *promptIDStr != "" {
		id, err := uuid.Parse(*promptIDStr)
		if err != nil {
			return emitError("invalid", "parse --prompt-id: "+err.Error(), nil)
		}
		opts.PromptID = &id
	}
	if *archIDStr != "" {
		id, err := uuid.Parse(*archIDStr)
		if err != nil {
			return emitError("invalid", "parse --archetype-id: "+err.Error(), nil)
		}
		opts.ArchetypeID = &id
	}
	if *since != "" {
		t, err := time.Parse("2006-01-02", *since)
		if err != nil {
			return emitError("invalid", "parse --since: "+err.Error(), nil)
		}
		opts.Since = &t
	}
	if *until != "" {
		t, err := time.Parse("2006-01-02", *until)
		if err != nil {
			return emitError("invalid", "parse --until: "+err.Error(), nil)
		}
		eod := t.Add(24*time.Hour - time.Second)
		opts.Until = &eod
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	bots, err := d.ListBots(ctx, opts)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	if *asTable {
		rows := make([][]any, 0, len(bots))
		for _, b := range bots {
			rows = append(rows, []any{
				b.ID.String()[:8], b.Status, b.ArchetypeSlug,
				b.CreatedAt.UTC().Format("2006-01-02"),
				truncate(b.PromptText, 30), truncate(b.Text, 50),
			})
		}
		return emitTable([]string{"ID", "STATUS", "ARCH", "CREATED", "PROMPT", "TEXT"}, rows)
	}
	return emitJSON(botsToJSON(bots))
}

// --- show --------------------------------------------------------------------

func botShow(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("bot show", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	asTable := fs.Bool("table", false, "human-readable view")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return emitError("invalid", "bot id (UUID) is required", nil)
	}
	id, err := uuid.Parse(fs.Arg(0))
	if err != nil {
		return emitError("invalid", "parse bot id: "+err.Error(), nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	b, err := d.BotByID(ctx, id)
	if err != nil {
		if db.IsNotFound(err) {
			return emitError("not_found", "bot candidate not found", map[string]any{"id": id.String()})
		}
		return emitError("db", err.Error(), nil)
	}
	if *asTable {
		fmt.Fprintf(os.Stdout, "Bot %s\n", b.ID)
		fmt.Fprintf(os.Stdout, "Status:    %s\n", b.Status)
		fmt.Fprintf(os.Stdout, "Archetype: %s\n", b.ArchetypeSlug)
		fmt.Fprintf(os.Stdout, "Created:   %s\n", b.CreatedAt.UTC().Format(time.RFC3339))
		fmt.Fprintf(os.Stdout, "Prompt:    %s\n", b.PromptText)
		fmt.Fprintf(os.Stdout, "Text:      %s\n", b.Text)
		if b.LLMModel != nil {
			fmt.Fprintf(os.Stdout, "Model:     %s\n", *b.LLMModel)
		}
		if b.GeneratorRun != nil {
			fmt.Fprintf(os.Stdout, "Run:       %s\n", b.GeneratorRun.String())
		}
		return nil
	}
	return emitJSON(botToJSON(b))
}

// --- review ------------------------------------------------------------------

func botReview(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("bot review", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	decision := fs.String("decision", "", "approve|reject|retire (required)")
	note := fs.String("note", "", "moderation note")
	reviewerEmail := fs.String("reviewer-email", envOr("BBG_REVIEWER_EMAIL", ""), "email of the reviewing user (required; also via BBG_REVIEWER_EMAIL)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return emitError("invalid", "bot id (UUID) is required", nil)
	}
	botID, err := uuid.Parse(fs.Arg(0))
	if err != nil {
		return emitError("invalid", "parse bot id: "+err.Error(), nil)
	}
	status, ok := mapDecision(*decision)
	if !ok {
		return emitError("invalid", "--decision must be approve|reject|retire", nil)
	}
	if *reviewerEmail == "" {
		return emitError("invalid", "--reviewer-email (or BBG_REVIEWER_EMAIL) is required because moderation_reviews.reviewer_user_id is NOT NULL", nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	reviewerID, err := d.UserIDByEmail(ctx, *reviewerEmail)
	if err != nil {
		if db.IsNotFound(err) {
			return emitError("not_found", "reviewer email not found", map[string]any{"email": *reviewerEmail, "hint": "run `bbg-admin promote --email ... --role reviewer` to create one"})
		}
		return emitError("db", err.Error(), nil)
	}
	if err := d.ReviewBot(ctx, botID, reviewerID, status, *note); err != nil {
		if db.IsNotFound(err) {
			return emitError("not_found", "bot candidate not found", map[string]any{"id": botID.String()})
		}
		return emitError("db", err.Error(), nil)
	}
	log.Info("bot reviewed", "id", botID, "decision", status, "reviewer", *reviewerEmail)
	return emitOK("review", map[string]any{
		"bot_id":      botID.String(),
		"decision":    status,
		"reviewer_id": reviewerID.String(),
	})
}

// --- bulk-review -------------------------------------------------------------

func botBulkReview(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("bot bulk-review", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	decision := fs.String("decision", "", "approve|reject|retire (required)")
	statusFilter := fs.String("status", "pending", "review only bots with this current status")
	promptIDStr := fs.String("prompt-id", "", "only bots for this prompt")
	archIDStr := fs.String("archetype-id", "", "only bots from this archetype")
	idsStr := fs.String("ids", "", "comma-separated bot UUIDs (overrides --status/--prompt-id/--archetype-id)")
	note := fs.String("note", "", "moderation note applied to every row")
	reviewerEmail := fs.String("reviewer-email", envOr("BBG_REVIEWER_EMAIL", ""), "email of the reviewing user (required)")
	limit := fs.Int("limit", 100, "safety cap on number of bots reviewed")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	status, ok := mapDecision(*decision)
	if !ok {
		return emitError("invalid", "--decision must be approve|reject|retire", nil)
	}
	if *reviewerEmail == "" {
		return emitError("invalid", "--reviewer-email (or BBG_REVIEWER_EMAIL) is required", nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	reviewerID, err := d.UserIDByEmail(ctx, *reviewerEmail)
	if err != nil {
		if db.IsNotFound(err) {
			return emitError("not_found", "reviewer email not found", map[string]any{"email": *reviewerEmail})
		}
		return emitError("db", err.Error(), nil)
	}

	// Resolve target ids.
	var targetIDs []uuid.UUID
	if *idsStr != "" {
		for _, s := range strings.Split(*idsStr, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			id, err := uuid.Parse(s)
			if err != nil {
				return emitError("invalid", "parse id "+s+": "+err.Error(), nil)
			}
			targetIDs = append(targetIDs, id)
		}
	} else {
		// Pull one extra so a count of limit+1 trips limit_exceeded.
		opts := db.BotListOpts{Limit: *limit + 1}
		if *statusFilter != "" {
			opts.Status = statusFilter
		}
		if *promptIDStr != "" {
			id, err := uuid.Parse(*promptIDStr)
			if err != nil {
				return emitError("invalid", "parse --prompt-id: "+err.Error(), nil)
			}
			opts.PromptID = &id
		}
		if *archIDStr != "" {
			id, err := uuid.Parse(*archIDStr)
			if err != nil {
				return emitError("invalid", "parse --archetype-id: "+err.Error(), nil)
			}
			opts.ArchetypeID = &id
		}
		matched, err := d.ListBots(ctx, opts)
		if err != nil {
			return emitError("db", err.Error(), nil)
		}
		if len(matched) > *limit {
			return emitError("limit_exceeded", fmt.Sprintf("matched at least %d bots; --limit is %d. Raise --limit explicitly to confirm.", len(matched), *limit), nil)
		}
		for _, b := range matched {
			targetIDs = append(targetIDs, b.ID)
		}
	}

	type result struct {
		ID     string `json:"id"`
		Status string `json:"status"` // "reviewed" | "failed"
		Error  string `json:"error,omitempty"`
	}
	out := make([]result, 0, len(targetIDs))
	for _, id := range targetIDs {
		if err := d.ReviewBot(ctx, id, reviewerID, status, *note); err != nil {
			out = append(out, result{ID: id.String(), Status: "failed", Error: err.Error()})
			continue
		}
		out = append(out, result{ID: id.String(), Status: "reviewed"})
	}
	log.Info("bulk bot review done", "count", len(out), "decision", status)
	return emitJSON(map[string]any{
		"decision":    status,
		"reviewer_id": reviewerID.String(),
		"count":       len(out),
		"results":     out,
	})
}

// --- helpers -----------------------------------------------------------------

func botsToJSON(bots []db.Bot) []map[string]any {
	out := make([]map[string]any, 0, len(bots))
	for i := range bots {
		out = append(out, botToJSON(&bots[i]))
	}
	return out
}

func botToJSON(b *db.Bot) map[string]any {
	var runID *string
	if b.GeneratorRun != nil {
		s := b.GeneratorRun.String()
		runID = &s
	}
	return map[string]any{
		"id":               b.ID.String(),
		"prompt_id":        b.PromptID.String(),
		"prompt_text":      b.PromptText,
		"archetype_id":     b.ArchetypeID.String(),
		"archetype_slug":   b.ArchetypeSlug,
		"text":             b.Text,
		"status":           b.Status,
		"llm_model":        b.LLMModel,
		"generator_run_id": runID,
		"created_at":       b.CreatedAt.UTC().Format(time.RFC3339),
	}
}
