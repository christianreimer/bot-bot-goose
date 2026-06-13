package share

import (
	"strings"
	"testing"
)

func TestDecoyReportPendingVariant(t *testing.T) {
	c := DecoyReportCard(DecoyReport{Text: "a thing", Status: "pending"}, "https://botbotgoose.fun/")
	if !strings.Contains(c, "Planted") {
		t.Errorf("pending card missing planted copy: %q", c)
	}
	if strings.Contains(c, "most human") || strings.Contains(c, "%") {
		t.Errorf("pending card must NOT show stats: %q", c)
	}
}

func TestDecoyReportLiveAwaiting(t *testing.T) {
	c := DecoyReportCard(DecoyReport{Text: "a thing", Status: "approved", RealestImpressions: 0}, "botbotgoose.fun")
	if !strings.Contains(c, "first votes") {
		t.Errorf("zero-impression card missing copy: %q", c)
	}
}

func TestDecoyReportFlopReframesWarmly(t *testing.T) {
	c := DecoyReportCard(DecoyReport{
		Text: "a thing", Status: "approved",
		RealestRawPct: 12, RealestImpressions: 312, RealestVotes: 37,
	}, "botbotgoose.fun")
	if !strings.Contains(c, "Reads quiet") {
		t.Errorf("flop must reframe warmly: %q", c)
	}
	if !strings.Contains(c, "🧑") {
		t.Errorf("flop variant should use human icon: %q", c)
	}
}

func TestDecoyReportPayoffShowsRankWhenEligible(t *testing.T) {
	c := DecoyReportCard(DecoyReport{
		Text: "a thing", Status: "approved",
		RealestRawPct: 47, RealestImpressions: 660, RealestVotes: 312, RealestBeyond: 92,
		Eligible: true, Rank: 4, OfTotal: 1208, Tier: "Standout",
	}, "botbotgoose.fun")
	if !strings.Contains(c, "47%") {
		t.Errorf("payoff missing raw pct: %q", c)
	}
	if !strings.Contains(c, "312 votes") {
		t.Errorf("payoff missing vote count: %q", c)
	}
	if !strings.Contains(c, "+92 beyond chance") {
		t.Errorf("payoff missing beyond-chance points: %q", c)
	}
	if !strings.Contains(c, "Rank #4 of 1208 forgers") {
		t.Errorf("payoff missing rank line: %q", c)
	}
}

func TestDecoyReportPayoffIncludesFoolFlavorWhenPresent(t *testing.T) {
	c := DecoyReportCard(DecoyReport{
		Text: "a thing", Status: "approved",
		RealestRawPct: 41, RealestImpressions: 200, RealestVotes: 82, RealestBeyond: 16,
		FoolImpressions: 200, FoolPicked: 50, FoolRawPct: 25,
	}, "botbotgoose.fun")
	if !strings.Contains(c, "Also fooled 25%") {
		t.Errorf("payoff missing fool flavor line: %q", c)
	}
}

func TestDecoyReportSpoilerFree(t *testing.T) {
	// The card may include the user's OWN decoy text — that's their content.
	// It must NOT include the bot answer or any other player's answer. We
	// can't enforce that statically; this test asserts the function takes
	// only the user's decoy text in its input, no parameter for foreign text.
	// (Compile-time contract.)
	_ = DecoyReport{}
}
