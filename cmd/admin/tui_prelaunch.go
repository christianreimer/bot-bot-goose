// Interactive prelaunch review TUI built on bubbletea + lipgloss.
//
// Two screens:
//
//   1. PROMPT LIST — every prompt with its pending/ingested/rejected counts
//      and current approved-decoy pool size. Sorted by PENDING desc so the
//      reviewer always opens the prompt that needs the most attention.
//
//   2. REVIEW — one submission at a time, full text on screen. Keys:
//        a   approve
//        t   approve marked as trap
//        r   reject
//        s   skip (don't decide; move on)
//        n   edit note (attaches to the next decision only)
//        u   undo skip / jump back one submission
//        esc back to prompt list
//        q   quit
//
// Why a TUI and not a script: triage rhythm is "read line, decide, next."
// Round-tripping through `bbg-admin prelaunch list/show/review` for every
// row costs three subprocesses and a context switch each. The TUI keeps
// the cursor in one place and the decisions in muscle memory.
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

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/christianreimer/bot-bot-goose/internal/db"
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
)

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

	// review screen
	subs            []db.PrelaunchSubmission
	subIndex        int
	currentPromptID uuid.UUID

	// note input (toggled with 'n')
	note     textinput.Model
	noteOpen bool

	// session stats
	approved int
	rejected int
	skipped  int

	// transient bottom line
	flash   string
	flashAt time.Time

	width, height int
	err           error
	quitting      bool
}

// --- runner -----------------------------------------------------------------

func runPrelaunchTUI(ctx context.Context, log *slog.Logger) error {
	fs := flag.NewFlagSet("prelaunch tui", flag.ExitOnError)
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
	decision string // "approved" | "rejected"
	err      error
}

func loadPromptsCmd(ctx context.Context, d *db.DB) tea.Cmd {
	return func() tea.Msg {
		ps, err := d.PrelaunchPromptCounts(ctx)
		return promptsLoadedMsg{prompts: ps, err: err}
	}
}

func loadSubsCmd(ctx context.Context, d *db.DB, promptID uuid.UUID) tea.Cmd {
	return func() tea.Msg {
		status := db.PrelaunchPending
		subs, err := d.ListPrelaunch(ctx, db.PrelaunchListOpts{
			Status: &status, PromptID: &promptID, Limit: 200,
		})
		return subsLoadedMsg{promptID: promptID, subs: subs, err: err}
	}
}

func approveCmd(ctx context.Context, d *db.DB, prelaunchID, reviewerID uuid.UUID, isTrap bool, note string) tea.Cmd {
	return func() tea.Msg {
		_, err := d.ApprovePrelaunch(ctx, prelaunchID, reviewerID, isTrap, note)
		return decisionDoneMsg{decision: "approved", err: err}
	}
}

func rejectCmd(ctx context.Context, d *db.DB, prelaunchID, reviewerID uuid.UUID, note string) tea.Cmd {
	return func() tea.Msg {
		err := d.RejectPrelaunch(ctx, prelaunchID, reviewerID, note)
		return decisionDoneMsg{decision: "rejected", err: err}
	}
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
		if len(msg.subs) == 0 {
			// Nothing pending for this prompt anymore — bounce back.
			m.screen = screenPrompts
			m.flash = "no pending submissions for that prompt"
			m.flashAt = time.Now()
			return m, loadPromptsCmd(m.ctx, m.db)
		}
		m.screen = screenReview
		return m, nil

	case decisionDoneMsg:
		if msg.err != nil {
			if errors.Is(msg.err, db.ErrPrelaunchAlreadyDecided) {
				m.flash = "already decided — skipping"
				m.flashAt = time.Now()
			} else {
				m.err = msg.err
				return m, tea.Quit
			}
		} else {
			switch msg.decision {
			case "approved":
				m.approved++
				m.flash = styleApprove.Render("approved")
			case "rejected":
				m.rejected++
				m.flash = styleReject.Render("rejected")
			}
			m.flashAt = time.Now()
		}
		// Clear the note (one-shot per decision) and advance.
		m.note.SetValue("")
		m.subIndex++
		if m.subIndex >= len(m.subs) {
			// Exhausted this prompt — go back to overview, refresh.
			m.screen = screenPrompts
			return m, loadPromptsCmd(m.ctx, m.db)
		}
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
		if m.screen == screenReview {
			m.screen = screenPrompts
			m.subs = nil
			m.subIndex = 0
			return m, loadPromptsCmd(m.ctx, m.db)
		}
		return m, nil
	}

	if m.screen == screenPrompts {
		return m.handleKeyPrompts(msg)
	}
	return m.handleKeyReview(msg)
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
	case "enter":
		visible := m.visiblePrompts()
		if m.promptCursor >= len(visible) {
			return m, nil
		}
		p := visible[m.promptCursor]
		if p.Pending == 0 {
			m.flash = "no pending submissions for that prompt"
			m.flashAt = time.Now()
			return m, nil
		}
		return m, loadSubsCmd(m.ctx, m.db, p.PromptID)
	}
	return m, nil
}

