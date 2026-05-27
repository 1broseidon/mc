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

// TestCombinedChannelVariance pins the texture-gate calibration. A
// flat solid color (the v0.3.2 false-positive trap) must sit at or
// near zero variance and BELOW minimumDesktopVarianceThreshold; a
// gradient or noisy patch must sit clearly ABOVE it.
func TestCombinedChannelVariance(t *testing.T) {
	flat := makeSolidPatch(50, 50, color.RGBA{R: 20, G: 20, B: 20, A: 255})
	if v := combinedChannelVariance(flat); v != 0 {
		t.Fatalf("flat solid color variance = %d, want 0", v)
	}
	if combinedChannelVariance(flat) >= minimumDesktopVarianceThreshold {
		t.Fatalf("flat solid color must fall BELOW the desktop variance gate")
	}

	// A horizontal R gradient 0..255 across 50 columns. Population
	// variance of a uniform 0..255 sample is ((N²-1)/12) ≈ 5439 for
	// N=256. Even after our coarser 50-step sampling it sits in the
	// thousands — comfortably above the gate.
	gradient := makeRedGradientPatch(50, 50)
	gv := combinedChannelVariance(gradient)
	if gv < minimumDesktopVarianceThreshold {
		t.Fatalf("R gradient variance = %d, want >= %d (minimum desktop threshold)", gv, minimumDesktopVarianceThreshold)
	}

	// A mostly-flat patch with one wildly different pixel — should
	// still be below the threshold (a single outlier pixel in 2500
	// shouldn't qualify as "texture").
	noisy := makeSolidPatch(50, 50, color.RGBA{R: 30, G: 30, B: 30, A: 255})
	noisy[1234] = color.RGBA{R: 255, G: 255, B: 255, A: 255}
	if v := combinedChannelVariance(noisy); v >= minimumDesktopVarianceThreshold {
		t.Fatalf("single-pixel outlier variance = %d, want < %d", v, minimumDesktopVarianceThreshold)
	}

	if combinedChannelVariance(nil) != 0 {
		t.Fatalf("variance of nil slice should be 0")
	}
}

// TestColorHistogram confirms the per-channel histogram correctly
// buckets pixels into 8 bins and uses the full 24-slot layout
// (R[0..7], G[8..15], B[16..23]).
func TestColorHistogram(t *testing.T) {
	pixels := []color.RGBA{
		{R: 0, G: 0, B: 0, A: 255},       // R bin 0, G bin 0, B bin 0
		{R: 255, G: 255, B: 255, A: 255}, // R bin 7, G bin 7, B bin 7
		{R: 64, G: 64, B: 64, A: 255},    // bin 2 each
		{R: 128, G: 128, B: 128, A: 255}, // bin 4 each
	}
	hist := colorHistogram(pixels, 8)
	want := map[int]int{
		0: 1, 2: 1, 4: 1, 7: 1, // R
		8: 1, 10: 1, 12: 1, 15: 1, // G
		16: 1, 18: 1, 20: 1, 23: 1, // B
	}
	for i, count := range hist {
		if count != want[i] {
			t.Fatalf("histogram[%d] = %d, want %d", i, count, want[i])
		}
	}

	// Total counts per channel must equal the pixel count (every pixel
	// lands in exactly one R-bin, one G-bin, one B-bin).
	rSum, gSum, bSum := 0, 0, 0
	for i := 0; i < 8; i++ {
		rSum += hist[i]
		gSum += hist[8+i]
		bSum += hist[16+i]
	}
	if rSum != len(pixels) || gSum != len(pixels) || bSum != len(pixels) {
		t.Fatalf("per-channel sums = (%d,%d,%d), want (%d,%d,%d)", rSum, gSum, bSum, len(pixels), len(pixels), len(pixels))
	}
}

