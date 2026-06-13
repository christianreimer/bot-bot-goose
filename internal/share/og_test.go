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

func TestRenderResultOGHandlesAllOutcomes(t *testing.T) {
	for _, outs := range [][]game.Outcome{
		{game.Green, game.Green, game.Green},
		{game.Red, game.Red, game.Red},
		{game.Yellow, game.Yellow, game.Yellow},
		{game.Green, game.Yellow, game.Red},
	} {
		if _, err := RenderResultOG(ResultOG{Outcomes: outs}); err != nil {
			t.Errorf("outs=%v: %v", outs, err)
		}
	}
}
