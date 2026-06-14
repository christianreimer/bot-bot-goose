package httpx

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/christianreimer/bot-bot-goose/internal/share"
	"github.com/christianreimer/bot-bot-goose/internal/users"
	"github.com/google/uuid"
)

const (
	prelaunchPoolCacheNS  = "prelaunch"
	prelaunchPoolCacheKey = "prelaunch:eligible:v1"
	prelaunchPoolCacheTTL = 60 * time.Second
)

// Prelaunch is the Phase-0 collection campaign surface (design doc §3). The
// deck is 21 under-supplied prompts at a time, laid out as a 3-column ×
// 7-row grid. Submissions land in pre_launch_submissions — they DO NOT
// flow into the live game or the composer's bandit. Manual promotion
// (today: a SQL one-liner) is the only path from "prelaunched" to
// "in-game decoy."
const (
	prelaunchDeckSize = 21

	// Per-device + per-IP rate limits, looser than the regular decoy
	// endpoint because a 21-card sitting is the intended use. 30/hour
	// gives the user comfortable room to finish a deck in one go; the
	// per-IP ceiling tolerates shared NAT.
	prelaunchSubmitDeviceCapacity = 30
	prelaunchSubmitDeviceRefill   = 30.0 // per hour

	prelaunchSubmitIPCapacity = 100
	prelaunchSubmitIPRefill   = 100.0 // per hour
)

// prelaunchCard is one entry rendered server-side into the deck grid.
// The prelaunch UI is fully server-rendered; the JS reads prompt IDs off
// data-prompt-id attributes, not from an embedded JSON state.
type prelaunchCard struct {
	ID   string
	Text string
}

func (s *Server) handlePrelaunch(w http.ResponseWriter, r *http.Request) {
	u := users.FromContext(r.Context())
	prompts, err := s.prelaunchDeckFor(r.Context(), u.ID)
	if err != nil {
		s.cfg.Logger.Error("prelaunch deck", "err", err)
		http.Error(w, "deck", http.StatusInternalServerError)
		return
	}

	deck := make([]prelaunchCard, 0, len(prompts))
	for _, p := range prompts {
		deck = append(deck, prelaunchCard{ID: p.ID.String(), Text: p.Text})
	}

	baseURL := s.requestBaseURL(r)
	s.renderHTML(w, http.StatusOK, "pages/prelaunch.html", map[string]any{
		"PuzzleNumber": int32(0), // satisfies the base-layout pad3 cosmetic
		"DeckSize":     len(prompts),
		"Deck":         deck, // server-rendered into the grid via {{ range .Deck }}
		"BaseURL":      baseURL,
		"ShareURL":     baseURL + "/prelaunch",
		"OGImageURL":   baseURL + "/prelaunch/og.png",
	})
}

