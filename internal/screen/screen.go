package screen

import (
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"

	"github.com/1broseidon/mc/internal/contract"
	imageutil "github.com/1broseidon/mc/internal/image"
	"github.com/1broseidon/mc/internal/window"
	"github.com/1broseidon/mc/internal/x11"
)

// logicalCoordsEnabled is the process-wide toggle for the
// experimental --logical-coords HiDPI translation layer. When true,
// Capture divides screenshot output dimensions by the primary
// monitor's RandR-derived scale and input.Move/Click/Drag multiply
// incoming coordinates by the same scale before XTest. The toggle
// defaults to false; production agents should keep MyComputer in
// physical-pixel mode. Set once during command bootstrap via
// SetLogicalCoords.
var logicalCoordsEnabled bool

// SetLogicalCoords sets the process-wide logical-coords toggle.
// Called once from the CLI/MCP bootstrap after the effective config
// is loaded. Safe to call concurrently before any capture/input
// operation runs.
func SetLogicalCoords(enabled bool) {
	logicalCoordsEnabled = enabled
}

// LogicalCoordsEnabled reports the current state of the toggle so
// sibling packages (input) can decide whether to apply the
// translation. Returns false by default.
func LogicalCoordsEnabled() bool {
	return logicalCoordsEnabled
}

// PrimaryScale returns the RandR-derived scale of the primary
// monitor, or 1.0 when no primary is reported. Best-effort —
// callers should treat any failure as scale=1.0. Used by the
// experimental --logical-coords translation.
func PrimaryScale(ctx context.Context) float64 {
	info, err := Info(ctx)
	if err != nil {
		return 1.0
	}
	for _, mon := range info.Monitors {
		if mon.Primary && mon.Scale > 0 {
			return mon.Scale
		}
	}
	if len(info.Monitors) > 0 && info.Monitors[0].Scale > 0 {
		return info.Monitors[0].Scale
	}
	return 1.0
}

// CaptureRequest is the wire shape for the screenshot/capture verb.
// Region accepts both the v0.1/v0.2 bare-bounds shape
// (`{x,y,width,height}`) AND the task-12 extended shape
// (`{x,y,width,height,space:"window"|"window_frame"|"monitor",target?,monitor_index?}`).
// Both unmarshal into RegionRef; bare-bounds payloads resolve as
// screen-space without translation, preserving v0.1 wire compatibility.
type CaptureRequest struct {
	Out         string             `json:"out,omitempty" jsonschema:"output image path; defaults to a temp file"`
	Region      contract.RegionRef `json:"region,omitzero" jsonschema:"capture region; bare {x,y,width,height} is screen-space (v0.1/v0.2 compat). Extended shape adds space=window|window_frame|monitor plus target or monitor_index for in-window or per-monitor regions."`
	MaxEdge     int                `json:"max_edge,omitempty" jsonschema:"downscale output so the longest edge is at most this value; 0 disables"`
	Zoom        bool               `json:"zoom,omitempty" jsonschema:"capture a square crop centered on zoom_x,zoom_y"`
	ZoomX       int                `json:"zoom_x,omitempty"`
	ZoomY       int                `json:"zoom_y,omitempty"`
	ZoomSize    int                `json:"zoom_size,omitempty"`
	Format      string             `json:"format,omitempty" jsonschema:"png or jpeg; defaults to png"`
	Cursor      bool               `json:"cursor,omitempty" jsonschema:"overlay the current cursor when XFixes is available"`
	JPEGQuality int                `json:"jpeg_quality,omitempty" jsonschema:"JPEG quality from 1 to 100; defaults to 85"`
}

func Info(ctx context.Context) (contract.ScreenInfo, error) {
	if err := ctx.Err(); err != nil {
		return contract.ScreenInfo{}, contract.Cancelled("screen info cancelled")
	}
	d, err := x11.Open()
	if err != nil {
		return contract.ScreenInfo{}, err
	}
	defer d.Close()
	info := contract.ScreenInfo{
		Bounds:  x11.ScreenBounds(d),
		Backend: "x11",
	}
	info.Monitors = collectMonitors(d)
	if len(info.Monitors) == 0 {
		// Fallback: single virtual monitor matching the root window.
		// Always reports primary:true so single-monitor agents have a
		// usable target for point.space="monitor".
		info.Monitors = []contract.MonitorInfo{{
			Index:   0,
			Name:    "root",
			Bounds:  info.Bounds,
			Scale:   1.0,
			Primary: true,
		}}
	}
	return info, nil
}

