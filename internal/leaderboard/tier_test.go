package leaderboard

import (
	"math"
	"testing"
)

func TestWeightedBaselineRespectsMix(t *testing.T) {
	if got := WeightedBaseline(0, 0); got != 0.25 {
		t.Errorf("empty mix = %f, want 0.25", got)
	}
	if got := WeightedBaseline(100, 0); math.Abs(got-0.25) > 1e-9 {
		t.Errorf("all-bot = %f, want 0.25", got)
	}
	if got := WeightedBaseline(0, 100); math.Abs(got-0.75) > 1e-9 {
		t.Errorf("all-human = %f, want 0.75", got)
	}
	if got := WeightedBaseline(100, 100); math.Abs(got-0.50) > 1e-9 {
		t.Errorf("even split = %f, want 0.50", got)
	}
}

func TestTierFor(t *testing.T) {
	cases := []struct {
		r    float64
		want string
	}{
		{0.20, TierDecoy},
		{0.31, TierMimic},
		{0.45, TierForger},
		{0.55, TierDoppelganger},
		{0.70, TierHonorary},
	}
	for _, c := range cases {
		if got := TierFor(c.r); got != c.want {
			t.Errorf("TierFor(%f) = %s, want %s", c.r, got, c.want)
		}
	}
}

func TestFoolsToNextTier(t *testing.T) {
	// 200 picks / 1000 impressions in find_the_bot — raw 20%, baseline 25%.
	// Adjusted = (200 + 5) / 1020 = 0.201 — still Decoy. Mimic at 0.30:
	// d = 0.30 * 1020 - 200 - 5 = 306 - 205 = 101.
	d, next := FoolsToNextTier(200, 1000, 0, 0)
	if next != TierMimic {
		t.Errorf("next = %q, want Mimic", next)
	}
	if d < 95 || d > 110 {
		t.Errorf("d = %d, want ~101", d)
	}

	// Already at Honorary — no further tier.
	_, next = FoolsToNextTier(8000, 10000, 0, 0)
	if next != "" {
		t.Errorf("expected no next tier above honorary, got %q", next)
	}
}
