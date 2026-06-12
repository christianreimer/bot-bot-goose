package game

import (
	"math"
	"testing"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		correct, hint bool
		want          Outcome
	}{
		{true, false, Green},
		{true, true, Yellow},
		{false, false, Red},
		{false, true, Red},
	}
	for _, c := range cases {
		if got := Resolve(c.correct, c.hint); got != c.want {
			t.Errorf("Resolve(%v,%v) = %s, want %s", c.correct, c.hint, got, c.want)
		}
	}
}

func TestScorePct(t *testing.T) {
	cases := []struct {
		outs []Outcome
		want int
	}{
		{[]Outcome{Green, Green, Green}, 100},
		{[]Outcome{Green, Yellow, Red}, 66},
		{[]Outcome{Red, Red, Red}, 0},
		{nil, 0},
	}
	for _, c := range cases {
		if got := ScorePct(c.outs); got != c.want {
			t.Errorf("ScorePct(%v) = %d, want %d", c.outs, got, c.want)
		}
	}
}

func TestAdjustedFoolRateAnchorsToBaselineWithoutImpressions(t *testing.T) {
	bot := AdjustedFoolRate(0, 0, FindTheBot)
	if math.Abs(bot-0.25) > 1e-9 {
		t.Errorf("find_the_bot zero-impression baseline = %f, want 0.25", bot)
	}
	human := AdjustedFoolRate(0, 0, FindTheHuman)
	if math.Abs(human-0.75) > 1e-9 {
		t.Errorf("find_the_human zero-impression baseline = %f, want 0.75", human)
	}
}

func TestAdjustedFoolRateConvergesToRaw(t *testing.T) {
	// A high-impression decoy at 41% in find_the_bot should converge near 0.41,
	// not still be pinned to 0.25.
	r := AdjustedFoolRate(820, 2000, FindTheBot)
	if r < 0.39 || r > 0.42 {
		t.Errorf("convergent rate = %f, want ~0.41", r)
	}

	// Same volume in find_the_human at 87% should converge near 0.87.
	r = AdjustedFoolRate(1740, 2000, FindTheHuman)
	if r < 0.86 || r > 0.88 {
		t.Errorf("convergent human rate = %f, want ~0.87", r)
	}
}

func TestForgerPointsFlooredAtZero(t *testing.T) {
	// 5 picks out of 100 in find_the_bot is below chance (25 expected). Points = 0.
	if pts := ForgerPoints(5, 100, FindTheBot); pts != 0 {
		t.Errorf("below-chance pts = %d, want 0", pts)
	}
	// 60 picks out of 100 = 35 beyond chance in find_the_bot.
	if pts := ForgerPoints(60, 100, FindTheBot); pts != 35 {
		t.Errorf("above-chance pts = %d, want 35", pts)
	}
}

func TestUnifiedFoolRateMonotonicInImpressions(t *testing.T) {
	// At a fixed raw rate of 100%, the adjusted rate grows with impressions
	// because there's less shrinkage toward the 0.25 baseline. This is the
	// shrinkage property that ranks high-volume forgers above flukes when
	// the min-impressions eligibility gate (separate from shrinkage) lets
	// both be eligible.
	r10 := AdjustedFoolRate(10, 10, FindTheBot)
	r100 := AdjustedFoolRate(100, 100, FindTheBot)
	r1000 := AdjustedFoolRate(1000, 1000, FindTheBot)
	if !(r10 < r100 && r100 < r1000) {
		t.Errorf("expected monotone growth: r10=%.3f r100=%.3f r1000=%.3f", r10, r100, r1000)
	}
}
