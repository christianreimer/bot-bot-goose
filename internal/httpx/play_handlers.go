package httpx

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"html/template"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/christianreimer/bot-bot-goose/internal/game"
	"github.com/christianreimer/bot-bot-goose/internal/play"
	"github.com/christianreimer/bot-bot-goose/internal/share"
	"github.com/christianreimer/bot-bot-goose/internal/users"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func randInt(n int) int {
	if n <= 0 {
		return 0
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("rand: " + err.Error())
	}
	return int(binary.BigEndian.Uint32(b[:])) % n
}

// ---------------------------------------------------------------------------
// Page handlers
// ---------------------------------------------------------------------------

// playPageState is what we embed in <script id="bbg-state"> for the JS to
// hydrate. It MUST NOT contain any field that reveals which answer is the
// target — that's the whole point of server authority.
type playPageState struct {
	PuzzleNumber int32           `json:"puzzle_number"`
	Mode         string          `json:"mode"`
	PlayID       string          `json:"play_id"`
	Round        *clientRound    `json:"round"`
	Outcomes     []game.Outcome  `json:"outcomes"`
	Completed    bool            `json:"completed"`
	Streak       int             `json:"streak"`
	BaseURL      string          `json:"base_url"`
}

// clientRound is the per-round state the player sees. No labels.
type clientRound struct {
	Index       int16    `json:"index"`
	Prompt      string   `json:"prompt"`
	Answers     []string `json:"answers"` // in slot order
	Token       string   `json:"token"`
	HintUsed    bool     `json:"hint_used"`
	RemovedSlot *int16   `json:"removed_slot"`
	TargetLabel string   `json:"target_label"` // "bot" or "human" — what to hunt
}

