package share

import (
	"strings"
	"testing"

	"github.com/christianreimer/bot-bot-goose/internal/game"
)

func TestGridIsSpoilerFree(t *testing.T) {
	// The grid contains ONLY emoji cells — no answer text, no archetype, no
	// "the bot was X" reveal.
	out := Grid([]game.Outcome{game.Green, game.Yellow, game.Red})
	if out != "🟩🟨🟥" {
		t.Errorf("grid = %q, want 🟩🟨🟥", out)
	}
}

func TestCardCarriesBrandIcon(t *testing.T) {
	c := Card(42, []game.Outcome{game.Green, game.Green, game.Green}, 5, -1, "https://botbotgoose.fun/")
	if !strings.Contains(c, IconFindBot) {
		t.Errorf("card missing 🪿: %q", c)
	}
	if !strings.Contains(c, "Bot-Dar") {
		t.Errorf("card missing Bot-Dar label")
	}
}

func TestCardIncludesScoreAndStreak(t *testing.T) {
	c := Card(7, []game.Outcome{game.Green, game.Yellow, game.Red}, 12, -1, "botbotgoose.fun")
	if !strings.Contains(c, "66%") {
		t.Errorf("missing 66%% score: %q", c)
	}
	if !strings.Contains(c, "🔥12") {
		t.Errorf("missing streak: %q", c)
	}
	if !strings.Contains(c, "#007") {
		t.Errorf("missing zero-padded puzzle #: %q", c)
	}
}

func TestCardOmitsCollectiveLineWhenNegative(t *testing.T) {
	c := Card(7, []game.Outcome{game.Green, game.Yellow, game.Red}, 0, -1, "botbotgoose.fun")
	if strings.Contains(c, "Humans yesterday") {
		t.Errorf("collective line should be absent when pct = -1: %q", c)
	}
}

func TestCardIncludesCollectiveLineWhenSet(t *testing.T) {
	c := Card(7, []game.Outcome{game.Green, game.Yellow, game.Red}, 3, 64, "botbotgoose.fun")
	if !strings.Contains(c, "Humans yesterday: 64%") {
		t.Errorf("missing collective line: %q", c)
	}
	// Order matters: scoreLine, then collective, then URL. The URL must be
	// the final line so chat clients still autodetect it for unfurls.
	idxScore := strings.Index(c, "Bot-Dar")
	idxHum := strings.Index(c, "Humans yesterday")
	idxURL := strings.Index(c, "https://")
	if !(idxScore < idxHum && idxHum < idxURL) {
		t.Errorf("expected score < humans < url, got %d/%d/%d in %q", idxScore, idxHum, idxURL, c)
	}
}

func TestCardWithCollectiveLineAndSweep(t *testing.T) {
	c := Card(7, []game.Outcome{game.Red, game.Red, game.Red}, 0, 42, "botbotgoose.fun")
	if !strings.Contains(c, "Goose got away") {
		t.Errorf("sweep card missing honest-verb prefix: %q", c)
	}
	if !strings.Contains(c, "Humans yesterday: 42%") {
		t.Errorf("sweep card missing collective line: %q", c)
	}
}
