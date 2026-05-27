package window

import (
	"image/color"
	"testing"

	"github.com/1broseidon/mc/internal/contract"
)

// TestCountColorMatches verifies the tolerance check on per-channel
// distance — exact matches, within-tolerance matches, and outliers
// must all be classified correctly. This is the core arithmetic that
// drives the WINDOW_GEOMETRY_DIVERGED >=70% fraction decision.
func TestCountColorMatches(t *testing.T) {
	ref := color.RGBA{R: 100, G: 150, B: 200, A: 255}
	pixels := []color.RGBA{
		{R: 100, G: 150, B: 200, A: 255}, // exact match
		{R: 110, G: 140, B: 210, A: 255}, // within ±15
		{R: 80, G: 130, B: 180, A: 255},  // within ±20 (rejected at tol=15)
		{R: 0, G: 0, B: 0, A: 255},       // wildly off
		{R: 115, G: 165, B: 215, A: 255}, // edge of ±15
	}
	if got, want := countColorMatches(pixels, ref, 15), 3; got != want {
		t.Fatalf("matches at tol=15 = %d, want %d", got, want)
	}
	if got, want := countColorMatches(pixels, ref, 0), 1; got != want {
		t.Fatalf("matches at tol=0 = %d, want %d", got, want)
	}
	if got, want := countColorMatches(pixels, ref, 255), len(pixels); got != want {
		t.Fatalf("matches at tol=255 = %d, want %d", got, want)
	}
}

// TestMedianColor confirms the median is robust to outliers — the
// dominant background color should win even when a few pixels carry
// stray content (taskbar widgets, mouse cursor in the wallpaper
// reference patch).
func TestMedianColor(t *testing.T) {
	pixels := []color.RGBA{
		{R: 30, G: 30, B: 30, A: 255},
		{R: 32, G: 31, B: 28, A: 255},
		{R: 31, G: 30, B: 29, A: 255},
		{R: 30, G: 30, B: 30, A: 255},
		{R: 200, G: 100, B: 50, A: 255}, // outlier
	}
	got := medianColor(pixels)
	if got.R < 28 || got.R > 35 {
		t.Fatalf("R median should be around 30, got %d", got.R)
	}
	if got.G < 28 || got.G > 35 {
		t.Fatalf("G median should be around 30, got %d", got.G)
	}
	if got.B < 25 || got.B > 32 {
		t.Fatalf("B median should be around 30, got %d", got.B)
	}
	if got.A != 255 {
		t.Fatalf("median should preserve A=255, got %d", got.A)
	}
	if c := medianColor(nil); c.A != 255 {
		t.Fatalf("nil pixel slice should yield opaque zero color, got %+v", c)
	}
}