func (s *Server) handlePlayLanding(w http.ResponseWriter, r *http.Request) {
	puzzle, err := s.cfg.DB.LatestPuzzle(r.Context(), time.Now().UTC())
	if err != nil {
		if db.IsNotFound(err) {
			s.renderHTML(w, http.StatusOK, "pages/no_puzzle.html", map[string]any{"BaseURL": s.cfg.BaseURL})
			return
		}
		s.cfg.Logger.Error("latest puzzle", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	s.renderPlayPage(w, r, puzzle)
}

func (s *Server) handlePlaySpecific(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(chi.URLParam(r, "n"))
	if err != nil || n <= 0 {
		http.NotFound(w, r)
		return
	}
	puzzle, err := s.cfg.DB.PuzzleByNumber(r.Context(), int32(n))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.renderPlayPage(w, r, puzzle)
}

func (s *Server) renderPlayPage(w http.ResponseWriter, r *http.Request, puzzle *db.DailyPuzzle) {
	u := users.FromContext(r.Context())
	// Derive the base URL from the request so share artifacts carry the URL
	// the player actually browsed (e.g., the ngrok host), not BBG_BASE_URL.
	baseURL := s.requestBaseURL(r)
	state, err := s.composePlayState(r.Context(), u, puzzle, baseURL)
	if err != nil {
		s.cfg.Logger.Error("compose play state", "err", err)
		http.Error(w, "play state", http.StatusInternalServerError)
		return
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		http.Error(w, "encode state", http.StatusInternalServerError)
		return
	}

	// template.JS tells html/template the value is already safe to embed
	// verbatim. Without this the engine treats stateJSON as a JS string
	// literal and wraps it in quotes again — JSON.parse then returns the
	// inner string instead of the object.
	embedded := template.JS(stateJSON)

	if state.Completed {
		// Pre-build the share grid + scorecard. The share button uses the
		// public /r/<short> URL so chat clients unfurl into a card with the
		// grid OG image — see internal/httpx/result_share.go.
		outcomes := state.Outcomes
		shareURL := ""
		if u != nil {
			// Resolve the user's play row for this puzzle to derive the short id.
			if p, err := s.cfg.DB.PlayByUserAndPuzzle(r.Context(), u.ID, puzzle.ID); err == nil {
				shareURL = baseURL + "/r/" + share.PlayShortID(p.ID)
			}
		}
		card := share.Card(puzzle.PuzzleNumber, outcomes, game.Mode(puzzle.Mode), state.Streak, baseURL)

		// Decoy solicitation: pick a prompt to ask the player to write for.
		// Soft-fail if none is available — the page still renders without
		// the contribution form.
		var nextPromptID, nextPromptText string
		var pidUUID uuid.UUID
		if pid, ptext, err := s.cfg.DB.NextSolicitPrompt(r.Context(), puzzle.PuzzleNumber); err == nil {
			nextPromptID = pid.String()
			nextPromptText = ptext
			pidUUID = pid
		}

		// If the user has already planted a decoy for this prompt, hide the
		// form and surface a link back to /me. Design doc §4: one scored
		// decoy per prompt per user — show that state instead of letting
		// them submit and hit a 409.
		var existingDecoyShareURL, existingDecoyStatus string
		if u != nil && pidUUID != uuid.Nil {
			if ex, err := s.cfg.DB.DecoyForUserAndPrompt(r.Context(), u.ID, pidUUID); err == nil {
				existingDecoyShareURL = "/d/" + share.DecoyShortID(ex.ID)
				existingDecoyStatus = ex.Status
			}
		}

		// Anonymous users see a soft sign-in CTA on the result page —
		// design §12 picks streak + leaderboard as the gating prompts.
		signedIn := u != nil && u.Email != nil && *u.Email != ""

		s.renderHTML(w, http.StatusOK, "pages/result.html", map[string]any{
			"PuzzleNumber":          puzzle.PuzzleNumber,
			"Mode":                  string(puzzle.Mode),
			"Outcomes":              outcomes,
			"Grid":                  share.Grid(outcomes),
			"ScorePct":              game.ScorePct(outcomes),
			"Streak":                state.Streak,
			"ShareCard":             card,
			"ShareURL":              shareURL,
			"BaseURL":               baseURL,
			"State":                 embedded,
			"NextPromptID":          nextPromptID,
			"NextPromptText":        nextPromptText,
			"ExistingDecoyShareURL": existingDecoyShareURL,
			"ExistingDecoyStatus":   existingDecoyStatus,
			"SignedIn":              signedIn,
		})
		return
	}
	signedIn := u != nil && u.Email != nil && *u.Email != ""
	s.renderHTML(w, http.StatusOK, "pages/play.html", map[string]any{
		"PuzzleNumber": puzzle.PuzzleNumber,
		"Mode":         string(puzzle.Mode),
		"State":        embedded,
		"BaseURL":      baseURL,
		"SignedIn":     signedIn,
	})
}

func (s *Server) handlePlayResult(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(chi.URLParam(r, "n"))
	if err != nil || n <= 0 {
		http.NotFound(w, r)
		return
	}
	puzzle, err := s.cfg.DB.PuzzleByNumber(r.Context(), int32(n))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.renderPlayPage(w, r, puzzle)
}

// composePlayState builds the page-load state. It will create a play row +
// the current round (or return the in-progress one) so the page can render
// without any further API round-trip.
func (s *Server) composePlayState(ctx context.Context, u *db.User, puzzle *db.DailyPuzzle, baseURL string) (*playPageState, error) {
	// Get or create play row.
	hmacSecret := play.NewSecret()
	playRow, _, err := s.cfg.DB.CreateOrGetPlay(ctx, u.ID, puzzle.ID, hmacSecret)
	if err != nil {
		return nil, err
	}
	rounds, err := s.cfg.DB.Rounds(ctx, puzzle.ID)
	if err != nil {
		return nil, err
	}
	if len(rounds) == 0 {
		return nil, errors.New("puzzle has no rounds")
	}

	// Read outcomes-so-far.
	outcomes, err := s.cfg.DB.LastOutcomes(ctx, playRow.ID)
	if err != nil {
		return nil, err
	}
	streak, _ := s.cfg.DB.StreakFor(ctx, u.ID)

	state := &playPageState{
		PuzzleNumber: puzzle.PuzzleNumber,
		Mode:         string(puzzle.Mode),
		PlayID:       playRow.ID.String(),
		Outcomes:     outcomes,
		Streak:       streak,
		BaseURL:      baseURL,
	}
	if playRow.CompletedAt != nil {
		state.Completed = true
		return state, nil
	}

	currentIdx := int16(len(outcomes))
	if currentIdx >= int16(len(rounds)) {
		// All rounds played but completed_at not set — should be repaired by
		// the guess handler. Treat as complete.
		state.Completed = true
		return state, nil
	}

	cr, err := s.openClientRound(ctx, playRow, &rounds[currentIdx])
	if err != nil {
		return nil, err
	}
	state.Round = cr
	return state, nil
}

// openClientRound creates-or-returns the play_round at idx and renders the
// client-facing slot order. Issues a fresh token with the current time.
func (s *Server) openClientRound(ctx context.Context, playRow *db.Play, round *db.PuzzleRound) (*clientRound, error) {
	canonical, err := s.cfg.DB.AnswersForRound(ctx, round.ID)
	if err != nil {
		return nil, err
	}
	if len(canonical) == 0 {
		return nil, errors.New("round has no answers")
	}

	// Generate fresh permutation iff this is the first time we open this round.
	// Otherwise reuse the stored one (so reloads don't re-shuffle).
	perm := play.NewPermutation(len(canonical))
	pr, _, err := s.cfg.DB.UpsertPlayRound(ctx, playRow.ID, round.RoundIndex, perm)
	if err != nil {
		return nil, err
	}
	// Use the stored permutation (might be the newly-inserted one or the existing).
	perm = pr.SlotPermutation

	shuffled := make([]string, len(canonical))
	for slot, ordinal := range perm {
		shuffled[slot] = canonical[ordinal].AnswerText
	}

	target := "bot"
	if round.TargetKind == "human" {
		target = "human"
	}
	token := play.Issue(playRow.HMACSecret, playRow.ID, round.RoundIndex, perm, time.Now())
	return &clientRound{
		Index:       round.RoundIndex,
		Prompt:      round.PromptText,
		Answers:     shuffled,
		Token:       token,
		HintUsed:    pr.HintUsed,
		RemovedSlot: pr.RemovedSlot,
		TargetLabel: target,
	}, nil
}

// ---------------------------------------------------------------------------
// API handlers
// ---------------------------------------------------------------------------

type startResp struct {
	PlayID   string       `json:"play_id"`
	Round    *clientRound `json:"round"`
	Streak   int          `json:"streak"`
	Outcomes []game.Outcome `json:"outcomes"`
}

func (s *Server) handleAPIPlayStart(w http.ResponseWriter, r *http.Request) {
	u := users.FromContext(r.Context())
	puzzle, err := s.cfg.DB.LatestPuzzle(r.Context(), time.Now().UTC())
	if err != nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "no_puzzle", "no puzzle ready")
		return
	}
	hmacSecret := play.NewSecret()
	playRow, _, err := s.cfg.DB.CreateOrGetPlay(r.Context(), u.ID, puzzle.ID, hmacSecret)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "db", err.Error())
		return
	}
	rounds, err := s.cfg.DB.Rounds(r.Context(), puzzle.ID)
	if err != nil || len(rounds) == 0 {
		writeJSONErr(w, http.StatusInternalServerError, "rounds", "no rounds")
		return
	}
	outs, _ := s.cfg.DB.LastOutcomes(r.Context(), playRow.ID)
	if int(len(outs)) >= len(rounds) {
		writeJSON(w, http.StatusOK, map[string]any{
			"play_id":   playRow.ID,
			"completed": true,
			"outcomes":  outs,
		})
		return
	}
	cr, err := s.openClientRound(r.Context(), playRow, &rounds[len(outs)])
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "open_round", err.Error())
		return
	}
	streak, _ := s.cfg.DB.StreakFor(r.Context(), u.ID)
	writeJSON(w, http.StatusOK, startResp{
		PlayID:   playRow.ID.String(),
		Round:    cr,
		Streak:   streak,
		Outcomes: outs,
	})
}

