package collective

import (
	"context"
	"encoding/json"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/cache"
	"github.com/christianreimer/bot-bot-goose/internal/db"
)

// Cache key, namespace, and TTL for the "yesterday humans caught X%" stat.
// Exported so the rollup, the http handlers, and any future cli read the
// same names.
const (
	cacheNS  = "collective"
	cacheKey = "collective:latest"
	cacheTTL = 10 * time.Minute
)

// Latest returns the most recently-frozen collective stat above the
// MinPlaysFloor, going through the Valkey cache when one is configured.
// Mirrors db.LatestCollectiveStat semantics: ok=false means no qualifying
// stat yet (e.g. day 1), render nothing in that case.
//
// A nil cache or a cache miss falls through to Postgres; a successful
// Postgres read populates the cache for the next call.
func Latest(ctx context.Context, d *db.DB, c *cache.Cache) (db.CollectiveStat, bool, error) {
	if c.Enabled() {
		if b, ok := c.Get(ctx, cacheNS, cacheKey); ok {
			var cs db.CollectiveStat
			if err := json.Unmarshal(b, &cs); err == nil {
				return cs, true, nil
			}
			// Bad blob — pretend it was a miss; the upcoming Set overwrites it.
		}
	}
	cs, ok, err := d.LatestCollectiveStat(ctx, MinPlaysFloor)
	if err != nil || !ok {
		return cs, ok, err
	}
	if c.Enabled() {
		if b, err := json.Marshal(cs); err == nil {
			c.Set(ctx, cacheNS, cacheKey, b, cacheTTL)
		}
	}
	return cs, true, nil
}

// InvalidateLatest evicts the cached stat. The nightly Rollup calls this
// after a successful write so the next reader picks up the new puzzle's
// number without waiting on the TTL.
func InvalidateLatest(ctx context.Context, c *cache.Cache) {
	if !c.Enabled() {
		return
	}
	c.Del(ctx, cacheNS, cacheKey)
}
