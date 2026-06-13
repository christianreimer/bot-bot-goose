package share

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"

	"github.com/christianreimer/bot-bot-goose/internal/game"
	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// OG image dimensions per the OpenGraph spec — 1200x630 is what every chat
// client expects and is what Twitter/X requires.
const (
	OGWidth  = 1200
	OGHeight = 630
)

// Dark-pond palette mirrored from web/static/css/app.css so the social card
// looks like the rest of the brand.
var (
	colorPondDeep = color.RGBA{0x0e, 0x1a, 0x20, 0xff} // background
	colorSurface  = color.RGBA{0x1d, 0x35, 0x40, 0xff} // card panel
	colorInk      = color.RGBA{0xf4, 0xef, 0xe3, 0xff} // text
	colorMuted    = color.RGBA{0x8f, 0xa6, 0xae, 0xff}
	colorHonk     = color.RGBA{0xf4, 0xa2, 0x3b, 0xff} // accent
	colorReed     = color.RGBA{0x6f, 0xb3, 0x6a, 0xff} // 🟩
	colorAmber    = color.RGBA{0xf4, 0xa2, 0x3b, 0xff} // 🟨 (same as honk)
	colorMiss     = color.RGBA{0xe0, 0x60, 0x4f, 0xff} // 🟥
	colorLine     = color.RGBA{0xff, 0xff, 0xff, 0x1a} // 10% white
)

// ResultOG holds the data the OG image renders. It deliberately excludes
// anything that could spoil today's puzzle — no prompts, no answers, no
// archetype. Just the grid + brand + score.
type ResultOG struct {
	PuzzleNumber int32
	Outcomes     []game.Outcome
	Mode         game.Mode
	Streak       int
}

// RenderResultOG returns a 1200x630 PNG suitable for og:image.
func RenderResultOG(r ResultOG) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, OGWidth, OGHeight))
	fill(img, img.Bounds(), colorPondDeep)

	// Subtle gradient at the top — pure Go, no library: vertical band of
	// slightly lighter color above the centerline.
	band := image.Rect(0, 0, OGWidth, 220)
	fill(img, band, color.RGBA{0x1c, 0x33, 0x40, 0xff})

	// Card panel that holds the grid.
	cardW, cardH := 760, 360
	cardX := (OGWidth - cardW) / 2
	cardY := (OGHeight - cardH) / 2
	cardRect := image.Rect(cardX, cardY, cardX+cardW, cardY+cardH)
	fillRounded(img, cardRect, 28, colorSurface)
	strokeRounded(img, cardRect, 28, colorLine)

	// Fonts.
	face48, err := loadFace(54)
	if err != nil {
		return nil, err
	}
	defer face48.Close()
	face24, err := loadFace(28)
	if err != nil {
		return nil, err
	}
	defer face24.Close()
	face18, err := loadFace(22)
	if err != nil {
		return nil, err
	}
	defer face18.Close()
	face14, err := loadFace(18)
	if err != nil {
		return nil, err
	}
	defer face14.Close()

	// Header — 🪿/🧍 ASCII fallback (no color-emoji font server-side), brand wordmark, puzzle number.
	brand := "Bot Bot Goose"
	drawString(img, face24, brand, cardX+36, cardY+58, colorInk)
	subtitle := fmt.Sprintf("Daily Goose #%03d", r.PuzzleNumber)
	if r.Mode == game.FindTheHuman {
		subtitle = fmt.Sprintf("Daily Human #%03d", r.PuzzleNumber)
	}
	drawString(img, face14, subtitle, cardX+36, cardY+86, colorMuted)

	// The grid — three squares with rounded corners, sized to dominate the card.
	gridY := cardY + 130
	cellSize := 130
	gap := 22
	totalGridW := len(r.Outcomes)*cellSize + (len(r.Outcomes)-1)*gap
	gridX := cardX + (cardW-totalGridW)/2
	for _, o := range r.Outcomes {
		rect := image.Rect(gridX, gridY, gridX+cellSize, gridY+cellSize)
		fillRounded(img, rect, 20, outcomeColor(o))
		gridX += cellSize + gap
	}

	// Score line below the grid.
	pct := game.ScorePct(r.Outcomes)
	statLabel := "Bot-Dar"
	if r.Mode == game.FindTheHuman {
		statLabel = "Human-Dar"
	}
	scoreLine := fmt.Sprintf("%s %d%%", statLabel, pct)
	drawString(img, face48, scoreLine, cardX+36, cardY+cardH-44, colorHonk)

	if r.Streak > 0 {
		streakLine := fmt.Sprintf("streak %d", r.Streak)
		drawStringRight(img, face18, streakLine, cardX+cardW-36, cardY+cardH-50, colorMuted)
	}

	// Footer.
	footer := "botbotgoose.fun · spot the AI hiding among real humans"
	drawStringCentered(img, face18, footer, OGWidth/2, OGHeight-44, colorMuted)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderHarvestOG produces the static unfurl card for the /harvest landing