type hintReq struct {
	Token string `json:"token"`
}

type hintResp struct {
	RemovedSlot int16  `json:"removed_slot"`
	Token       string `json:"token"`
}

func (s *Server) handleAPIHint(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(chi.URLParam(r, "n"))
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "bad_round", "")
		return
	}
	var body hintReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "bad_body", "")
		return
	}
	u := users.FromContext(r.Context())

	ctx := r.Context()
	now := time.Now()

	// Step 1: identify the play behind this token (the token IS the play_id).
	// We need the play's HMAC secret to verify.
	playRow, prRow, qRound, perm, canonical, herr := s.loadVerified(ctx, u, body.Token, int16(n), now)
	if herr != nil {
		writeJSONErr(w, herr.status, herr.code, herr.msg)
		return
	}
	if prRow.HintUsed {
		writeJSONErr(w, http.StatusConflict, "hint_used", "")
		return
	}
	if prRow.CommittedAt != nil {
		writeJSONErr(w, http.StatusConflict, "committed", "")
		return
	}

	target := targetContentKind(qRound.TargetKind)
	var wrong []int16
	for slot, ordinal := range perm {
		if string(canonical[ordinal].ContentKind) != target {
			wrong = append(wrong, int16(slot))
		}
	}
	if len(wrong) == 0 {
		writeJSONErr(w, http.StatusInternalServerError, "no_wrong", "")
		return
	}
	pick := wrong[randInt(len(wrong))]
	if err := s.cfg.DB.MarkHint(ctx, prRow.ID, pick); err != nil {
		writeJSONErr(w, http.StatusConflict, "hint_failed", err.Error())
		return
	}
	tok := play.Issue(playRow.HMACSecret, playRow.ID, int16(n), perm, now)
	writeJSON(w, http.StatusOK, hintResp{RemovedSlot: pick, Token: tok})
}

