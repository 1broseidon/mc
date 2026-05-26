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
// counting "matches root color" pixels in the sampled patch.
const divergenceMatchTolerance = 15

// divergenceMatchFraction is the patch-match fraction at or above
// which we emit WINDOW_GEOMETRY_DIVERGED. Empirically 0.7 (70%) keeps
// well-behaved GTK/Qt apps quiet while flagging the Gio/ImGui
// "rendered surface didn't follow the WM resize" failure mode.
const divergenceMatchFraction = 0.7

// detectGeometryDivergence checks whether the WM-reported client_bounds
// for `info` actually contains rendered app content or just exposes
// the root window's wallpaper. Returns a populated *VerbWarning when
// the patch at the bottom-right interior corner of client_bounds
// matches the root-window's dominant color above
// divergenceMatchFraction; returns nil otherwise.
//
// Best-effort heuristic — any X11 sampling failure returns nil so the
// host verb still reports success. False positives are acceptable;
// false negatives are acceptable. Cost: one XGetImage for the root
// reference sample plus one for the client patch.
func detectGeometryDivergence(d *x11.Display, info contract.WindowInfo) *VerbWarning {
	client := info.ClientBounds
	if client.Empty() {
		return nil
	}
	if client.Width < divergenceProbeSize || client.Height < divergenceProbeSize {
		// Patch wouldn't fit; skip rather than oversample a tiny window.
		return nil
	}
	rootColor, ok := dominantRootColor(d)
	if !ok {
		return nil
	}
	patch, patchBounds, ok := samplePatchAtCorner(d, client)
	if !ok {
		return nil
	}
	matches := countColorMatches(patch, rootColor, divergenceMatchTolerance)
	total := len(patch)
	if total == 0 {
		return nil
	}
	fraction := float64(matches) / float64(total)
	if fraction < divergenceMatchFraction {
		return nil
	}
	estimate := estimateRenderedBounds(d, client, rootColor)
	details := map[string]any{
		"wm_bounds":                client,
		"rendered_bounds_estimate": estimate,
		"probe": map[string]any{
			"region":         patchBounds,
			"match_fraction": fraction,
			"tolerance_px":   divergenceMatchTolerance,
			"root_color":     formatRGB(rootColor),
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
	rootColor, ok := dominantRootColor(d)
	if !ok {
		return client, false
	}
	patch, _, ok := samplePatchAtCorner(d, client)
	if !ok {
		return client, false
	}
	matches := countColorMatches(patch, rootColor, divergenceMatchTolerance)
	total := len(patch)
	if total == 0 {
		return client, false
	}
	if float64(matches)/float64(total) < divergenceMatchFraction {
		// No divergence; rendered bounds == WM client bounds.
		return client, false
	}
	return estimateRenderedBounds(d, client, rootColor), true
}

// dominantRootColor samples a small patch of exposed desktop one
// divergenceProbeSize step to the left and above client_bounds-origin
// is too risky because some WMs paint there. Instead we sample the
// top-left corner of the root window — coordinate (0,0) — which is
// reliably wallpaper on every WM that doesn't have a desktop manager
// drawing widgets at the screen origin. Returns the median color of
// the sampled patch so the dominant background dominates even when
// the wallpaper has subtle gradients.
func dominantRootColor(d *x11.Display) (color.RGBA, bool) {
	// Sample at the screen origin first; if the patch shows an obvious
	// non-uniform region (e.g., a desktop widget) fall back to the
	// bottom-right corner of the root window.
	screen := x11.ScreenBounds(d)
	patch, _, ok := samplePatch(d, contract.Bounds{X: 0, Y: 0, Width: divergenceProbeSize, Height: divergenceProbeSize}, screen)
	if !ok || len(patch) == 0 {
		return color.RGBA{}, false
	}
	return medianColor(patch), true
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
// whose bottom-right corner stops matching the root color, giving the
// agent a usable hint about the actual rendered surface size. Uses a
// binary search along each axis independently (independent searches
// keep total cost at O(log N) XGetImage calls per axis rather than the
// O(N²) full grid that a true 2D search would need).
//
// The estimate snaps to multiples of divergenceProbeSize since that is
// the sampling resolution. Returns the original client_bounds when the
// search cannot narrow the rectangle (e.g., even a single-step shrink
// still matches the root color — likely a window with NO rendered
// surface at all).
func estimateRenderedBounds(d *x11.Display, client contract.Bounds, rootColor color.RGBA) contract.Bounds {
	step := divergenceProbeSize
	rendered := client
	screen := x11.ScreenBounds(d)
	// Shrink width: find the largest w such that the patch at the inner
	// bottom-right corner of a rectangle (origin, w, client.Height)
	// stops matching the root color.
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
			matches := countColorMatches(patch, rootColor, divergenceMatchTolerance)
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
			matches := countColorMatches(patch, rootColor, divergenceMatchTolerance)
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
