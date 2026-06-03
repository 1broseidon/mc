package window

import (
	"context"
	"image"
	"image/color"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
)

// divergenceProbeSize is the square edge length (in pixels) of the
// patches sampled at the bottom-right interior corner of client_bounds
// and at the desktop reference candidate locations. Small enough that
// each grab round-trip stays cheap; large enough that a stable
// per-channel variance and histogram emerge from real wallpaper.
const divergenceProbeSize = 50

// divergenceMatchTolerance is the per-channel tolerance used when
// counting "matches desktop reference color" pixels in the sampled
// window patch. Wide enough to absorb JPEG/scaling noise on wallpaper
// photos.
const divergenceMatchTolerance = 15

// divergenceMatchFraction is the patch-match fraction at or above which
// the per-channel color check votes "patch ≈ desktop". 0.7 was the
// empirically calibrated threshold for the Gio/ImGui "rendered surface
// didn't follow the WM resize" failure mode; retained as one of two
// gates (the histogram check below is the second).
const divergenceMatchFraction = 0.7

// minimumDesktopVarianceThreshold is the lower bound on combined
// (R+G+B) per-channel variance that a candidate patch must exceed to be
// eligible as the desktop reference. Rejects solid/near-solid samples
// that would let any dark app trivially "match" the reference. When no
// candidate clears this bar the divergence check returns nil — the
// heuristic is undecidable on a solid-color rig.
const minimumDesktopVarianceThreshold = 90

// histogramBinsPerChannel controls the resolution of the per-channel
// color histogram used as the second gate. 8 bins per channel (×3 = 24)
// groups 32 consecutive intensity values per bin — coarse enough to be
// robust to per-pixel noise, fine enough to distinguish a textured
// wallpaper from a solid app surface.
const histogramBinsPerChannel = 8

// histogramSimilarityThreshold is the lower bound on per-channel
// histogram intersection averaged across R/G/B. 0.8 was calibrated so
// two crops of the same wallpaper agree above it while a dark app
// surface vs. textured wallpaper falls clearly below it.
const histogramSimilarityThreshold = 0.8

// detectGeometryDivergence checks whether the WM-reported client_bounds
// for `info` actually contains rendered app content or just exposes the
// root window's wallpaper. Returns a populated *VerbWarning when a
// textured desktop reference can be located AND the window patch at the
// bottom-right interior corner of client_bounds matches it under BOTH the
// color-tolerance gate AND the histogram-intersection gate.
//
// When no candidate desktop patch exceeds minimumDesktopVarianceThreshold
// the heuristic is undecidable and the function returns nil. Pixel access
// goes through the platform screen backend, so the heuristic is portable.
func detectGeometryDivergence(ctx context.Context, info contract.WindowInfo) *VerbWarning {
	client := info.ClientBounds
	if client.Empty() {
		return nil
	}
	if client.Width < divergenceProbeSize || client.Height < divergenceProbeSize {
		return nil
	}
	ref, ok := pickDesktopReference(ctx, info.XID)
	if !ok {
		return nil
	}
	patch, patchBounds, ok := samplePatchAtCorner(ctx, client)
	if !ok {
		return nil
	}
	matches := countColorMatches(patch, ref.median, divergenceMatchTolerance)
	total := len(patch)
	if total == 0 {
		return nil
	}
	fraction := float64(matches) / float64(total)
	if fraction < divergenceMatchFraction {
		return nil
	}
	histSim := histogramIntersection(patch, ref.histogram, histogramBinsPerChannel)
	if histSim < histogramSimilarityThreshold {
		return nil
	}
	estimate := estimateRenderedBounds(ctx, client, ref.median)
	details := map[string]any{
		"wm_bounds":                client,
		"rendered_bounds_estimate": estimate,
		"probe": map[string]any{
			"region":               patchBounds,
			"match_fraction":       fraction,
			"tolerance_px":         divergenceMatchTolerance,
			"histogram_similarity": histSim,
			"desktop_reference":    ref.region,
			"desktop_variance":     ref.variance,
			"desktop_color":        formatRGB(ref.median),
		},
		"suggestion": "app may not be tracking ConfigureNotify; coordinate-based clicks at WM bounds may miss the rendered surface",
	}
	return &VerbWarning{
		Code:    contract.WindowGeometryDivergedCode,
		Message: "rendered surface appears smaller than WM-reported client_bounds; fall back to find_color/find_text targeting",
		Details: details,
	}
}

