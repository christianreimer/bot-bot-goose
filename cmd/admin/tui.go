// Interactive bbg-admin console built on bubbletea + lipgloss. Invoked
// as `bbg-admin tui`. Tab cycles three top-level modes: REVIEW →
// UPCOMING → HISTORY → REVIEW.
//
// REVIEW mode (default):
//
//   1. PROMPT LIST — every non-locked prompt with its pending/ingested/
//      rejected counts and current approved pool size. Locked prompts
//      (already baked into a built puzzle) are excluded — they live in
//      UPCOMING / HISTORY.
//
//   2. REVIEW — the full list of submissions for one prompt, with
//      per-row decisions. Cursor moves with ↑↓; a/t/r decide the cursor
//      row and auto-advance to the next pending. Already-decided rows
//      are read-only this iteration (reversal is a separate change).
//      Once ≥3 humans are approved, the screen grows a BOT LINE block:
//        g   generate (random archetype, calls Anthropic)
//        y   accept the generated bot
//        m   make puzzle — assembles the next open round of the next
//             daily_puzzle and locks the prompt
//
// UPCOMING / HISTORY modes:
//
//   - List of daily_puzzles, newest-first for history, soonest-first
//     for upcoming.
//   - Enter on a row drills in: 3 rounds × 4 answers each, with the
//     bot highlighted and per-answer pick counts for history (data
//     pulled from play_guesses + play_rounds.slot_permutation).
//
// Why a TUI and not a script: triage rhythm is "read line, decide,
// next" — round-tripping through `bbg-admin prelaunch list/show/review`
// for every row costs three subprocesses and a context switch each.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/christianreimer/bot-bot-goose/internal/content"
	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/christianreimer/bot-bot-goose/internal/llm"
	"github.com/christianreimer/bot-bot-goose/internal/puzzle"
	"github.com/google/uuid"
)

// --- styles -----------------------------------------------------------------
//
// Pulled from the web app's pond-deep palette so the terminal feels like the
// same product. Colors are passed as hex strings; lipgloss handles 256-color
// and truecolor degradation per the operator's terminal.

var (
	colInk    = lipgloss.Color("#f4efe3")
	colMuted  = lipgloss.Color("#8fa6ae")
	colHonk   = lipgloss.Color("#f4a23b")
	colReed   = lipgloss.Color("#6fb36a")
	colMiss   = lipgloss.Color("#e0604f")
	colLine   = lipgloss.Color("#3c4f57")

	styleTitle = lipgloss.NewStyle().Foreground(colHonk).Bold(true)
	styleBrand = lipgloss.NewStyle().Foreground(colInk).Bold(true)
	styleMuted = lipgloss.NewStyle().Foreground(colMuted)
	styleKick  = lipgloss.NewStyle().Foreground(colHonk).Bold(true)

	styleApprove = lipgloss.NewStyle().Foreground(colReed).Bold(true)
	styleReject  = lipgloss.NewStyle().Foreground(colMiss).Bold(true)
	styleSkip    = lipgloss.NewStyle().Foreground(colMuted)

	styleSelected = lipgloss.NewStyle().
			Foreground(colInk).Background(lipgloss.Color("#244553")).
			Bold(true)

	styleRule = lipgloss.NewStyle().Foreground(colLine)

	stylePrompt = lipgloss.NewStyle().
			Foreground(colInk).Bold(true).
			MarginBottom(1)

	styleSub = lipgloss.NewStyle().
			Foreground(colInk).
			Border(lipgloss.RoundedBorder()).BorderForeground(colLine).
			Padding(1, 2).
			MarginTop(1).MarginBottom(1)

	styleMeta = lipgloss.NewStyle().Foreground(colMuted).Italic(true)

	styleFooter = lipgloss.NewStyle().Foreground(colMuted)

	styleNoteEditing = lipgloss.NewStyle().Foreground(colHonk).Bold(true)
)

// --- screens ----------------------------------------------------------------

type screen int

const (
	screenPrompts screen = iota
	screenReview
	screenPuzzles       // list of either upcoming or past puzzles per puzzleMode
	screenPuzzleDetail  // one puzzle's 3 rounds with answers + pick stats
)

// puzzleMode toggles screenPuzzles between "what's queued" and "what shipped."
// Tab cycles screenPrompts → upcoming → history → screenPrompts.
type puzzleMode int

const (
	puzzleModeUpcoming puzzleMode = iota
	puzzleModeHistory
)

// sortColumn identifies which column the prompt list is sorted by. The
// numeric values double as the digit-keybinding (1..5) the operator
// presses to switch column; pressing the same key flips the direction.
type sortColumn int

const (
	sortPending  sortColumn = 1
	sortIngested sortColumn = 2
	sortRejected sortColumn = 3
	sortPool     sortColumn = 4
	sortPrompt   sortColumn = 5
)

func (c sortColumn) label() string {
	switch c {
	case sortPending:
		return "PENDING"
	case sortIngested:
		return "INGESTED"
	case sortRejected:
		return "REJECTED"
	case sortPool:
		return "POOL"
	case sortPrompt:
		return "PROMPT"
	}
	return "?"
}

// defaultDesc returns the conventional initial direction when the operator
// freshly switches to this column. Numbers default to descending ("show
// me the biggest" — the prompts that need attention) and the text column
// defaults to ascending alphabetic.
func (c sortColumn) defaultDesc() bool {
	return c != sortPrompt
}

// --- model ------------------------------------------------------------------

type tuiModel struct {
	ctx           context.Context
	db            *db.DB
	reviewerID    uuid.UUID
	reviewerEmail string

	screen screen

	// prompt-list screen
	prompts       []db.PrelaunchPromptRollup
	promptsLoaded bool
	promptCursor  int
	// pendingOnly hides prompts with no pending submissions from the
	// prompt-list view. Default ON so the operator opens straight into
	// "what needs work right now." Toggled with 'p'.
	pendingOnly bool

	// sort state. Pressing 1..5 selects the column; pressing the active
	// column's key again toggles direction.
	sortCol  sortColumn
	sortDesc bool

	// review screen
	subs            []db.PrelaunchSubmission
	subIndex        int
	currentPromptID uuid.UUID

	// note input (toggled with 'n')
	note     textinput.Model
	noteOpen bool

	// bot-line build state — populated after the reviewer has approved
	// ≥3 humans on the current prompt. The reviewer drives it with g
	// (generate), y (accept), e (edit), m (make puzzle).
	llmClient   *llm.Client
	llmInitErr  error
	generating  bool
	botPending  *generatedBot // non-nil once a candidate is in hand
	botAccepted bool          // true after y → InsertApprovedBotLine

	// upcoming / history mode state. Lifecycle: Tab cycles the mode,
	// each switch triggers a fresh load of m.puzzles. Enter on a row
	// loads m.detail and flips to screenPuzzleDetail.
	puzzleMode    puzzleMode
	puzzles       []db.DailyPuzzleListRow
	puzzlesLoaded bool
	puzzleCursor  int
	detail        *puzzleDetail
	detailLoaded  bool

	// session stats
	approved int
	rejected int

	// transient bottom line
	flash   string
	flashAt time.Time

	width, height int
	err           error
	quitting      bool
}

// --- runner -----------------------------------------------------------------