// TestFormatRGB confirms the human-readable color encoding used in
// divergence detail payloads.
func TestFormatRGB(t *testing.T) {
	cases := []struct {
		in   color.RGBA
		want string
	}{
		{color.RGBA{R: 0, G: 0, B: 0, A: 255}, "#000000"},
		{color.RGBA{R: 255, G: 255, B: 255, A: 255}, "#ffffff"},
		{color.RGBA{R: 0xab, G: 0xcd, B: 0xef, A: 255}, "#abcdef"},
		{color.RGBA{R: 18, G: 52, B: 86, A: 255}, "#123456"},
	}
	for _, tc := range cases {
		if got := formatRGB(tc.in); got != tc.want {
			t.Fatalf("formatRGB(%+v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestClampToScreen mirrors the screen-package clamp logic; mostly a
// guard to catch off-by-one changes in the divergence sampler.
func TestClampToScreen(t *testing.T) {
	screen := contract.Bounds{X: 0, Y: 0, Width: 1920, Height: 1080}
	cases := []struct {
		name string
		in   contract.Bounds
		want contract.Bounds
	}{
		{"inside", contract.Bounds{X: 100, Y: 100, Width: 50, Height: 50}, contract.Bounds{X: 100, Y: 100, Width: 50, Height: 50}},
		{"left-edge", contract.Bounds{X: -20, Y: 100, Width: 50, Height: 50}, contract.Bounds{X: 0, Y: 100, Width: 30, Height: 50}},
		{"top-edge", contract.Bounds{X: 100, Y: -10, Width: 50, Height: 50}, contract.Bounds{X: 100, Y: 0, Width: 50, Height: 40}},
		{"right-edge", contract.Bounds{X: 1900, Y: 100, Width: 50, Height: 50}, contract.Bounds{X: 1900, Y: 100, Width: 20, Height: 50}},
		{"bottom-edge", contract.Bounds{X: 100, Y: 1050, Width: 50, Height: 50}, contract.Bounds{X: 100, Y: 1050, Width: 50, Height: 30}},
		{"fully out", contract.Bounds{X: 5000, Y: 5000, Width: 50, Height: 50}, contract.Bounds{X: 5000, Y: 5000, Width: 0, Height: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clampToScreen(tc.in, screen)
			// Negative widths (fully out case) are not normalized — the
			// "empty" semantics is captured by Bounds.Empty(), not by
			// nullifying x/y. Compare the empty bit, not the exact rect.
			if got.Empty() && tc.want.Empty() {
				return
			}
			if got != tc.want {
				t.Fatalf("clampToScreen(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestAnalyzeGeometryDivergence_EdgeConsensusFallbackWhenFullscreenOriginCovered(t *testing.T) {
	screen := contract.Bounds{X: 0, Y: 0, Width: 500, Height: 400}
	client := screen
	rendered := contract.Bounds{X: 0, Y: 0, Width: 220, Height: 170}
	app := color.RGBA{R: 180, G: 40, B: 40, A: 255}
	desktop := color.RGBA{R: 24, G: 70, B: 110, A: 255}
	sampler := syntheticDivergenceSampler(screen, func(x, y int) color.RGBA {
		if rendered.Contains(x, y) {
			return app
		}
		return desktop
	})

	if got := desktopBackgroundCandidates(client, screen, sampler); len(got) != 0 {
		t.Fatalf("fullscreen client should leave no outside desktop candidates, got %d", len(got))
	}
	analysis, ok := analyzeGeometryDivergence(client, screen, sampler)
	if !ok {
		t.Fatal("expected edge-consensus fallback to detect stale top-left rendered surface")
	}
	if analysis.backgroundSource != "edge_consensus" {
		t.Fatalf("backgroundSource = %q, want edge_consensus", analysis.backgroundSource)
	}
	if analysis.edgeConsensus < divergenceEdgeConsensusMin {
		t.Fatalf("edgeConsensus = %d, want >= %d", analysis.edgeConsensus, divergenceEdgeConsensusMin)
	}
	if analysis.anchorMatchFraction >= divergenceAnchorMaxMatchFraction {
		t.Fatalf("anchorMatchFraction = %.3f, want below %.3f", analysis.anchorMatchFraction, divergenceAnchorMaxMatchFraction)
	}
	estimate := analysis.estimate
	if estimate.Width < 200 || estimate.Width > 280 {
		t.Fatalf("estimate width = %d, want near rendered width 220", estimate.Width)
	}
	if estimate.Height < 150 || estimate.Height > 230 {
		t.Fatalf("estimate height = %d, want near rendered height 170", estimate.Height)
	}
}

func TestAnalyzeGeometryDivergence_NoWarningForUniformFullscreenSurface(t *testing.T) {
	screen := contract.Bounds{X: 0, Y: 0, Width: 500, Height: 400}
	client := screen
	app := color.RGBA{R: 245, G: 245, B: 245, A: 255}
	sampler := syntheticDivergenceSampler(screen, func(x, y int) color.RGBA {
		return app
	})

	if analysis, ok := analyzeGeometryDivergence(client, screen, sampler); ok {
		t.Fatalf("uniform fullscreen app surface should not warn, got %+v", analysis)
	}
}

func TestAnalyzeGeometryDivergence_UsesOutsideDesktopWhenAvailable(t *testing.T) {
	screen := contract.Bounds{X: 0, Y: 0, Width: 600, Height: 420}
	client := contract.Bounds{X: 80, Y: 60, Width: 400, Height: 280}
	rendered := contract.Bounds{X: 80, Y: 60, Width: 230, Height: 170}
	app := color.RGBA{R: 40, G: 150, B: 80, A: 255}
	desktop := color.RGBA{R: 20, G: 30, B: 70, A: 255}
	sampler := syntheticDivergenceSampler(screen, func(x, y int) color.RGBA {
		if rendered.Contains(x, y) {
			return app
		}
		return desktop
	})

	analysis, ok := analyzeGeometryDivergence(client, screen, sampler)
	if !ok {
		t.Fatal("expected outside-desktop reference to detect exposed client corner")
	}
	if analysis.backgroundSource != "desktop_reference" {
		t.Fatalf("backgroundSource = %q, want desktop_reference", analysis.backgroundSource)
	}
	if analysis.matchFraction < divergenceMatchFraction {
		t.Fatalf("matchFraction = %.3f, want >= %.3f", analysis.matchFraction, divergenceMatchFraction)
	}
}

func syntheticDivergenceSampler(screen contract.Bounds, pixel func(x, y int) color.RGBA) patchSampler {
	return func(region contract.Bounds) ([]color.RGBA, contract.Bounds, bool) {
		clamped := clampToScreen(region, screen)
		if clamped.Empty() {
			return nil, contract.Bounds{}, false
		}
		out := make([]color.RGBA, 0, clamped.Width*clamped.Height)
		for y := clamped.Y; y < clamped.Y+clamped.Height; y++ {
			for x := clamped.X; x < clamped.X+clamped.Width; x++ {
				out = append(out, pixel(x, y))
			}
		}
		return out, clamped, true
	}
}