// EstimateRenderedBounds returns a best-effort inner rectangle where the
// window's rendered surface stops, based on the texture-aware
// patch-vs-desktop heuristic used by detectGeometryDivergence. Exported so
// the `windows --detect-rendered` flag can attach a
// `rendered_bounds_estimate` to each window record. Returns the input
// bounds (and ok=false) when the heuristic cannot run or no divergence is
// detected.
func EstimateRenderedBounds(info contract.WindowInfo) (contract.Bounds, bool) {
	ctx := context.Background()
	client := info.ClientBounds
	if client.Empty() || client.Width < divergenceProbeSize || client.Height < divergenceProbeSize {
		return client, false
	}
	ref, ok := pickDesktopReference(ctx, info.XID)
	if !ok {
		return client, false
	}
	patch, _, ok := samplePatchAtCorner(ctx, client)
	if !ok {
		return client, false
	}
	matches := countColorMatches(patch, ref.median, divergenceMatchTolerance)
	total := len(patch)
	if total == 0 {
		return client, false
	}
	if float64(matches)/float64(total) < divergenceMatchFraction {
		return client, false
	}
	if histogramIntersection(patch, ref.histogram, histogramBinsPerChannel) < histogramSimilarityThreshold {
		return client, false
	}
	return estimateRenderedBounds(ctx, client, ref.median), true
}

// desktopReference bundles the result of pickDesktopReference: the region
// sampled, its median color, its per-channel variance sum, and its color
// histogram.
type desktopReference struct {
	region    contract.Bounds
	median    color.RGBA
	variance  int
	histogram [histogramBinsPerChannel * 3]int
}

// pickDesktopReference samples up to three candidate desktop patches at
// locations usually exposed as wallpaper (bottom and right screen edges,
// far from origin so a Gio surface stuck at top-left can't bleed in).
// Candidates intersecting any known window's bounds are dropped. Of the
// survivors, the highest-variance patch is returned — provided variance
// exceeds minimumDesktopVarianceThreshold; otherwise ok=false.
//
// excludeXID is the window currently being checked; its own bounds are
// not used to filter candidates. When the window list cannot be
// enumerated the function proceeds without the filter and relies on the
// variance gate as the backstop.
func pickDesktopReference(ctx context.Context, excludeXID uint32) (desktopReference, bool) {
	screen, err := platform.Current().Screen().ScreenBounds(ctx)
	if err != nil {
		return desktopReference{}, false
	}
	if screen.Width < divergenceProbeSize*2 || screen.Height < divergenceProbeSize*2 {
		return desktopReference{}, false
	}
	margin := divergenceProbeSize / 5
	candidates := []contract.Bounds{
		{
			X:      screen.Width/2 - divergenceProbeSize/2,
			Y:      screen.Height - divergenceProbeSize - margin,
			Width:  divergenceProbeSize,
			Height: divergenceProbeSize,
		},
		{
			X:      screen.Width - divergenceProbeSize - margin,
			Y:      screen.Height/2 - divergenceProbeSize/2,
			Width:  divergenceProbeSize,
			Height: divergenceProbeSize,
		},
		{
			X:      screen.Width / 4,
			Y:      screen.Height - divergenceProbeSize - margin,
			Width:  divergenceProbeSize,
			Height: divergenceProbeSize,
		},
	}
	occupied := otherWindowBounds(ctx, excludeXID)
	var best *desktopReference
	for _, region := range candidates {
		if intersectsAny(region, occupied) {
			continue
		}
		patch, clamped, ok := samplePatch(ctx, region)
		if !ok || len(patch) == 0 {
			continue
		}
		variance := combinedChannelVariance(patch)
		if variance < minimumDesktopVarianceThreshold {
			continue
		}
		if best == nil || variance > best.variance {
			candidate := desktopReference{
				region:    clamped,
				median:    medianColor(patch),
				variance:  variance,
				histogram: colorHistogram(patch, histogramBinsPerChannel),
			}
			best = &candidate
		}
	}
	if best == nil {
		return desktopReference{}, false
	}
	return *best, true
}

// otherWindowBounds returns the outer bounds of visible top-level windows
// other than excludeXID, so pickDesktopReference can skip candidate
// patches that would land inside an app surface. Best-effort: a failed
// list returns nil and the variance gate is the backstop.
//
// NOTE: this no longer filters iconified windows by ICCCM WM_STATE (that
// check was X11-specific and lived in the pre-seam implementation). A
// minimized window whose stale bounds still describe a large rectangle
// can therefore over-exclude candidates; the variance gate prevents a
// false positive, so the only effect is occasionally returning nil
// (undecidable) instead of a reference — a safe degradation.
func otherWindowBounds(ctx context.Context, excludeXID uint32) []contract.Bounds {
	wins, err := List(ctx)
	if err != nil {
		return nil
	}
	out := make([]contract.Bounds, 0, len(wins))
	for _, w := range wins {
		if w.XID == excludeXID {
			continue
		}
		if w.Bounds.Empty() {
			continue
		}
		out = append(out, w.Bounds)
	}
	return out
}