func runTUI(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	dbf := registerDBFlags(fs)
	reviewerEmail := fs.String("reviewer-email",
		envOr("BBG_REVIEWER_EMAIL", ""),
		"email of the reviewing user (required; also via BBG_REVIEWER_EMAIL)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *reviewerEmail == "" {
		return emitError("invalid",
			"--reviewer-email (or BBG_REVIEWER_EMAIL) is required because moderation_reviews.reviewer_user_id is NOT NULL",
			nil)
	}
	d, err := openDB(ctx, dbf, log)
	if err != nil {
		return err
	}
	defer d.Close()

	reviewerID, err := d.UserIDByEmail(ctx, *reviewerEmail)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return emitError("not_found", "reviewer email not found", map[string]any{
				"email": *reviewerEmail,
				"hint":  "run `bbg-admin promote --email ... --role reviewer` to create one",
			})
		}
		return err
	}

	ti := textinput.New()
	ti.Placeholder = "optional note (e.g. \"good voice\", \"spam batch\") — Enter to confirm, Esc to cancel"
	ti.Prompt = "› "
	ti.CharLimit = 200
	ti.Width = 60

	m := tuiModel{
		ctx:           ctx,
		db:            d,
		reviewerID:    reviewerID,
		reviewerEmail: *reviewerEmail,
		screen:        screenPrompts,
		note:          ti,
		pendingOnly:   true, // default to triage mode
		sortCol:       sortPending,
		sortDesc:      true, // most pending first — matches the historical SQL ORDER BY
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	if fm, ok := final.(tuiModel); ok && fm.err != nil {
		return fm.err
	}
	return nil
}

// --- tea.Model --------------------------------------------------------------

func (m tuiModel) Init() tea.Cmd {
	return loadPromptsCmd(m.ctx, m.db)
}

type promptsLoadedMsg struct {
	prompts []db.PrelaunchPromptRollup
	err     error
}

type subsLoadedMsg struct {
	promptID uuid.UUID
	subs     []db.PrelaunchSubmission
	err      error
}

type decisionDoneMsg struct {
	decision    string // "approved" | "rejected"
	promptID    uuid.UUID
	prelaunchID uuid.UUID
	// decoyID is the new decoy_submissions.id minted by ApprovePrelaunch;
	// only set on the approve path. The handler uses this to stamp
	// m.subs[i].IngestedDecoy with the *real* id so a subsequent "Make
	// puzzle" can pass it through ComposeRoundAnswersExplicit (which
	// validates against decoy_submissions.id, not pre_launch.id).
	decoyID uuid.UUID
	err     error
}

func loadPromptsCmd(ctx context.Context, d *db.DB) tea.Cmd {
	return func() tea.Msg {
		ps, err := d.PrelaunchPromptCounts(ctx)
		return promptsLoadedMsg{prompts: ps, err: err}
	}
}

func loadSubsCmd(ctx context.Context, d *db.DB, promptID uuid.UUID) tea.Cmd {
	return func() tea.Msg {
		// No Status filter: load every submission for the prompt so the
		// reviewer can see what's already approved or rejected, not just
		// the pending queue. ListPrelaunch returns rows ORDER BY
		// consent_at DESC; we re-order in Go to put PENDING first
		// (still the actionable bucket).
		subs, err := d.ListPrelaunch(ctx, db.PrelaunchListOpts{
			PromptID: &promptID, Limit: 200,
		})
		if err == nil {
			sort.SliceStable(subs, func(i, j int) bool {
				return statusRank(subs[i]) < statusRank(subs[j])
			})
		}
		return subsLoadedMsg{promptID: promptID, subs: subs, err: err}
	}
}

// statusRank orders submissions: pending (0) → approved (1) → rejected
// (2). Used as the primary sort key so the actionable rows surface first
// when the reviewer opens a prompt.
func statusRank(s db.PrelaunchSubmission) int {
	switch {
	case s.IngestedDecoy != nil:
		return 1
	case s.RejectedAt != nil:
		return 2
	default:
		return 0
	}
}

// generatedBot is the in-memory state of a bot line awaiting reviewer
// decision. Promoted to bot_candidates only when the reviewer presses y.
// archetypeID is resolved up-front (via ArchetypeBySlug) so the accept
// path doesn't hit the DB twice.
type generatedBot struct {
	text          string
	archetypeID   uuid.UUID
	archetypeSlug string
	archetypeName string
	llmModel      string
	// botID is populated once accepted and persisted; "Make puzzle"
	// passes it into ComposeRoundAnswersExplicit.
	botID uuid.UUID
}

// puzzleDetail is the data the detail view renders: 3 rounds, each with
// its prompt + 4 answers + per-answer pick counts. catchPct is the
// frozen daily_collective_stats value (nil before the nightly rollup
// has run, which is normal for the same-day or future puzzles).
type puzzleDetail struct {
	puzzle      db.DailyPuzzle
	rounds      []db.PuzzleRound
	answers     [][]db.RoundAnswerStat // outer index = round_index
	totalPlays  []int                   // per round (typically equal)
	catchPct    *int
	catchTotal  *int
}

func approveCmd(ctx context.Context, d *db.DB, promptID, prelaunchID, reviewerID uuid.UUID, note string) tea.Cmd {
	return func() tea.Msg {
		decoyID, err := d.ApprovePrelaunch(ctx, prelaunchID, reviewerID, note)
		return decisionDoneMsg{
			decision:    "approved",
			promptID:    promptID,
			prelaunchID: prelaunchID,
			decoyID:     decoyID,
			err:         err,
		}
	}
}

func rejectCmd(ctx context.Context, d *db.DB, promptID, prelaunchID, reviewerID uuid.UUID, note string) tea.Cmd {
	return func() tea.Msg {
		err := d.RejectPrelaunch(ctx, prelaunchID, reviewerID, note)
		return decisionDoneMsg{
			decision:    "rejected",
			promptID:    promptID,
			prelaunchID: prelaunchID,
			err:         err,
		}
	}
}

// --- upcoming/history messages + commands -----------------------------------

type puzzlesLoadedMsg struct {
	mode    puzzleMode
	puzzles []db.DailyPuzzleListRow
	err     error
}

type puzzleDetailLoadedMsg struct {
	detail *puzzleDetail
	err    error
}

// loadPuzzlesCmd fetches the upcoming or historical list. Upcoming is
// puzzle_date >= today (no upper bound); history is the most-recent
// past 100 puzzles ordered newest-first. The 100-row cap on history
// keeps load time bounded as the archive grows; older puzzles are
// queryable through `bbg-admin puzzle list` if needed.
func loadPuzzlesCmd(ctx context.Context, d *db.DB, mode puzzleMode) tea.Cmd {
	return func() tea.Msg {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		switch mode {
		case puzzleModeUpcoming:
			ps, err := d.ListDailyPuzzlesWithFirstPrompt(ctx, db.PuzzleListOpts{
				From: &today, Limit: 0,
			})
			return puzzlesLoadedMsg{mode: mode, puzzles: ps, err: err}
		case puzzleModeHistory:
			// To = yesterday (today - 1ns to make the inclusive-date filter exclude today)
			endOfYesterday := today.Add(-time.Nanosecond)
			ps, err := d.ListDailyPuzzlesWithFirstPrompt(ctx, db.PuzzleListOpts{
				To: &endOfYesterday, IncludePast: true, Limit: 100,
			})
			// History wants newest-first; the list query returns ASC.
			for i, j := 0, len(ps)-1; i < j; i, j = i+1, j-1 {
				ps[i], ps[j] = ps[j], ps[i]
			}
			return puzzlesLoadedMsg{mode: mode, puzzles: ps, err: err}
		}
		return puzzlesLoadedMsg{mode: mode, err: fmt.Errorf("unknown puzzle mode")}
	}
}

