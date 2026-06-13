// Package game owns the rules: outcome resolution, score_pct, adjusted fool
// rate. These are pure functions so they're easy to test.
package game

// Outcome is the per-round result that becomes a feather color in the share grid.
type Outcome string

const (
	Green  Outcome = "green"  // nailed it: found target, first guess, no hint
	Yellow Outcome = "yellow" // caught it: found target, but used a hint
	Red    Outcome = "red"    // fooled: picked the wrong answer
)

// Resolve returns the round outcome given whether the player picked the
// target slot and whether they used a hint. The target is always the bot:
// three human answers, one bot, tap the bot.
func Resolve(correct, hintUsed bool) Outcome {
	if !correct {
		return Red
	}
	if hintUsed {
		return Yellow
	}
	return Green
}

// ScorePct converts a slice of outcomes to a 0..100 "Bot-Dar" percentage.
// Green and yellow both count as caught (per the prototype + design doc §2).
func ScorePct(outs []Outcome) int {
	if len(outs) == 0 {
		return 0
	}
	caught := 0
	for _, o := range outs {
		if o == Green || o == Yellow {
			caught++
		}
	}
	return (caught * 100) / len(outs)
}

// Baseline is the chance-level fool rate: 1-in-4. Every prompt round shows
// four answers and one is the bot; a player guessing at random picks any
// given decoy 25% of the time.
const Baseline = 0.25

// AdjustedFoolRate shrinks the raw rate toward the baseline using a
// pseudo-count of k impressions at the baseline. This is what the forger
// leaderboard ranks on — raw rate is only ever shown to the user alongside
// the baseline line on the chart.
//
//	adjusted = (picked + k * baseline) / (impressions + k)
//
// Per the plan: k = 20. A 41%-over-2000 decoy correctly beats a
// 100%-over-6 fluke; a brand-new decoy with no impressions reports the
// baseline (not 0).
const FoolRateK = 20

func AdjustedFoolRate(pickedAsBot, impressions int) float64 {
	return (float64(pickedAsBot) + FoolRateK*Baseline) / (float64(impressions) + FoolRateK)
}

// ForgerPoints is the user-facing "how many you fooled beyond chance" stat
// from §4. Floored at 0 — being below chance is "charmingly human", not negative.
func ForgerPoints(pickedAsBot, impressions int) int {
	expected := Baseline * float64(impressions)
	pts := float64(pickedAsBot) - expected
	if pts < 0 {
		return 0
	}
	return int(pts + 0.5)
}
