package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/christianreimer/bot-bot-goose/internal/puzzle"
	"github.com/google/uuid"
)

// runPuzzle dispatches `bbg-admin puzzle <verb>`. The outer main() already
// stripped os.Args[0..1]; this layer strips one more for the verb.
func runPuzzle(ctx context.Context, log *slog.Logger) error {
	if len(os.Args) < 2 {
		puzzleUsage()
		os.Exit(2)
	}
	verb := os.Args[1]
	os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
	switch verb {
	case "list":
		return puzzleList(ctx, log)
	case "show":
		return puzzleShow(ctx, log)
	case "create":
		return puzzleCreate(ctx, log)
	case "compose":
		return puzzleCompose(ctx, log)
	case "edit":
		return puzzleEdit(ctx, log)
	case "set-round":
		return puzzleSetRound(ctx, log)
	case "set-answer":
		return puzzleSetAnswer(ctx, log)
	case "delete":
		return puzzleDelete(ctx, log)
	case "schedule":
		return puzzleSchedule(ctx, log)
	default:
		puzzleUsage()
		os.Exit(2)
	}
	return nil
}

func puzzleUsage() {
	fmt.Fprintln(os.Stderr, `usage: bbg-admin puzzle <verb> [flags]
  list       List puzzles (defaults to today + upcoming).
  show       Show one puzzle with rounds + answers.
  create     Create an empty puzzle slot for a date (no rounds).
  compose    Compose a full puzzle (3 rounds, 4 answers each) for a date.
  edit       Edit mutable fields (--mode, --theme, --date) on unplayed puzzles.
  set-round  Set or replace one round's prompt + re-pick its answers.
  set-answer Override one answer's text snapshot (slot 0..3).
  delete     Delete an unplayed puzzle.
  schedule   Loop `+"`compose`"+` for N consecutive days, skipping existing dates.`)
}

// --- list --------------------------------------------------------------------