// loadPuzzleDetailCmd assembles the full detail of one puzzle: its 3
// rounds (with prompt text), each round's 4 answers + pick counts, and
// the collective catch-rate from the nightly rollup (if available).
func loadPuzzleDetailCmd(ctx context.Context, d *db.DB, p db.DailyPuzzle) tea.Cmd {
	return func() tea.Msg {
		det := &puzzleDetail{puzzle: p}
		rounds, err := d.Rounds(ctx, p.ID)
		if err != nil {
			return puzzleDetailLoadedMsg{err: fmt.Errorf("rounds: %w", err)}
		}
		det.rounds = rounds
		det.answers = make([][]db.RoundAnswerStat, len(rounds))
		det.totalPlays = make([]int, len(rounds))
		for i, r := range rounds {
			stats, total, err := d.RoundAnswerStats(ctx, r.ID, p.ID, r.RoundIndex)
			if err != nil {
				return puzzleDetailLoadedMsg{err: fmt.Errorf("round %d stats: %w", r.RoundIndex, err)}
			}
			det.answers[i] = stats
			det.totalPlays[i] = total
		}
		if pct, total, err := d.CollectiveCatchPct(ctx, p.PuzzleNumber); err == nil {
			det.catchPct = &pct
			det.catchTotal = &total
		}
		return puzzleDetailLoadedMsg{detail: det}
	}
}

// --- build-flow messages + commands -----------------------------------------

type botGeneratedMsg struct {
	// promptID is the prompt this generation was kicked off for. The
	// handler ignores results whose promptID doesn't match
	// m.currentPromptID — guards against the user navigating to a
	// different prompt while the LLM call was in flight.
	promptID uuid.UUID
	bot      *generatedBot
	err      error
}

type botAcceptedMsg struct {
	promptID uuid.UUID
	botID    uuid.UUID
	err      error
}

type assembledMsg struct {
	puzzleNumber int32
	puzzleDate   time.Time
	roundIndex   int
	sealed       bool
	err          error
}

// generateBotCmd dispatches one LLM call. The archetype is picked at the
// caller (so it's stable across regenerate cycles if the reviewer wants
// to re-run the same voice) and the resolved archetype id flows through
// so the accept path doesn't re-query.
func generateBotCmd(ctx context.Context, client *llm.Client, req llm.BotLineRequest, promptID, archetypeID uuid.UUID, archetypeSlug string) tea.Cmd {
	return func() tea.Msg {
		res, err := client.GenerateBotLine(ctx, req)
		if err != nil {
			return botGeneratedMsg{promptID: promptID, err: err}
		}
		return botGeneratedMsg{
			promptID: promptID,
			bot: &generatedBot{
				text:          res.Text,
				archetypeID:   archetypeID,
				archetypeSlug: archetypeSlug,
				archetypeName: req.ArchetypeName,
				llmModel:      res.Model,
			},
		}
	}
}

// acceptBotCmd persists the in-memory candidate as an approved
// bot_candidates row. Returns the new bot id which is later passed to
// the round-assembly step.
func acceptBotCmd(ctx context.Context, d *db.DB, promptID, archetypeID uuid.UUID, text, llmModel string) tea.Cmd {
	return func() tea.Msg {
		id, err := d.InsertApprovedBotLine(ctx, promptID, archetypeID, text, llmModel)
		return botAcceptedMsg{promptID: promptID, botID: id, err: err}
	}
}

// assembleRoundCmd fills the next open round of the next open puzzle.
// Concretely:
//   1. Find or create the puzzle with < 3 rounds.
//   2. Insert puzzle_rounds at index = rounds-filled-so-far.
//   3. Compose 1 bot + 3 decoy answers (validated against the approved pool).
//   4. Write them via ReplaceRoundAnswers.
// When the just-filled round was index 2, the puzzle is now sealed.
func assembleRoundCmd(ctx context.Context, d *db.DB, promptID, botID uuid.UUID, decoyIDs []uuid.UUID) tea.Cmd {
	return func() tea.Msg {
		op, err := d.FindOrCreateOpenPuzzle(ctx)
		if err != nil {
			return assembledMsg{err: fmt.Errorf("find open puzzle: %w", err)}
		}
		roundIdx := int16(op.RoundsFilled)
		roundID, err := d.InsertPuzzleRound(ctx, op.ID, roundIdx, promptID, 1)
		if err != nil {
			return assembledMsg{err: fmt.Errorf("insert round: %w", err)}
		}
		answers, err := puzzle.ComposeRoundAnswersExplicit(ctx, d, promptID, []uuid.UUID{botID}, decoyIDs)
		if err != nil {
			return assembledMsg{err: fmt.Errorf("compose answers: %w", err)}
		}
		if err := d.ReplaceRoundAnswers(ctx, roundID, answers); err != nil {
			return assembledMsg{err: fmt.Errorf("write answers: %w", err)}
		}
		return assembledMsg{
			puzzleNumber: op.PuzzleNumber,
			puzzleDate:   op.PuzzleDate,
			roundIndex:   int(roundIdx),
			sealed:       op.RoundsFilled == 2,
		}
	}
}

// handleKeyPuzzles drives the upcoming/history list (mode is m.puzzleMode).
// Tab is handled one level up to cycle modes; this handles within-list nav.
func (m tuiModel) handleKeyPuzzles(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	last := len(m.puzzles) - 1
	pageStep := m.height - 9
	if pageStep < 5 {
		pageStep = 10
	}
	switch msg.String() {
	case "down", "j":
		if m.puzzleCursor < last {
			m.puzzleCursor++
		}
		return m, nil
	case "up", "k":
		if m.puzzleCursor > 0 {
			m.puzzleCursor--
		}
		return m, nil
	case "pgdown", "ctrl+f", "f":
		m.puzzleCursor += pageStep
		if m.puzzleCursor > last {
			m.puzzleCursor = last
		}
		return m, nil
	case "pgup", "ctrl+b", "b":
		m.puzzleCursor -= pageStep
		if m.puzzleCursor < 0 {
			m.puzzleCursor = 0
		}
		return m, nil
	case "home":
		m.puzzleCursor = 0
		return m, nil
	case "end", "G":
		m.puzzleCursor = last
		if m.puzzleCursor < 0 {
			m.puzzleCursor = 0
		}
		return m, nil
	case "r":
		m.puzzlesLoaded = false
		return m, loadPuzzlesCmd(m.ctx, m.db, m.puzzleMode)
	case "enter":
		if m.puzzleCursor < 0 || m.puzzleCursor >= len(m.puzzles) {
			return m, nil
		}
		return m, loadPuzzleDetailCmd(m.ctx, m.db, m.puzzles[m.puzzleCursor].DailyPuzzle)
	}
	return m, nil
}

// handleKeyPuzzleDetail: detail view is read-only. Just esc/q (handled
// at the outer level). Future iteration could add up/down to scroll
// long puzzles, but 3 rounds × 4 answers always fits.
func (m tuiModel) handleKeyPuzzleDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	return m, nil
}

// approvedDecoyIDsForPrompt returns the IDs of approved decoys produced
// by prelaunch ingestion of this prompt's submissions, in submission
// order. Used to grab the first 3 the reviewer approved when "Make
// puzzle" fires.
func approvedDecoyIDsForPrompt(subs []db.PrelaunchSubmission) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(subs))
	for _, s := range subs {
		if s.IngestedDecoy != nil {
			out = append(out, *s.IngestedDecoy)
		}
	}
	return out
}

// approvedHumanTextsForPrompt mirrors approvedDecoyIDsForPrompt but
// returns the texts — fed to the LLM as "hide-among-these" context.
func approvedHumanTextsForPrompt(subs []db.PrelaunchSubmission) []string {
	out := make([]string, 0, len(subs))
	for _, s := range subs {
		if s.IngestedDecoy != nil {
			out = append(out, s.Text)
		}
	}
	return out
}

