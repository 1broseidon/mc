package window

import (
	"encoding/binary"
	"image/color"
	"math/bits"

	"github.com/jezek/xgb/xproto"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/x11"
)

// divergenceProbeSize is the square edge length (in pixels) of the
// patches sampled at the bottom-right interior corner of client_bounds
// and at the desktop reference candidate locations. Small enough that
// each XGetImage round-trip stays cheap; large enough that a stable
// per-channel variance and histogram emerge from real wallpaper.
const divergenceProbeSize = 50

// divergenceMatchTolerance is the per-channel tolerance used when
// counting "matches desktop reference color" pixels in the sampled
// window patch. Mirrors the v0.3.1 tolerance — wide enough to absorb
// JPEG/scaling noise on wallpaper photos.
const divergenceMatchTolerance = 15

// divergenceMatchFraction is the patch-match fraction at or above
// which the per-channel color check votes "patch ≈ desktop". 0.7 was
// the empirically calibrated threshold in v0.3.1 for the Gio/ImGui
// "rendered surface didn't follow the WM resize" failure mode; we
// retain it as one of two gates (the histogram check below is the
// second).
const divergenceMatchFraction = 0.7

// minimumDesktopVarianceThreshold is the lower bound on combined
// (R+G+B) per-channel variance that a candidate patch must exceed to
// be eligible as the desktop reference. The intent is to reject solid
// or near-solid color samples that would let any dark/black app
// trivially "match" the reference (the v0.3.2 false-positive failure
// mode). A flat black or solid color patch has per-channel variance
// ≈0; a subtle gradient has per-channel variance in the low tens; a
// real photo/wallpaper with texture or shading has per-channel
// variance in the hundreds-to-thousands range. 30 per channel (×3 =
// 90 summed) sits comfortably above flat/solid and below any real
// wallpaper we've observed. When no candidate clears this bar the
// divergence check returns nil — the heuristic is undecidable on a
// solid-color rig.
const minimumDesktopVarianceThreshold = 90

// histogramBinsPerChannel controls the resolution of the per-channel
// color histogram used as the second gate of the divergence check.
// 8 bins per channel (×3 = 24 bins) groups 32 consecutive intensity
// values per bin — coarse enough to be robust to per-pixel noise,
// fine enough to distinguish a textured wallpaper from a solid black
// or dark-gray app surface.
const histogramBinsPerChannel = 8

// histogramSimilarityThreshold is the lower bound on per-channel
// histogram intersection (sum of min(window_bin, desktop_bin) divided
// by total pixels, averaged across R/G/B). 0.8 was calibrated so that
// (a) two crops of the same wallpaper agree well above this bound,
// (b) a dark app surface vs. a textured wallpaper falls clearly below
// it, even when the median colors happen to match.
const histogramSimilarityThreshold = 0.8

// detectGeometryDivergence checks whether the WM-reported client_bounds
// for `info` actually contains rendered app content or just exposes
// the root window's wallpaper. Returns a populated *VerbWarning when:
//
//   - A textured (high-variance) desktop reference patch can be located
//     outside known windows;
//   - The window patch at the bottom-right interior corner of
//     client_bounds matches that reference under BOTH the per-channel
//     color tolerance gate AND the histogram-intersection gate.
//
// When no candidate desktop patch exceeds minimumDesktopVarianceThreshold
// the heuristic is undecidable (solid wallpaper + dark UI is
// indistinguishable from a stale Gio surface from pixels alone) and
// the function returns nil. The verb still reports success — the agent
// should not assume bounds are reliable in that case, but we refuse to
// false-positive on every dark app.
//
// Cost: up to 3 XGetImage calls for desktop candidates + 1 for the
// window patch. The EstimateRenderedBounds binary search runs only
// when a warning would actually fire.
func detectGeometryDivergence(d *x11.Display, info contract.WindowInfo) *VerbWarning {
	client := info.ClientBounds
	if client.Empty() {
		return nil
	}
	if client.Width < divergenceProbeSize || client.Height < divergenceProbeSize {
		// Patch wouldn't fit; skip rather than oversample a tiny window.
		return nil
	}
	ref, ok := pickDesktopReference(d, info.XID)
	if !ok {
		// Undecidable case: no high-variance desktop sample available.
		// Don't false-positive AND don't false-negative — return nil and
		// let the agent see "no warning" as "we could not tell."
		return nil
	}
	patch, patchBounds, ok := samplePatchAtCorner(d, client)
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
		// Color medians agree but value distributions don't — the window
		// patch is some other dark/solid surface, not the wallpaper.
		return nil
	}
	estimate := estimateRenderedBounds(d, client, ref.median)
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