func puzzleList(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("puzzle list", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	from := fs.String("from", "", "puzzle_date >= YYYY-MM-DD")
	to := fs.String("to", "", "puzzle_date <= YYYY-MM-DD")
	includePast := fs.Bool("include-past", false, "include puzzles whose date is before today")
	limit := fs.Int("limit", 50, "max rows")
	asTable := fs.Bool("table", false, "human-readable table instead of JSON")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()

	opts := db.PuzzleListOpts{IncludePast: *includePast, Limit: *limit}
	if *from != "" {
		t, err := time.Parse("2006-01-02", *from)
		if err != nil {
			return emitError("invalid", "parse --from: "+err.Error(), nil)
		}
		opts.From = &t
	}
	if *to != "" {
		t, err := time.Parse("2006-01-02", *to)
		if err != nil {
			return emitError("invalid", "parse --to: "+err.Error(), nil)
		}
		opts.To = &t
	}
	puzzles, err := d.ListDailyPuzzles(ctx, opts)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	if *asTable {
		rows := make([][]any, 0, len(puzzles))
		for _, p := range puzzles {
			rows = append(rows, []any{p.PuzzleNumber, p.PuzzleDate.Format("2006-01-02"), p.Mode, derefOr(p.Theme, "-")})
		}
		return emitTable([]string{"NUMBER", "DATE", "MODE", "THEME"}, rows)
	}
	return emitJSON(puzzlesToJSON(puzzles))
}

// --- show --------------------------------------------------------------------

func puzzleShow(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("puzzle show", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	dateStr := fs.String("date", "", "puzzle date YYYY-MM-DD (alternative to positional puzzle_number)")
	asTable := fs.Bool("table", false, "human-readable table instead of JSON")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()

	p, err := resolvePuzzle(ctx, d, fs.Args(), *dateStr)
	if err != nil {
		return err
	}
	rounds, err := d.Rounds(ctx, p.ID)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	hasPlays, err := d.PuzzleHasPlays(ctx, p.ID)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}

	type answerJSON struct {
		Slot           int     `json:"slot"`
		ContentKind    string  `json:"content_kind"`
		AnswerText     string  `json:"answer_text"`
		IsTrap         bool    `json:"is_trap"`
		AuthorUserID   *string `json:"author_user_id"`
		BotCandidateID *string `json:"bot_candidate_id"`
		DecoyID        *string `json:"decoy_id"`
	}
	type roundJSON struct {
		RoundIndex  int          `json:"round_index"`
		PromptID    string       `json:"prompt_id"`
		PromptText  string       `json:"prompt_text"`
		TargetKind  string       `json:"target_kind"`
		TargetCount int          `json:"target_count"`
		Answers     []answerJSON `json:"answers"`
	}

	roundsOut := make([]roundJSON, 0, len(rounds))
	for _, r := range rounds {
		answers, err := d.AnswersForRound(ctx, r.ID)
		if err != nil {
			return emitError("db", err.Error(), nil)
		}
		ajson := make([]answerJSON, 0, len(answers))
		for slot, a := range answers {
			ajson = append(ajson, answerJSON{
				Slot:           slot,
				ContentKind:    string(a.ContentKind),
				AnswerText:     a.AnswerText,
				IsTrap:         a.IsTrap,
				AuthorUserID:   uuidPtrToString(a.AuthorUserID),
				BotCandidateID: uuidPtrToString(a.BotCandidateID),
				DecoyID:        uuidPtrToString(a.DecoyID),
			})
		}
		roundsOut = append(roundsOut, roundJSON{
			RoundIndex:  int(r.RoundIndex),
			PromptID:    r.PromptID.String(),
			PromptText:  r.PromptText,
			TargetKind:  r.TargetKind,
			TargetCount: int(r.TargetCount),
			Answers:     ajson,
		})
	}

	if *asTable {
		fmt.Fprintf(os.Stdout, "Puzzle #%d  %s  mode=%s  theme=%s  has_plays=%v\n\n",
			p.PuzzleNumber, p.PuzzleDate.Format("2006-01-02"), p.Mode, derefOr(p.Theme, "-"), hasPlays)
		for _, r := range roundsOut {
			fmt.Fprintf(os.Stdout, "Round %d  [%s]  %s\n", r.RoundIndex, r.TargetKind, r.PromptText)
			rows := make([][]any, 0, len(r.Answers))
			for _, a := range r.Answers {
				rows = append(rows, []any{a.Slot, a.ContentKind, truncate(a.AnswerText, 80)})
			}
			if err := emitTable([]string{"SLOT", "KIND", "TEXT"}, rows); err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout)
		}
		return nil
	}
	return emitJSON(map[string]any{
		"puzzle_number": p.PuzzleNumber,
		"puzzle_date":   p.PuzzleDate.Format("2006-01-02"),
		"mode":          string(p.Mode),
		"frozen_at":     p.FrozenAt.UTC().Format(time.RFC3339),
		"theme":         p.Theme,
		"has_plays":     hasPlays,
		"rounds":        roundsOut,
	})
}

// --- create ------------------------------------------------------------------