// pickArchetype returns a random archetype from the starter roster. The
// TUI picks one per generate call; regenerate re-rolls. Difficulty 5
// (Mirror) is included — the model is just told its tell, the actual
// pool-trained behavior is on the bot-candidates batch generator's TODO.
func pickArchetype() content.Archetype {
	roster := content.StarterRoster
	return roster[rand.IntN(len(roster))]
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.note.Width = msg.Width - 8
		return m, nil

	case promptsLoadedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, tea.Quit
		}
		m.prompts = msg.prompts
		m.promptsLoaded = true
		if m.promptCursor >= len(m.prompts) {
			m.promptCursor = 0
		}
		return m, nil

	case subsLoadedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, tea.Quit
		}
		m.subs = msg.subs
		m.subIndex = 0
		m.currentPromptID = msg.promptID
		// Fresh prompt: clear any build state left over from a previous
		// prompt the reviewer was working on.
		m.botPending = nil
		m.botAccepted = false
		m.generating = false
		if len(msg.subs) == 0 {
			// No submissions of any status — nothing to look at.
			m.screen = screenPrompts
			m.flash = "no submissions yet for that prompt"
			m.flashAt = time.Now()
			return m, loadPromptsCmd(m.ctx, m.db)
		}
		m.screen = screenReview
		return m, nil

	case botGeneratedMsg:
		// Stale-generation guard: ignore results for prompts the
		// reviewer has navigated away from. Without this, a slow LLM
		// call kicked off on prompt A could pop up on prompt B and
		// the reviewer could accept a bot whose text was generated
		// for the wrong prompt.
		if msg.promptID != m.currentPromptID {
			return m, nil
		}
		m.generating = false
		if msg.err != nil {
			m.flash = styleReject.Render("llm: " + msg.err.Error())
			m.flashAt = time.Now()
			return m, nil
		}
		m.botPending = msg.bot
		m.botAccepted = false
		m.flash = styleKick.Render("bot generated (" + msg.bot.archetypeName + ")")
		m.flashAt = time.Now()
		return m, nil

	case botAcceptedMsg:
		// Same stale-result guard as botGeneratedMsg. The DB write
		// is committed regardless; we just don't apply the in-memory
		// state to a different prompt's screen.
		if msg.promptID != m.currentPromptID {
			return m, nil
		}
		if msg.err != nil {
			m.flash = styleReject.Render("accept bot: " + msg.err.Error())
			m.flashAt = time.Now()
			return m, nil
		}
		if m.botPending != nil {
			m.botPending.botID = msg.botID
		}
		m.botAccepted = true
		m.flash = styleApprove.Render("bot accepted — press m to assemble")
		m.flashAt = time.Now()
		return m, nil

	case assembledMsg:
		if msg.err != nil {
			m.flash = styleReject.Render("assemble: " + msg.err.Error())
			m.flashAt = time.Now()
			return m, nil
		}
		if msg.sealed {
			m.flash = styleApprove.Render(fmt.Sprintf("puzzle #%d sealed (3/3) · serves %s",
				msg.puzzleNumber, msg.puzzleDate.Format("2006-01-02")))
		} else {
			m.flash = styleApprove.Render(fmt.Sprintf("puzzle #%d round %d/3 · %s",
				msg.puzzleNumber, msg.roundIndex+1, msg.puzzleDate.Format("2006-01-02")))
		}
		m.flashAt = time.Now()
		// Round committed — prompt is now locked. Reset build state
		// and bounce back to the prompt list (which excludes locked).
		m.botPending = nil
		m.botAccepted = false
		m.generating = false
		m.subs = nil
		m.subIndex = 0
		m.screen = screenPrompts
		return m, loadPromptsCmd(m.ctx, m.db)

	case puzzlesLoadedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, tea.Quit
		}
		m.puzzles = msg.puzzles
		m.puzzleMode = msg.mode
		m.puzzlesLoaded = true
		m.puzzleCursor = 0
		return m, nil

	case puzzleDetailLoadedMsg:
		if msg.err != nil {
			m.flash = styleReject.Render("detail: " + msg.err.Error())
			m.flashAt = time.Now()
			m.screen = screenPuzzles
			return m, nil
		}
		m.detail = msg.detail
		m.detailLoaded = true
		m.screen = screenPuzzleDetail
		return m, nil

	case decisionDoneMsg:
		// Stale-result guard: if the user navigated to a different prompt
		// while this decision was in flight, drop the result silently.
		// The DB write is already committed; only the in-memory mutation
		// is suppressed. The new prompt's list was loaded fresh from the
		// DB, so it already reflects the correct state.
		if msg.promptID != m.currentPromptID {
			return m, nil
		}
		if msg.err != nil {
			if errors.Is(msg.err, db.ErrPrelaunchAlreadyDecided) {
				m.flash = "already decided — skipping"
				m.flashAt = time.Now()
			} else {
				m.err = msg.err
				return m, tea.Quit
			}
		} else {
			// Find the row by prelaunchID rather than indexing by
			// m.subIndex — the cursor may have moved since the
			// async approve was kicked off.
			rowIdx := -1
			for i, s := range m.subs {
				if s.ID == msg.prelaunchID {
					rowIdx = i
					break
				}
			}
			switch msg.decision {
			case "approved":
				m.approved++
				m.flash = styleApprove.Render("approved")
				if rowIdx >= 0 {
					id := msg.decoyID
					m.subs[rowIdx].IngestedDecoy = &id
				}
			case "rejected":
				m.rejected++
				m.flash = styleReject.Render("rejected")
				if rowIdx >= 0 {
					now := time.Now()
					m.subs[rowIdx].RejectedAt = &now
				}
			}
			m.flashAt = time.Now()
		}
		// Clear the note (one-shot per decision) and step to next pending.
		m.note.SetValue("")
		m.advanceCursorToNextPending()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Note editor swallows keys while open, except esc / enter.
	if m.noteOpen {
		switch msg.String() {
		case "esc":
			m.noteOpen = false
			m.note.Blur()
			return m, nil
		case "enter":
			m.noteOpen = false
			m.note.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.note, cmd = m.note.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "ctrl+c", "q":
		m.quitting = true
		return m, tea.Quit

	case "esc":
		switch m.screen {
		case screenReview:
			m.screen = screenPrompts
			m.subs = nil
			m.subIndex = 0
			return m, loadPromptsCmd(m.ctx, m.db)
		case screenPuzzles:
			m.screen = screenPrompts
			return m, loadPromptsCmd(m.ctx, m.db)
		case screenPuzzleDetail:
			m.screen = screenPuzzles
			m.detail = nil
			m.detailLoaded = false
			return m, nil
		}
		return m, nil

	case "tab":
		// Cycle: prompts → upcoming puzzles → history → prompts.
		// Tab is inert inside the review/detail subscreens (esc out first).
		switch m.screen {
		case screenPrompts:
			m.screen = screenPuzzles
			m.puzzleMode = puzzleModeUpcoming
			m.puzzlesLoaded = false
			return m, loadPuzzlesCmd(m.ctx, m.db, puzzleModeUpcoming)
		case screenPuzzles:
			if m.puzzleMode == puzzleModeUpcoming {
				m.puzzleMode = puzzleModeHistory
				m.puzzlesLoaded = false
				return m, loadPuzzlesCmd(m.ctx, m.db, puzzleModeHistory)
			}
			m.screen = screenPrompts
			return m, loadPromptsCmd(m.ctx, m.db)
		}
		return m, nil
	}

	switch m.screen {
	case screenPrompts:
		return m.handleKeyPrompts(msg)
	case screenReview:
		return m.handleKeyReview(msg)
	case screenPuzzles:
		return m.handleKeyPuzzles(msg)
	case screenPuzzleDetail:
		return m.handleKeyPuzzleDetail(msg)
	}
	return m, nil
}

