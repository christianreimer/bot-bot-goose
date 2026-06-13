package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/google/uuid"
)

func runPrompt(ctx context.Context, log *slog.Logger) error {
	if len(os.Args) < 2 {
		promptUsage()
		os.Exit(2)
	}
	verb := os.Args[1]
	os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
	switch verb {
	case "list":
		return promptList(ctx, log)
	case "show":
		return promptShow(ctx, log)
	case "create":
		return promptCreate(ctx, log)
	case "edit":
		return promptEdit(ctx, log)
	case "retire":
		return promptRetire(ctx, log)
	case "delete":
		return promptDelete(ctx, log)
	case "supply":
		return promptSupply(ctx, log)
	default:
		promptUsage()
		os.Exit(2)
	}
	return nil
}

func promptUsage() {
	fmt.Fprintln(os.Stderr, `usage: bbg-admin prompt <verb> [flags]
  list    List prompts (defaults to non-retired).
  show    Show one prompt by id.
  create  Create a new prompt.
  edit    Edit prompt text or theme.
  retire  Soft-retire (sets retired_at; reversible only via SQL).
  delete  Hard delete; refuses when referenced by any puzzle_rounds.
  supply  Per-prompt bot/decoy/pending counts + puzzle usage. Ready threshold: ≥1 approved bot, ≥4 approved decoys.`)
}