func puzzleCreate(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("puzzle create", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	dateStr := fs.String("date", "", "YYYY-MM-DD (required)")
	modeStr := fs.String("mode", "find_the_bot", "find_the_bot | find_the_human")
	theme := fs.String("theme", "", "optional theme tag")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *dateStr == "" {
		return emitError("invalid", "--date is required", nil)
	}
	date, err := time.Parse("2006-01-02", *dateStr)
	if err != nil {
		return emitError("invalid", "parse --date: "+err.Error(), nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()

	if existing, err := d.PuzzleByDate(ctx, date); err == nil {
		return emitOK("create", map[string]any{
			"created":       false,
			"puzzle_number": existing.PuzzleNumber,
			"note":          "puzzle already exists for this date",
		})
	} else if !db.IsNotFound(err) {
		return emitError("db", err.Error(), nil)
	}

	n, err := d.NextPuzzleNumber(ctx)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	var themePtr *string
	if *theme != "" {
		themePtr = theme
	}
	id, err := d.InsertDailyPuzzle(ctx, n, date, db.Mode(*modeStr), themePtr)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	return emitOK("create", map[string]any{
		"created":       true,
		"puzzle_number": n,
		"puzzle_id":     id.String(),
		"puzzle_date":   date.Format("2006-01-02"),
		"mode":          *modeStr,
	})
}

// --- compose -----------------------------------------------------------------

func puzzleCompose(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("puzzle compose", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	dateStr := fs.String("date", "", "YYYY-MM-DD (required)")
	modeStr := fs.String("mode", "", "force mode (default: rotation)")
	promptsStr := fs.String("prompts", "", "comma-separated prompt UUIDs (3 required); default = uniform random")
	r0Bots := fs.String("round0-bots", "", "explicit bot id(s) for round 0 (CSV); pairs with --round0-decoys")
	r0Decoys := fs.String("round0-decoys", "", "explicit decoy id(s) for round 0 (CSV); pairs with --round0-bots")
	r1Bots := fs.String("round1-bots", "", "explicit bot id(s) for round 1")
	r1Decoys := fs.String("round1-decoys", "", "explicit decoy id(s) for round 1")
	r2Bots := fs.String("round2-bots", "", "explicit bot id(s) for round 2")
	r2Decoys := fs.String("round2-decoys", "", "explicit decoy id(s) for round 2")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *dateStr == "" {
		return emitError("invalid", "--date is required", nil)
	}
	date, err := time.Parse("2006-01-02", *dateStr)
	if err != nil {
		return emitError("invalid", "parse --date: "+err.Error(), nil)
	}
	picks, err := parseExplicitPicks([3]string{*r0Bots, *r1Bots, *r2Bots}, [3]string{*r0Decoys, *r1Decoys, *r2Decoys})
	if err != nil {
		return err
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	return composePuzzle(ctx, d, log, date, db.Mode(*modeStr), *promptsStr, picks)
}

// roundPick is one round's explicit-pick spec. zero value (both empty) means
// "no explicit pick — fall back to random selection from the approved pool".
type roundPick struct {
	BotIDs   []uuid.UUID
	DecoyIDs []uuid.UUID
}

func (rp roundPick) explicit() bool { return len(rp.BotIDs) > 0 || len(rp.DecoyIDs) > 0 }

func parseExplicitPicks(botCSV, decoyCSV [3]string) ([3]roundPick, error) {
	var out [3]roundPick
	for i := 0; i < 3; i++ {
		bots, err := parseUUIDCSV(botCSV[i])
		if err != nil {
			return out, emitError("invalid", fmt.Sprintf("parse --round%d-bots: %s", i, err.Error()), nil)
		}
		decoys, err := parseUUIDCSV(decoyCSV[i])
		if err != nil {
			return out, emitError("invalid", fmt.Sprintf("parse --round%d-decoys: %s", i, err.Error()), nil)
		}
		// All-or-nothing per round.
		if (len(bots) == 0) != (len(decoys) == 0) {
			return out, emitError("invalid",
				fmt.Sprintf("round %d: must pass both --round%d-bots and --round%d-decoys or neither", i, i, i),
				nil)
		}
		out[i] = roundPick{BotIDs: bots, DecoyIDs: decoys}
	}
	return out, nil
}

func parseUUIDCSV(s string) ([]uuid.UUID, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]uuid.UUID, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := uuid.Parse(p)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", p, err)
		}
		out = append(out, id)
	}
	return out, nil
}

func composePuzzle(ctx context.Context, d *db.DB, log *slog.Logger, date time.Time, forcedMode db.Mode, promptsCSV string, picks [3]roundPick) error {
	mode := forcedMode
	if mode == "" {
		mode = puzzle.PickMode(date)
	}

	// Pick prompts (explicit or random).
	var promptIDs []uuid.UUID
	if promptsCSV != "" {
		for _, s := range strings.Split(promptsCSV, ",") {
			s = strings.TrimSpace(s)
			id, err := uuid.Parse(s)
			if err != nil {
				return emitError("invalid", "parse prompt id "+s+": "+err.Error(), nil)
			}
			promptIDs = append(promptIDs, id)
		}
		if len(promptIDs) != 3 {
			return emitError("invalid", fmt.Sprintf("--prompts must list exactly 3 ids, got %d", len(promptIDs)), nil)
		}
	} else {
		ids, err := puzzle.SelectPrompts(ctx, d, 3)
		if err != nil {
			return emitError("db", err.Error(), nil)
		}
		if len(ids) < 3 {
			return emitError("insufficient_content", fmt.Sprintf("need 3 prompts, only %d available", len(ids)), nil)
		}
		promptIDs = ids
	}

	// Reuse existing puzzle for date if present (idempotent).
	var puzzleID uuid.UUID
	var puzzleNumber int32
	if existing, err := d.PuzzleByDate(ctx, date); err == nil {
		// Refuse to recompose a played puzzle.
		played, perr := d.PuzzleHasPlays(ctx, existing.ID)
		if perr != nil {
			return emitError("db", perr.Error(), nil)
		}
		if played {
			return emitError("has_plays", "puzzle already has plays; refuse to recompose", map[string]any{
				"puzzle_number": existing.PuzzleNumber,
			})
		}
		puzzleID = existing.ID
		puzzleNumber = existing.PuzzleNumber
		// Allow updating mode and theme to the new values via InsertDailyPuzzle's upsert below.
	}
	if puzzleID == uuid.Nil {
		n, err := d.NextPuzzleNumber(ctx)
		if err != nil {
			return emitError("db", err.Error(), nil)
		}
		puzzleNumber = n
	}
	id, err := d.InsertDailyPuzzle(ctx, puzzleNumber, date, mode, nil)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	puzzleID = id

	for i, promptID := range promptIDs {
		targetKind := "bot"
		if mode == db.ModeFindHuman {
			targetKind = "human"
		}
		roundID, err := d.InsertPuzzleRound(ctx, puzzleID, int16(i), promptID, targetKind, 1)
		if err != nil {
			return emitError("db", err.Error(), nil)
		}
		var answers []db.Answer
		if picks[i].explicit() {
			answers, err = puzzle.ComposeRoundAnswersExplicit(ctx, d, promptID, mode, picks[i].BotIDs, picks[i].DecoyIDs)
			if err != nil {
				return composeAnswersErr(i, promptID, err)
			}
		} else {
			answers, err = puzzle.ComposeRoundAnswers(ctx, d, promptID, mode)
			if err != nil {
				return emitError("insufficient_content", fmt.Sprintf("round %d (prompt %s): %s", i, promptID, err.Error()), nil)
			}
		}
		if err := d.ReplaceRoundAnswers(ctx, roundID, answers); err != nil {
			return emitError("db", err.Error(), nil)
		}
	}
	log.Info("composed puzzle", "n", puzzleNumber, "date", date.Format("2006-01-02"), "mode", mode)
	return emitOK("compose", map[string]any{
		"puzzle_number": puzzleNumber,
		"puzzle_date":   date.Format("2006-01-02"),
		"mode":          string(mode),
	})
}

// composeAnswersErr maps a puzzle-package error from a round-composition call
// into the right structured envelope. Explicit-pick failures get `invalid`;
// "approved pool too small" gets `insufficient_content`.
func composeAnswersErr(roundIdx int, promptID uuid.UUID, err error) error {
	var bad *puzzle.ErrBadExplicitPick
	if errors.As(err, &bad) {
		return emitError("invalid", fmt.Sprintf("round %d (prompt %s): %s", roundIdx, promptID, bad.Msg), nil)
	}
	return emitError("insufficient_content", fmt.Sprintf("round %d (prompt %s): %s", roundIdx, promptID, err.Error()), nil)
}

// --- edit --------------------------------------------------------------------

func puzzleEdit(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("puzzle edit", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	modeStr := fs.String("mode", "", "find_the_bot | find_the_human")
	theme := fs.String("theme", "", "new theme (empty string clears nothing — pass --clear-theme)")
	dateStr := fs.String("date", "", "new puzzle_date YYYY-MM-DD")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	n, err := parsePuzzleNumber(fs.Args())
	if err != nil {
		return err
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()

	var modePtr *db.Mode
	if *modeStr != "" {
		m := db.Mode(*modeStr)
		modePtr = &m
	}
	var themePtr *string
	if *theme != "" {
		themePtr = theme
	}
	var datePtr *time.Time
	if *dateStr != "" {
		t, err := time.Parse("2006-01-02", *dateStr)
		if err != nil {
			return emitError("invalid", "parse --date: "+err.Error(), nil)
		}
		datePtr = &t
	}
	if err := d.UpdateDailyPuzzle(ctx, n, modePtr, themePtr, datePtr); err != nil {
		return puzzleErr(err)
	}
	return emitOK("edit", map[string]any{"puzzle_number": n})
}

// --- set-round ---------------------------------------------------------------

func puzzleSetRound(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("puzzle set-round", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	roundIdx := fs.Int("round", -1, "round index 0..2 (required)")
	promptStr := fs.String("prompt-id", "", "new prompt UUID (required)")
	botIDsCSV := fs.String("bot-ids", "", "explicit bot id(s) CSV; pairs with --decoy-ids. Omit both for random picks.")
	decoyIDsCSV := fs.String("decoy-ids", "", "explicit decoy id(s) CSV; pairs with --bot-ids.")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	n, err := parsePuzzleNumber(fs.Args())
	if err != nil {
		return err
	}
	if *roundIdx < 0 || *roundIdx > 2 {
		return emitError("invalid", "--round must be 0, 1, or 2", nil)
	}
	if *promptStr == "" {
		return emitError("invalid", "--prompt-id is required", nil)
	}
	promptID, err := uuid.Parse(*promptStr)
	if err != nil {
		return emitError("invalid", "parse --prompt-id: "+err.Error(), nil)
	}
	botIDs, err := parseUUIDCSV(*botIDsCSV)
	if err != nil {
		return emitError("invalid", "parse --bot-ids: "+err.Error(), nil)
	}
	decoyIDs, err := parseUUIDCSV(*decoyIDsCSV)
	if err != nil {
		return emitError("invalid", "parse --decoy-ids: "+err.Error(), nil)
	}
	if (len(botIDs) == 0) != (len(decoyIDs) == 0) {
		return emitError("invalid", "must pass both --bot-ids and --decoy-ids or neither", nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()

	p, err := d.PuzzleByNumber(ctx, n)
	if err != nil {
		return puzzleErr(err)
	}
	played, err := d.PuzzleHasPlays(ctx, p.ID)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	if played {
		return emitError("has_plays", "puzzle has plays; refuse to mutate rounds", map[string]any{"puzzle_number": n})
	}
	targetKind := "bot"
	if p.Mode == db.ModeFindHuman {
		targetKind = "human"
	}
	roundID, err := d.InsertPuzzleRound(ctx, p.ID, int16(*roundIdx), promptID, targetKind, 1)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	var answers []db.Answer
	if len(botIDs) > 0 {
		answers, err = puzzle.ComposeRoundAnswersExplicit(ctx, d, promptID, p.Mode, botIDs, decoyIDs)
		if err != nil {
			return composeAnswersErr(*roundIdx, promptID, err)
		}
	} else {
		answers, err = puzzle.ComposeRoundAnswers(ctx, d, promptID, p.Mode)
		if err != nil {
			return emitError("insufficient_content", err.Error(), nil)
		}
	}
	if err := d.ReplaceRoundAnswers(ctx, roundID, answers); err != nil {
		return emitError("db", err.Error(), nil)
	}
	return emitOK("set-round", map[string]any{
		"puzzle_number": n,
		"round_index":   *roundIdx,
		"prompt_id":     promptID.String(),
	})
}

// --- set-answer --------------------------------------------------------------

func puzzleSetAnswer(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("puzzle set-answer", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	roundIdx := fs.Int("round", -1, "round index 0..2 (required)")
	slot := fs.Int("slot", -1, "answer slot 0..3 (required, canonical order by id)")
	text := fs.String("text", "", "new answer text (text-only override; use with no --bot-id/--decoy-id)")
	botIDStr := fs.String("bot-id", "", "replace the slot with this approved bot_candidate (mutually exclusive with --decoy-id and --text)")
	decoyIDStr := fs.String("decoy-id", "", "replace the slot with this approved decoy_submission (mutually exclusive with --bot-id and --text)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	n, err := parsePuzzleNumber(fs.Args())
	if err != nil {
		return err
	}
	if *roundIdx < 0 || *roundIdx > 2 {
		return emitError("invalid", "--round must be 0, 1, or 2", nil)
	}
	if *slot < 0 || *slot > 3 {
		return emitError("invalid", "--slot must be 0..3", nil)
	}
	hasText := *text != ""
	hasBot := *botIDStr != ""
	hasDecoy := *decoyIDStr != ""
	setCount := 0
	for _, b := range []bool{hasText, hasBot, hasDecoy} {
		if b {
			setCount++
		}
	}
	if setCount != 1 {
		return emitError("invalid", "exactly one of --text, --bot-id, --decoy-id is required", nil)
	}
	var (
		botID, decoyID *uuid.UUID
	)
	if hasBot {
		id, err := uuid.Parse(*botIDStr)
		if err != nil {
			return emitError("invalid", "parse --bot-id: "+err.Error(), nil)
		}
		botID = &id
	}
	if hasDecoy {
		id, err := uuid.Parse(*decoyIDStr)
		if err != nil {
			return emitError("invalid", "parse --decoy-id: "+err.Error(), nil)
		}
		decoyID = &id
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	p, err := d.PuzzleByNumber(ctx, n)
	if err != nil {
		return puzzleErr(err)
	}
	rounds, err := d.Rounds(ctx, p.ID)
	if err != nil {
		return emitError("db", err.Error(), nil)
	}
	var roundID uuid.UUID
	var roundPromptID uuid.UUID
	found := false
	for _, r := range rounds {
		if int(r.RoundIndex) == *roundIdx {
			roundID = r.ID
			roundPromptID = r.PromptID
			found = true
			break
		}
	}
	if !found {
		return emitError("not_found", fmt.Sprintf("round %d not in puzzle #%d", *roundIdx, n), nil)
	}

	switch {
	case hasText:
		if err := d.OverrideAnswerText(ctx, p.ID, roundID, *slot, *text); err != nil {
			return puzzleErr(err)
		}
	case hasBot:
		// Validate the bot is approved + matches the round's prompt.
		if _, err := puzzle.PickBotsByIDs(ctx, d, roundPromptID, []uuid.UUID{*botID}); err != nil {
			return composeAnswersErr(*roundIdx, roundPromptID, err)
		}
		if err := d.OverrideAnswerContent(ctx, p.ID, roundID, *slot, botID, nil); err != nil {
			return puzzleErr(err)
		}
	case hasDecoy:
		if _, err := puzzle.PickDecoysByIDs(ctx, d, roundPromptID, []uuid.UUID{*decoyID}); err != nil {
			return composeAnswersErr(*roundIdx, roundPromptID, err)
		}
		if err := d.OverrideAnswerContent(ctx, p.ID, roundID, *slot, nil, decoyID); err != nil {
			return puzzleErr(err)
		}
	}
	payload := map[string]any{
		"puzzle_number": n,
		"round_index":   *roundIdx,
		"slot":          *slot,
	}
	if hasBot {
		payload["bot_id"] = botID.String()
	}
	if hasDecoy {
		payload["decoy_id"] = decoyID.String()
	}
	return emitOK("set-answer", payload)
}

// --- delete ------------------------------------------------------------------

func puzzleDelete(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("puzzle delete", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	n, err := parsePuzzleNumber(fs.Args())
	if err != nil {
		return err
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.DeleteDailyPuzzle(ctx, n); err != nil {
		return puzzleErr(err)
	}
	return emitOK("delete", map[string]any{"puzzle_number": n})
}

// --- schedule ----------------------------------------------------------------

func puzzleSchedule(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("puzzle schedule", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	startStr := fs.String("start", "", "YYYY-MM-DD (required)")
	days := fs.Int("days", 7, "number of consecutive days to schedule")
	modeStr := fs.String("mode", "", "force mode for every day (default: rotation)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *startStr == "" {
		return emitError("invalid", "--start is required", nil)
	}
	start, err := time.Parse("2006-01-02", *startStr)
	if err != nil {
		return emitError("invalid", "parse --start: "+err.Error(), nil)
	}
	if *days < 1 || *days > 365 {
		return emitError("invalid", "--days must be 1..365", nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()

	type result struct {
		Date         string `json:"date"`
		Status       string `json:"status"` // "composed" | "skipped" | "failed"
		PuzzleNumber int32  `json:"puzzle_number,omitempty"`
		Mode         string `json:"mode,omitempty"`
		Error        string `json:"error,omitempty"`
	}
	results := make([]result, 0, *days)

	for i := 0; i < *days; i++ {
		date := start.AddDate(0, 0, i)
		// Skip if a puzzle already exists for this date.
		if existing, err := d.PuzzleByDate(ctx, date); err == nil {
			results = append(results, result{
				Date: date.Format("2006-01-02"), Status: "skipped",
				PuzzleNumber: existing.PuzzleNumber, Mode: string(existing.Mode),
			})
			continue
		} else if !db.IsNotFound(err) {
			results = append(results, result{Date: date.Format("2006-01-02"), Status: "failed", Error: err.Error()})
			continue
		}
		mode := db.Mode(*modeStr)
		if mode == "" {
			mode = puzzle.PickMode(date)
		}
		if err := composeOne(ctx, d, log, date, mode); err != nil {
			results = append(results, result{Date: date.Format("2006-01-02"), Status: "failed", Error: err.Error()})
			continue
		}
		p, _ := d.PuzzleByDate(ctx, date)
		results = append(results, result{
			Date: date.Format("2006-01-02"), Status: "composed",
			PuzzleNumber: p.PuzzleNumber, Mode: string(mode),
		})
	}
	return emitJSON(map[string]any{"scheduled": results})
}

// composeOne is the schedule-loop's per-day composer. It does not write the
// per-day error envelope itself (the loop aggregates results).
func composeOne(ctx context.Context, d *db.DB, log *slog.Logger, date time.Time, mode db.Mode) error {
	promptIDs, err := puzzle.SelectPrompts(ctx, d, 3)
	if err != nil {
		return err
	}
	if len(promptIDs) < 3 {
		return fmt.Errorf("need 3 prompts, only %d available", len(promptIDs))
	}
	n, err := d.NextPuzzleNumber(ctx)
	if err != nil {
		return err
	}
	puzzleID, err := d.InsertDailyPuzzle(ctx, n, date, mode, nil)
	if err != nil {
		return err
	}
	for i, promptID := range promptIDs {
		targetKind := "bot"
		if mode == db.ModeFindHuman {
			targetKind = "human"
		}
		roundID, err := d.InsertPuzzleRound(ctx, puzzleID, int16(i), promptID, targetKind, 1)
		if err != nil {
			return err
		}
		answers, err := puzzle.ComposeRoundAnswers(ctx, d, promptID, mode)
		if err != nil {
			return fmt.Errorf("round %d (prompt %s): %w", i, promptID, err)
		}
		if err := d.ReplaceRoundAnswers(ctx, roundID, answers); err != nil {
			return err
		}
	}
	log.Info("scheduled puzzle", "n", n, "date", date.Format("2006-01-02"), "mode", mode)
	return nil
}

// --- helpers -----------------------------------------------------------------

func parsePuzzleNumber(args []string) (int32, error) {
	if len(args) < 1 {
		return 0, emitError("invalid", "puzzle_number is required", nil)
	}
	n, err := strconv.Atoi(args[0])
	if err != nil {
		return 0, emitError("invalid", "puzzle_number must be an integer: "+err.Error(), nil)
	}
	return int32(n), nil
}

func resolvePuzzle(ctx context.Context, d *db.DB, args []string, dateStr string) (*db.DailyPuzzle, error) {
	if dateStr != "" {
		t, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			return nil, emitError("invalid", "parse --date: "+err.Error(), nil)
		}
		p, err := d.PuzzleByDate(ctx, t)
		if err != nil {
			return nil, puzzleErr(err)
		}
		return p, nil
	}
	n, err := parsePuzzleNumber(args)
	if err != nil {
		return nil, err
	}
	p, err := d.PuzzleByNumber(ctx, n)
	if err != nil {
		return nil, puzzleErr(err)
	}
	return p, nil
}

// puzzleErr maps DB errors to error envelope codes.
func puzzleErr(err error) error {
	switch {
	case db.IsNotFound(err):
		return emitError("not_found", err.Error(), nil)
	case err == db.ErrHasPlays:
		return emitError("has_plays", err.Error(), nil)
	default:
		return emitError("db", err.Error(), nil)
	}
}

func puzzlesToJSON(puzzles []db.DailyPuzzle) []map[string]any {
	out := make([]map[string]any, 0, len(puzzles))
	for _, p := range puzzles {
		out = append(out, map[string]any{
			"puzzle_number": p.PuzzleNumber,
			"puzzle_date":   p.PuzzleDate.Format("2006-01-02"),
			"mode":          string(p.Mode),
			"frozen_at":     p.FrozenAt.UTC().Format(time.RFC3339),
			"theme":         p.Theme,
		})
	}
	return out
}

func uuidPtrToString(p *uuid.UUID) *string {
	if p == nil {
		return nil
	}
	s := p.String()
	return &s
}

func derefOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