func (m tuiModel) handleKeyPrompts(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Operate on the filtered slice so cursor bounds reflect what the
	// operator actually sees on screen.
	visible := m.visiblePrompts()
	last := len(visible) - 1
	// A reasonable page-step for full-page jumps. We don't have the rendered
	// viewport height in scope here so approximate from the terminal height
	// minus chrome — matches what viewPrompts uses to compute its window.
	pageStep := m.height - 9
	if pageStep < 5 {
		pageStep = 10
	}
	switch msg.String() {
	case "down", "j":
		if m.promptCursor < last {
			m.promptCursor++
		}
		return m, nil
	case "up", "k":
		if m.promptCursor > 0 {
			m.promptCursor--
		}
		return m, nil
	case "pgdown", "ctrl+f", "f":
		m.promptCursor += pageStep
		if m.promptCursor > last {
			m.promptCursor = last
		}
		return m, nil
	case "pgup", "ctrl+b", "b":
		m.promptCursor -= pageStep
		if m.promptCursor < 0 {
			m.promptCursor = 0
		}
		return m, nil
	case "g", "home":
		m.promptCursor = 0
		return m, nil
	case "G", "end":
		m.promptCursor = last
		if m.promptCursor < 0 {
			m.promptCursor = 0
		}
		return m, nil
	case "r":
		return m, loadPromptsCmd(m.ctx, m.db)
	case "p":
		// Toggle pending-only filter. Reset cursor so we don't end up
		// pointing at a row that just disappeared from the visible set.
		m.pendingOnly = !m.pendingOnly
		m.promptCursor = 0
		return m, nil
	case "1", "2", "3", "4", "5":
		// Sort by column N (numeric label matches sortColumn const). Same
		// key on the active column flips direction; switching to a new
		// column adopts that column's conventional default (DESC for
		// numbers, ASC for the prompt text).
		col := sortColumn(int(msg.String()[0] - '0'))
		if m.sortCol == col {
			m.sortDesc = !m.sortDesc
		} else {
			m.sortCol = col
			m.sortDesc = col.defaultDesc()
		}
		m.promptCursor = 0
		return m, nil
	case "enter":
		// Always allow opening so the reviewer can see what's already
		// approved or rejected on this prompt, not just pending. The
		// subs-loaded handler bounces back to the prompt list with a
		// flash if the prompt has truly zero submissions of any kind.
		visible := m.visiblePrompts()
		if m.promptCursor >= len(visible) {
			return m, nil
		}
		p := visible[m.promptCursor]
		return m, loadSubsCmd(m.ctx, m.db, p.PromptID)
	}
	return m, nil
}

// visiblePrompts is the slice the prompt-list view + Enter handler operate
// on — m.prompts filtered by pendingOnly and sorted by the active sort
// column. Kept as a method (recomputed each access) instead of a cached
// field so any change to m.prompts / m.pendingOnly / m.sortCol / m.sortDesc
// flows through without an explicit recompute step. The slice is always
// freshly allocated so callers' sort.Slice() never reaches back and
// reorders the underlying m.prompts.
func (m tuiModel) visiblePrompts() []db.PrelaunchPromptRollup {
	out := make([]db.PrelaunchPromptRollup, 0, len(m.prompts))
	for _, p := range m.prompts {
		if m.pendingOnly && p.Pending == 0 {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		// Text column sorts on prompt text directly.
		if m.sortCol == sortPrompt {
			if m.sortDesc {
				return a.PromptText > b.PromptText
			}
			return a.PromptText < b.PromptText
		}
		// Numeric columns: pull the int pair, then tiebreak on prompt
		// text alphabetic so equal-count rows have a stable order.
		var av, bv int
		switch m.sortCol {
		case sortPending:
			av, bv = a.Pending, b.Pending
		case sortIngested:
			av, bv = a.Ingested, b.Ingested
		case sortRejected:
			av, bv = a.Rejected, b.Rejected
		case sortPool:
			av, bv = a.ApprovedDec, b.ApprovedDec
		}
		if av != bv {
			if m.sortDesc {
				return av > bv
			}
			return av < bv
		}
		return a.PromptText < b.PromptText
	})
	return out
}

func (m tuiModel) handleKeyReview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(m.subs) == 0 {
		return m, nil
	}
	if m.subIndex >= len(m.subs) {
		m.subIndex = len(m.subs) - 1
	}
	cur := m.subs[m.subIndex]
	note := m.note.Value()
	decided := cur.IngestedDecoy != nil || cur.RejectedAt != nil

	switch msg.String() {
	case "down", "j":
		if m.subIndex < len(m.subs)-1 {
			m.subIndex++
		}
		return m, nil
	case "up", "k":
		if m.subIndex > 0 {
			m.subIndex--
		}
		return m, nil
	case "home":
		m.subIndex = 0
		return m, nil
	case "G", "end":
		m.subIndex = len(m.subs) - 1
		return m, nil
	case "a", "r":
		if decided {
			m.flash = styleMuted.Render("already decided — read-only")
			m.flashAt = time.Now()
			return m, nil
		}
		switch msg.String() {
		case "a":
			return m, approveCmd(m.ctx, m.db, cur.PromptID, cur.ID, m.reviewerID, note)
		case "r":
			return m, rejectCmd(m.ctx, m.db, cur.PromptID, cur.ID, m.reviewerID, note)
		}
	case "n":
		if decided {
			m.flash = styleMuted.Render("can't attach a note to a decided row")
			m.flashAt = time.Now()
			return m, nil
		}
		m.noteOpen = true
		m.note.Focus()
		return m, textinput.Blink

	// --- build-flow keys ------------------------------------------------
	case "g":
		// Generate (or regenerate) a bot line. Gate: ≥3 approved humans.
		approved := approvedDecoyIDsForPrompt(m.subs)
		if len(approved) < 3 {
			m.flash = styleMuted.Render(fmt.Sprintf("need 3 approved humans first (have %d)", len(approved)))
			m.flashAt = time.Now()
			return m, nil
		}
		if m.generating {
			m.flash = styleMuted.Render("already generating…")
			m.flashAt = time.Now()
			return m, nil
		}
		if m.llmClient == nil {
			c, err := llm.NewFromEnv()
			if err != nil {
				m.llmInitErr = err
				m.flash = styleReject.Render("llm: " + err.Error())
				m.flashAt = time.Now()
				return m, nil
			}
			m.llmClient = c
		}
		a := pickArchetype()
		archID, err := m.db.ArchetypeBySlug(m.ctx, a.Slug)
		if err != nil {
			m.flash = styleReject.Render("archetype lookup: " + err.Error())
			m.flashAt = time.Now()
			return m, nil
		}
		// Regenerate clears any pending/accepted bot — re-running the
		// LLM means the reviewer wants a different line.
		m.botPending = nil
		m.botAccepted = false
		m.generating = true
		m.flash = styleKick.Render("generating (" + a.Name + ")…")
		m.flashAt = time.Now()
		req := llm.BotLineRequest{
			Prompt:              cur.PromptText,
			ArchetypeName:       a.Name,
			ArchetypeTell:       a.Tell,
			ArchetypeDifficulty: a.Difficulty,
			HumanLines:          approvedHumanTextsForPrompt(m.subs),
		}
		return m, generateBotCmd(m.ctx, m.llmClient, req, cur.PromptID, archID, a.Slug)

	case "y":
		if m.botPending == nil {
			m.flash = styleMuted.Render("no bot line — press g to generate")
			m.flashAt = time.Now()
			return m, nil
		}
		if m.botAccepted {
			m.flash = styleMuted.Render("bot already accepted — press m to assemble")
			m.flashAt = time.Now()
			return m, nil
		}
		return m, acceptBotCmd(m.ctx, m.db, cur.PromptID,
			m.botPending.archetypeID, m.botPending.text, m.botPending.llmModel)

	case "m":
		// Make puzzle: 3 approved humans + 1 accepted bot → assemble.
		approved := approvedDecoyIDsForPrompt(m.subs)
		if len(approved) < 3 {
			m.flash = styleMuted.Render(fmt.Sprintf("need 3 approved humans (have %d)", len(approved)))
			m.flashAt = time.Now()
			return m, nil
		}
		if !m.botAccepted || m.botPending == nil || m.botPending.botID == uuid.Nil {
			m.flash = styleMuted.Render("need an accepted bot line (g, then y)")
			m.flashAt = time.Now()
			return m, nil
		}
		return m, assembleRoundCmd(m.ctx, m.db, cur.PromptID, m.botPending.botID, approved[:3])
	}
	return m, nil
}