// EstimateRenderedBounds returns a best-effort inner rectangle where
// the window's rendered surface stops, based on the texture-aware
// patch-vs-desktop heuristic used by detectGeometryDivergence. Exported
// so the `windows --detect-rendered` flag can attach a
// `rendered_bounds_estimate` to each window record. Returns the input
// bounds (and ok=false) when the heuristic cannot run (undecidable
// desktop, sampling failure) or when no divergence is detected.
func EstimateRenderedBounds(info contract.WindowInfo) (contract.Bounds, bool) {
	d, err := x11.Open()
	if err != nil {
		return info.ClientBounds, false
	}
	defer d.Close()
	client := info.ClientBounds
	if client.Empty() || client.Width < divergenceProbeSize || client.Height < divergenceProbeSize {
		return client, false
	}
	ref, ok := pickDesktopReference(d, info.XID)
	if !ok {
		return client, false
	}
	patch, _, ok := samplePatchAtCorner(d, client)
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
	return estimateRenderedBounds(d, client, ref.median), true
}

// desktopReference bundles the result of pickDesktopReference: the
// region we sampled, its median color, its per-channel variance sum,
// and its color histogram. detectGeometryDivergence uses median for
// the color-tolerance gate and histogram for the distribution gate;
// estimateRenderedBounds uses just the median when binary-searching
// the rendered edge.
type desktopReference struct {
	region    contract.Bounds
	median    color.RGBA
	variance  int
	histogram [histogramBinsPerChannel * 3]int
}

