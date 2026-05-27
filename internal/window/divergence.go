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
// patch sampled at the bottom-right interior corner of client_bounds
// during the WINDOW_GEOMETRY_DIVERGED post-op check. Small enough that
// the extra XGetImage round-trip stays cheap; large enough that a
// distinct dominant color reliably emerges.
const divergenceProbeSize = 50

// divergenceMatchTolerance is the per-channel tolerance used when
// counting "matches background color" pixels in the sampled patch.
const divergenceMatchTolerance = 15

// divergenceMatchFraction is the patch-match fraction at or above
// which we emit WINDOW_GEOMETRY_DIVERGED. Empirically 0.7 (70%) keeps
// well-behaved GTK/Qt apps quiet while flagging the Gio/ImGui
// "rendered surface didn't follow the WM resize" failure mode.
const divergenceMatchFraction = 0.7

// divergenceEdgeConsensusMin is the number of edge probes that must
// agree on the exposed-background color before the no-outside-desktop
// fallback fires. This keeps the fullscreen/maximized path from trusting
// one lucky corner sample.
const divergenceEdgeConsensusMin = 3

// divergenceAnchorMaxMatchFraction is the maximum allowed match between
// the top-left client anchor and the exposed-background color in the
// edge-consensus fallback. A matching anchor means the window may simply
// have a uniform app background, not a stale smaller rendered surface.
const divergenceAnchorMaxMatchFraction = 0.4

// divergenceMinFallbackShrinkPx and divergenceMinRenderedExtentPx keep
// the edge-consensus fallback conservative: without an outside-client
// desktop reference, only warn when both axes shrink materially and the
// estimated rendered surface is large enough to plausibly be the old app
// surface rather than a toolbar/sidebar sliver.
const (
	divergenceMinFallbackShrinkPx = divergenceProbeSize
	divergenceMinRenderedExtentPx = divergenceProbeSize * 2
)

type patchSampler func(contract.Bounds) ([]color.RGBA, contract.Bounds, bool)

type backgroundSample struct {
	color  color.RGBA
	region contract.Bounds
	source string
}

type divergenceAnalysis struct {
	background          color.RGBA
	backgroundSource    string
	backgroundRegion    contract.Bounds
	probeRegion         contract.Bounds
	matchFraction       float64
	edgeConsensus       int
	edgeProbes          int
	anchorMatchFraction float64
	estimate            contract.Bounds
}

// detectGeometryDivergence checks whether the WM-reported client_bounds
// for `info` actually contains rendered app content or exposed desktop.
// It first compares the bottom-right client patch against desktop
// samples that are outside the client rectangle. When a maximized window
// leaves no outside desktop to sample, it falls back to a conservative
// multi-edge consensus: several client-edge patches must agree on the
// same exposed-background color while the top-left anchor differs.
//
// Best-effort heuristic: X11 sampling failures return nil so the host
// verb still reports success. The warning is advisory and includes a
// rendered_bounds_estimate for agents that need a safer target region.
func detectGeometryDivergence(d *x11.Display, info contract.WindowInfo) *VerbWarning {
	client := info.ClientBounds
	if client.Empty() {
		return nil
	}
	if client.Width < divergenceProbeSize || client.Height < divergenceProbeSize {
		// Patch wouldn't fit; skip rather than oversample a tiny window.
		return nil
	}
	screen := x11.ScreenBounds(d)
	sampler := func(region contract.Bounds) ([]color.RGBA, contract.Bounds, bool) {
		return samplePatch(d, region, screen)
	}
	analysis, ok := analyzeGeometryDivergence(client, screen, sampler)
	if !ok {
		return nil
	}
	return newDivergenceWarning(client, analysis)
}

// EstimateRenderedBounds returns a best-effort inner rectangle where
// the window's rendered surface stops, based on the same patch-vs-root
// heuristic used by detectGeometryDivergence. Exported so the
// `windows --detect-rendered` flag can attach a `rendered_bounds_estimate`
// to each window record. Returns the input bounds (and ok=false) when
// the heuristic cannot run or no divergence is detected.
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
	screen := x11.ScreenBounds(d)
	sampler := func(region contract.Bounds) ([]color.RGBA, contract.Bounds, bool) {
		return samplePatch(d, region, screen)
	}
	analysis, ok := analyzeGeometryDivergence(client, screen, sampler)
	if !ok {
		return client, false
	}
	return analysis.estimate, true
}