// intersectsAny reports whether `region` overlaps any rectangle in
// `others`.
func intersectsAny(region contract.Bounds, others []contract.Bounds) bool {
	for _, o := range others {
		if rectsIntersect(region, o) {
			return true
		}
	}
	return false
}

// rectsIntersect is the standard AABB overlap test.
func rectsIntersect(a, b contract.Bounds) bool {
	if a.Empty() || b.Empty() {
		return false
	}
	return a.X < b.X+b.Width &&
		b.X < a.X+a.Width &&
		a.Y < b.Y+b.Height &&
		b.Y < a.Y+a.Height
}

// combinedChannelVariance returns the sum of per-channel population
// variances across R, G, B. Higher values indicate textured content;
// near-zero indicates a solid color. See minimumDesktopVarianceThreshold.
func combinedChannelVariance(pixels []color.RGBA) int {
	n := len(pixels)
	if n == 0 {
		return 0
	}
	var sumR, sumG, sumB int
	for _, p := range pixels {
		sumR += int(p.R)
		sumG += int(p.G)
		sumB += int(p.B)
	}
	meanR := sumR / n
	meanG := sumG / n
	meanB := sumB / n
	var varR, varG, varB int
	for _, p := range pixels {
		dr := int(p.R) - meanR
		dg := int(p.G) - meanG
		db := int(p.B) - meanB
		varR += dr * dr
		varG += dg * dg
		varB += db * db
	}
	return (varR + varG + varB) / n
}

// colorHistogram returns a flat per-channel histogram (R bins, then G,
// then B). With bins=8 the 24-element slice splits [0,255] into 32-wide
// buckets per channel.
func colorHistogram(pixels []color.RGBA, bins int) [histogramBinsPerChannel * 3]int {
	var hist [histogramBinsPerChannel * 3]int
	if bins != histogramBinsPerChannel {
		return hist
	}
	binWidth := 256 / bins
	if binWidth <= 0 {
		binWidth = 1
	}
	for _, p := range pixels {
		ri := int(p.R) / binWidth
		gi := int(p.G) / binWidth
		bi := int(p.B) / binWidth
		if ri >= bins {
			ri = bins - 1
		}
		if gi >= bins {
			gi = bins - 1
		}
		if bi >= bins {
			bi = bins - 1
		}
		hist[ri]++
		hist[bins+gi]++
		hist[2*bins+bi]++
	}
	return hist
}

// histogramIntersection compares a patch histogram against a reference
// and returns a similarity score in [0,1], computed per channel as
// sum(min(patch_bin, ref_bin))/total averaged across R/G/B.
func histogramIntersection(patch []color.RGBA, ref [histogramBinsPerChannel * 3]int, bins int) float64 {
	if len(patch) == 0 || bins != histogramBinsPerChannel {
		return 0
	}
	patchHist := colorHistogram(patch, bins)
	total := len(patch)
	if total == 0 {
		return 0
	}
	var sims [3]float64
	for c := 0; c < 3; c++ {
		offset := c * bins
		var inter int
		for i := 0; i < bins; i++ {
			a := patchHist[offset+i]
			b := ref[offset+i]
			if a < b {
				inter += a
			} else {
				inter += b
			}
		}
		sims[c] = float64(inter) / float64(total)
	}
	return (sims[0] + sims[1] + sims[2]) / 3.0
}

// samplePatchAtCorner samples a divergenceProbeSize patch anchored to the
// bottom-right interior corner of the supplied client_bounds.
func samplePatchAtCorner(ctx context.Context, client contract.Bounds) ([]color.RGBA, contract.Bounds, bool) {
	patchBounds := contract.Bounds{
		X:      client.X + client.Width - divergenceProbeSize,
		Y:      client.Y + client.Height - divergenceProbeSize,
		Width:  divergenceProbeSize,
		Height: divergenceProbeSize,
	}
	return samplePatch(ctx, patchBounds)
}