// collectMonitors enumerates physical monitors via RandR. Refresh
// rate is derived from the CRTC's current mode (DotClock /
// (Htotal * Vtotal)). Scale is derived from the millimeter
// dimensions when available (px/inch divided by the canonical 96
// DPI), and falls back to 1.0 otherwise. Best-effort: any RandR
// failure simply yields an empty slice and the caller falls back to
// a synthesized root monitor.
func collectMonitors(d *x11.Display) []contract.MonitorInfo {
	if err := randr.Init(d.Conn); err != nil {
		return nil
	}
	reply, err := randr.GetMonitors(d.Conn, d.Screen.Root, true).Reply()
	if err != nil {
		return nil
	}
	// Pre-compute mode id -> refresh hz for refresh lookups.
	modeRefresh := map[uint32]int{}
	if res, err := randr.GetScreenResources(d.Conn, d.Screen.Root).Reply(); err == nil {
		for _, m := range res.Modes {
			hz := refreshFromMode(m.DotClock, m.Htotal, m.Vtotal)
			if hz > 0 {
				modeRefresh[m.Id] = hz
			}
		}
	}
	out := make([]contract.MonitorInfo, 0, len(reply.Monitors))
	for i, mon := range reply.Monitors {
		name := ""
		if mon.Name != 0 {
			if atom, err := xproto.GetAtomName(d.Conn, mon.Name).Reply(); err == nil {
				name = atom.Name
			}
		}
		bounds := contract.Bounds{X: int(mon.X), Y: int(mon.Y), Width: int(mon.Width), Height: int(mon.Height)}
		scale := computeScale(bounds.Width, mon.WidthInMillimeters)
		hz := 0
		// Derive refresh rate from the first output's current CRTC mode.
		for _, output := range mon.Outputs {
			if oi, err := randr.GetOutputInfo(d.Conn, output, 0).Reply(); err == nil && oi.Crtc != 0 {
				if ci, err := randr.GetCrtcInfo(d.Conn, oi.Crtc, 0).Reply(); err == nil && ci.Mode != 0 {
					if v, ok := modeRefresh[uint32(ci.Mode)]; ok {
						hz = v
						break
					}
				}
			}
		}
		out = append(out, contract.MonitorInfo{
			Index:     i,
			Name:      name,
			Bounds:    bounds,
			Scale:     scale,
			Primary:   mon.Primary,
			RefreshHz: hz,
		})
	}
	// Defensive: if RandR reports zero primaries but at least one
	// monitor exists (some headless / VNC servers do this), promote
	// index 0 to primary so callers always have a deterministic
	// target.
	if len(out) > 0 {
		hasPrimary := false
		for _, m := range out {
			if m.Primary {
				hasPrimary = true
				break
			}
		}
		if !hasPrimary {
			out[0].Primary = true
		}
	}
	return out
}

func refreshFromMode(dotClock uint32, htotal, vtotal uint16) int {
	if dotClock == 0 || htotal == 0 || vtotal == 0 {
		return 0
	}
	hz := float64(dotClock) / (float64(htotal) * float64(vtotal))
	if hz <= 0 {
		return 0
	}
	return int(hz + 0.5)
}

func computeScale(widthPx int, widthMm uint32) float64 {
	if widthPx <= 0 || widthMm == 0 {
		return 1.0
	}
	const standardDPI = 96.0
	inches := float64(widthMm) / 25.4
	if inches <= 0 {
		return 1.0
	}
	dpi := float64(widthPx) / inches
	if dpi <= 0 {
		return 1.0
	}
	return dpi / standardDPI
}

func Cursor(ctx context.Context) (*contract.Point, error) {
	if err := ctx.Err(); err != nil {
		return nil, contract.Cancelled("cursor query cancelled")
	}
	d, err := x11.Open()
	if err != nil {
		return nil, err
	}
	defer d.Close()
	reply, err := xproto.QueryPointer(d.Conn, d.Screen.Root).Reply()
	if err != nil {
		return nil, contract.Dependency("POINTER_QUERY_FAILED", "failed to query pointer position", map[string]any{"error": err.Error()})
	}
	return &contract.Point{X: int(reply.RootX), Y: int(reply.RootY), Space: "screen"}, nil
}

