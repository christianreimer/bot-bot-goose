package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/christianreimer/bot-bot-goose/internal/share"
	"github.com/christianreimer/bot-bot-goose/internal/users"
	"github.com/google/uuid"
)

// Harvest is the Phase-0 collection campaign surface (design doc §3). The
// deck is 21 under-supplied prompts at a time, laid out as a 3-column ×
// 7-row grid. Submissions land in pre_launch_submissions — they DO NOT
// flow into the live game or the composer's bandit. Manual promotion
// (today: a SQL one-liner) is the only path from "harvested" to
// "in-game decoy."
const (
	harvestDeckSize = 21

	// Per-device + per-IP rate limits, looser than the regular decoy
	// endpoint because a 21-card sitting is the intended use. 30/hour
	// gives the user comfortable room to finish a deck in one go; the
	// per-IP ceiling tolerates shared NAT.
	harvestSubmitDeviceCapacity = 30
	harvestSubmitDeviceRefill   = 30.0 // per hour

	harvestSubmitIPCapacity = 100
	harvestSubmitIPRefill   = 100.0 // per hour
)

// harvestCard is one entry rendered server-side into the deck grid.
// The harvest UI is fully server-rendered; the JS reads prompt IDs off
// data-prompt-id attributes, not from an embedded JSON state.
type harvestCard struct {
	ID   string
	Text string
}

func (s *Server) handleHarvest(w http.ResponseWriter, r *http.Request) {
	u := users.FromContext(r.Context())
	prompts, err := s.cfg.DB.HarvestDeck(r.Context(), u.ID, harvestDeckSize)
	if err != nil {
		s.cfg.Logger.Error("harvest deck", "err", err)
		http.Error(w, "deck", http.StatusInternalServerError)
		return
	}

	deck := make([]harvestCard, 0, len(prompts))
	for _, p := range prompts {
		deck = append(deck, harvestCard{ID: p.ID.String(), Text: p.Text})
	}

	baseURL := s.requestBaseURL(r)
	s.renderHTML(w, http.StatusOK, "pages/harvest.html", map[string]any{
		"PuzzleNumber": int32(0), // satisfies the base-layout pad3 cosmetic
		"DeckSize":     len(prompts),
		"Deck":         deck, // server-rendered into the grid via {{ range .Deck }}
		"BaseURL":      baseURL,
		"ShareURL":     baseURL + "/harvest",
		"OGImageURL":   baseURL + "/harvest/og.png",
	})
}

// handleHarvestOG renders the static landing PNG. Cacheable forever; the
// content doesn't depend on the request.
func (s *Server) handleHarvestOG(w http.ResponseWriter, r *http.Request) {
	png, err := share.RenderHarvestOG()
	if err != nil {
		s.cfg.Logger.Error("render harvest og", "err", err)
		http.Error(w, "render", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("X-Robots-Tag", "noindex")
	_, _ = w.Write(png)
}

type harvestSubmitReq struct {
	PromptID string `json:"prompt_id"`
	Text     string `json:"text"`
}

// handleHarvestSubmit writes one harvested answer to pre_launch_submissions.
// Mirrors the shape of handleAPIDecoySubmit but targets the harvest table
// only — nothing here ever touches decoy_submissions.
func (s *Server) handleHarvestSubmit(w http.ResponseWriter, r *http.Request) {
	var body harvestSubmitReq
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
	if !s.allowHarvestSubmit(ctx, w, "harvest_submit:device:"+u.ID.String(), harvestSubmitDeviceCapacity, harvestSubmitDeviceRefill) {
		return
	}
	if !s.allowHarvestSubmit(ctx, w, "harvest_submit:ip:"+clientIP(r), harvestSubmitIPCapacity, harvestSubmitIPRefill) {
		return
	}

	// Insert. The partial unique index catches the rare TOCTOU race; we
	// surface that as already_submitted (with the existing row's id so the
	// client can recover) rather than a raw pg error.
	id, err := s.cfg.DB.InsertHarvestSubmission(ctx, u.ID, promptID, body.Text, clientIP(r))
	if err != nil {
		if errors.Is(err, db.ErrHarvestAlreadySubmitted) {
			existingID, _ := s.cfg.DB.HarvestSubmissionForUserAndPrompt(ctx, u.ID, promptID)
			writeJSON(w, http.StatusConflict, map[string]any{
				"code":         "already_submitted",
				"existing_id":  existingID,
			})
			return
		}
		s.cfg.Logger.Warn("harvest_submit insert", "err", err)
		writeJSONErr(w, http.StatusInternalServerError, "submit_failed", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) allowHarvestSubmit(ctx context.Context, w http.ResponseWriter, key string, capacity int, refillPerHour float64) bool {
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
