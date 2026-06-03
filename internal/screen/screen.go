package screen

// Anvil · target: internal/screen · kind: package · scope: package
// caller profile: agent,script · surface pattern: package-API (delegating) · risk class: R0
// contracts: Info/Capture/CaptureRGBA/Cursor/PrimaryScale signatures + error codes preserved
// obligations: OS-specific pixel/geometry acquisition delegated to platform.Provider

import (
	"context"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/1broseidon/mc/internal/contract"
	imageutil "github.com/1broseidon/mc/internal/image"
	"github.com/1broseidon/mc/internal/platform"
	"github.com/1broseidon/mc/internal/window"
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
	Cursor      bool               `json:"cursor,omitempty" jsonschema:"overlay the current cursor when the platform can supply one"`
	JPEGQuality int                `json:"jpeg_quality,omitempty" jsonschema:"JPEG quality from 1 to 100; defaults to 85"`
}

// Info reports the screen bounds and physical monitor list. Geometry
// acquisition is delegated to the active platform backend; this
// function owns only the single-monitor fallback contract.
func Info(ctx context.Context) (contract.ScreenInfo, error) {
	if err := ctx.Err(); err != nil {
		return contract.ScreenInfo{}, contract.Cancelled("screen info cancelled")
	}
	sg := platform.Current().Screen()
	bounds, err := sg.ScreenBounds(ctx)
	if err != nil {
		return contract.ScreenInfo{}, err
	}
	monitors, err := sg.Monitors(ctx)
	if err != nil {
		return contract.ScreenInfo{}, err
	}
	info := contract.ScreenInfo{
		Bounds:   bounds,
		Backend:  platform.Current().Labels().Screen,
		Monitors: monitors,
	}
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

// Cursor reports the current pointer position in screen space.
func Cursor(ctx context.Context) (*contract.Point, error) {
	if err := ctx.Err(); err != nil {
		return nil, contract.Cancelled("cursor query cancelled")
	}
	p, err := platform.Current().Screen().CursorPos(ctx)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ResolveCaptureRegionDetailed turns a coord-space-aware RegionRef
// into a contract.ResolveResult carrying the absolute screen-space
// Bounds suitable for capture along with optional clamp metadata.
// Used by Capture and by external callers (pipeline.runFind*,
// wait.WaitForPixelChange, wait.WaitForText) that need to translate a
// window-space or monitor-space region into screen coordinates before
// capturing, and that surface region_clamped / original_region /
// clamped_region in their action responses.
//
// Screen-space (or empty) RegionRefs pass through unchanged (the
// physical-screen clamp happens in Capture / CaptureRGBA, not here).
// Window/window_frame regions require the window list (collected via
// window.List); monitor regions require the monitor list (collected
// via Info). Returns the validation/precondition errors emitted by
// contract.ResolveRegionDetailed verbatim. Unknown spaces drop through
// to contract.ResolveRegionDetailed so the canonical
// INVALID_COORDINATE_SPACE error is consistent.
func ResolveCaptureRegionDetailed(ctx context.Context, ref contract.RegionRef) (contract.ResolveResult, error) {
	switch ref.Space {
	case "", contract.CoordSpaceScreen:
		// Fast path: no extra round-trips when the caller already
		// supplied screen-space coords. Screen-space callers that want
		// oversized-region clamping can still set Strict=true to opt
		// into validation errors, but the actual clamp against the
		// physical screen bounds happens in Capture/CaptureRGBA below
		// (those clamp() calls already exist).
		return contract.ResolveRegionDetailed(ref, contract.ResolveContext{})
	case contract.CoordSpaceWindow, contract.CoordSpaceWindowFrame:
		wins, err := window.List(ctx)
		if err != nil {
			return contract.ResolveResult{}, err
		}
		return contract.ResolveRegionDetailed(ref, contract.ResolveContext{Windows: wins})
	case contract.CoordSpaceMonitor:
		info, err := Info(ctx)
		if err != nil {
			return contract.ResolveResult{}, err
		}
		return contract.ResolveRegionDetailed(ref, contract.ResolveContext{Monitors: info.Monitors})
	default:
		// Let ResolveRegionDetailed produce the canonical
		// INVALID_COORDINATE_SPACE error so the wire surface is
		// consistent.
		return contract.ResolveRegionDetailed(ref, contract.ResolveContext{})
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
	return platform.Current().Screen().Grab(ctx, region)
}

func Capture(ctx context.Context, req CaptureRequest) (contract.ScreenshotResult, error) {
	if err := ctx.Err(); err != nil {
		return contract.ScreenshotResult{}, contract.Cancelled("capture cancelled")
	}

	// Resolve the coord-space-aware region BEFORE touching the platform
	// backend so resolution failures (target not found, monitor OOB)
	// surface as validation/precondition errors with no side-effects.
	// Empty regions stay empty; the platform Grab promotes them to the
	// full screen.
	var capture contract.Bounds
	var resolveRes contract.ResolveResult
	if !req.Region.Empty() {
		res, err := ResolveCaptureRegionDetailed(ctx, req.Region)
		if err != nil {
			return contract.ScreenshotResult{}, err
		}
		capture = res.Bounds
		resolveRes = res
	}

	if req.Zoom {
		size := req.ZoomSize
		if size <= 0 {
			size = 400
		}
		capture = contract.Bounds{X: req.ZoomX - size/2, Y: req.ZoomY - size/2, Width: size, Height: size}
	}

	// Grab clamps to the real screen and promotes an empty region to the
	// full screen, returning the rectangle actually captured.
	sg := platform.Current().Screen()
	img, captured, err := sg.Grab(ctx, capture)
	if err != nil {
		return contract.ScreenshotResult{}, err
	}
	capture = captured

	if req.Cursor {
		if err := overlayCursor(ctx, sg, img, capture); err != nil {
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
	result := contract.ScreenshotResult{
		ImagePath:     out,
		MimeType:      mime,
		CaptureBounds: capture,
		ImageSize:     size,
		CoordMap:      contract.NewCoordMap(capture, size).String(),
		Backend:       platform.Current().Labels().Capture,
	}
	if resolveRes.Clamped {
		orig := resolveRes.OriginalRegion
		clamped := resolveRes.ClampedRegion
		result.RegionClamped = true
		result.OriginalRegion = &orig
		result.ClampedRegion = &clamped
	}
	return result, nil
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

// overlayCursor composites the platform cursor sprite onto a captured
// image. The platform backend acquires the sprite pixels and hotspot;
// this function owns only the cross-platform alpha compositing. When
// the platform cannot supply a cursor image (ok=false) the overlay is
// skipped — cursor overlay is best-effort and never fails the capture.
func overlayCursor(ctx context.Context, sg platform.ScreenGrabber, img *image.RGBA, capture contract.Bounds) error {
	ci, ok, err := sg.CursorImage(ctx)
	if err != nil {
		return err
	}
	if !ok || ci == nil || ci.Image == nil {
		return nil
	}
	left := ci.X - ci.HotX - capture.X
	top := ci.Y - ci.HotY - capture.Y
	src := ci.Image
	w := src.Bounds().Dx()
	h := src.Bounds().Dy()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dstX := left + x
			dstY := top + y
			if dstX < 0 || dstY < 0 || dstX >= img.Bounds().Dx() || dstY >= img.Bounds().Dy() {
				continue
			}
			sp := src.RGBAAt(x, y)
			if sp.A == 0 {
				continue
			}
			dst := img.RGBAAt(dstX, dstY)
			img.SetRGBA(dstX, dstY, alphaOver(sp, dst))
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