// advanceCursorToNextPending moves m.subIndex to the next PENDING row at or
// after the current cursor (skipping anything already decided). If nothing
// pending remains below, falls back to the next decided row so the reviewer
// can still page forward visually. If the list is exhausted, leaves the
// cursor at the last index.
func (m *tuiModel) advanceCursorToNextPending() {
	for i := m.subIndex + 1; i < len(m.subs); i++ {
		if m.subs[i].IngestedDecoy == nil && m.subs[i].RejectedAt == nil {
			m.subIndex = i
			return
		}
	}
	if m.subIndex < len(m.subs)-1 {
		m.subIndex++
	}
}

// --- view -------------------------------------------------------------------

func (m tuiModel) View() string {
	if m.quitting {
		return ""
	}
	if m.err != nil {
		return lipgloss.NewStyle().Foreground(colMiss).Render("error: " + m.err.Error())
	}

	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n")
	switch m.screen {
	case screenPrompts:
		b.WriteString(m.viewPrompts())
	case screenReview:
		b.WriteString(m.viewReview())
	case screenPuzzles:
		b.WriteString(m.viewPuzzles())
	case screenPuzzleDetail:
		b.WriteString(m.viewPuzzleDetail())
	}
	b.WriteString("\n")
	b.WriteString(m.footer())
	return b.String()
}

// modeChip renders a single mode tab in the header. Active tab gets
// honk-orange highlight; the others are muted. Tabs visually anchor the
// reviewer's current location and the Tab cycle order: REVIEW → UPCOMING
// → HISTORY → REVIEW.
func (m tuiModel) modeChip(label string, active bool) string {
	if active {
		return styleKick.Render("▣ " + label)
	}
	return styleMuted.Render("▫ " + label)
}

func (m tuiModel) header() string {
	left := styleBrand.Render("🪿  bot bot goose")
	// Mode chips reflect the screen the reviewer is on. The review and
	// puzzle subscreens (review one prompt / view one puzzle) inherit
	// their parent list's chip so the reviewer never loses their place.
	reviewActive := m.screen == screenPrompts || m.screen == screenReview
	upcomingActive := (m.screen == screenPuzzles || m.screen == screenPuzzleDetail) && m.puzzleMode == puzzleModeUpcoming
	historyActive := (m.screen == screenPuzzles || m.screen == screenPuzzleDetail) && m.puzzleMode == puzzleModeHistory
	chips := strings.Join([]string{
		m.modeChip("REVIEW", reviewActive),
		m.modeChip("UPCOMING", upcomingActive),
		m.modeChip("HISTORY", historyActive),
	}, "  ")
	right := fmt.Sprintf("%s %d  %s %d",
		styleApprove.Render("approved"), m.approved,
		styleReject.Render("rejected"), m.rejected,
	)
	leftBlock := left + "  " + chips
	gap := m.width - lipgloss.Width(leftBlock) - lipgloss.Width(right)
	if gap < 2 {
		gap = 2
	}
	line := leftBlock + strings.Repeat(" ", gap) + right
	rule := styleRule.Render(strings.Repeat("─", maxInt(m.width, 40)))
	return line + "\n" + rule
}

func (m tuiModel) viewPrompts() string {
	if !m.promptsLoaded {
		return styleMuted.Render("loading…")
	}
	if len(m.prompts) == 0 {
		return styleMuted.Render("no prompts in the database — load some via `bbg-admin import`")
	}
	visible := m.visiblePrompts()
	if len(visible) == 0 {
		// Filter is on but nothing matches. Most likely "pendingOnly is
		// on, no prompts have pending submissions" — successful empty
		// state, not an error. Tell the operator how to see the rest.
		return styleMuted.Render("nothing pending. press p to show all prompts, r to refresh.")
	}

	// Column widths
	const (
		wPending = 8
		wIng     = 9
		wRej     = 9
		wPool    = 5
	)
	wPrompt := m.width - wPending - wIng - wRej - wPool - 4
	if wPrompt < 20 {
		wPrompt = 20
	}

	// Compute the visible window. The chrome (header line + rule + blank +
	// footer rule + footer line) takes ~6 rows. Reserve 1 more for the
	// table header and 2 for scroll indicators. Anything left is the
	// scrollable list area.
	const chrome = 6 + 1 + 2 // header+rule+blank, table-header, scroll-indicators
	rowsAvailable := m.height - chrome
	if rowsAvailable < 5 {
		// Either height isn't known yet (WindowSizeMsg pending) or the
		// terminal is tiny. Render everything and let the operator scroll
		// their terminal — degrading gracefully beats clipping silently.
		rowsAvailable = len(visible)
	}

	// Cursor-following window: keep the selected row in view. If the
	// cursor drifts off the bottom, scroll just enough to keep it; same
	// for the top. Center-on-cursor would be smoother but the jitter is
	// distracting during fast j/k navigation.
	total := len(visible)
	start := 0
	if total > rowsAvailable {
		// Keep a 1-row buffer above and below where possible.
		if m.promptCursor < rowsAvailable-1 {
			start = 0
		} else if m.promptCursor >= total-1 {
			start = total - rowsAvailable
		} else {
			// Place cursor roughly in the middle of the viewport.
			start = m.promptCursor - rowsAvailable/2
			if start < 0 {
				start = 0
			}
			if start+rowsAvailable > total {
				start = total - rowsAvailable
			}
		}
	}
	end := start + rowsAvailable
	if end > total {
		end = total
	}

	// Column headers — bracket the active column with the sort indicator.
	// The indicator stays a single visible character so column widths
	// don't drift when the sort changes.
	head := fmt.Sprintf("%*s %*s %*s %*s  %s",
		wPending, decorate("PENDING", sortPending, m),
		wIng, decorate("INGESTED", sortIngested, m),
		wRej, decorate("REJECTED", sortRejected, m),
		wPool, decorate("POOL", sortPool, m),
		decorate("PROMPT", sortPrompt, m))
	out := []string{styleKick.Render(head)}

	above := start
	if above > 0 {
		out = append(out, styleMuted.Render(fmt.Sprintf("↑ %d more above", above)))
	} else {
		out = append(out, "")
	}

	for i := start; i < end; i++ {
		p := visible[i]
		text := truncateW(p.PromptText, wPrompt)
		row := fmt.Sprintf("%*d %*d %*d %*d  %s",
			wPending, p.Pending, wIng, p.Ingested, wRej, p.Rejected, wPool, p.ApprovedDec, text)
		if i == m.promptCursor {
			row = styleSelected.Render(row)
		}
		out = append(out, row)
	}

	below := total - end
	if below > 0 {
		out = append(out, styleMuted.Render(fmt.Sprintf("↓ %d more below", below)))
	} else {
		out = append(out, "")
	}

	// Position indicator + active filter + active sort
	filter := "all"
	if m.pendingOnly {
		filter = fmt.Sprintf("pending-only (p to show all %d)", len(m.prompts))
	}
	dir := "↑ asc"
	if m.sortDesc {
		dir = "↓ desc"
	}
	pos := styleMuted.Render(fmt.Sprintf("row %d of %d · filter: %s · sort: %s %s",
		m.promptCursor+1, total, filter, m.sortCol.label(), dir))
	out = append([]string{pos}, out...)

	return strings.Join(out, "\n")
}

