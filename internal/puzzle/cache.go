package puzzle

import (
	"context"
	"encoding/json"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/cache"
	"github.com/christianreimer/bot-bot-goose/internal/db"
	"github.com/google/uuid"
)

// RoundsBundle is one puzzle's rounds + each round's canonical answers,
// the smallest amount of state the play loop needs to verify a guess.
// The same payload sits behind every guess/hint/realest call —
// loadVerified in internal/httpx/play_handlers.go runs Rounds +
// AnswersForRound on every request. Caching the bundle is the single
// highest-leverage read cache for launch traffic. See plan §2.6.
type RoundsBundle struct {
	Rounds  []db.PuzzleRound
	// Answers is keyed by round id (uuid string) so JSON round-trip is
	// stable. Use AnswersFor to fetch by uuid.UUID.
	Answers map[string][]db.Answer
}

// AnswersFor returns the canonical answer list for a round id.
func (b *RoundsBundle) AnswersFor(roundID uuid.UUID) []db.Answer {
	if b == nil || b.Answers == nil {
		return nil
	}
	return b.Answers[roundID.String()]
}

const (
	roundsCacheNS  = "rounds"
	roundsCacheTTL = time.Hour
)

func roundsCacheKey(puzzleID uuid.UUID) string {
	return "rounds:" + puzzleID.String()
}

// LoadRoundsBundle returns the puzzle's rounds + per-round canonical
// answers, going through the Valkey cache when available. The freshly
// assembled bundle is written back to the cache on a Postgres-served
// path so the next request hits the fast path.
//
// IMPORTANT: this cache holds canonical answer text including the bot
// answer. ValKey access controls matter — the launch-capacity plan
// requires the deployment to bind ValKey to the VPC private network
// and authenticate clients with a password.
func LoadRoundsBundle(ctx context.Context, d *db.DB, c *cache.Cache, puzzleID uuid.UUID) (*RoundsBundle, error) {
	key := roundsCacheKey(puzzleID)
	if c.Enabled() {
		if b, ok := c.Get(ctx, roundsCacheNS, key); ok {
			var bundle RoundsBundle
			if err := json.Unmarshal(b, &bundle); err == nil {
				return &bundle, nil
			}
			// Bad blob → fall through to Postgres; the upcoming Set overwrites it.
		}
	}

	rounds, err := d.Rounds(ctx, puzzleID)
	if err != nil {
		return nil, err
	}
	answers := make(map[string][]db.Answer, len(rounds))
	for _, r := range rounds {
		as, err := d.AnswersForRound(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		answers[r.ID.String()] = as
	}
	bundle := &RoundsBundle{Rounds: rounds, Answers: answers}
	if c.Enabled() {
		if b, err := json.Marshal(bundle); err == nil {
			c.Set(ctx, roundsCacheNS, key, b, roundsCacheTTL)
		}
	}
	return bundle, nil
}

// InvalidateRoundsBundle evicts the cached bundle for puzzleID. Callers:
// the composer (puzzle-build) after a successful compose, and any admin
// tool that mutates puzzle_rounds or puzzle_round_answers.
func InvalidateRoundsBundle(ctx context.Context, c *cache.Cache, puzzleID uuid.UUID) {
	if !c.Enabled() {
		return
	}
	c.Del(ctx, roundsCacheNS, roundsCacheKey(puzzleID))
}