// TestHistogramIntersection drives the second gate of the divergence
// check: identical distributions must score 1.0; disjoint
// distributions must score 0.0; a textured wallpaper sampled twice
// must score above histogramSimilarityThreshold; a solid dark patch
// versus a textured wallpaper must score below it (the regression
// guard for the v0.3.2 false-positive class).
func TestHistogramIntersection(t *testing.T) {
	wallpaper := makeRedGradientPatch(50, 50)
	wallHist := colorHistogram(wallpaper, 8)

	// Same patch against itself — must be 1.0 (every pixel falls in
	// the same bin in both histograms).
	if sim := histogramIntersection(wallpaper, wallHist, 8); sim != 1.0 {
		t.Fatalf("self-similarity = %f, want 1.0", sim)
	}

	// Solid black against the gradient — must score well below the
	// threshold. Even though black does land in the lowest R-bin of
	// the gradient too, the G/B channels of black don't overlap with
	// the gradient's "everywhere" distribution heavily.
	black := makeSolidPatch(50, 50, color.RGBA{R: 20, G: 20, B: 20, A: 255})
	blackSim := histogramIntersection(black, wallHist, 8)
	if blackSim >= histogramSimilarityThreshold {
		t.Fatalf("solid-black vs gradient similarity = %f, want < %f", blackSim, histogramSimilarityThreshold)
	}

	// Two independently-generated gradients — should score high (this
	// is the "agent re-samples the same wallpaper twice" case).
	other := makeRedGradientPatch(50, 50)
	if sim := histogramIntersection(other, wallHist, 8); sim < histogramSimilarityThreshold {
		t.Fatalf("two gradient samples similarity = %f, want >= %f", sim, histogramSimilarityThreshold)
	}

	// Disjoint solid colors: dark gray vs white. Must be near 0.
	white := makeSolidPatch(50, 50, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	whiteHist := colorHistogram(white, 8)
	disjoint := histogramIntersection(black, whiteHist, 8)
	if disjoint > 0.05 {
		t.Fatalf("disjoint solid-color similarity = %f, want ≈ 0", disjoint)
	}

	if sim := histogramIntersection(nil, wallHist, 8); sim != 0 {
		t.Fatalf("empty patch similarity = %f, want 0", sim)
	}
}

// TestRectsIntersect is a guard for the candidate-vs-window overlap
// test used by pickDesktopReference. Empty rectangles never intersect.
func TestRectsIntersect(t *testing.T) {
	cases := []struct {
		name string
		a, b contract.Bounds
		want bool
	}{
		{"overlap", contract.Bounds{X: 10, Y: 10, Width: 50, Height: 50}, contract.Bounds{X: 40, Y: 40, Width: 50, Height: 50}, true},
		{"contained", contract.Bounds{X: 0, Y: 0, Width: 200, Height: 200}, contract.Bounds{X: 50, Y: 50, Width: 10, Height: 10}, true},
		{"touching-edge", contract.Bounds{X: 0, Y: 0, Width: 50, Height: 50}, contract.Bounds{X: 50, Y: 0, Width: 50, Height: 50}, false},
		{"disjoint", contract.Bounds{X: 0, Y: 0, Width: 50, Height: 50}, contract.Bounds{X: 200, Y: 200, Width: 50, Height: 50}, false},
		{"empty-a", contract.Bounds{X: 0, Y: 0, Width: 0, Height: 0}, contract.Bounds{X: 0, Y: 0, Width: 50, Height: 50}, false},
		{"empty-b", contract.Bounds{X: 0, Y: 0, Width: 50, Height: 50}, contract.Bounds{X: 0, Y: 0, Width: 0, Height: 0}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rectsIntersect(tc.a, tc.b); got != tc.want {
				t.Fatalf("rectsIntersect(%+v, %+v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestIntersectsAny pins the multi-window filter used by
// pickDesktopReference. A candidate must be rejected when it overlaps
// ANY window in the supplied list, accepted when it overlaps none, and
// accepted when the list is empty (no information => don't filter).
func TestIntersectsAny(t *testing.T) {
	windows := []contract.Bounds{
		{X: 0, Y: 0, Width: 800, Height: 600},
		{X: 1000, Y: 100, Width: 400, Height: 400},
	}
	inside := contract.Bounds{X: 100, Y: 100, Width: 50, Height: 50}
	if !intersectsAny(inside, windows) {
		t.Fatalf("patch inside first window should intersect")
	}
	insideSecond := contract.Bounds{X: 1100, Y: 200, Width: 50, Height: 50}
	if !intersectsAny(insideSecond, windows) {
		t.Fatalf("patch inside second window should intersect")
	}
	outside := contract.Bounds{X: 900, Y: 700, Width: 50, Height: 50}
	if intersectsAny(outside, windows) {
		t.Fatalf("patch in the gap should not intersect any window")
	}
	if intersectsAny(outside, nil) {
		t.Fatalf("empty window list must yield false (no information => don't filter)")
	}
}

// TestTextureGateRejectsSolidWallpaper is the regression guard for
// v0.3.2. A would-be desktop reference patch sampled from a solid
// dark wallpaper (variance ≈ 0) must fail the variance gate even when
// the window patch happens to share the same dark median color —
// because we never use that patch as the reference in the first
// place. Encoded as: solid patches must have variance below
// minimumDesktopVarianceThreshold, and a textured wallpaper sampled
// twice must agree above histogramSimilarityThreshold.
func TestTextureGateRejectsSolidWallpaper(t *testing.T) {
	// Stock "dark Cinnamon wallpaper" surrogate: flat near-black.
	solidWall := makeSolidPatch(50, 50, color.RGBA{R: 18, G: 18, B: 22, A: 255})
	// Brave/VS Code window content: another flat dark region with a
	// near-identical median color.
	darkApp := makeSolidPatch(50, 50, color.RGBA{R: 22, G: 22, B: 22, A: 255})

	if combinedChannelVariance(solidWall) >= minimumDesktopVarianceThreshold {
		t.Fatalf("solid wallpaper would have been accepted as desktop reference — regression of v0.3.2")
	}

	// And confirm that even IF the variance gate had let it through,
	// the histogram check would have still flagged the dark-app patch
	// as similar — so the variance gate is what saves us from false
	// positives on solid rigs.
	histSolid := colorHistogram(solidWall, 8)
	if sim := histogramIntersection(darkApp, histSolid, 8); sim < histogramSimilarityThreshold {
		t.Fatalf("two solid dark patches with similar medians should score high on histogram intersection; got %f — this means the variance gate is the ONLY guard against the v0.3.2 false-positive class, which is intentional", sim)
	}
}

// TestTextureGateAcceptsTexturedWallpaper is the positive case for
// the variance gate: a textured wallpaper (gradient or photo
// surrogate) must clear the threshold and produce a usable desktop
// reference.
func TestTextureGateAcceptsTexturedWallpaper(t *testing.T) {
	wall := makeRedGradientPatch(50, 50)
	v := combinedChannelVariance(wall)
	if v < minimumDesktopVarianceThreshold {
		t.Fatalf("textured wallpaper variance = %d, want >= %d", v, minimumDesktopVarianceThreshold)
	}
}

// TestStuckGioSurfaceFiresWarning is the synthetic Gio failure-mode
// fixture: textured wallpaper exposed at bottom-right WM-bounds, dark
// app content rendered at top-left only. The window-patch sample at
// the stale WM corner is identical to the wallpaper sample, so both
// gates (color tolerance + histogram intersection) must agree the
// patch matches the desktop reference.
//
// Calibration window: the v0.3.1 gates (tolerance ±15, fraction ≥0.7)
// require that the wallpaper be tonally coherent enough that ≥70% of
// pixels fall within ±15 of the median ON ALL THREE channels. At the
// same time the v0.3.4 variance gate requires combined channel
// variance ≥ 90. The fixture below uses ±10 jitter per channel:
// per-channel variance ≈ 10·11/3 ≈ 37, sum ≈ 110 (clears variance
// gate); tolerance ≥ jitter, so 100% of pixels match per-channel.
// This intentionally models the "subtle texture" band that the
// heuristic is designed for; wildly high-contrast wallpapers fall
// outside the calibrated window and produce undecidable results
// (which is the documented trade-off — see
// minimumDesktopVarianceThreshold).
func TestStuckGioSurfaceFiresWarning(t *testing.T) {
	wallRef := makeTexturedPatch(50, 50, color.RGBA{R: 80, G: 70, B: 60, A: 255}, 10, 1)
	wallMedian := medianColor(wallRef)
	wallHist := colorHistogram(wallRef, 8)

	// The Gio surface is stuck at top-left, so the patch at the
	// bottom-right interior corner of WM-bounds is just more
	// wallpaper. Generate a second crop drawn from the same
	// distribution to simulate sampling the same wallpaper twice.
	exposedCorner := makeTexturedPatch(50, 50, color.RGBA{R: 80, G: 70, B: 60, A: 255}, 10, 2)

	// Color tolerance gate.
	matches := countColorMatches(exposedCorner, wallMedian, divergenceMatchTolerance)
	frac := float64(matches) / float64(len(exposedCorner))
	if frac < divergenceMatchFraction {
		t.Fatalf("exposed wallpaper at WM corner should match wallpaper color; got fraction %f, want >= %f", frac, divergenceMatchFraction)
	}

	// Histogram gate.
	sim := histogramIntersection(exposedCorner, wallHist, 8)
	if sim < histogramSimilarityThreshold {
		t.Fatalf("exposed wallpaper histogram should match wallpaper reference; got %f, want >= %f", sim, histogramSimilarityThreshold)
	}

	// And the wallpaper itself must clear the variance gate so it
	// would be accepted as a reference in the first place.
	if combinedChannelVariance(wallRef) < minimumDesktopVarianceThreshold {
		t.Fatalf("wallpaper variance below gate; can't pick as reference")
	}
}

// TestDarkAppPatchVsTexturedWallpaperHistogramReject confirms the
// second gate (histogram intersection) catches the failure mode
// where the per-channel color-tolerance gate fires on a dark app
// surface but the underlying distributions don't actually match: the
// app patch is a tight spike in one bin per channel while the
// wallpaper spreads across multiple bins.
//
// For this differentiation to be visible we need the wallpaper to
// span more than one bin per channel (bin width = 256/8 = 32). The
// fixture below uses jitter ±24, so per-channel pixel range is ~48
// wide — crosses 1-2 bin boundaries — and per-channel variance ≈
// 24·25/3 ≈ 200 (sum ≈ 600, well above the variance gate). A solid
// app at the wallpaper median lives entirely in one bin per channel
// and overlaps only the wallpaper's primary bin.
func TestDarkAppPatchVsTexturedWallpaperHistogramReject(t *testing.T) {
	wallRef := makeTexturedPatch(50, 50, color.RGBA{R: 96, G: 96, B: 96, A: 255}, 24, 1)
	wallHist := colorHistogram(wallRef, 8)
	wallMedian := medianColor(wallRef)

	// Dark app patch: same median tone but near-solid (tight cluster
	// around the median, no variance to speak of). A few outlier
	// pixels keep it from being a literal 100% match.
	appPatch := makeSolidPatch(50, 50, wallMedian)
	for i := 0; i < 100; i++ {
		appPatch[i*25] = color.RGBA{R: wallMedian.R + 5, G: wallMedian.G - 3, B: wallMedian.B + 2, A: 255}
	}

	// Histogram gate must REJECT — single-bin spike vs spread-across-
	// bins distribution.
	sim := histogramIntersection(appPatch, wallHist, 8)
	if sim >= histogramSimilarityThreshold {
		t.Fatalf("dark-app patch should fail histogram gate vs textured wallpaper; got similarity %f >= %f", sim, histogramSimilarityThreshold)
	}
}

// TestDarkAppOnSolidWallpaperNoWarning is the regression guard for
// the v0.3.2 false-positive class: a dark Electron app rendered on
// top of a solid-color wallpaper must NOT fire the warning, because
// the desktop reference selection step refuses to use a solid-color
// patch as the reference. We encode the contract directly: the
// variance gate rejects the candidate.
func TestDarkAppOnSolidWallpaperNoWarning(t *testing.T) {
	solid := makeSolidPatch(50, 50, color.RGBA{R: 18, G: 18, B: 18, A: 255})
	if combinedChannelVariance(solid) >= minimumDesktopVarianceThreshold {
		t.Fatalf("solid wallpaper accepted as desktop reference; would re-introduce v0.3.2 false positives")
	}
}

// --- helpers ---------------------------------------------------------

// makeSolidPatch returns a w*h pixel slice of identical RGBA values.
func makeSolidPatch(w, h int, c color.RGBA) []color.RGBA {
	out := make([]color.RGBA, w*h)
	for i := range out {
		out[i] = c
	}
	return out
}

// makeRedGradientPatch returns a w*h pixel slice whose R channel
// ramps from 0..255 across columns, with constant G and B. The result
// has high combined variance (R sweeps the full 0..255 range) so it
// clears the desktop-variance gate.
func makeRedGradientPatch(w, h int) []color.RGBA {
	out := make([]color.RGBA, 0, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r := uint8(x * 255 / (w - 1))
			out = append(out, color.RGBA{R: r, G: 60, B: 90, A: 255})
		}
	}
	return out
}

// makeTexturedPatch returns a w*h pixel slice whose pixels are
// deterministically jittered ±jitter per channel around mean. The
// seed parameter lets a test draw two independent samples from the
// same distribution (modeling "same wallpaper sampled twice in
// different regions"). Each channel value is wrapped/clamped to
// [0,255]. The resulting patch has per-channel variance ≈ jitter²/3
// (uniform-jitter variance), and pixel-by-pixel deviation from the
// per-channel median is uniformly distributed in [-jitter, +jitter],
// so ≈ (2·tolerance+1) / (2·jitter+1) of pixels land within the
// color-tolerance window. With jitter=20 and tolerance=15 that's
// ~75%, comfortably above the 70% gate.
func makeTexturedPatch(w, h int, mean color.RGBA, jitter int, seed int64) []color.RGBA {
	out := make([]color.RGBA, 0, w*h)
	// Linear-congruential generator — fixed sequence per seed, no
	// stdlib rand dependency, deterministic across Go versions.
	state := uint64(seed) * 2862933555777941757
	next := func() int {
		state = state*6364136223846793005 + 1442695040888963407
		v := int(state >> 33)
		mod := 2*jitter + 1
		return (v % mod) - jitter
	}
	clamp := func(v int) uint8 {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return uint8(v)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out = append(out, color.RGBA{
				R: clamp(int(mean.R) + next()),
				G: clamp(int(mean.G) + next()),
				B: clamp(int(mean.B) + next()),
				A: 255,
			})
		}
	}
	return out
}
