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

func runDecoy(ctx context.Context, log *slog.Logger) error {
	if len(os.Args) < 2 {
		decoyUsage()
		os.Exit(2)
	}
	verb := os.Args[1]
	os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
	switch verb {
	case "list":
		return decoyList(ctx, log)
	case "show":
		return decoyShow(ctx, log)
	case "review":
		return decoyReview(ctx, log)
	case "bulk-review":
		return decoyBulkReview(ctx, log)
	default:
		decoyUsage()
		os.Exit(2)
	}
	return nil
}

func decoyUsage() {
	fmt.Fprintln(os.Stderr, `usage: bbg-admin decoy <verb> [flags]
  list         List decoy submissions with filters.
  show         Show one decoy by id.
  review       Transition status of one decoy (approve|reject|retire).
  bulk-review  Apply the same decision to many decoys at once.`)
}

// --- list --------------------------------------------------------------------

func decoyList(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("decoy list", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	status := fs.String("status", "", "pending|approved|rejected|retired")
	promptIDStr := fs.String("prompt-id", "", "filter by prompt UUID")
	userIDStr := fs.String("user-id", "", "filter by user UUID")
	since := fs.String("since", "", "submitted_at >= YYYY-MM-DD")
	until := fs.String("until", "", "submitted_at <= YYYY-MM-DD")
	limit := fs.Int("limit", 50, "max rows")
	offset := fs.Int("offset", 0, "skip rows")
	includeDeleted := fs.Bool("include-deleted", false, "include soft-deleted rows")
	asTable := fs.Bool("table", false, "human-readable table instead of JSON")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *status != "" && !validStatus(*status) {
		return emitError("invalid", "invalid --status (want pending|approved|rejected|retired)", nil)
	}
	opts := db.DecoyListOpts{Limit: *limit, Offset: *offset, IncludeDeleted: *includeDeleted}
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
	if *userIDStr != "" {
		id, err := uuid.Parse(*userIDStr)
		if err != nil {
			return emitError("invalid", "parse --user-id: "+err.Error(), nil)
		}
		opts.UserID = &id
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
		// Inclusive end-of-day.
		eod := t.Add(24*time.Hour - time.Second)
		opts.Until = &eod
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	decoys, err := d.ListDecoys(ctx, opts)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	if *asTable {
		rows := make([][]any, 0, len(decoys))
		for _, dc := range decoys {
			rows = append(rows, []any{
				dc.ID.String()[:8], dc.Status, dc.SubmittedAt.UTC().Format("2006-01-02"),
				truncate(dc.PromptText, 30), truncate(dc.Text, 50),
			})
		}
		return emitTable([]string{"ID", "STATUS", "SUBMITTED", "PROMPT", "TEXT"}, rows)
	}
	return emitJSON(decoysToJSON(decoys))
}

// --- show --------------------------------------------------------------------

func decoyShow(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("decoy show", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	asTable := fs.Bool("table", false, "human-readable view")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return emitError("invalid", "decoy id (UUID) is required", nil)
	}
	id, err := uuid.Parse(fs.Arg(0))
	if err != nil {
		return emitError("invalid", "parse decoy id: "+err.Error(), nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	dc, err := d.DecoyByID(ctx, id)
	if err != nil {
		if db.IsNotFound(err) {
			return emitError("not_found", "decoy not found", map[string]any{"id": id.String()})
		}
		return emitError("db", err.Error(), nil)
	}
	if *asTable {
		fmt.Fprintf(os.Stdout, "Decoy %s\n", dc.ID)
		fmt.Fprintf(os.Stdout, "Status:    %s\n", dc.Status)
		fmt.Fprintf(os.Stdout, "Submitted: %s\n", dc.SubmittedAt.UTC().Format(time.RFC3339))
		fmt.Fprintf(os.Stdout, "Prompt:    %s\n", dc.PromptText)
		fmt.Fprintf(os.Stdout, "Text:      %s\n", dc.Text)
		if dc.UserID != nil {
			fmt.Fprintf(os.Stdout, "Author:    %s\n", dc.UserID.String())
		}
		return nil
	}
	return emitJSON(decoyToJSON(dc))
}

// --- review ------------------------------------------------------------------

func decoyReview(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("decoy review", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	decision := fs.String("decision", "", "approve|reject|retire (required)")
	note := fs.String("note", "", "moderation note")
	reviewerEmail := fs.String("reviewer-email", envOr("BBG_REVIEWER_EMAIL", ""), "email of the reviewing user (required; also via BBG_REVIEWER_EMAIL)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return emitError("invalid", "decoy id (UUID) is required", nil)
	}
	decoyID, err := uuid.Parse(fs.Arg(0))
	if err != nil {
		return emitError("invalid", "parse decoy id: "+err.Error(), nil)
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
	if err := d.ReviewDecoy(ctx, decoyID, reviewerID, status, *note); err != nil {
		if db.IsNotFound(err) {
			return emitError("not_found", "decoy not found or already soft-deleted", map[string]any{"id": decoyID.String()})
		}
		return emitError("db", err.Error(), nil)
	}
	log.Info("decoy reviewed", "id", decoyID, "decision", status, "reviewer", *reviewerEmail)
	return emitOK("review", map[string]any{
		"decoy_id":    decoyID.String(),
		"decision":    status,
		"reviewer_id": reviewerID.String(),
	})
}

// --- bulk-review -------------------------------------------------------------

func decoyBulkReview(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("decoy bulk-review", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	decision := fs.String("decision", "", "approve|reject|retire (required)")
	statusFilter := fs.String("status", "pending", "review only decoys with this current status")
	promptIDStr := fs.String("prompt-id", "", "only decoys for this prompt")
	idsStr := fs.String("ids", "", "comma-separated decoy UUIDs (overrides --status/--prompt-id)")
	note := fs.String("note", "", "moderation note applied to every row")
	reviewerEmail := fs.String("reviewer-email", envOr("BBG_REVIEWER_EMAIL", ""), "email of the reviewing user (required)")
	limit := fs.Int("limit", 100, "safety cap on number of decoys reviewed")
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
		opts := db.DecoyListOpts{Limit: *limit}
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
		decoys, err := d.ListDecoys(ctx, opts)
		if err != nil {
			return emitError("db", err.Error(), nil)
		}
		for _, dc := range decoys {
			targetIDs = append(targetIDs, dc.ID)
		}
	}

	if len(targetIDs) > *limit {
		return emitError("limit_exceeded", fmt.Sprintf("matched %d decoys; --limit is %d. Raise --limit explicitly to confirm.", len(targetIDs), *limit), nil)
	}

	type result struct {
		ID     string `json:"id"`
		Status string `json:"status"` // "reviewed" | "failed"
		Error  string `json:"error,omitempty"`
	}
	out := make([]result, 0, len(targetIDs))
	for _, id := range targetIDs {
		if err := d.ReviewDecoy(ctx, id, reviewerID, status, *note); err != nil {
			out = append(out, result{ID: id.String(), Status: "failed", Error: err.Error()})
			continue
		}
		out = append(out, result{ID: id.String(), Status: "reviewed"})
	}
	log.Info("bulk review done", "count", len(out), "decision", status)
	return emitJSON(map[string]any{
		"decision":    status,
		"reviewer_id": reviewerID.String(),
		"count":       len(out),
		"results":     out,
	})
}

// --- helpers -----------------------------------------------------------------

// mapDecision converts the CLI verb (approve/reject/retire) to the underlying
// moderation_status enum value (approved/rejected/retired).
func mapDecision(s string) (string, bool) {
	switch s {
	case "approve", "approved":
		return "approved", true
	case "reject", "rejected":
		return "rejected", true
	case "retire", "retired":
		return "retired", true
	default:
		return "", false
	}
}

func validStatus(s string) bool {
	switch s {
	case "pending", "approved", "rejected", "retired":
		return true
	}
	return false
}

func decoysToJSON(decoys []db.Decoy) []map[string]any {
	out := make([]map[string]any, 0, len(decoys))
	for _, dc := range decoys {
		out = append(out, decoyToJSON(&dc))
	}
	return out
}

func decoyToJSON(dc *db.Decoy) map[string]any {
	var userID *string
	if dc.UserID != nil {
		s := dc.UserID.String()
		userID = &s
	}
	var deletedAt *string
	if dc.DeletedAt != nil {
		s := dc.DeletedAt.UTC().Format(time.RFC3339)
		deletedAt = &s
	}
	return map[string]any{
		"id":             dc.ID.String(),
		"prompt_id":      dc.PromptID.String(),
		"prompt_text":    dc.PromptText,
		"user_id":        userID,
		"text":           dc.Text,
		"status":         dc.Status,
		"is_trap":        dc.IsTrap,
		"ai_score":       dc.AIDetectorScore,
		"submitted_at":   dc.SubmittedAt.UTC().Format(time.RFC3339),
		"deleted_at":     deletedAt,
	}
}