// handlePrelaunchOG serves the static landing PNG. Content is generic, so the
// bytes are precomputed once and shared across requests — see PrelaunchOGBytes
// in internal/share/og.go.
func (s *Server) handlePrelaunchOG(w http.ResponseWriter, r *http.Request) {
	png, err := share.PrelaunchOGBytes()
	if err != nil {
		s.cfg.Logger.Error("render prelaunch og", "err", err)
		http.Error(w, "render", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("X-Robots-Tag", "noindex")
	_, _ = w.Write(png)
}

type prelaunchSubmitReq struct {
	PromptID string `json:"prompt_id"`
	Text     string `json:"text"`
}

// handlePrelaunchSubmit writes one prelaunched answer to pre_launch_submissions.
// Mirrors the shape of handleAPIDecoySubmit but targets the prelaunch table
// only — nothing here ever touches decoy_submissions.
func (s *Server) handlePrelaunchSubmit(w http.ResponseWriter, r *http.Request) {
	var body prelaunchSubmitReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "bad_body", "")
		return
	}
	if len(body.Text) < 4 || len(body.Text) > 280 {
		writeJSONErr(w, http.StatusBadRequest, "bad_text", "answer must be 4..280 chars")
		return
	}
	promptID, err := uuid.Parse(body.PromptID)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "bad_prompt_id", "")
		return
	}
	u := users.FromContext(r.Context())
	ctx := r.Context()

	// Rate limit. Both device and IP must allow. Looser than the regular
	// decoy endpoint because this is the campaign's burst path.
	if !s.allowPrelaunchSubmit(ctx, w, "prelaunch_submit:device:"+u.ID.String(), prelaunchSubmitDeviceCapacity, prelaunchSubmitDeviceRefill) {
		return
	}
	if !s.allowPrelaunchSubmit(ctx, w, "prelaunch_submit:ip:"+clientIP(r), prelaunchSubmitIPCapacity, prelaunchSubmitIPRefill) {
		return
	}

	// Insert. The partial unique index catches the rare TOCTOU race; we
	// surface that as already_submitted (with the existing row's id so the
	// client can recover) rather than a raw pg error.
	id, err := s.cfg.DB.InsertPrelaunchSubmission(ctx, u.ID, promptID, body.Text, clientIP(r))
	if err != nil {
		if errors.Is(err, db.ErrPrelaunchAlreadySubmitted) {
			existingID, _ := s.cfg.DB.PrelaunchSubmissionForUserAndPrompt(ctx, u.ID, promptID)
			writeJSON(w, http.StatusConflict, map[string]any{
				"code":         "already_submitted",
				"existing_id":  existingID,
			})
			return
		}
		s.cfg.Logger.Warn("prelaunch_submit insert", "err", err)
		writeJSONErr(w, http.StatusInternalServerError, "submit_failed", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// prelaunchDeckFor builds the per-user deck on top of a cached eligible-prompt
// pool. The pool ignores the per-user "already answered" filter (so it can
// be shared across all prelaunchers) and lives in ValKey for 60s — see plan
// §2.7 shared-candidate-pool option. The per-user filter is a small
// indexed SELECT in Postgres.
func (s *Server) prelaunchDeckFor(ctx context.Context, userID uuid.UUID) ([]db.PrelaunchPrompt, error) {
	pool, err := s.loadPrelaunchPool(ctx)
	if err != nil {
		return nil, err
	}
	submittedIDs, err := s.cfg.DB.PrelaunchSubmittedPromptIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	skip := make(map[uuid.UUID]struct{}, len(submittedIDs))
	for _, id := range submittedIDs {
		skip[id] = struct{}{}
	}
	eligible := make([]db.PrelaunchPrompt, 0, len(pool))
	for _, p := range pool {
		if _, blocked := skip[p.ID]; blocked {
			continue
		}
		eligible = append(eligible, p)
	}
	cryptoShuffle(eligible)
	if len(eligible) > prelaunchDeckSize {
		eligible = eligible[:prelaunchDeckSize]
	}
	return eligible, nil
}

func (s *Server) loadPrelaunchPool(ctx context.Context) ([]db.PrelaunchPrompt, error) {
	if s.cfg.Cache.Enabled() {
		if b, ok := s.cfg.Cache.Get(ctx, prelaunchPoolCacheNS, prelaunchPoolCacheKey); ok {
			var out []db.PrelaunchPrompt
			if err := json.Unmarshal(b, &out); err == nil {
				return out, nil
			}
		}
	}
	pool, err := s.cfg.DB.PrelaunchEligiblePool(ctx)
	if err != nil {
		return nil, err
	}
	if s.cfg.Cache.Enabled() {
		if b, err := json.Marshal(pool); err == nil {
			s.cfg.Cache.Set(ctx, prelaunchPoolCacheNS, prelaunchPoolCacheKey, b, prelaunchPoolCacheTTL)
		}
	}
	return pool, nil
}

// cryptoShuffle is a Fisher–Yates shuffle backed by crypto/rand so the deck
// order leaks nothing about server time or PID. Cost is fine at deck size
// (the entire eligible pool is ~hundreds at v1).
func cryptoShuffle(p []db.PrelaunchPrompt) {
	for i := len(p) - 1; i > 0; i-- {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic("rand: " + err.Error())
		}
		j := int(binary.BigEndian.Uint32(b[:])) % (i + 1)
		p[i], p[j] = p[j], p[i]
	}
}

func (s *Server) allowPrelaunchSubmit(ctx context.Context, w http.ResponseWriter, key string, capacity int, refillPerHour float64) bool {
	ok, retry, err := s.limiter.Allow(ctx, key, capacity, refillPerHour)
	if err != nil {
		s.cfg.Logger.Warn("ratelimit allow failed", "key", key, "err", err)
		return true // fail-open
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
