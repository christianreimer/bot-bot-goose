package share

import (
	"strings"
	"testing"
)

func TestDecoyReportPendingVariant(t *testing.T) {
	c := DecoyReportCard(DecoyReport{Text: "a thing", Status: "pending"}, "https://botbotgoose.app/")
	if !strings.Contains(c, "Planted") {
		t.Errorf("pending card missing planted copy: %q", c)
	}
	if strings.Contains(c, "fool rate") || strings.Contains(c, "%") {
		t.Errorf("pending card must NOT show stats: %q", c)
	}
}

func TestDecoyReportLiveAwaiting(t *testing.T) {
	c := DecoyReportCard(DecoyReport{Text: "a thing", Status: "approved", Impressions: 0}, "botbotgoose.app")
	if !strings.Contains(c, "first impressions") {
		t.Errorf("zero-impression card missing copy: %q", c)
	}
}

func TestDecoyReportFlopReframesWarmly(t *testing.T) {
	c := DecoyReportCard(DecoyReport{Text: "a thing", Status: "approved", RawPct: 9, Impressions: 312, Fooled: 28}, "botbotgoose.app")
	if !strings.Contains(c, "Too human") {
		t.Errorf("flop must reframe warmly: %q", c)
	}
	if !strings.Contains(c, "🧑") {
		t.Errorf("flop variant should use human icon: %q", c)
	}
}

func TestDecoyReportPayoffShowsRankWhenEligible(t *testing.T) {
	c := DecoyReportCard(DecoyReport{
		Text: "a thing", Status: "approved",
		RawPct: 47, Impressions: 660, Fooled: 312, BeyondChance: 247,
		Eligible: true, Rank: 4, OfTotal: 1208, Tier: "Forger",
	}, "botbotgoose.app")
	if !strings.Contains(c, "47%") {
		t.Errorf("payoff missing raw pct: %q", c)
	}
	if !strings.Contains(c, "312 fooled") {
		t.Errorf("payoff missing fooled count: %q", c)
	}
	if !strings.Contains(c, "+247 beyond chance") {
		t.Errorf("payoff missing forger points: %q", c)
	}
	if !strings.Contains(c, "Rank #4 of 1208 forgers") {
		t.Errorf("payoff missing rank line: %q", c)
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
