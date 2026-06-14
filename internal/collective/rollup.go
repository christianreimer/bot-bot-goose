// Package collective owns the "yesterday, humans caught X%" stat.
//
// Computed once per night by the rollup, frozen into daily_collective_stats,
// then read by the result page and the share card. Holding it in its own
// row (not on daily_puzzles, not as a live query) makes it stable for the
// day, identical across players, and screenshot-proof — which is the whole
// point of the surface: a flat, dry rallying number, not a live ticker.
package collective

import (
	"context"

	"github.com/christianreimer/bot-bot-goose/internal/cache"
	"github.com/christianreimer/bot-bot-goose/internal/db"
)

// MinPlaysFloor is the minimum completed-plays count a puzzle needs before
// its collective catch rate is considered meaningful enough to publish.
// Below the floor the read path returns no stat at all (renders nothing),
// avoiding "humans caught 100% (3 plays)" on early days.
const MinPlaysFloor = 20

// Rollup computes the collective catch rate for the most recent completed
// puzzle and freezes it. Idempotent: re-running the rollup overwrites the
// existing row. Returns (false, nil) when there's no qualifying prior
// puzzle yet (e.g., day 1 of the game) — the caller logs and moves on.
//
// On a successful write, the cached "latest stat" key is evicted so the
// next reader sees the new puzzle's number within seconds, not after the
// 10-minute TTL window. cache may be nil — the rollup still works, the
// next read just waits for the natural TTL expiry.
func Rollup(ctx context.Context, d *db.DB, c *cache.Cache) (bool, error) {
	s, err := d.ComputePreviousPuzzleCatchRate(ctx)
	if err != nil {
		if db.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if s.TotalPlays == 0 {
		// Puzzle existed but no one finished it. Nothing to freeze.
		return false, nil
	}
	if err := d.UpsertCollectiveStat(ctx, s); err != nil {
		return false, err
	}
	InvalidateLatest(ctx, c)
	return true, nil
}
