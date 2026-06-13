package leaderboard

import (
	"context"
	"time"

	"github.com/christianreimer/bot-bot-goose/internal/db"
)

// Rollup walks decoy_daily_stats, aggregates per author, computes the
// adjusted fool rate + tier, and writes forger_rankings.
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
		adj := AdjustedFoolRate(a.PickedAsBot, a.Impressions)
		// Realest math lands with the vote-UI work. Until then we write the
		// 1/3 baseline so the column stays well-defined and the leaderboard
		// gate (realest_total_impressions >= MinImpressionsEligible) keeps
		// behaving the way the old fool gate did once real votes flow in.
		realestAdj := AdjustedMostHumanRate(a.RealestVotes, a.RealestImpressions)
		tier := TierFor(realestAdj)
		if err := d.UpsertForgerRanking(ctx, db.ForgerRankingUpsert{
			UserID:                  a.UserID,
			AdjustedFoolRate:        adj,
			TotalImpressions:        a.Impressions,
			TotalPickedAsBot:        a.PickedAsBot,
			AdjustedRealestRate:     realestAdj,
			RealestTotalImpressions: a.RealestImpressions,
			RealestTotalVotes:       a.RealestVotes,
			RealestBeyondChance:     RealestBeyondChance(a.RealestVotes, a.RealestImpressions),
			Tier:                    tier,
			ComputedAt:              now,
		}); err != nil {
			return 0, err
		}
	}
	return len(aggs), nil
}