// decorate appends the sort-direction indicator to a column header when
// that column is the active sort. Keeping the marker a single rune means
// column widths stay stable across re-sorts.
func decorate(label string, col sortColumn, m tuiModel) string {
	if m.sortCol != col {
		return label
	}
	if m.sortDesc {
		return label + "↓"
	}
	return label + "↑"
}

func (m tuiModel) viewReview() string {
	if len(m.subs) == 0 {
		return styleMuted.Render("no submissions for this prompt")
	}
	if m.subIndex >= len(m.subs) {
		m.subIndex = len(m.subs) - 1
	}
	cur := m.subs[m.subIndex]

	// Per-prompt rollup of statuses for the header. "3 needed" reflects
	// the puzzle-build target — every round wants exactly 3 humans. Anything
	// beyond is overshoot the reviewer can ignore (or keep for the pool).
	var pending, approved, rejected int
	for _, s := range m.subs {
		switch {
		case s.IngestedDecoy != nil:
			approved++
		case s.RejectedAt != nil:
			rejected++
		default:
			pending++
		}
	}
	progress := fmt.Sprintf("%s approved of 3 needed  ·  %d pending  ·  %d rejected",
		approvedCounter(approved), pending, rejected)

	prompt := styleKick.Render("PROMPT") + "\n" +
		stylePrompt.Render(cur.PromptText)

	// List: one row per submission. Each row carries its status badge so
	// the reviewer can scan the whole queue at a glance instead of paging
	// through one at a time.
	const badgeWidth = 11 // "[APPROVED] " widest visible badge
	rowTextWidth := m.width - badgeWidth - 4
	if rowTextWidth < 20 {
		rowTextWidth = 20
	}
	var listLines []string
	for i, s := range m.subs {
		var badge string
		switch {
		case s.IngestedDecoy != nil:
			badge = styleApprove.Render("[APPROVED]")
		case s.RejectedAt != nil:
			badge = styleReject.Render("[REJECTED]")
		default:
			badge = styleKick.Render("[PENDING] ")
		}
		text := truncateW(singleLine(s.Text), rowTextWidth)
		prefix := "  "
		if i == m.subIndex {
			prefix = styleKick.Render("› ")
		}
		row := prefix + badge + "  " + text
		if i == m.subIndex {
			row = styleSelected.Render(row)
		}
		listLines = append(listLines, row)
	}

	// Focused detail box — full text + metadata for the cursor row.
	subBox := styleSub.Width(maxInt(m.width-2, 40)).Render(cur.Text)

	meta := []string{}
	if !cur.ConsentAt.IsZero() {
		meta = append(meta, "submitted "+humanizeAgo(time.Since(cur.ConsentAt)))
	}
	if cur.RequestedIP != nil && *cur.RequestedIP != "" {
		meta = append(meta, "ip "+maskIP(*cur.RequestedIP))
	}
	if cur.UserID != nil {
		meta = append(meta, "user "+cur.UserID.String()[:8]+"…")
	}
	metaLine := styleMeta.Render(strings.Join(meta, " · "))

	var noteLine string
	if m.noteOpen {
		noteLine = styleNoteEditing.Render("Note (enter to confirm, esc to cancel):") + "\n  " + m.note.View()
	} else {
		v := m.note.Value()
		if v == "" {
			noteLine = styleMuted.Render("note: ") + styleMuted.Render("(none — press n to add one)")
		} else {
			noteLine = styleKick.Render("note: ") + v
		}
	}

	parts := []string{
		styleMuted.Render(progress),
		"",
		prompt,
		styleKick.Render("SUBMISSIONS"),
	}
	parts = append(parts, listLines...)
	parts = append(parts, "", subBox, metaLine, "", noteLine)
	// Bot section only appears once the human-line gate is met (≥3
	// approved). Below that, the reviewer's job is still selection;
	// surfacing the generate key earlier would invite premature
	// generation against a not-yet-curated pool.
	if approved >= 3 {
		parts = append(parts, "", m.viewBotSection(approved))
	}
	return strings.Join(parts, "\n")
}

// viewPuzzles renders the upcoming-or-history list. Columns: puzzle#,
// date, sealed/open marker, and the round-0 prompt as a snippet so the
// reviewer can identify the puzzle without drilling in.
func (m tuiModel) viewPuzzles() string {
	if !m.puzzlesLoaded {
		return styleMuted.Render("loading…")
	}
	if len(m.puzzles) == 0 {
		switch m.puzzleMode {
		case puzzleModeUpcoming:
			return styleMuted.Render("no upcoming puzzles. press tab to flip to history, or back out (esc) to start building.")
		case puzzleModeHistory:
			return styleMuted.Render("no historical puzzles yet.")
		}
	}

	// Column widths
	const (
		wNum  = 6
		wDate = 12
		wRds  = 6
	)
	wPrompt := m.width - wNum - wDate - wRds - 6
	if wPrompt < 20 {
		wPrompt = 20
	}

	// Viewport math (matches viewPrompts).
	const chrome = 6 + 1 + 2
	rowsAvailable := m.height - chrome
	if rowsAvailable < 5 {
		rowsAvailable = len(m.puzzles)
	}
	total := len(m.puzzles)
	start := 0
	if total > rowsAvailable {
		if m.puzzleCursor < rowsAvailable-1 {
			start = 0
		} else if m.puzzleCursor >= total-1 {
			start = total - rowsAvailable
		} else {
			start = m.puzzleCursor - rowsAvailable/2
			if start < 0 {
				start = 0
			}
			if start+rowsAvailable > total {
				start = total - rowsAvailable
			}
		}
	}
	end := start + rowsAvailable
	if end > total {
		end = total
	}

	head := fmt.Sprintf("%*s  %*s  %*s  %s",
		wNum, "#", wDate, "DATE", wRds, "RDS", "ROUND 0 PROMPT")
	out := []string{styleKick.Render(head)}

	above := start
	if above > 0 {
		out = append(out, styleMuted.Render(fmt.Sprintf("↑ %d more above", above)))
	} else {
		out = append(out, "")
	}

	for i := start; i < end; i++ {
		p := m.puzzles[i]
		rds := fmt.Sprintf("%d/3", p.RoundCount)
		ptext := p.FirstPromptText
		if ptext == "" {
			ptext = styleMuted.Render("(no rounds yet)")
		}
		row := fmt.Sprintf("%*d  %*s  %*s  %s",
			wNum, p.PuzzleNumber, wDate, p.PuzzleDate.Format("2006-01-02"),
			wRds, rds, truncateW(ptext, wPrompt))
		if i == m.puzzleCursor {
			row = styleSelected.Render(row)
		}
		out = append(out, row)
	}

	below := total - end
	if below > 0 {
		out = append(out, styleMuted.Render(fmt.Sprintf("↓ %d more below", below)))
	} else {
		out = append(out, "")
	}

	mode := "upcoming"
	if m.puzzleMode == puzzleModeHistory {
		mode = "history"
	}
	pos := styleMuted.Render(fmt.Sprintf("row %d of %d · %s", m.puzzleCursor+1, total, mode))
	out = append([]string{pos}, out...)
	return strings.Join(out, "\n")
}