// ResolveCaptureRegion turns a coord-space-aware RegionRef into an
// absolute screen-space Bounds suitable for XGetImage. Used by Capture
// and by external callers (pipeline.runFind*, wait.WaitForPixelChange,
// wait.WaitForText) that need to translate a window-space or
// monitor-space region into screen coordinates before capturing.
//
// Screen-space (or empty) RegionRefs pass through unchanged.
// Window/window_frame regions require the window list (collected via
// window.List); monitor regions require the monitor list (collected via
// Info). Returns the validation/precondition errors emitted by
// contract.ResolveRegion verbatim.
func ResolveCaptureRegion(ctx context.Context, ref contract.RegionRef) (contract.Bounds, error) {
	switch ref.Space {
	case "", contract.CoordSpaceScreen:
		// Fast path: no extra X11 round-trips when the caller already
		// supplied screen-space coords.
		return contract.ResolveRegion(ref, contract.ResolveContext{})
	case contract.CoordSpaceWindow, contract.CoordSpaceWindowFrame:
		wins, err := window.List(ctx)
		if err != nil {
			return contract.Bounds{}, err
		}
		return contract.ResolveRegion(ref, contract.ResolveContext{Windows: wins})
	case contract.CoordSpaceMonitor:
		info, err := Info(ctx)
		if err != nil {
			return contract.Bounds{}, err
		}
		return contract.ResolveRegion(ref, contract.ResolveContext{Monitors: info.Monitors})
	default:
		// Let ResolveRegion produce the canonical INVALID_COORDINATE_SPACE
		// error so the wire surface is consistent.
		return contract.ResolveRegion(ref, contract.ResolveContext{})
	}
}

// CaptureRGBA grabs the requested screen region into an in-memory RGBA
// image without writing a file. Used by image-finding primitives that
// need pixel data, not an encoded screenshot. The returned bounds are
// the actual capture rectangle in screen coordinates (after clamping).
//
// Accepts a screen-space contract.Bounds directly — callers that have a
// coord-space-aware contract.RegionRef MUST resolve it via
// ResolveCaptureRegion before calling this function so the cache key
// and downstream find_* coord math see consistent screen coordinates.
func CaptureRGBA(ctx context.Context, region contract.Bounds) (*image.RGBA, contract.Bounds, error) {
	if err := ctx.Err(); err != nil {
		return nil, contract.Bounds{}, contract.Cancelled("capture cancelled")
	}
	d, err := x11.Open()
	if err != nil {
		return nil, contract.Bounds{}, err
	}
	defer d.Close()
	capture := region
	if capture.Empty() {
		capture = x11.ScreenBounds(d)
	}
	capture = clamp(capture, x11.ScreenBounds(d))
	if capture.Empty() {
		return nil, contract.Bounds{}, contract.Validation("EMPTY_CAPTURE", "capture region is empty or outside the screen", map[string]any{"region": region})
	}
	reply, err := xproto.GetImage(d.Conn, xproto.ImageFormatZPixmap, xproto.Drawable(d.Screen.Root), int16(capture.X), int16(capture.Y), uint16(capture.Width), uint16(capture.Height), 0xffffffff).Reply()
	if err != nil {
		return nil, contract.Bounds{}, contract.Dependency("SCREENSHOT_FAILED", "failed to capture X11 image", map[string]any{"error": err.Error()})
	}
	img, err := imageFromReply(d, reply, capture.Width, capture.Height)
	if err != nil {
		return nil, contract.Bounds{}, err
	}
	return img, capture, nil
}

