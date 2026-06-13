package share

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/christianreimer/bot-bot-goose/internal/game"
)

func TestRenderResultOGProducesValidPNG(t *testing.T) {
	data, err := RenderResultOG(ResultOG{
		PuzzleNumber:       7,
		Outcomes:           []game.Outcome{game.Green, game.Yellow, game.Red},
		Streak:             5,
		HumansYesterdayPct: -1,
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
		if _, err := RenderResultOG(ResultOG{Outcomes: outs, HumansYesterdayPct: -1}); err != nil {
			t.Errorf("outs=%v: %v", outs, err)
		}
	}
}

// The collective rally line is an optional extra paint pass; it must not
// break the renderer regardless of value. We can't inspect text inside the
// PNG cheaply, so this just exercises the code path.
func TestRenderResultOGWithCollectiveLine(t *testing.T) {
	for _, pct := range []int{0, 1, 50, 99, 100} {
		data, err := RenderResultOG(ResultOG{
			PuzzleNumber:       12,
			Outcomes:           []game.Outcome{game.Green, game.Green, game.Yellow},
			Streak:             3,
			HumansYesterdayPct: pct,
		})
		if err != nil {
			t.Fatalf("pct=%d: %v", pct, err)
		}
		if _, err := png.Decode(bytes.NewReader(data)); err != nil {
			t.Fatalf("pct=%d decode: %v", pct, err)
		}
	}
}
