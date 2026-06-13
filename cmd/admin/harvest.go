package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/google/uuid"
)

// runHarvest dispatches `bbg-admin harvest <verb>`. Drives the reviewer
// workflow over pre_launch_submissions: list per prompt, inspect one,
// approve (ingest into decoy_submissions), reject (soft via rejected_at),
// or roll up prompt counts to find the most undersupplied targets.
func runHarvest(ctx context.Context, log *slog.Logger) error {
	if len(os.Args) < 2 {
		harvestUsage()
		os.Exit(2)
	}
	verb := os.Args[1]
	os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
	switch verb {
	case "list":
		return harvestList(ctx, log)
	case "show":
		return harvestShow(ctx, log)
	case "review":
		return harvestReview(ctx, log)
	case "bulk-review":
		return harvestBulkReview(ctx, log)
	case "prompts":
		return harvestPrompts(ctx, log)
	default:
		harvestUsage()
		os.Exit(2)
	}
	return nil
}

func harvestUsage() {
	fmt.Fprintln(os.Stderr, `usage: bbg-admin harvest <verb> [flags]
  list         List pre_launch_submissions with filters.
  show         Show one harvested submission by id.
  review       Decide one submission (approve | reject).
  bulk-review  Apply the same decision to many submissions at once.
  prompts      Per-prompt rollup of pending / ingested / rejected counts.`)
}

// --- list --------------------------------------------------------------------