type guessReq struct {
	Token string `json:"token"`
	Slot  int16  `json:"slot"`
}

type guessResp struct {
	Outcome      game.Outcome   `json:"outcome"`
	TargetSlots  []int16        `json:"target_slots"`
	NextRound    *clientRound   `json:"next_round"` // nil when complete
	Completed    bool           `json:"completed"`
	Outcomes     []game.Outcome `json:"outcomes"`
	ScorePct     int            `json:"score_pct,omitempty"`
	PuzzleNumber int32          `json:"puzzle_number"`
}

func (s *Server) handleAPIGuess(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(chi.URLParam(r, "n"))
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "bad_round", "")
		return
	}
	var body guessReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "bad_body", "")
		return
	}
	if body.Slot < 0 || body.Slot > 31 {
		writeJSONErr(w, http.StatusBadRequest, "bad_slot", "")
		return
	}
	u := users.FromContext(r.Context())
	ctx := r.Context()
	now := time.Now()

	playRow, prRow, qRound, perm, canonical, herr := s.loadVerified(ctx, u, body.Token, int16(n), now)
	if herr != nil {
		writeJSONErr(w, herr.status, herr.code, herr.msg)
		return
	}
	if prRow.CommittedAt != nil {
		writeJSONErr(w, http.StatusConflict, "committed", "")
		return
	}

	// Enforce in-order play.
	if ok, err := s.cfg.DB.PriorRoundsAllCommitted(ctx, playRow.ID, int16(n)); err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "db", err.Error())
		return
	} else if !ok {
		writeJSONErr(w, http.StatusConflict, "out_of_order", "")
		return
	}

	if int(body.Slot) >= len(perm) {
		writeJSONErr(w, http.StatusBadRequest, "slot_oob", "")
		return
	}
	if prRow.RemovedSlot != nil && *prRow.RemovedSlot == body.Slot {
		writeJSONErr(w, http.StatusBadRequest, "slot_removed", "")
		return
	}

	target := targetContentKind(qRound.TargetKind)
	// Compute target slots (1+) for the reveal.
	var targetSlots []int16
	for slot, ordinal := range perm {
		if string(canonical[ordinal].ContentKind) == target {
			targetSlots = append(targetSlots, int16(slot))
		}
	}
	chosenOrdinal := perm[body.Slot]
	correct := string(canonical[chosenOrdinal].ContentKind) == target
	outcome := game.Resolve(correct, prRow.HintUsed)

	if err := s.cfg.DB.CommitGuess(ctx, prRow.ID, body.Slot, db.Outcome(outcome)); err != nil {
		writeJSONErr(w, http.StatusConflict, "commit_failed", err.Error())
		return
	}

	// Flag suspicious-fast guesses (no hard reject).
	if now.Sub(prRow.StartedAt) < play.SuspiciousFastGuessFloor {
		s.cfg.Logger.Info("suspicious_fast_guess", "play", playRow.ID, "round", n, "delta_ms", now.Sub(prRow.StartedAt).Milliseconds())
	}

	rounds, err := s.cfg.DB.Rounds(ctx, playRow.DailyPuzzleID)
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "rounds", err.Error())
		return
	}

	outs, _ := s.cfg.DB.LastOutcomes(ctx, playRow.ID)

	resp := guessResp{
		Outcome:      outcome,
		TargetSlots:  targetSlots,
		Outcomes:     outs,
		PuzzleNumber: 0,
	}
	if int(len(outs)) >= len(rounds) {
		// Final round — complete the play.
		pct := game.ScorePct(outs)
		if err := s.cfg.DB.CompletePlay(ctx, playRow.ID, int16(pct), now); err != nil {
			s.cfg.Logger.Warn("complete_play", "err", err)
		}
		resp.Completed = true
		resp.ScorePct = pct
	} else {
		// Open the next round.
		next, err := s.openClientRound(ctx, playRow, &rounds[len(outs)])
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "next_round", err.Error())
			return
		}
		resp.NextRound = next
	}

	// Carry puzzle number for client-side share link assembly.
	if pz, err := s.cfg.DB.PuzzleByID(ctx, playRow.DailyPuzzleID); err == nil {
		resp.PuzzleNumber = pz.PuzzleNumber
	}

	writeJSON(w, http.StatusOK, resp)
}

