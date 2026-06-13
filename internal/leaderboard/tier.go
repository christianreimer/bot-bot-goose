// Package leaderboard implements §4 of the design doc — the offense mode.
// Per-decoy stats roll up into per-user forger_rankings; the ranking + tier
// math lives here so it's pure-Go and easy to unit-test.
package leaderboard

import "github.com/christianreimer/bot-bot-goose/internal/game"

// Tiers ladder up by how convincingly the player's decoys read as the
// "most human" answer in their round. Anchored at the realest baseline of
// 1/3 — a Decoy is at-or-below chance, The Realest is the rarest top tier.
const (
	TierDecoy        = "Decoy"
	TierVoice        = "Voice"
	TierStandout     = "Standout"
	TierUnmistakable = "Unmistakable"
	TierRealest      = "The Realest"
)

// MinImpressionsEligible is the leaderboard cut-off from the design doc.
// Below this we still compute the user's adjusted rate (the /me page wants
// it), but they don't appear on the public board until they've earned the
// sample size.
const MinImpressionsEligible = 50

// AdjustedFoolRate is the user-level rate, shrinking the raw rate toward the
// chance baseline by k impressions. This is what the leaderboard ranks on
// — raw rate is only ever shown to the author alongside the baseline line.
//
//	adjusted = (picked + k · baseline) / (impressions + k)
//
// k is borrowed from the per-decoy formula in internal/game; same shrinkage
// strength for both granularities.
func AdjustedFoolRate(picked, impressions int64) float64 {
	k := float64(game.FoolRateK)
	return (float64(picked) + k*game.Baseline) / (float64(impressions) + k)
}

// TierFor assigns a tier from an adjusted most-human rate. Baseline is
// 1/3; thresholds are tuned so the distribution skews:
// Decoy > Voice > Standout > Unmistakable > The Realest.
func TierFor(adjusted float64) string {
	switch {
	case adjusted >= 0.55:
		return TierRealest
	case adjusted >= 0.48:
		return TierUnmistakable
	case adjusted >= 0.42:
		return TierStandout
	case adjusted >= 0.37:
		return TierVoice
	default:
		return TierDecoy
	}
}

// RealestBaseline is chance for the post-reveal "felt most human?" vote:
// 1 of 3 human decoys per round. Used to anchor the adjusted rate the way
// 0.25 anchors fool rate.
const RealestBaseline = 1.0 / 3.0

// AdjustedMostHumanRate is the realest-track equivalent of AdjustedFoolRate.
// Shrinks toward the 1/3 chance baseline by k impressions.
//
//	adjusted = (votes + k · RealestBaseline) / (impressions + k)
func AdjustedMostHumanRate(votes, impressions int64) float64 {
	k := float64(game.FoolRateK)
	return (float64(votes) + k*RealestBaseline) / (float64(impressions) + k)
}

// RealestBeyondChance is the votes earned above what pure chance would
// predict, floored at 0. Used as a tiebreaker / "points" display.
func RealestBeyondChance(votes, impressions int64) int {
	beyond := float64(votes) - float64(impressions)*RealestBaseline
	if beyond < 0 {
		return 0
	}
	return int(beyond + 0.5)
}

// VotesToNextTier returns the (deltaVotes, nextTier) pair telling the user
// "you are N votes from <next tier>" on the realest track. If they're
// already at the top, both returns are zero/empty.
//
// Holds impressions fixed and asks: how many MORE realest votes would be
// needed for the adjusted rate to cross the next threshold? Solving
//
//	(votes + d + k·base) / (imp + k) ≥ threshold
//
// for d gives d ≥ threshold·(imp+k) - votes - k·base.
func VotesToNextTier(votes, impressions int64) (int, string) {
	thresholds := []struct {
		v float64
		t string
	}{
		{0.37, TierVoice},
		{0.42, TierStandout},
		{0.48, TierUnmistakable},
		{0.55, TierRealest},
	}
	k := float64(game.FoolRateK)
	current := AdjustedMostHumanRate(votes, impressions)
	for _, th := range thresholds {
		if current < th.v {
			d := th.v*(float64(impressions)+k) - float64(votes) - k*RealestBaseline
			if d < 1 {
				d = 1
			}
			return int(d + 0.5), th.t
		}
	}
	return 0, ""
}