func analyzeGeometryDivergence(client, screen contract.Bounds, sampler patchSampler) (*divergenceAnalysis, bool) {
	if client.Empty() || client.Width < divergenceProbeSize || client.Height < divergenceProbeSize {
		return nil, false
	}
	cornerPatch, cornerBounds, ok := samplePatchAtCornerWith(client, sampler)
	if !ok || len(cornerPatch) == 0 {
		return nil, false
	}

	var best *divergenceAnalysis
	for _, bg := range desktopBackgroundCandidates(client, screen, sampler) {
		fraction := patchMatchFraction(cornerPatch, bg.color)
		if fraction < divergenceMatchFraction {
			continue
		}
		analysis := &divergenceAnalysis{
			background:       bg.color,
			backgroundSource: bg.source,
			backgroundRegion: bg.region,
			probeRegion:      cornerBounds,
			matchFraction:    fraction,
			estimate:         estimateRenderedBoundsWithSampler(client, bg.color, sampler),
		}
		if best == nil || analysis.matchFraction > best.matchFraction {
			best = analysis
		}
	}
	if best != nil {
		return best, true
	}
	return edgeConsensusDivergence(client, screen, sampler, cornerPatch, cornerBounds)
}

func newDivergenceWarning(client contract.Bounds, analysis *divergenceAnalysis) *VerbWarning {
	probe := map[string]any{
		"region":            analysis.probeRegion,
		"match_fraction":    analysis.matchFraction,
		"tolerance_px":      divergenceMatchTolerance,
		"root_color":        formatRGB(analysis.background),
		"background_color":  formatRGB(analysis.background),
		"background_source": analysis.backgroundSource,
	}
	if !analysis.backgroundRegion.Empty() {
		probe["background_region"] = analysis.backgroundRegion
	}
	if analysis.edgeProbes > 0 {
		probe["edge_consensus"] = analysis.edgeConsensus
		probe["edge_probes"] = analysis.edgeProbes
		probe["anchor_match_fraction"] = analysis.anchorMatchFraction
	}
	details := map[string]any{
		"wm_bounds":                client,
		"rendered_bounds_estimate": analysis.estimate,
		"probe":                    probe,
		"suggestion":               "app may not be tracking ConfigureNotify; coordinate-based clicks at WM bounds may miss the rendered surface",
	}
	return &VerbWarning{
		Code:    contract.WindowGeometryDivergedCode,
		Message: "rendered surface appears smaller than WM-reported client_bounds; fall back to find_color/find_text targeting",
		Details: details,
	}
}

func desktopBackgroundCandidates(client, screen contract.Bounds, sampler patchSampler) []backgroundSample {
	if screen.Empty() {
		return nil
	}
	step := divergenceProbeSize
	regions := []contract.Bounds{
		{X: screen.X, Y: screen.Y, Width: step, Height: step},
		{X: screen.X + screen.Width - step, Y: screen.Y, Width: step, Height: step},
		{X: screen.X, Y: screen.Y + screen.Height - step, Width: step, Height: step},
		{X: screen.X + screen.Width - step, Y: screen.Y + screen.Height - step, Width: step, Height: step},
		{X: screen.X + (screen.Width-step)/2, Y: screen.Y, Width: step, Height: step},
		{X: screen.X + (screen.Width-step)/2, Y: screen.Y + screen.Height - step, Width: step, Height: step},
		{X: screen.X, Y: screen.Y + (screen.Height-step)/2, Width: step, Height: step},
		{X: screen.X + screen.Width - step, Y: screen.Y + (screen.Height-step)/2, Width: step, Height: step},
	}
	var out []backgroundSample
	for _, region := range uniqueProbeRegions(regions, screen) {
		if rectsIntersect(region, client) {
			continue
		}
		patch, actual, ok := sampler(region)
		if !ok || len(patch) == 0 {
			continue
		}
		out = append(out, backgroundSample{
			color:  medianColor(patch),
			region: actual,
			source: "desktop_reference",
		})
	}
	return out
}

func edgeConsensusDivergence(client, screen contract.Bounds, sampler patchSampler, cornerPatch []color.RGBA, cornerBounds contract.Bounds) (*divergenceAnalysis, bool) {
	background := medianColor(cornerPatch)
	regions := edgeProbeRegions(client, screen)
	if len(regions) < divergenceEdgeConsensusMin {
		return nil, false
	}
	consensus := 0
	for _, region := range regions {
		patch, _, ok := sampler(region)
		if !ok || len(patch) == 0 {
			continue
		}
		if patchMatchFraction(patch, background) >= divergenceMatchFraction {
			consensus++
		}
	}
	if consensus < divergenceEdgeConsensusMin {
		return nil, false
	}
	anchorPatch, _, ok := sampler(contract.Bounds{X: client.X, Y: client.Y, Width: divergenceProbeSize, Height: divergenceProbeSize})
	if !ok || len(anchorPatch) == 0 {
		return nil, false
	}
	anchorFraction := patchMatchFraction(anchorPatch, background)
	if anchorFraction >= divergenceAnchorMaxMatchFraction {
		return nil, false
	}
	estimate := estimateRenderedBoundsWithSampler(client, background, sampler)
	shrinkW := client.Width - estimate.Width
	shrinkH := client.Height - estimate.Height
	if shrinkW < divergenceMinFallbackShrinkPx || shrinkH < divergenceMinFallbackShrinkPx {
		return nil, false
	}
	if estimate.Width < divergenceMinRenderedExtentPx || estimate.Height < divergenceMinRenderedExtentPx {
		return nil, false
	}
	return &divergenceAnalysis{
		background:          background,
		backgroundSource:    "edge_consensus",
		probeRegion:         cornerBounds,
		matchFraction:       patchMatchFraction(cornerPatch, background),
		edgeConsensus:       consensus,
		edgeProbes:          len(regions),
		anchorMatchFraction: anchorFraction,
		estimate:            estimate,
	}, true
}

