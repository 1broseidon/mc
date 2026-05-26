//go:build genfixtures

// Regenerate the dark_calc_display.png / light_calc_display.png test
// fixtures used by TestFindTextDarkTheme. Hand-crafted 5x7 bitmap
// glyphs at scale 5 (≈35px tall) — no external Go dependency on
// golang.org/x/image or otherwise.
//
// Run with:
//
//	go test -tags=genfixtures -run TestGenerateFixtures ./internal/image/
//
// The fixtures are checked into testdata/ so normal `go test` does not
// require this generator.

package imageutil

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

var fixtureGlyphs = map[rune][7]uint8{
	'0': {0b01110, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01110},
	'1': {0b00100, 0b01100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110},
	'2': {0b01110, 0b10001, 0b00001, 0b00010, 0b00100, 0b01000, 0b11111},
	'3': {0b11110, 0b00001, 0b00001, 0b01110, 0b00001, 0b00001, 0b11110},
	'4': {0b00010, 0b00110, 0b01010, 0b10010, 0b11111, 0b00010, 0b00010},
	'5': {0b11111, 0b10000, 0b11110, 0b00001, 0b00001, 0b10001, 0b01110},
	'6': {0b00110, 0b01000, 0b10000, 0b11110, 0b10001, 0b10001, 0b01110},
	'7': {0b11111, 0b00001, 0b00010, 0b00100, 0b01000, 0b01000, 0b01000},
	'8': {0b01110, 0b10001, 0b10001, 0b01110, 0b10001, 0b10001, 0b01110},
	'9': {0b01110, 0b10001, 0b10001, 0b01111, 0b00001, 0b00010, 0b01100},
	' ': {0, 0, 0, 0, 0, 0, 0},
}

const fixtureScale = 5

func putFixtureGlyph(img *image.RGBA, x0, y0 int, g [7]uint8, fg color.RGBA) {
	for ry := 0; ry < 7; ry++ {
		row := g[ry]
		for rx := 0; rx < 5; rx++ {
			if row&(1<<(4-rx)) != 0 {
				for dy := 0; dy < fixtureScale; dy++ {
					for dx := 0; dx < fixtureScale; dx++ {
						img.SetRGBA(x0+rx*fixtureScale+dx, y0+ry*fixtureScale+dy, fg)
					}
				}
			}
		}
	}
}

func putFixtureString(img *image.RGBA, x0, y0 int, s string, fg color.RGBA) {
	x := x0
	for _, r := range s {
		g, ok := fixtureGlyphs[r]
		if !ok {
			g = fixtureGlyphs[' ']
		}
		putFixtureGlyph(img, x, y0, g, fg)
		x += 6 * fixtureScale
	}
}

func writeFixture(t *testing.T, path string, w, h int, bg, fg color.RGBA, str string, x0, y0 int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, bg)
		}
	}
	putFixtureString(img, x0, y0, str, fg)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
}

func TestGenerateFixtures(t *testing.T) {
	dir := filepath.Join("testdata")
	writeFixture(t, filepath.Join(dir, "dark_calc_display.png"), 200, 60,
		color.RGBA{0x1f, 0x1f, 0x1f, 0xff},
		color.RGBA{0xff, 0xff, 0xff, 0xff},
		"40", 75, 13)
	writeFixture(t, filepath.Join(dir, "light_calc_display.png"), 200, 60,
		color.RGBA{0xff, 0xff, 0xff, 0xff},
		color.RGBA{0x00, 0x00, 0x00, 0xff},
		"40", 75, 13)
}
