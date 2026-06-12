// Package leaderboard implements §4 of the design doc — the offense mode.
// Per-decoy stats roll up into per-user forger_rankings; the ranking + tier
// math lives here so it's pure-Go and easy to unit-test.
package leaderboard

import "github.com/christianreimer/bot-bot-goose/internal/game"

// Tiers from design doc §4. The ladder is intentionally themed around the
// inversion: every step is "more bot-like."
const (
	TierDecoy        = "Decoy"
	TierMimic        = "Mimic"
	TierForger       = "Forger"
	TierDoppelganger = "Doppelgänger"
	TierHonorary     = "Honorary Bot"
)

// MinImpressionsEligible is the leaderboard cut-off from the design doc.
// Below this we still compute the user's adjusted rate (the /me page wants
// it), but they don't appear on the public board until they've earned the
// sample size.
const MinImpressionsEligible = 50

// WeightedBaseline is the mode-mixed chance line a user is shrunk toward.
// In find_the_bot a decoy is hit by chance 25% of the time; in
// find_the_human it survives by chance 75%. A user who has been seen mostly
// in one mode pulls toward that mode's baseline.
func WeightedBaseline(botImp, humanImp int64) float64 {
	total := float64(botImp + humanImp)
	if total <= 0 {
		// No data — use the default-mode baseline so the user's first
		// impression doesn't push them straight to a tier.
		return 0.25
	}
	return (float64(botImp)*0.25 + float64(humanImp)*0.75) / total
}

// AdjustedFoolRate is the user-level rate, shrinking the raw rate toward the
// weighted baseline by k impressions. This is what the leaderboard ranks on
// — raw rate is only ever shown to the author with the right baseline line.
//
//	adjusted = (picked + k · weightedBaseline) / (impressions + k)
//
// k is borrowed from the per-decoy formula in internal/game; same shrinkage
// strength for both granularities.
func AdjustedFoolRate(botPicked, botImp, humanPicked, humanImp int64) float64 {
	totalPicked := botPicked + humanPicked
	totalImp := botImp + humanImp
	baseline := WeightedBaseline(botImp, humanImp)
	k := float64(game.FoolRateK)
	return (float64(totalPicked) + k*baseline) / (float64(totalImp) + k)
}

// TierFor assigns a tier from an adjusted rate. Thresholds are absolute on
// purpose: a Doppelgänger has to clear 50% adjusted regardless of mode mix.
// The plan/§4 doesn't bind exact cutoffs; these are tuned so the tier
// distribution is roughly Decoy > Mimic > Forger > Doppelgänger > Honorary.
func TierFor(adjusted float64) string {
	switch {
	case adjusted >= 0.60:
		return TierHonorary
	case adjusted >= 0.50:
		return TierDoppelganger
	case adjusted >= 0.40:
		return TierForger
	case adjusted >= 0.30:
		return TierMimic
	default:
		return TierDecoy
	}
}

// FoolsToNextTier returns the (deltaPicked, nextTier) pair telling the user
// "you are N fools from <next tier>". If they're already at the top, both
// returns are zero/empty.
//
// We hold impressions fixed and ask: how many MORE picked_as_bot would be
// needed for the adjusted rate to cross the next threshold? Solving the
// equation
//
//	(picked + d + k·base) / (imp + k) ≥ threshold
//
// for d gives d ≥ threshold·(imp+k) - picked - k·base.
func FoolsToNextTier(botPicked, botImp, humanPicked, humanImp int64) (int, string) {
	thresholds := []struct {
		v float64
		t string
	}{
		{0.30, TierMimic},
		{0.40, TierForger},
		{0.50, TierDoppelganger},
		{0.60, TierHonorary},
	}
	totalImp := botImp + humanImp
	totalPicked := botPicked + humanPicked
	baseline := WeightedBaseline(botImp, humanImp)
	k := float64(game.FoolRateK)
	current := AdjustedFoolRate(botPicked, botImp, humanPicked, humanImp)
	for _, th := range thresholds {
		if current < th.v {
			d := th.v*(float64(totalImp)+k) - float64(totalPicked) - k*baseline
			if d < 1 {
				d = 1
			}
			return int(d + 0.5), th.t
		}
	}
	return 0, ""
}
