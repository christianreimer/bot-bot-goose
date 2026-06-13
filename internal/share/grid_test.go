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
	c := Card(42, []game.Outcome{game.Green, game.Green, game.Green}, 5, "https://botbotgoose.fun/")
	if !strings.Contains(c, IconFindBot) {
		t.Errorf("card missing 🪿: %q", c)
	}
	if !strings.Contains(c, "Bot-Dar") {
		t.Errorf("card missing Bot-Dar label")
	}
}

func TestCardIncludesScoreAndStreak(t *testing.T) {
	c := Card(7, []game.Outcome{game.Green, game.Yellow, game.Red}, 12, "botbotgoose.fun")
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