func Capture(ctx context.Context, req CaptureRequest) (contract.ScreenshotResult, error) {
	if err := ctx.Err(); err != nil {
		return contract.ScreenshotResult{}, contract.Cancelled("capture cancelled")
	}

	// Resolve the coord-space-aware region BEFORE opening the X
	// connection so resolution failures (target not found, monitor OOB)
	// surface as validation/precondition errors with no side-effects on
	// the X server. Empty regions stay empty; the post-Open fallback
	// below promotes them to the full screen.
	var capture contract.Bounds
	if !req.Region.Empty() {
		resolved, err := ResolveCaptureRegion(ctx, req.Region)
		if err != nil {
			return contract.ScreenshotResult{}, err
		}
		capture = resolved
	}

	d, err := x11.Open()
	if err != nil {
		return contract.ScreenshotResult{}, err
	}
	defer d.Close()

	if capture.Empty() {
		capture = x11.ScreenBounds(d)
	}
	if req.Zoom {
		size := req.ZoomSize
		if size <= 0 {
			size = 400
		}
		capture = contract.Bounds{X: req.ZoomX - size/2, Y: req.ZoomY - size/2, Width: size, Height: size}
	}
	screenBounds := x11.ScreenBounds(d)
	capture = clamp(capture, screenBounds)
	if capture.Empty() {
		return contract.ScreenshotResult{}, contract.Validation("EMPTY_CAPTURE", "capture region is empty or outside the screen", map[string]any{"region": req.Region, "screen": screenBounds})
	}

	reply, err := xproto.GetImage(d.Conn, xproto.ImageFormatZPixmap, xproto.Drawable(d.Screen.Root), int16(capture.X), int16(capture.Y), uint16(capture.Width), uint16(capture.Height), 0xffffffff).Reply()
	if err != nil {
		return contract.ScreenshotResult{}, contract.Dependency("SCREENSHOT_FAILED", "failed to capture X11 image", map[string]any{"error": err.Error()})
	}
	img, err := imageFromReply(d, reply, capture.Width, capture.Height)
	if err != nil {
		return contract.ScreenshotResult{}, err
	}
	if req.Cursor {
		if err := overlayCursor(d, img, capture); err != nil {
			return contract.ScreenshotResult{}, err
		}
	}

	outImg := image.Image(img)
	if req.MaxEdge > 0 {
		outImg = imageutil.DownscaleNearest(img, req.MaxEdge)
	}
	// Experimental --logical-coords: divide screenshot output dims by
	// the primary monitor's RandR scale so HiDPI agents see logical
	// (scaled) pixels. Only effective when scale > 1.0; we never
	// upscale here because that would inflate file sizes without
	// adding information. Applied AFTER req.MaxEdge so callers can
	// still cap the long edge — the smaller of the two budgets wins.
	if logicalCoordsEnabled {
		scale := PrimaryScale(ctx)
		if scale > 1.0 {
			cur := outImg.Bounds()
			longEdge := cur.Dx()
			if cur.Dy() > longEdge {
				longEdge = cur.Dy()
			}
			target := int(float64(longEdge)/scale + 0.5)
			if target > 0 && target < longEdge {
				outImg = imageutil.DownscaleNearest(outImg, target)
			}
		}
	}
	size := contract.Size{Width: outImg.Bounds().Dx(), Height: outImg.Bounds().Dy()}
	format, mime, ext, err := captureFormat(req.Format)
	if err != nil {
		return contract.ScreenshotResult{}, err
	}
	out := req.Out
	if out == "" {
		out = filepath.Join(os.TempDir(), "mycomputer-"+time.Now().UTC().Format("20060102T150405.000000000")+"."+ext)
	}
	if format == "jpeg" {
		err = imageutil.SaveJPEG(out, outImg, req.JPEGQuality)
	} else {
		err = imageutil.SavePNG(out, outImg)
	}
	if err != nil {
		return contract.ScreenshotResult{}, contract.Dependency("SCREENSHOT_ENCODE_FAILED", "failed to encode screenshot image", map[string]any{"path": out, "format": format, "error": err.Error()})
	}
	return contract.ScreenshotResult{
		ImagePath:     out,
		MimeType:      mime,
		CaptureBounds: capture,
		ImageSize:     size,
		CoordMap:      contract.NewCoordMap(capture, size).String(),
		Backend:       "x11.GetImage",
	}, nil
}

func clamp(region, screen contract.Bounds) contract.Bounds {
	x1 := max(region.X, screen.X)
	y1 := max(region.Y, screen.Y)
	x2 := min(region.X+region.Width, screen.X+screen.Width)
	y2 := min(region.Y+region.Height, screen.Y+screen.Height)
	return contract.Bounds{X: x1, Y: y1, Width: x2 - x1, Height: y2 - y1}
}

func captureFormat(value string) (format string, mime string, ext string, err error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "png":
		return "png", "image/png", "png", nil
	case "jpeg", "jpg":
		return "jpeg", "image/jpeg", "jpg", nil
	default:
		return "", "", "", contract.Validation("INVALID_IMAGE_FORMAT", "screenshot format must be png or jpeg", map[string]any{"format": value})
	}
}

