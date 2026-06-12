package leaderboard

import (
	"context"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/db"
)

// Rollup walks decoy_daily_stats, aggregates per author across both modes,
// computes the unified adjusted fool rate + tier, and writes forger_rankings.
//
// Designed to be idempotent and cheap: it touches O(authors) rows. Run
// nightly (cron) or on-demand via `bbg-admin rollup`.
func Rollup(ctx context.Context, d *db.DB) (int, error) {
	aggs, err := d.AggregateDecoyStats(ctx)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	for _, a := range aggs {
		adj := AdjustedFoolRate(a.BotPicked, a.BotImp, a.HumanPicked, a.HumanImp)
		tier := TierFor(adj)
		totImp := a.BotImp + a.HumanImp
		totPicked := a.BotPicked + a.HumanPicked
		if err := d.UpsertForgerRanking(ctx, a.UserID, adj, totImp, totPicked, tier, now); err != nil {
			return 0, err
		}
	}
	return len(aggs), nil
}