// samplePatch grabs a rectangle of screen pixels through the platform
// backend and returns the raw RGBA slice plus the actual captured bounds
// (after the backend clamps to the screen). ok=false when the region is
// empty/offscreen or the grab failed.
func samplePatch(ctx context.Context, region contract.Bounds) ([]color.RGBA, contract.Bounds, bool) {
	if region.Empty() {
		return nil, contract.Bounds{}, false
	}
	img, captured, err := platform.Current().Screen().Grab(ctx, region)
	if err != nil || img == nil {
		return nil, contract.Bounds{}, false
	}
	return rgbaPixels(img), captured, true
}

// rgbaPixels flattens an image.RGBA into a []color.RGBA in row-major
// order. The platform backend already forces alpha to 255.
func rgbaPixels(img *image.RGBA) []color.RGBA {
	b := img.Bounds()
	out := make([]color.RGBA, 0, b.Dx()*b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			out = append(out, img.RGBAAt(x, y))
		}
	}
	return out
}

// medianColor returns the per-channel median of the supplied pixels.
func medianColor(pixels []color.RGBA) color.RGBA {
	if len(pixels) == 0 {
		return color.RGBA{A: 255}
	}
	rs := make([]uint8, len(pixels))
	gs := make([]uint8, len(pixels))
	bs := make([]uint8, len(pixels))
	for i, p := range pixels {
		rs[i] = p.R
		gs[i] = p.G
		bs[i] = p.B
	}
	return color.RGBA{
		R: medianU8(rs),
		G: medianU8(gs),
		B: medianU8(bs),
		A: 255,
	}
}

func medianU8(values []uint8) uint8 {
	var counts [256]int
	for _, v := range values {
		counts[v]++
	}
	target := (len(values) + 1) / 2
	acc := 0
	for i := 0; i < 256; i++ {
		acc += counts[i]
		if acc >= target {
			return uint8(i)
		}
	}
	return 0
}

// countColorMatches counts pixels within `tolerance` of `ref` on every
// channel.
func countColorMatches(pixels []color.RGBA, ref color.RGBA, tolerance int) int {
	matches := 0
	for _, p := range pixels {
		if absDiff(int(p.R), int(ref.R)) <= tolerance &&
			absDiff(int(p.G), int(ref.G)) <= tolerance &&
			absDiff(int(p.B), int(ref.B)) <= tolerance {
			matches++
		}
	}
	return matches
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

// estimateRenderedBounds binary-searches the rectangle inside
// client_bounds whose bottom-right corner stops matching the desktop
// reference color, snapping to multiples of divergenceProbeSize. Width is
// searched first, then height using the narrowed width. Returns the input
// bounds when the search cannot narrow the rectangle.
func estimateRenderedBounds(ctx context.Context, client contract.Bounds, desktopColor color.RGBA) contract.Bounds {
	step := divergenceProbeSize
	rendered := client
	if client.Width > step {
		lo, hi := step, client.Width
		for lo < hi {
			mid := (lo + hi + 1) / 2
			region := contract.Bounds{
				X:      client.X + mid - step,
				Y:      client.Y + client.Height - step,
				Width:  step,
				Height: step,
			}
			patch, _, ok := samplePatch(ctx, region)
			if !ok {
				break
			}
			matches := countColorMatches(patch, desktopColor, divergenceMatchTolerance)
			fraction := float64(matches) / float64(len(patch))
			if fraction >= divergenceMatchFraction {
				hi = mid - 1
			} else {
				lo = mid
			}
		}
		if lo < rendered.Width {
			rendered.Width = lo
		}
	}
	if client.Height > step {
		lo, hi := step, client.Height
		for lo < hi {
			mid := (lo + hi + 1) / 2
			region := contract.Bounds{
				X:      client.X + rendered.Width - step,
				Y:      client.Y + mid - step,
				Width:  step,
				Height: step,
			}
			patch, _, ok := samplePatch(ctx, region)
			if !ok {
				break
			}
			matches := countColorMatches(patch, desktopColor, divergenceMatchTolerance)
			fraction := float64(matches) / float64(len(patch))
			if fraction >= divergenceMatchFraction {
				hi = mid - 1
			} else {
				lo = mid
			}
		}
		if lo < rendered.Height {
			rendered.Height = lo
		}
	}
	return rendered
}

// formatRGB renders an RGBA color as `#RRGGBB` for human-readable debug
// fields. Alpha is dropped since the sampler always fills it with 255.
func formatRGB(c color.RGBA) string {
	const hex = "0123456789abcdef"
	return string([]byte{
		'#',
		hex[c.R>>4], hex[c.R&0x0f],
		hex[c.G>>4], hex[c.G&0x0f],
		hex[c.B>>4], hex[c.B&0x0f],
	})
}