func overlayCursor(d *x11.Display, img *image.RGBA, capture contract.Bounds) error {
	if err := xfixes.Init(d.Conn); err != nil {
		return contract.Dependency("CURSOR_OVERLAY_UNAVAILABLE", "XFixes is required for cursor overlay", map[string]any{"error": err.Error()})
	}
	if _, err := xfixes.QueryVersion(d.Conn, 4, 0).Reply(); err != nil {
		return contract.Dependency("CURSOR_OVERLAY_UNAVAILABLE", "failed to negotiate XFixes cursor support", map[string]any{"error": err.Error()})
	}
	reply, err := xfixes.GetCursorImage(d.Conn).Reply()
	if err != nil {
		return contract.Dependency("CURSOR_OVERLAY_FAILED", "failed to read cursor image", map[string]any{"error": err.Error()})
	}
	if reply == nil || reply.Width == 0 || reply.Height == 0 {
		return nil
	}
	left := int(reply.X) - int(reply.Xhot) - capture.X
	top := int(reply.Y) - int(reply.Yhot) - capture.Y
	for y := 0; y < int(reply.Height); y++ {
		for x := 0; x < int(reply.Width); x++ {
			dstX := left + x
			dstY := top + y
			if dstX < 0 || dstY < 0 || dstX >= img.Bounds().Dx() || dstY >= img.Bounds().Dy() {
				continue
			}
			pixel := reply.CursorImage[y*int(reply.Width)+x]
			a := uint8(pixel >> 24)
			if a == 0 {
				continue
			}
			src := color.RGBA{R: uint8(pixel >> 16), G: uint8(pixel >> 8), B: uint8(pixel), A: a}
			dst := img.RGBAAt(dstX, dstY)
			img.SetRGBA(dstX, dstY, alphaOver(src, dst))
		}
	}
	return nil
}

func alphaOver(src color.RGBA, dst color.RGBA) color.RGBA {
	a := int(src.A)
	inv := 255 - a
	return color.RGBA{
		R: uint8((int(src.R)*a + int(dst.R)*inv) / 255),
		G: uint8((int(src.G)*a + int(dst.G)*inv) / 255),
		B: uint8((int(src.B)*a + int(dst.B)*inv) / 255),
		A: 255,
	}
}

func imageFromReply(d *x11.Display, reply *xproto.GetImageReply, width, height int) (*image.RGBA, error) {
	visual, ok := x11.RootVisual(d)
	if !ok {
		return nil, contract.Dependency("VISUAL_UNSUPPORTED", "failed to find root visual metadata", nil)
	}
	format, ok := x11.PixmapFormat(d, reply.Depth)
	if !ok {
		return nil, contract.Dependency("PIXMAP_FORMAT_UNSUPPORTED", "failed to find pixmap format for screenshot depth", map[string]any{"depth": reply.Depth})
	}
	bpp := int(format.BitsPerPixel)
	if bpp != 24 && bpp != 32 && bpp != 16 {
		return nil, contract.Dependency("PIXMAP_FORMAT_UNSUPPORTED", "unsupported X11 screenshot bits-per-pixel", map[string]any{"bits_per_pixel": bpp, "depth": reply.Depth})
	}
	rowBits := width * bpp
	pad := int(format.ScanlinePad)
	if pad <= 0 {
		pad = 32
	}
	stride := ((rowBits + pad - 1) / pad) * pad / 8
	if len(reply.Data) < stride*height {
		return nil, contract.Dependency("SCREENSHOT_DATA_SHORT", "X11 returned less image data than expected", map[string]any{"got": len(reply.Data), "want": stride * height})
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	little := d.Setup.ImageByteOrder == 0
	for y := 0; y < height; y++ {
		row := reply.Data[y*stride:]
		for x := 0; x < width; x++ {
			offset := x * bpp / 8
			pixel := readPixel(row[offset:], bpp, little)
			img.SetRGBA(x, y, color.RGBA{
				R: extract(pixel, visual.RedMask),
				G: extract(pixel, visual.GreenMask),
				B: extract(pixel, visual.BlueMask),
				A: 255,
			})
		}
	}
	return img, nil
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

func extract(pixel uint32, mask uint32) uint8 {
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