// pickDesktopReference samples up to three candidate desktop patches at
// locations that are usually exposed wallpaper (bottom and right
// screen edges, well away from origin so a Gio surface stuck at
// top-left can't bleed into the sample). Candidates that intersect any
// known window's bounds are dropped. Of the surviving candidates,
// the one with the highest combined-channel variance is returned —
// PROVIDED that variance exceeds minimumDesktopVarianceThreshold. A
// "winning" patch with insufficient variance means the wallpaper is
// effectively solid color and the divergence heuristic cannot
// distinguish it from a dark app surface; in that case ok=false and
// the caller aborts.
//
// excludeXID is the XID of the window currently being checked; its
// bounds are not used to filter candidates (the function is about to
// sample inside its client_bounds anyway), but other visible windows
// ARE used. When the window list cannot be enumerated (no atoms, X11
// error) the function proceeds without the filter and relies on the
// variance gate to reject candidates that landed inside an app
// surface.
func pickDesktopReference(d *x11.Display, excludeXID uint32) (desktopReference, bool) {
	screen := x11.ScreenBounds(d)
	if screen.Width < divergenceProbeSize*2 || screen.Height < divergenceProbeSize*2 {
		// Screen too small to host any candidate that wouldn't overlap a
		// fullscreen app — bail out.
		return desktopReference{}, false
	}
	// Three candidate locations chosen on the bottom and right screen
	// edges, far from the screen origin (so a Gio surface stuck at
	// (0,0) cannot poison every sample). Each rectangle is clamped to
	// the screen by samplePatch().
	margin := divergenceProbeSize / 5 // small inset off the absolute edge
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
	// Best-effort exclusion of candidates that lie inside any other
	// visible window. Failure to enumerate is non-fatal; variance gate
	// is the backstop.
	occupied := otherWindowBounds(d, excludeXID)
	var best *desktopReference
	for _, region := range candidates {
		if intersectsAny(region, occupied) {
			continue
		}
		patch, clamped, ok := samplePatch(d, region, screen)
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

// otherWindowBounds enumerates visible top-level windows via
// _NET_CLIENT_LIST and returns their outer Bounds (decoration-included
// rectangles) so pickDesktopReference can skip candidate patches that
// would land inside an app surface. excludeXID is filtered out so a
// fullscreen-but-stuck window doesn't make every candidate look
// occupied. Iconified (minimized) windows are also dropped — they sit
// in _NET_CLIENT_LIST but their geometry no longer reflects something
// the user can see, so excluding their bounds prevents a minimized
// fullscreen app (e.g., parsecd in the system tray) from blocking
// every candidate. Errors are non-fatal — the function returns
// whatever it could read, and a nil slice if it could read nothing.
func otherWindowBounds(d *x11.Display, excludeXID uint32) []contract.Bounds {
	atomMap, err := atoms(d.Conn)
	if err != nil {
		return nil
	}
	ids, err := clientList(d.Conn, d.Screen.Root, atomMap)
	if err != nil {
		return nil
	}
	out := make([]contract.Bounds, 0, len(ids))
	for _, id := range ids {
		if uint32(id) == excludeXID {
			continue
		}
		if !isWindowVisible(d, id) {
			continue
		}
		info, err := infoFor(d.Conn, d.Screen.Root, id, atomMap, 0)
		if err != nil {
			continue
		}
		if info.Bounds.Empty() {
			continue
		}
		out = append(out, info.Bounds)
	}
	return out
}

// isWindowVisible reports whether `id` is currently in NormalState per
// ICCCM 4.1.4 (WM_STATE). Iconified and withdrawn windows return
// false; windows that lack the property (some override-redirect
// surfaces) default to true so we err on the side of filtering
// candidates that might overlap real UI. Any X11 error falls back to
// true for the same reason.
func isWindowVisible(d *x11.Display, id xproto.Window) bool {
	wmState, err := x11.InternAtom(d.Conn, "WM_STATE")
	if err != nil {
		return true
	}
	reply, err := xproto.GetProperty(d.Conn, false, id, wmState, wmState, 0, 2).Reply()
	if err != nil || len(reply.Value) < 4 {
		return true
	}
	// ICCCM WM_STATE layout: state CARD32 (offset 0), icon WINDOW (offset 4).
	// state: 0=Withdrawn, 1=Normal, 3=Iconic.
	state := uint32(reply.Value[0]) | uint32(reply.Value[1])<<8 | uint32(reply.Value[2])<<16 | uint32(reply.Value[3])<<24
	return state == 1
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

// combinedChannelVariance returns the sum of per-channel variances
// across R, G, B in the supplied patch. Variance is computed as the
// population variance E[(X-μ)²]. Higher values indicate more textured
// content (gradient, photo, noise); near-zero values indicate a solid
// or near-solid color. See minimumDesktopVarianceThreshold for the
// calibration rationale.
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

// colorHistogram returns a flat per-channel histogram for the supplied
// patch: bins for R first, then G, then B, each of length bins. With
// bins=8, the 24-element slice splits the [0,255] range into 32-wide
// buckets per channel.
func colorHistogram(pixels []color.RGBA, bins int) [histogramBinsPerChannel * 3]int {
	var hist [histogramBinsPerChannel * 3]int
	if bins != histogramBinsPerChannel {
		// Defensive: the public API takes a parameter but the array size
		// is fixed by the package-level constant. Callers always pass
		// histogramBinsPerChannel; this branch keeps the function honest.
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

// histogramIntersection compares the histogram of `patch` against a
// reference histogram and returns a similarity score in [0, 1] where
// 1 means identical distributions and 0 means disjoint. The score is
// computed per channel as sum(min(patch_bin, ref_bin))/total_pixels
// and averaged across R, G, B. This metric is well-behaved when the
// two patches have the same number of pixels (which they do — both are
// divergenceProbeSize × divergenceProbeSize).
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

// samplePatchAtCorner samples a divergenceProbeSize patch anchored to
// the bottom-right interior corner of the supplied client_bounds.
// Returns the raw pixel slice plus the actual patch bounds (after
// clamping to the screen).
func samplePatchAtCorner(d *x11.Display, client contract.Bounds) ([]color.RGBA, contract.Bounds, bool) {
	patchBounds := contract.Bounds{
		X:      client.X + client.Width - divergenceProbeSize,
		Y:      client.Y + client.Height - divergenceProbeSize,
		Width:  divergenceProbeSize,
		Height: divergenceProbeSize,
	}
	return samplePatch(d, patchBounds, x11.ScreenBounds(d))
}

// samplePatch grabs a rectangle of root-window pixels and returns the
// raw RGBA slice. Clamps to the screen bounds; returns ok=false when
// the resulting patch would be empty or when XGetImage fails.
func samplePatch(d *x11.Display, region, screen contract.Bounds) ([]color.RGBA, contract.Bounds, bool) {
	clamped := clampToScreen(region, screen)
	if clamped.Empty() {
		return nil, contract.Bounds{}, false
	}
	reply, err := xproto.GetImage(
		d.Conn,
		xproto.ImageFormatZPixmap,
		xproto.Drawable(d.Screen.Root),
		int16(clamped.X), int16(clamped.Y),
		uint16(clamped.Width), uint16(clamped.Height),
		0xffffffff,
	).Reply()
	if err != nil {
		return nil, contract.Bounds{}, false
	}
	visual, ok := x11.RootVisual(d)
	if !ok {
		return nil, contract.Bounds{}, false
	}
	format, ok := x11.PixmapFormat(d, reply.Depth)
	if !ok {
		return nil, contract.Bounds{}, false
	}
	bpp := int(format.BitsPerPixel)
	if bpp != 16 && bpp != 24 && bpp != 32 {
		return nil, contract.Bounds{}, false
	}
	pad := int(format.ScanlinePad)
	if pad <= 0 {
		pad = 32
	}
	rowBits := clamped.Width * bpp
	stride := ((rowBits + pad - 1) / pad) * pad / 8
	if len(reply.Data) < stride*clamped.Height {
		return nil, contract.Bounds{}, false
	}
	little := d.Setup.ImageByteOrder == 0
	out := make([]color.RGBA, 0, clamped.Width*clamped.Height)
	for y := 0; y < clamped.Height; y++ {
		row := reply.Data[y*stride:]
		for x := 0; x < clamped.Width; x++ {
			offset := x * bpp / 8
			pixel := readPixel(row[offset:], bpp, little)
			out = append(out, color.RGBA{
				R: extractChannel(pixel, visual.RedMask),
				G: extractChannel(pixel, visual.GreenMask),
				B: extractChannel(pixel, visual.BlueMask),
				A: 255,
			})
		}
	}
	return out, clamped, true
}

// clampToScreen mirrors screen.clamp without creating an import cycle.
func clampToScreen(region, screen contract.Bounds) contract.Bounds {
	x1 := maxInt(region.X, screen.X)
	y1 := maxInt(region.Y, screen.Y)
	x2 := minInt(region.X+region.Width, screen.X+screen.Width)
	y2 := minInt(region.Y+region.Height, screen.Y+screen.Height)
	return contract.Bounds{X: x1, Y: y1, Width: x2 - x1, Height: y2 - y1}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func readPixel(data []byte, bpp int, little bool) uint32 {
	switch bpp {
	case 16:
		if little {
			return uint32(binary.LittleEndian.Uint16(data[:2]))
		}
		return uint32(binary.BigEndian.Uint16(data[:2]))
	case 24:
		if little {
			return uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16
		}
		return uint32(data[2]) | uint32(data[1])<<8 | uint32(data[0])<<16
	default:
		if little {
			return binary.LittleEndian.Uint32(data[:4])
		}
		return binary.BigEndian.Uint32(data[:4])
	}
}

func extractChannel(pixel uint32, mask uint32) uint8 {
	if mask == 0 {
		return 0
	}
	shift := bits.TrailingZeros32(mask)
	value := (pixel & mask) >> shift
	width := 32 - bits.LeadingZeros32(mask>>shift)
	if width >= 8 {
		return uint8(value >> (width - 8))
	}
	return uint8((value * 255) / ((1 << width) - 1))
}

// medianColor returns the per-channel median of the supplied pixels.
// Cheap and good enough for "what is the dominant background color"
// — outliers (taskbar widgets, mouse cursor, gradients) cannot move
// the median when the wallpaper itself dominates the patch.
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
	// Counting-sort on a [0,255] alphabet is faster and allocation-free
	// vs sort.Slice for these small patches.
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

// estimateRenderedBounds searches for the rectangle inside client_bounds
// whose bottom-right corner stops matching the desktop reference color,
// giving the agent a usable hint about the actual rendered surface
// size. Uses a binary search along each axis independently (independent
// searches keep total cost at O(log N) XGetImage calls per axis rather
// than the O(N²) full grid that a true 2D search would need).
//
// The estimate snaps to multiples of divergenceProbeSize since that is
// the sampling resolution. Returns the original client_bounds when the
// search cannot narrow the rectangle (e.g., even a single-step shrink
// still matches the desktop color — likely a window with NO rendered
// surface at all).
func estimateRenderedBounds(d *x11.Display, client contract.Bounds, desktopColor color.RGBA) contract.Bounds {
	step := divergenceProbeSize
	rendered := client
	screen := x11.ScreenBounds(d)
	// Shrink width: find the largest w such that the patch at the inner
	// bottom-right corner of a rectangle (origin, w, client.Height)
	// stops matching the desktop reference color.
	if client.Width > step {
		lo, hi := step, client.Width
		// First confirm the corner at full width matches (it must, since
		// we only reach this path after detectGeometryDivergence flagged
		// the window). Then binary-search the boundary.
		for lo < hi {
			mid := (lo + hi + 1) / 2
			region := contract.Bounds{
				X:      client.X + mid - step,
				Y:      client.Y + client.Height - step,
				Width:  step,
				Height: step,
			}
			patch, _, ok := samplePatch(d, region, screen)
			if !ok {
				break
			}
			matches := countColorMatches(patch, desktopColor, divergenceMatchTolerance)
			fraction := float64(matches) / float64(len(patch))
			if fraction >= divergenceMatchFraction {
				// Inside the exposed-desktop band; rendered surface is
				// narrower than `mid`.
				hi = mid - 1
			} else {
				// Inside rendered content; rendered surface is at least
				// `mid` wide.
				lo = mid
			}
		}
		if lo < rendered.Width {
			rendered.Width = lo
		}
	}
	// Shrink height the same way using the (now-narrowed) width.
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
			patch, _, ok := samplePatch(d, region, screen)
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

// formatRGB renders an RGBA color as `#RRGGBB` for human-readable
// debug fields. Alpha is dropped since the divergence sampler always
// fills it with 255.
func formatRGB(c color.RGBA) string {
	const hex = "0123456789abcdef"
	return string([]byte{
		'#',
		hex[c.R>>4], hex[c.R&0x0f],
		hex[c.G>>4], hex[c.G&0x0f],
		hex[c.B>>4], hex[c.B&0x0f],
	})
}
