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

func TestCardCarriesModeIcon(t *testing.T) {
	bot := Card(42, []game.Outcome{game.Green, game.Green, game.Green}, game.FindTheBot, 5, "https://botbotgoose.fun/")
	if !strings.Contains(bot, IconFindBot) {
		t.Errorf("find_the_bot card missing 🪿: %q", bot)
	}
	if !strings.Contains(bot, "Bot-Dar") {
		t.Errorf("find_the_bot card missing Bot-Dar label")
	}
	human := Card(42, []game.Outcome{game.Green, game.Green, game.Green}, game.FindTheHuman, 5, "https://botbotgoose.fun/")
	if !strings.Contains(human, IconFindHuman) {
		t.Errorf("find_the_human card missing 🧍: %q", human)
	}
	if !strings.Contains(human, "Human-Dar") {
		t.Errorf("find_the_human card missing Human-Dar label")
	}
}

func TestCardIncludesScoreAndStreak(t *testing.T) {
	c := Card(7, []game.Outcome{game.Green, game.Yellow, game.Red}, game.FindTheBot, 12, "botbotgoose.fun")
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