func promptList(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("prompt list", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	includeRetired := fs.Bool("include-retired", false, "include retired prompts")
	theme := fs.String("theme", "", "filter by theme")
	limit := fs.Int("limit", 100, "max rows")
	asTable := fs.Bool("table", false, "human-readable table")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	var themePtr *string
	if *theme != "" {
		themePtr = theme
	}
	prompts, err := d.ListPrompts(ctx, *includeRetired, themePtr, *limit)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	if *asTable {
		rows := make([][]any, 0, len(prompts))
		for _, p := range prompts {
			retired := "-"
			if p.RetiredAt != nil {
				retired = p.RetiredAt.UTC().Format("2006-01-02")
			}
			rows = append(rows, []any{p.ID.String()[:8], derefOr(p.Theme, "-"), retired, truncate(p.Text, 70)})
		}
		return emitTable([]string{"ID", "THEME", "RETIRED", "TEXT"}, rows)
	}
	return emitJSON(promptsToJSON(prompts))
}

func promptShow(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("prompt show", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return emitError("invalid", "prompt id (UUID) is required", nil)
	}
	id, err := uuid.Parse(fs.Arg(0))
	if err != nil {
		return emitError("invalid", "parse prompt id: "+err.Error(), nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	p, err := d.PromptByID(ctx, id)
	if err != nil {
		if db.IsNotFound(err) {
			return emitError("not_found", "prompt not found", map[string]any{"id": id.String()})
		}
		return emitError("db", err.Error(), nil)
	}
	return emitJSON(promptToJSON(p))
}

func promptCreate(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("prompt create", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	text := fs.String("text", "", "prompt text (required)")
	theme := fs.String("theme", "", "optional theme")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *text == "" {
		return emitError("invalid", "--text is required", nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	var themePtr *string
	if *theme != "" {
		themePtr = theme
	}
	id, err := d.InsertPrompt(ctx, *text, themePtr)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	log.Info("prompt created", "id", id)
	return emitOK("create", map[string]any{"prompt_id": id.String()})
}

func promptEdit(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("prompt edit", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	text := fs.String("text", "", "new text (omit to leave unchanged)")
	theme := fs.String("theme", "", "new theme (omit to leave unchanged)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return emitError("invalid", "prompt id (UUID) is required", nil)
	}
	id, err := uuid.Parse(fs.Arg(0))
	if err != nil {
		return emitError("invalid", "parse prompt id: "+err.Error(), nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	var textPtr *string
	if *text != "" {
		textPtr = text
	}
	var themePtr *string
	if *theme != "" {
		themePtr = theme
	}
	if textPtr == nil && themePtr == nil {
		return emitError("invalid", "at least one of --text or --theme must be set", nil)
	}
	if _, err := d.PromptByID(ctx, id); err != nil {
		if db.IsNotFound(err) {
			return emitError("not_found", "prompt not found", map[string]any{"id": id.String()})
		}
		return emitError("db", err.Error(), nil)
	}
	if err := d.UpdatePrompt(ctx, id, textPtr, themePtr); err != nil {
		return emitError("db", err.Error(), nil)
	}
	return emitOK("edit", map[string]any{"prompt_id": id.String()})
}

func promptRetire(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("prompt retire", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return emitError("invalid", "prompt id (UUID) is required", nil)
	}
	id, err := uuid.Parse(fs.Arg(0))
	if err != nil {
		return emitError("invalid", "parse prompt id: "+err.Error(), nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.RetirePrompt(ctx, id); err != nil {
		if db.IsNotFound(err) {
			return emitError("not_found", "prompt not found", map[string]any{"id": id.String()})
		}
		return emitError("db", err.Error(), nil)
	}
	return emitOK("retire", map[string]any{"prompt_id": id.String()})
}

func promptDelete(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("prompt delete", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return emitError("invalid", "prompt id (UUID) is required", nil)
	}
	id, err := uuid.Parse(fs.Arg(0))
	if err != nil {
		return emitError("invalid", "parse prompt id: "+err.Error(), nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.DeletePrompt(ctx, id); err != nil {
		switch {
		case db.IsNotFound(err):
			return emitError("not_found", "prompt not found", map[string]any{"id": id.String()})
		case err == db.ErrReferenced:
			return emitError("referenced", "prompt is referenced by one or more puzzle_rounds; use `prompt retire` instead", map[string]any{"id": id.String()})
		default:
			return emitError("db", err.Error(), nil)
		}
	}
	return emitOK("delete", map[string]any{"prompt_id": id.String()})
}

// --- supply ------------------------------------------------------------------
//
// Per-prompt content-readiness rollup. Answers the "which prompts can I build
// a puzzle from right now" question that every puzzle-build session opens with.
//
// Ready threshold (doubled from the per-round requirement so there's a
// spare in the pool):
//
//   ≥1 approved bot, ≥4 approved decoys per prompt.
//
// Prompts that haven't met the threshold sort first by gap-size
// (most-undersupplied first).

func promptSupply(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("prompt supply", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	onlyUnready := fs.Bool("only-unready", false, "hide prompts that already meet the threshold")
	onlyUnused := fs.Bool("only-unused", false, "hide prompts that appear in any past or scheduled puzzle")
	asTable := fs.Bool("table", false, "human-readable table instead of JSON")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()

	rollups, err := d.PromptSupplyCounts(ctx)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}

	enrichedRollups := make([]promptSupplyRow, 0, len(rollups))
	for _, r := range rollups {
		if *onlyUnused && len(r.UsedInPuzzles) > 0 {
			continue
		}
		e := promptSupplyRow{r: r}
		e.gap, e.ready = supplyGap(r.ApprovedBots, r.ApprovedDecoys)
		if *onlyUnready && e.ready {
			continue
		}
		enrichedRollups = append(enrichedRollups, e)
	}
	// Unready first (largest gap first), then ready prompts (stable order
	// preserves the alphabetical-by-text sort from PromptSupplyCounts).
	sort.SliceStable(enrichedRollups, func(i, j int) bool {
		a, b := enrichedRollups[i], enrichedRollups[j]
		if a.ready != b.ready {
			return !a.ready // unready first
		}
		if !a.ready {
			return a.gap > b.gap // larger gap first among unready
		}
		return false
	})

	if *asTable {
		headers := []string{"PROMPT_ID", "BOTS", "DECOYS", "PENDING", "USED_IN", "READY", "GAP", "PROMPT"}
		rows := make([][]any, 0, len(enrichedRollups))
		for _, e := range enrichedRollups {
			usedIn := "-"
			if len(e.r.UsedInPuzzles) > 0 {
				usedIn = joinInt32s(e.r.UsedInPuzzles, ",")
			}
			rows = append(rows, []any{
				e.r.PromptID.String()[:8],
				e.r.ApprovedBots, e.r.ApprovedDecoys, e.r.PendingDecoys,
				usedIn,
				boolYN(e.ready),
				e.gap,
				truncate(e.r.PromptText, 60),
			})
		}
		return emitTable(headers, rows)
	}

	out := make([]map[string]any, 0, len(enrichedRollups))
	for _, e := range enrichedRollups {
		out = append(out, map[string]any{
			"prompt_id":       e.r.PromptID.String(),
			"prompt_text":     e.r.PromptText,
			"approved_bots":   e.r.ApprovedBots,
			"approved_decoys": e.r.ApprovedDecoys,
			"pending_decoys":  e.r.PendingDecoys,
			"used_in_puzzles": e.r.UsedInPuzzles,
			"ready":           e.ready,
			"gap":             e.gap,
		})
	}
	return emitJSON(out)
}

// supplyGap reports how short the (bots, decoys) supply is for one prompt
// against the fixed targets (1 bot, 4 decoys). Returns gap=0 + ready=true
// once both thresholds are met. The gap is the sum across both axes so a
// prompt missing 2 bots + 3 decoys is ranked behind one missing just 1 bot.
func supplyGap(bots, decoys int) (gap int, ready bool) {
	const botTarget, decoyTarget = 1, 4
	botMiss := botTarget - bots
	if botMiss < 0 {
		botMiss = 0
	}
	decoyMiss := decoyTarget - decoys
	if decoyMiss < 0 {
		decoyMiss = 0
	}
	gap = botMiss + decoyMiss
	return gap, gap == 0
}

// promptSupplyRow pairs a rollup row with the ready/gap state.
type promptSupplyRow struct {
	r     db.PromptSupplyRollup
	gap   int  // how many more rows needed to be ready; 0 = ready
	ready bool // whether this prompt meets the threshold
}

func joinInt32s(xs []int32, sep string) string {
	if len(xs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(xs))
	for _, x := range xs {
		parts = append(parts, fmt.Sprintf("%d", x))
	}
	return strings.Join(parts, sep)
}

func boolYN(b bool) string {
	if b {
		return "YES"
	}
	return "no"
}

// --- helpers -----------------------------------------------------------------

func promptToJSON(p *db.Prompt) map[string]any {
	var retired *string
	if p.RetiredAt != nil {
		s := p.RetiredAt.UTC().Format(time.RFC3339)
		retired = &s
	}
	return map[string]any{
		"id":         p.ID.String(),
		"text":       p.Text,
		"theme":      p.Theme,
		"retired_at": retired,
		"created_at": p.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func promptsToJSON(prompts []db.Prompt) []map[string]any {
	out := make([]map[string]any, 0, len(prompts))
	for i := range prompts {
		out = append(out, promptToJSON(&prompts[i]))
	}
	return out
}
