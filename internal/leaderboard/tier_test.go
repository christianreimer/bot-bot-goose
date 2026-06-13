package leaderboard

import (
	"testing"
)

func TestTierFor(t *testing.T) {
	cases := []struct {
		r    float64
		want string
	}{
		{0.30, TierQuiet},
		{0.38, TierVoice},
		{0.44, TierStandout},
		{0.50, TierUnmistakable},
		{0.60, TierRealest},
	}
	for _, c := range cases {
		if got := TierFor(c.r); got != c.want {
			t.Errorf("TierFor(%f) = %s, want %s", c.r, got, c.want)
		}
	}
}

func TestAdjustedMostHumanRateAnchorsToBaseline(t *testing.T) {
	// Zero impressions returns the baseline exactly.
	got := AdjustedMostHumanRate(0, 0)
	if got < 0.332 || got > 0.334 {
		t.Errorf("0/0 adj rate = %f, want ~0.333", got)
	}
}

func TestAdjustedMostHumanRateConvergesToRaw(t *testing.T) {
	// Large samples should pull the adjusted rate close to the raw rate.
	got := AdjustedMostHumanRate(8000, 10000)
	if got < 0.79 || got > 0.81 {
		t.Errorf("8000/10000 adj rate = %f, want ~0.80", got)
	}
}

func TestVotesToNextTier(t *testing.T) {
	// 200 votes / 1000 impressions — raw 20%, baseline 33%.
	// Adjusted = (200 + 20*0.333) / 1020 ≈ 0.203 — Quiet.
	// Voice at 0.37: d ≥ 0.37*1020 - 200 - 20*0.333 ≈ 377.4 - 206.66 ≈ 170.7
	d, next := VotesToNextTier(200, 1000)
	if next != TierVoice {
		t.Errorf("next = %q, want %q", next, TierVoice)
	}
	if d < 160 || d > 180 {
		t.Errorf("d = %d, want ~171", d)
	}

	// Already at The Realest — no further tier.
	_, next = VotesToNextTier(8000, 10000)
	if next != "" {
		t.Errorf("expected no next tier above realest, got %q", next)
	}
}

func TestRealestBeyondChanceFloorsAtZero(t *testing.T) {
	// Below baseline = 0 beyond-chance points.
	if got := RealestBeyondChance(100, 1000); got != 0 {
		t.Errorf("100/1000 (10%%) beyond = %d, want 0", got)
	}
	// Above baseline = picked - imp/3.
	if got := RealestBeyondChance(500, 1000); got < 165 || got > 168 {
		t.Errorf("500/1000 beyond = %d, want ~167", got)
	}
}