func harvestList(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("harvest list", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	status := fs.String("status", "pending", "pending|approved|rejected")
	promptIDStr := fs.String("prompt-id", "", "filter by prompt UUID")
	limit := fs.Int("limit", 50, "max rows")
	offset := fs.Int("offset", 0, "skip rows")
	asTable := fs.Bool("table", false, "human-readable table instead of JSON")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if !validHarvestStatus(*status) {
		return emitError("invalid", "--status must be pending|approved|rejected", nil)
	}
	st := db.HarvestStatus(*status)
	opts := db.HarvestListOpts{Status: &st, Limit: *limit, Offset: *offset}
	if *promptIDStr != "" {
		id, err := uuid.Parse(*promptIDStr)
		if err != nil {
			return emitError("invalid", "parse --prompt-id: "+err.Error(), nil)
		}
		opts.PromptID = &id
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	rows, err := d.ListHarvest(ctx, opts)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	if *asTable {
		out := make([][]any, 0, len(rows))
		for _, h := range rows {
			out = append(out, []any{
				h.ID.String()[:8],
				h.ConsentAt.UTC().Format("2006-01-02"),
				truncate(h.PromptText, 30),
				truncate(h.Text, 60),
			})
		}
		return emitTable([]string{"ID", "AT", "PROMPT", "TEXT"}, out)
	}
	return emitJSON(harvestsToJSON(rows))
}

// --- show --------------------------------------------------------------------

func harvestShow(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("harvest show", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	asTable := fs.Bool("table", false, "human-readable view")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return emitError("invalid", "harvest id (UUID) is required", nil)
	}
	id, err := uuid.Parse(fs.Arg(0))
	if err != nil {
		return emitError("invalid", "parse harvest id: "+err.Error(), nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	h, err := d.HarvestByID(ctx, id)
	if err != nil {
		if db.IsNotFound(err) {
			return emitError("not_found", "harvest submission not found", map[string]any{"id": id.String()})
		}
		return emitError("db", err.Error(), nil)
	}
	if *asTable {
		fmt.Fprintf(os.Stdout, "Harvest %s\n", h.ID)
		fmt.Fprintf(os.Stdout, "Status:    %s\n", deriveHarvestStatus(h))
		fmt.Fprintf(os.Stdout, "Submitted: %s\n", h.ConsentAt.UTC().Format(time.RFC3339))
		fmt.Fprintf(os.Stdout, "Prompt:    %s\n", h.PromptText)
		fmt.Fprintf(os.Stdout, "Text:      %s\n", h.Text)
		if h.UserID != nil {
			fmt.Fprintf(os.Stdout, "Device:    %s\n", h.UserID.String())
		}
		if h.RequestedIP != nil {
			fmt.Fprintf(os.Stdout, "IP:        %s\n", *h.RequestedIP)
		}
		if h.IngestedDecoy != nil {
			fmt.Fprintf(os.Stdout, "Decoy:     %s\n", h.IngestedDecoy.String())
		}
		return nil
	}
	return emitJSON(harvestToJSON(h))
}

// --- review ------------------------------------------------------------------

func harvestReview(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("harvest review", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	decision := fs.String("decision", "", "approve|reject (required)")
	note := fs.String("note", "", "moderation note")
	isTrap := fs.Bool("is-trap", false, "approve as a trap decoy (curated to look bot-ish)")
	reviewerEmail := fs.String("reviewer-email", envOr("BBG_REVIEWER_EMAIL", ""), "email of the reviewing user (required; also via BBG_REVIEWER_EMAIL)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return emitError("invalid", "harvest id (UUID) is required", nil)
	}
	harvestID, err := uuid.Parse(fs.Arg(0))
	if err != nil {
		return emitError("invalid", "parse harvest id: "+err.Error(), nil)
	}
	verb, ok := mapHarvestDecision(*decision)
	if !ok {
		return emitError("invalid", "--decision must be approve|reject", nil)
	}
	if *reviewerEmail == "" {
		return emitError("invalid", "--reviewer-email (or BBG_REVIEWER_EMAIL) is required because moderation_reviews.reviewer_user_id is NOT NULL", nil)
	}
	if verb == "reject" && *isTrap {
		return emitError("invalid", "--is-trap is only valid with --decision approve", nil)
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
	switch verb {
	case "approve":
		newDecoyID, err := d.ApproveHarvest(ctx, harvestID, reviewerID, *isTrap, *note)
		if err != nil {
			return harvestErr(err, harvestID)
		}
		log.Info("harvest approved", "id", harvestID, "decoy_id", newDecoyID, "reviewer", *reviewerEmail)
		return emitOK("review", map[string]any{
			"harvest_id":  harvestID.String(),
			"decision":    "approve",
			"decoy_id":    newDecoyID.String(),
			"is_trap":     *isTrap,
			"reviewer_id": reviewerID.String(),
		})
	case "reject":
		if err := d.RejectHarvest(ctx, harvestID, reviewerID, *note); err != nil {
			return harvestErr(err, harvestID)
		}
		log.Info("harvest rejected", "id", harvestID, "reviewer", *reviewerEmail)
		return emitOK("review", map[string]any{
			"harvest_id":  harvestID.String(),
			"decision":    "reject",
			"reviewer_id": reviewerID.String(),
		})
	}
	return nil // unreachable — mapHarvestDecision ensures coverage
}

// --- bulk-review -------------------------------------------------------------

func harvestBulkReview(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("harvest bulk-review", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	decision := fs.String("decision", "", "approve|reject (required)")
	statusFilter := fs.String("status", "pending", "review only submissions with this current status")
	promptIDStr := fs.String("prompt-id", "", "only submissions for this prompt")
	idsStr := fs.String("ids", "", "comma-separated harvest UUIDs (overrides --status/--prompt-id)")
	note := fs.String("note", "", "moderation note applied to every row")
	isTrap := fs.Bool("is-trap", false, "approve every row as a trap decoy")
	reviewerEmail := fs.String("reviewer-email", envOr("BBG_REVIEWER_EMAIL", ""), "email of the reviewing user (required)")
	limit := fs.Int("limit", 100, "safety cap on number of submissions reviewed")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	verb, ok := mapHarvestDecision(*decision)
	if !ok {
		return emitError("invalid", "--decision must be approve|reject", nil)
	}
	if *reviewerEmail == "" {
		return emitError("invalid", "--reviewer-email (or BBG_REVIEWER_EMAIL) is required", nil)
	}
	if verb == "reject" && *isTrap {
		return emitError("invalid", "--is-trap is only valid with --decision approve", nil)
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
		if !validHarvestStatus(*statusFilter) {
			return emitError("invalid", "--status must be pending|approved|rejected", nil)
		}
		st := db.HarvestStatus(*statusFilter)
		// Pull one extra so a count of limit+1 triggers limit_exceeded.
		opts := db.HarvestListOpts{Status: &st, Limit: *limit + 1}
		if *promptIDStr != "" {
			id, err := uuid.Parse(*promptIDStr)
			if err != nil {
				return emitError("invalid", "parse --prompt-id: "+err.Error(), nil)
			}
			opts.PromptID = &id
		}
		matched, err := d.ListHarvest(ctx, opts)
		if err != nil {
			return emitError("db", err.Error(), nil)
		}
		if len(matched) > *limit {
			return emitError("limit_exceeded", fmt.Sprintf("matched at least %d submissions; --limit is %d. Raise --limit explicitly to confirm.", len(matched), *limit), nil)
		}
		for _, h := range matched {
			targetIDs = append(targetIDs, h.ID)
		}
	}

	type result struct {
		ID       string `json:"id"`
		Status   string `json:"status"` // "reviewed" | "failed"
		DecoyID  string `json:"decoy_id,omitempty"`
		Error    string `json:"error,omitempty"`
		ErrCode  string `json:"code,omitempty"`
	}
	out := make([]result, 0, len(targetIDs))
	for _, id := range targetIDs {
		switch verb {
		case "approve":
			newDecoyID, err := d.ApproveHarvest(ctx, id, reviewerID, *isTrap, *note)
			if err != nil {
				out = append(out, result{ID: id.String(), Status: "failed", Error: err.Error(), ErrCode: codeForHarvestErr(err)})
				continue
			}
			out = append(out, result{ID: id.String(), Status: "reviewed", DecoyID: newDecoyID.String()})
		case "reject":
			if err := d.RejectHarvest(ctx, id, reviewerID, *note); err != nil {
				out = append(out, result{ID: id.String(), Status: "failed", Error: err.Error(), ErrCode: codeForHarvestErr(err)})
				continue
			}
			out = append(out, result{ID: id.String(), Status: "reviewed"})
		}
	}
	log.Info("bulk harvest review done", "count", len(out), "decision", verb)
	return emitJSON(map[string]any{
		"decision":    verb,
		"reviewer_id": reviewerID.String(),
		"count":       len(out),
		"results":     out,
	})
}

// --- prompts (per-prompt rollup) ---------------------------------------------

func harvestPrompts(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("harvest prompts", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	asTable := fs.Bool("table", false, "human-readable table instead of JSON")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	// Single-mode: every prompt needs 4 approved decoys (3 per round + a
	// spare in the pool). Gap = how many more decoys would need approval.
	const target = 4
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	rollups, err := d.HarvestPromptCounts(ctx)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	if *asTable {
		headers := []string{"PROMPT_ID", "PROMPT", "PENDING", "INGESTED", "REJECTED", "APPROVED_DECOYS", "GAP"}
		rows := make([][]any, 0, len(rollups))
		for _, r := range rollups {
			gap := target - r.ApprovedDec
			if gap < 0 {
				gap = 0
			}
			rows = append(rows, []any{
				r.PromptID.String()[:8],
				truncate(r.PromptText, 40),
				r.Pending, r.Ingested, r.Rejected, r.ApprovedDec, gap,
			})
		}
		return emitTable(headers, rows)
	}
	out := make([]map[string]any, 0, len(rollups))
	for _, r := range rollups {
		gap := target - r.ApprovedDec
		if gap < 0 {
			gap = 0
		}
		out = append(out, map[string]any{
			"prompt_id":       r.PromptID.String(),
			"prompt_text":     r.PromptText,
			"pending":         r.Pending,
			"ingested":        r.Ingested,
			"rejected":        r.Rejected,
			"approved_decoys": r.ApprovedDec,
			"target":          target,
			"gap":             gap,
		})
	}
	return emitJSON(out)
}

// --- helpers -----------------------------------------------------------------

func validHarvestStatus(s string) bool {
	switch s {
	case "pending", "approved", "rejected":
		return true
	}
	return false
}

// mapHarvestDecision returns the normalized verb ("approve" | "reject") so the
// caller can switch on it. Distinct from decoy's mapDecision because harvest
// has no retire and no retry-already-decided.
func mapHarvestDecision(s string) (string, bool) {
	switch s {
	case "approve", "approved":
		return "approve", true
	case "reject", "rejected":
		return "reject", true
	default:
		return "", false
	}
}

func harvestErr(err error, harvestID uuid.UUID) error {
	switch {
	case db.IsNotFound(err):
		return emitError("not_found", "harvest submission not found", map[string]any{"id": harvestID.String()})
	case errors.Is(err, db.ErrHarvestAlreadyDecided):
		return emitError("already_decided", err.Error(), map[string]any{"id": harvestID.String()})
	default:
		return emitError("db", err.Error(), nil)
	}
}

func codeForHarvestErr(err error) string {
	switch {
	case db.IsNotFound(err):
		return "not_found"
	case errors.Is(err, db.ErrHarvestAlreadyDecided):
		return "already_decided"
	default:
		return "db"
	}
}

func deriveHarvestStatus(h *db.HarvestSubmission) string {
	switch {
	case h.IngestedDecoy != nil:
		return "approved"
	case h.RejectedAt != nil:
		return "rejected"
	default:
		return "pending"
	}
}

func harvestsToJSON(rows []db.HarvestSubmission) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for i := range rows {
		out = append(out, harvestToJSON(&rows[i]))
	}
	return out
}

func harvestToJSON(h *db.HarvestSubmission) map[string]any {
	var userID, decoyID, rejectedAt *string
	if h.UserID != nil {
		s := h.UserID.String()
		userID = &s
	}
	if h.IngestedDecoy != nil {
		s := h.IngestedDecoy.String()
		decoyID = &s
	}
	if h.RejectedAt != nil {
		s := h.RejectedAt.UTC().Format(time.RFC3339)
		rejectedAt = &s
	}
	return map[string]any{
		"id":                h.ID.String(),
		"status":            deriveHarvestStatus(h),
		"prompt_id":         h.PromptID.String(),
		"prompt_text":       h.PromptText,
		"text":              h.Text,
		"user_id":           userID,
		"email":             h.Email,
		"requested_ip":      h.RequestedIP,
		"consent_at":        h.ConsentAt.UTC().Format(time.RFC3339),
		"ingested_decoy_id": decoyID,
		"rejected_at":       rejectedAt,
	}
}