// targetContentKind maps a puzzle_rounds.target_kind ('bot'|'human') to
// the matching puzzle_round_answers.content_kind ('bot'|'decoy').
func targetContentKind(target string) string {
	if target == "human" {
		return "decoy"
	}
	return "bot"
}

// ---------------------------------------------------------------------------
// Verification helper used by both /hint and /guess.
// ---------------------------------------------------------------------------

type httpErr struct {
	status int
	code   string
	msg    string
}

// loadVerified parses the play token, looks up the play (verifying user
// ownership), reloads the round + canonical answers, and verifies the
// token's perm hash matches what's in the DB. Any failure returns a
// uniformly-shaped httpErr so the handler can early-return cleanly.
func (s *Server) loadVerified(ctx context.Context, u *db.User, rawToken string, roundIdx int16, now time.Time) (*db.Play, *db.PlayRound, *db.PuzzleRound, []int16, []db.Answer, *httpErr) {
	// Token's play_id is needed before we can read its secret. Parse twice:
	// once raw, once verified. Cheap because the first parse only splits.
	raw := struct {
		PlayID uuid.UUID
	}{}
	// Use a no-op verify (zero secret) to pull the play_id out — the real
	// verify happens below. We re-implement the structural parse here to
	// avoid exposing a "trust the token's claims" API on the play package.
	parts := splitFirstFive(rawToken)
	if parts == nil {
		return nil, nil, nil, nil, nil, &httpErr{http.StatusBadRequest, "bad_token", ""}
	}
	pid, err := uuid.Parse(parts[0])
	if err != nil {
		return nil, nil, nil, nil, nil, &httpErr{http.StatusBadRequest, "bad_token_play_id", ""}
	}
	raw.PlayID = pid

	playRow, err := s.cfg.DB.PlayByID(ctx, raw.PlayID)
	if err != nil {
		return nil, nil, nil, nil, nil, &httpErr{http.StatusBadRequest, "play_missing", ""}
	}
	if playRow.UserID != u.ID {
		// Cross-play submission attempt.
		return nil, nil, nil, nil, nil, &httpErr{http.StatusForbidden, "play_owner_mismatch", ""}
	}

	tok, err := play.Verify(playRow.HMACSecret, rawToken, now)
	if err != nil {
		return nil, nil, nil, nil, nil, &httpErr{http.StatusUnauthorized, "token_invalid", err.Error()}
	}
	if tok.RoundIndex != roundIdx {
		return nil, nil, nil, nil, nil, &httpErr{http.StatusBadRequest, "round_mismatch", ""}
	}

	pr, err := s.cfg.DB.PlayRoundByIndex(ctx, playRow.ID, roundIdx)
	if err != nil {
		return nil, nil, nil, nil, nil, &httpErr{http.StatusBadRequest, "round_missing", ""}
	}
	wantHash := play.PermutationHash(pr.SlotPermutation)
	if !bytesEqual(wantHash, tok.PermHash) {
		// Stored perm changed under the token — should never happen, but
		// fail closed.
		return nil, nil, nil, nil, nil, &httpErr{http.StatusConflict, "perm_drift", ""}
	}

	// Resolve canonical answers.
	rounds, err := s.cfg.DB.Rounds(ctx, playRow.DailyPuzzleID)
	if err != nil || int(roundIdx) >= len(rounds) {
		return nil, nil, nil, nil, nil, &httpErr{http.StatusInternalServerError, "rounds_missing", ""}
	}
	canonical, err := s.cfg.DB.AnswersForRound(ctx, rounds[roundIdx].ID)
	if err != nil {
		return nil, nil, nil, nil, nil, &httpErr{http.StatusInternalServerError, "answers_missing", ""}
	}
	qRound := rounds[roundIdx]
	return playRow, pr, &qRound, pr.SlotPermutation, canonical, nil
}