// page. No per-request data — the content is the campaign pitch only, so
// callers can cache the output indefinitely. Editorial composition: small
// wordmark, big hook line, the campaign tagline, route footer.
func RenderHarvestOG() ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, OGWidth, OGHeight))
	fill(img, img.Bounds(), colorPondDeep)

	// Gentle bleed of light at the top to match the in-app gradient.
	fill(img, image.Rect(0, 0, OGWidth, 240), color.RGBA{0x1c, 0x33, 0x40, 0xff})

	// Big honk dot on the left to anchor the eye — same role the wordmark
	// emoji plays in-app, sized up for the poster context.
	fillCircle(img, 180, OGHeight/2, 96, colorHonk)

	face64, err := loadFace(72)
	if err != nil {
		return nil, err
	}
	defer face64.Close()
	face28, err := loadFace(32)
	if err != nil {
		return nil, err
	}
	defer face28.Close()
	face18, err := loadFace(22)
	if err != nil {
		return nil, err
	}
	defer face18.Close()
	face14, err := loadFace(18)
	if err != nil {
		return nil, err
	}
	defer face14.Close()

	// Kicker (the "department label" editorial move) and brand.
	drawString(img, face14, "BOT BOT GOOSE", 320, 178, colorHonk)

	// Headline. "Help build the goose" is the campaign hook — same string
	// the page itself opens with.
	drawString(img, face64, "Help build the goose.", 320, 274, colorInk)

	// Sub — the campaign tagline, two lines.
	drawString(img, face28, "Type like a person.", 320, 348, colorMuted)
	drawString(img, face28, "Your answers become future traps.", 320, 392, colorMuted)

	// Footer line, brand-anchored.
	drawString(img, face18, "botbotgoose.fun/harvest", 320, OGHeight-58, colorHonk)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func outcomeColor(o game.Outcome) color.RGBA {
	switch o {
	case game.Green:
		return colorReed
	case game.Yellow:
		return colorAmber
	case game.Red:
		return colorMiss
	}
	return colorLine
}

// ---------------------------------------------------------------------------
// drawing primitives (kept here so the package has no other image deps).
// ---------------------------------------------------------------------------

func fill(img *image.RGBA, r image.Rectangle, c color.Color) {
	draw.Draw(img, r, &image.Uniform{C: c}, image.Point{}, draw.Src)
}

// fillRounded paints a rounded-rect. Cheap approximation: paint the inset
// rect + four "extended" rects + four corner discs.
func fillRounded(img *image.RGBA, r image.Rectangle, radius int, c color.Color) {
	if radius <= 0 {
		fill(img, r, c)
		return
	}
	// center + edges
	fill(img, image.Rect(r.Min.X+radius, r.Min.Y, r.Max.X-radius, r.Max.Y), c)
	fill(img, image.Rect(r.Min.X, r.Min.Y+radius, r.Max.X, r.Max.Y-radius), c)
	// four corner discs
	fillCircle(img, r.Min.X+radius, r.Min.Y+radius, radius, c)
	fillCircle(img, r.Max.X-radius-1, r.Min.Y+radius, radius, c)
	fillCircle(img, r.Min.X+radius, r.Max.Y-radius-1, radius, c)
	fillCircle(img, r.Max.X-radius-1, r.Max.Y-radius-1, radius, c)
}

// strokeRounded paints a 1px outline around a rounded rect — used for the
// subtle border on the card panel. Cheap version: fill the rounded rect at
// the outline color, then fill an inset rounded rect with transparent (no-op
// here since we're on top of the bg already painted). To keep it simple we
// instead paint a hairline-thin frame using straight rects.
func strokeRounded(img *image.RGBA, r image.Rectangle, radius int, c color.Color) {
	// top + bottom
	fill(img, image.Rect(r.Min.X+radius, r.Min.Y, r.Max.X-radius, r.Min.Y+1), c)
	fill(img, image.Rect(r.Min.X+radius, r.Max.Y-1, r.Max.X-radius, r.Max.Y), c)
	// left + right
	fill(img, image.Rect(r.Min.X, r.Min.Y+radius, r.Min.X+1, r.Max.Y-radius), c)
	fill(img, image.Rect(r.Max.X-1, r.Min.Y+radius, r.Max.X, r.Max.Y-radius), c)
}

func fillCircle(img *image.RGBA, cx, cy, radius int, c color.Color) {
	r2 := radius * radius
	for y := -radius; y <= radius; y++ {
		for x := -radius; x <= radius; x++ {
			if x*x+y*y <= r2 {
				img.Set(cx+x, cy+y, c)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// text — uses gofont/gobold from x/image so we don't bundle our own TTF.
// ---------------------------------------------------------------------------

func loadFace(size float64) (closableFace, error) {
	f, err := opentype.Parse(gobold.TTF)
	if err != nil {
		return closableFace{}, err
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return closableFace{}, err
	}
	return closableFace{Face: face}, nil
}

type closableFace struct{ font.Face }

func (c closableFace) Close() error { return c.Face.Close() }

func drawString(img *image.RGBA, face closableFace, text string, x, y int, c color.Color) {
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: face.Face,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(text)
}

func drawStringCentered(img *image.RGBA, face closableFace, text string, cx, y int, c color.Color) {
	d := &font.Drawer{Face: face.Face}
	width := d.MeasureString(text).Round()
	drawString(img, face, text, cx-width/2, y, c)
}

func drawStringRight(img *image.RGBA, face closableFace, text string, rightX, y int, c color.Color) {
	d := &font.Drawer{Face: face.Face}
	width := d.MeasureString(text).Round()
	drawString(img, face, text, rightX-width, y, c)
}