// samplePatchAtCornerWith samples a divergenceProbeSize patch anchored to
// the bottom-right interior corner of the supplied client_bounds via the
// caller-supplied sampler. Returns the raw pixel slice plus the actual
// patch bounds (after clamping to the screen).
func samplePatchAtCornerWith(client contract.Bounds, sampler patchSampler) ([]color.RGBA, contract.Bounds, bool) {
	patchBounds := contract.Bounds{
		X:      client.X + client.Width - divergenceProbeSize,
		Y:      client.Y + client.Height - divergenceProbeSize,
		Width:  divergenceProbeSize,
		Height: divergenceProbeSize,
	}
	return sampler(patchBounds)
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

func patchMatchFraction(pixels []color.RGBA, ref color.RGBA) float64 {
	if len(pixels) == 0 {
		return 0
	}
	return float64(countColorMatches(pixels, ref, divergenceMatchTolerance)) / float64(len(pixels))
}

func edgeProbeRegions(client, screen contract.Bounds) []contract.Bounds {
	step := divergenceProbeSize
	regions := []contract.Bounds{
		{X: client.X + client.Width - step, Y: client.Y + client.Height - step, Width: step, Height: step},   // bottom-right
		{X: client.X + client.Width - step, Y: client.Y, Width: step, Height: step},                          // top-right
		{X: client.X, Y: client.Y + client.Height - step, Width: step, Height: step},                         // bottom-left
		{X: client.X + client.Width - step, Y: client.Y + (client.Height-step)/2, Width: step, Height: step}, // right-middle
		{X: client.X + (client.Width-step)/2, Y: client.Y + client.Height - step, Width: step, Height: step}, // bottom-middle
	}
	return uniqueProbeRegions(regions, screen)
}

func uniqueProbeRegions(regions []contract.Bounds, screen contract.Bounds) []contract.Bounds {
	seen := map[contract.Bounds]bool{}
	out := make([]contract.Bounds, 0, len(regions))
	for _, region := range regions {
		clamped := clampToScreen(region, screen)
		if clamped.Empty() || seen[clamped] {
			continue
		}
		seen[clamped] = true
		out = append(out, clamped)
	}
	return out
}

func rectsIntersect(a, b contract.Bounds) bool {
	if a.Empty() || b.Empty() {
		return false
	}
	return a.X < b.X+b.Width &&
		a.X+a.Width > b.X &&
		a.Y < b.Y+b.Height &&
		a.Y+a.Height > b.Y
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

// estimateRenderedBoundsWithSampler searches for a top-left anchored
// rendered rectangle inside client_bounds. It looks along the top edge
// for the width boundary and along the left edge for the height boundary
// instead of probing only the bottom-right corner. That matters for the
// stale Gio/ImGui failure mode: the entire bottom band can be exposed
// desktop, so a bottom-row width search collapses to one probe even when
// the old rendered surface is still visible in the top-left.
//
// The estimate snaps to multiples of divergenceProbeSize since that is
// the sampling resolution. Returns the original client_bounds when the
// search cannot narrow the rectangle (e.g., even a single-step shrink
// still matches the background color — likely a window with NO rendered
// surface at all).
func estimateRenderedBoundsWithSampler(client contract.Bounds, background color.RGBA, sampler patchSampler) contract.Bounds {
	step := divergenceProbeSize
	rendered := client
	// Shrink width: find the largest w whose top-edge patch still looks
	// like rendered content rather than exposed background.
	if client.Width > step {
		lo, hi := step, client.Width
		for lo < hi {
			mid := (lo + hi + 1) / 2
			region := contract.Bounds{
				X:      client.X + mid - step,
				Y:      client.Y,
				Width:  step,
				Height: step,
			}
			patch, _, ok := sampler(region)
			if !ok || len(patch) == 0 {
				break
			}
			if patchMatchFraction(patch, background) >= divergenceMatchFraction {
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
	// Shrink height the same way down the left edge.
	if client.Height > step {
		lo, hi := step, client.Height
		for lo < hi {
			mid := (lo + hi + 1) / 2
			region := contract.Bounds{
				X:      client.X,
				Y:      client.Y + mid - step,
				Width:  step,
				Height: step,
			}
			patch, _, ok := sampler(region)
			if !ok || len(patch) == 0 {
				break
			}
			if patchMatchFraction(patch, background) >= divergenceMatchFraction {
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