func splitFirstFive(s string) []string {
	out := make([]string, 0, 5)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
			if len(out) == 4 {
				out = append(out, s[start:])
				return out
			}
		}
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Decoy submission (Phase 2 — see plan §7 build order step 7)
// ---------------------------------------------------------------------------
//
// Two layers of defense beyond the schema's unique constraint:
//
//  1. Per-device + per-IP rate limit. Device cookies can be rotated, so the
//     IP bucket catches scripted attempts behind a single egress; the device
//     bucket catches a single client retrying in a tight loop.
//  2. An explicit existence check before the insert so legitimate users get
//     a friendly "already_submitted" response with their existing decoy's
//     status, not a generic 409 from a unique-violation error.
//
// Per design doc §4: "one scored decoy per prompt per user — no
// brute-forcing." The rule lives in the schema; this handler just makes
// the user-facing behavior clear.

type decoySubmitReq struct {
	PromptID string `json:"prompt_id"`
	Text     string `json:"text"`
}

const (
	// Per-device: 5 submissions per hour, refilled at 5/hr. A normal user
	// will submit at most once per puzzle (and there's one puzzle per day),
	// so this is generous enough to never bite them but tight enough to
	// catch a scripted client.
	decoySubmitDeviceCapacity = 5
	decoySubmitDeviceRefill   = 5.0 // per hour

	// Per-IP: looser. Shared networks (offices, coffee shops, IPv4 NAT)
	// must not lock everyone out because one user is enthusiastic.
	decoySubmitIPCapacity = 20
	decoySubmitIPRefill   = 20.0 // per hour
)