// visiblePrompts is the slice the prompt-list view + Enter handler operate
// on — m.prompts filtered by pendingOnly. Kept as a method (recomputed each
// access) instead of a cached field so any change to m.prompts or
// m.pendingOnly flows through without an explicit recompute step.
func (m tuiModel) visiblePrompts() []db.PrelaunchPromptRollup {
	if !m.pendingOnly {
		return m.prompts
	}
	out := make([]db.PrelaunchPromptRollup, 0, len(m.prompts))
	for _, p := range m.prompts {
		if p.Pending > 0 {
			out = append(out, p)
		}
	}
	return out
}

func (m tuiModel) handleKeyReview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.subIndex >= len(m.subs) {
		return m, nil
	}
	cur := m.subs[m.subIndex]
	note := m.note.Value()

	switch msg.String() {
	case "a":
		return m, approveCmd(m.ctx, m.db, cur.ID, m.reviewerID, false, note)
	case "t":
		return m, approveCmd(m.ctx, m.db, cur.ID, m.reviewerID, true, note)
	case "r":
		return m, rejectCmd(m.ctx, m.db, cur.ID, m.reviewerID, note)
	case "s":
		m.skipped++
		m.flash = styleSkip.Render("skipped")
		m.flashAt = time.Now()
		m.subIndex++
		if m.subIndex >= len(m.subs) {
			m.screen = screenPrompts
			return m, loadPromptsCmd(m.ctx, m.db)
		}
		return m, nil
	case "u":
		if m.subIndex > 0 {
			m.subIndex--
		}
		return m, nil
	case "n":
		m.noteOpen = true
		m.note.Focus()
		return m, textinput.Blink
	}
	return m, nil
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
	}
	b.WriteString("\n")
	b.WriteString(m.footer())
	return b.String()
}

func (m tuiModel) header() string {
	left := styleBrand.Render("🪿  bot bot goose") + styleMuted.Render(" · prelaunch review")
	right := fmt.Sprintf("%s %d  %s %d  %s %d",
		styleApprove.Render("approved"), m.approved,
		styleReject.Render("rejected"), m.rejected,
		styleSkip.Render("skipped"), m.skipped,
	)
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 2 {
		gap = 2
	}
	line := left + strings.Repeat(" ", gap) + right
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

	head := fmt.Sprintf("%*s %*s %*s %*s  %s",
		wPending, "PENDING", wIng, "INGESTED", wRej, "REJECTED", wPool, "POOL", "PROMPT")
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

	// Position indicator + active-filter hint
	filter := "all"
	if m.pendingOnly {
		filter = fmt.Sprintf("pending-only (p to show all %d)", len(m.prompts))
	}
	pos := styleMuted.Render(fmt.Sprintf("row %d of %d · filter: %s",
		m.promptCursor+1, total, filter))
	out = append([]string{pos}, out...)

	return strings.Join(out, "\n")
}

func (m tuiModel) viewReview() string {
	if m.subIndex >= len(m.subs) {
		return styleMuted.Render("no more pending submissions for this prompt")
	}
	cur := m.subs[m.subIndex]

	progress := fmt.Sprintf("submission %d of %d pending for this prompt",
		m.subIndex+1, len(m.subs))

	prompt := styleKick.Render("PROMPT") + "\n" +
		stylePrompt.Render(cur.PromptText)

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

	return strings.Join([]string{
		styleMuted.Render(progress),
		"",
		prompt,
		styleKick.Render("SUBMISSION"),
		subBox,
		metaLine,
		"",
		noteLine,
	}, "\n")
}

func (m tuiModel) footer() string {
	rule := styleRule.Render(strings.Repeat("─", maxInt(m.width, 40)))
	var keys string
	switch m.screen {
	case screenPrompts:
		keys = "↑↓ nav · f/b page · g/G top/bot · enter open · p toggle pending-only · r refresh · q quit"
	case screenReview:
		if m.noteOpen {
			keys = "type note · enter confirm · esc cancel"
		} else {
			keys = styleApprove.Render("a approve") + "  " +
				styleApprove.Render("t approve+trap") + "  " +
				styleReject.Render("r reject") + "  " +
				styleSkip.Render("s skip") + "  " +
				"n note  u undo  esc back  q quit"
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