// viewPuzzleDetail renders the 3 rounds + 4 answers each, with per-answer
// pick stats when the puzzle has been played. The bot row gets a honk-
// orange highlight so it's obvious which one was the AI.
func (m tuiModel) viewPuzzleDetail() string {
	if !m.detailLoaded || m.detail == nil {
		return styleMuted.Render("loading…")
	}
	d := m.detail
	hdr := fmt.Sprintf("PUZZLE #%d  ·  %s",
		d.puzzle.PuzzleNumber, d.puzzle.PuzzleDate.Format("2006-01-02"))
	if d.catchPct != nil && d.catchTotal != nil {
		hdr += fmt.Sprintf("  ·  catch rate %s of %d plays",
			styleApprove.Render(fmt.Sprintf("%d%%", *d.catchPct)),
			*d.catchTotal)
	} else if anyPlays(d.totalPlays) {
		hdr += "  ·  " + styleMuted.Render("rollup pending")
	}

	parts := []string{styleKick.Render(hdr), ""}
	for i, r := range d.rounds {
		parts = append(parts, styleKick.Render(fmt.Sprintf("ROUND %d", r.RoundIndex+1)),
			stylePrompt.Render(r.PromptText))
		stats := d.answers[i]
		total := d.totalPlays[i]
		for _, a := range stats {
			label := "HUMAN"
			text := a.Text
			if a.IsBot {
				label = styleKick.Render("BOT  ")
			}
			var picks string
			if total > 0 {
				pct := 0
				if total > 0 {
					pct = (a.Picks * 100) / total
				}
				picks = fmt.Sprintf("%5d picks · %3d%%", a.Picks, pct)
			} else {
				picks = styleMuted.Render("        no plays yet")
			}
			row := fmt.Sprintf("  %s  %s  %s",
				label, picks, truncateW(singleLine(text), m.width-32))
			if a.IsBot {
				row = styleKick.Render(row)
			}
			parts = append(parts, row)
		}
		if total > 0 {
			parts = append(parts, styleMuted.Render(fmt.Sprintf("  total: %d plays", total)))
		}
		parts = append(parts, "")
	}
	return strings.Join(parts, "\n")
}

// anyPlays reports whether at least one round of the detail has plays.
// Drives the "rollup pending" hint vs. omitting catch-rate context
// entirely for upcoming puzzles.
func anyPlays(per []int) bool {
	for _, n := range per {
		if n > 0 {
			return true
		}
	}
	return false
}

// viewBotSection renders the bot-line region of the review screen. Three
// states:
//   - none: prompt to press `g` (with the LLM model id)
//   - generating: "calling LLM…"
//   - pending: model output + accept/regenerate/edit keys
//   - accepted: confirmed + invitation to press `m`
//
// `approvedHumans` is passed in so the section can show the make-puzzle
// readiness line without recounting.
func (m tuiModel) viewBotSection(approvedHumans int) string {
	header := styleKick.Render("BOT LINE")
	if m.llmInitErr != nil {
		return header + "\n" + styleReject.Render("llm unavailable: "+m.llmInitErr.Error())
	}
	if m.generating {
		return header + "\n" + styleMuted.Render("calling LLM…")
	}
	if m.botPending == nil {
		model := llm.DefaultModel
		if m.llmClient != nil {
			model = m.llmClient.Model()
		}
		return header + "\n" + styleMuted.Render("(none — press g to generate with "+model+")")
	}

	subhead := fmt.Sprintf("%s · %s",
		styleMuted.Render(m.botPending.archetypeName),
		styleMuted.Render(m.botPending.llmModel))
	box := styleSub.Width(maxInt(m.width-2, 40)).Render(m.botPending.text)

	var status, ready string
	if m.botAccepted {
		status = styleApprove.Render("✓ accepted")
		ready = fmt.Sprintf("%s · press %s to assemble into the next round",
			styleApprove.Render(fmt.Sprintf("READY: %d/3 humans · 1/1 bot", approvedHumans)),
			styleKick.Render("m"))
	} else {
		status = styleKick.Render("press: y accept · g regenerate")
		ready = styleMuted.Render(fmt.Sprintf("not ready: %d/3 humans · bot not yet accepted", approvedHumans))
	}
	return strings.Join([]string{header + "  " + subhead, status, box, ready}, "\n")
}

// approvedCounter renders the approved count with reed (green) once the
// puzzle-build target of 3 is hit, muted before that. Visual cue that the
// human-line slot is ready to use in a "Make puzzle" step.
func approvedCounter(n int) string {
	if n >= 3 {
		return styleApprove.Render(fmt.Sprintf("%d", n))
	}
	return fmt.Sprintf("%d", n)
}

// singleLine collapses internal newlines to spaces so multi-line submissions
// render predictably in the one-line-per-row list. The focus detail box
// below the list still wraps to show the full text.
func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func (m tuiModel) footer() string {
	rule := styleRule.Render(strings.Repeat("─", maxInt(m.width, 40)))
	var keys string
	switch m.screen {
	case screenPrompts:
		keys = "↑↓ nav · f/b page · enter open · 1-5 sort · p pending-only · r refresh · tab→upcoming · q quit"
	case screenPuzzles:
		mode := "upcoming"
		nextMode := "history"
		if m.puzzleMode == puzzleModeHistory {
			mode = "history"
			nextMode = "review"
		}
		_ = mode
		keys = fmt.Sprintf("↑↓ nav · enter open · r refresh · tab→%s · esc back · q quit", nextMode)
	case screenPuzzleDetail:
		keys = "esc back · q quit"
	case screenReview:
		// Build-mode keys show up once 3+ humans are approved on this
		// prompt — that's when the bot section becomes interactive.
		approvedH := 0
		for _, s := range m.subs {
			if s.IngestedDecoy != nil {
				approvedH++
			}
		}
		buildKeys := ""
		if approvedH >= 3 {
			buildKeys = "  " + styleKick.Render("g gen") + "  " +
				styleKick.Render("y accept") + "  " +
				styleApprove.Render("m make puzzle")
		}
		switch {
		case m.noteOpen:
			keys = "type note · enter confirm · esc cancel"
		case m.subIndex < len(m.subs) && (m.subs[m.subIndex].IngestedDecoy != nil || m.subs[m.subIndex].RejectedAt != nil):
			// Cursor row is already decided: a/r are no-ops because
			// reversal is not yet supported. Navigation still works,
			// and the build keys are still live (they're per-prompt,
			// not per-row).
			keys = "↑↓ nav  " +
				styleMuted.Render("a/r disabled (already decided)") + buildKeys + "  esc back  q quit"
		default:
			keys = "↑↓ nav  " +
				styleApprove.Render("a approve") + "  " +
				styleReject.Render("r reject") + "  " +
				"n note" + buildKeys + "  esc back  q quit"
		}
	}
	flash := ""
	if m.flash != "" && time.Since(m.flashAt) < 3*time.Second {
		flash = "  " + m.flash
	}
	reviewer := styleMuted.Render("reviewer: " + m.reviewerEmail)
	left := styleFooter.Render(keys) + flash
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(reviewer)
	if gap < 2 {
		gap = 2
	}
	return rule + "\n" + left + strings.Repeat(" ", gap) + reviewer
}

// --- helpers ----------------------------------------------------------------

// truncateW shortens s to at most w display columns, ellipsizing with "…"
// when the input is wider. Named with the W suffix because cmd/admin/puzzle.go
// already owns `truncate`.
func truncateW(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	if w < 2 {
		return strings.Repeat(".", w)
	}
	out := []rune{}
	width := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if width+rw > w-1 {
			break
		}
		out = append(out, r)
		width += rw
	}
	return string(out) + "…"
}

func humanizeAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// maskIP drops the last octet (IPv4) so the reviewer sees enough for spam
// triage without the IP itself dominating the screen. IPv6 truncated to the
// first 4 hextets.
func maskIP(ip string) string {
	if strings.Contains(ip, ":") {
		parts := strings.Split(ip, ":")
		if len(parts) > 4 {
			parts = parts[:4]
		}
		return strings.Join(parts, ":") + "::x"
	}
	parts := strings.Split(ip, ".")
	if len(parts) == 4 {
		parts[3] = "x"
		return strings.Join(parts, ".")
	}
	return ip
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