func (s *Server) handleAPIDecoySubmit(w http.ResponseWriter, r *http.Request) {
	var body decoySubmitReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "bad_body", "")
		return
	}
	if len(body.Text) < 4 || len(body.Text) > 280 {
		writeJSONErr(w, http.StatusBadRequest, "bad_text", "decoy text must be 4..280 chars")
		return
	}
	promptID, err := uuid.Parse(body.PromptID)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "bad_prompt_id", "")
		return
	}
	u := users.FromContext(r.Context())
	ctx := r.Context()

	// 1. Rate limit. Check device first (cheaper to hit, narrower target);
	//    then IP. Both must allow.
	if !s.allowDecoySubmit(ctx, w, "decoy_submit:device:"+u.ID.String(), decoySubmitDeviceCapacity, decoySubmitDeviceRefill) {
		return
	}
	if !s.allowDecoySubmit(ctx, w, "decoy_submit:ip:"+clientIP(r), decoySubmitIPCapacity, decoySubmitIPRefill) {
		return
	}

	// 2. Existence check. If they already have a non-deleted decoy for this
	//    prompt, tell them — and include the existing row's status + short id
	//    so the client can link back to /me or /d/<short>.
	if existing, err := s.cfg.DB.DecoyForUserAndPrompt(ctx, u.ID, promptID); err == nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"code": "already_submitted",
			"existing": map[string]string{
				"text":      existing.Text,
				"status":    existing.Status,
				"share_url": "/d/" + share.DecoyShortID(existing.ID),
			},
		})
		return
	} else if !db.IsNotFound(err) {
		writeJSONErr(w, http.StatusInternalServerError, "db", err.Error())
		return
	}

	// 3. Insert. The unique constraint is the last line of defense — if a
	//    racing concurrent submission squeezed in between our check and now,
	//    we still surface a clean error.
	id, err := s.cfg.DB.SubmitDecoy(ctx, u.ID, promptID, body.Text)
	if err != nil {
		// Re-check in case of a TOCTOU race so we still return the
		// already_submitted code rather than a raw DB error.
		if existing, e2 := s.cfg.DB.DecoyForUserAndPrompt(ctx, u.ID, promptID); e2 == nil {
			writeJSON(w, http.StatusConflict, map[string]any{
				"code": "already_submitted",
				"existing": map[string]string{
					"text":      existing.Text,
					"status":    existing.Status,
					"share_url": "/d/" + share.DecoyShortID(existing.ID),
				},
			})
			return
		}
		// Log the raw error for diagnostics; return a generic code to the
		// client so a leaked pg message can't be reflected back through
		// the API.
		s.cfg.Logger.Warn("decoy_submit insert", "err", err)
		writeJSONErr(w, http.StatusConflict, "submit_failed", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":        id,
		"share_url": "/d/" + share.DecoyShortID(id),
	})
}

func (s *Server) allowDecoySubmit(ctx context.Context, w http.ResponseWriter, key string, capacity int, refillPerHour float64) bool {
	ok, retry, err := s.limiter.Allow(ctx, key, capacity, refillPerHour)
	if err != nil {
		s.cfg.Logger.Warn("ratelimit allow failed", "key", key, "err", err)
		return true // fail-open: never block submissions due to limiter problems
	}
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"code":            "rate_limited",
			"retry_after_sec": int(retry.Seconds()),
		})
		return false
	}
	return true
}

// clientIP extracts the request's IP, honoring proxy headers. chi's
// middleware.RealIP already sets r.RemoteAddr to the X-Forwarded-For
// origin behind a trusted proxy. We then split host:port using the
// stdlib so IPv6 (which is bracketed by Go's HTTP server, e.g.
// `[::1]:54321`) survives intact. A previous naive "strip after last
// colon" left brackets on IPv6 addresses, which then failed
// netip.ParseAddr on the magic-link insert path.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// No port suffix — return the bare addr, stripping any brackets
		// in case the proxy already gave us `[::1]`.
		return strings.Trim(r.RemoteAddr, "[]")
	}
	return host
}
