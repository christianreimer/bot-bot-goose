package share

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/christianreimer/bot-bot-goose/internal/game"
)

func TestRenderResultOGProducesValidPNG(t *testing.T) {
	data, err := RenderResultOG(ResultOG{
		PuzzleNumber: 7,
		Outcomes:     []game.Outcome{game.Green, game.Yellow, game.Red},
		Mode:         game.FindTheBot,
		Streak:       5,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(data) < 1024 {
		t.Fatalf("png suspiciously small: %d bytes", len(data))
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if img.Bounds().Dx() != OGWidth || img.Bounds().Dy() != OGHeight {
		t.Errorf("size = %dx%d, want %dx%d", img.Bounds().Dx(), img.Bounds().Dy(), OGWidth, OGHeight)
	}
}

func TestRenderResultOGHandlesAllOutcomesAndModes(t *testing.T) {
	for _, mode := range []game.Mode{game.FindTheBot, game.FindTheHuman} {
		for _, outs := range [][]game.Outcome{
			{game.Green, game.Green, game.Green},
			{game.Red, game.Red, game.Red},
			{game.Yellow, game.Yellow, game.Yellow},
			{game.Green, game.Yellow, game.Red},
		} {
			if _, err := RenderResultOG(ResultOG{Mode: mode, Outcomes: outs}); err != nil {
				t.Errorf("mode=%s outs=%v: %v", mode, outs, err)
			}
		}
	}
}
