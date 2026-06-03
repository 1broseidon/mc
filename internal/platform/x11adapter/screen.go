//go:build linux

package x11adapter

import (
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"math/bits"

	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
	"github.com/1broseidon/mc/internal/x11"
)

// screenGrabber implements platform.ScreenGrabber over X11. All image
// post-processing (clamp-to-request, zoom, downscale, encode, cursor
// compositing) stays in the screen service; this type owns only the raw
// pixel/geometry acquisition.
type screenGrabber struct{}

// Grab returns the pixels within an absolute screen-space rectangle. The
// rectangle is clamped to the real screen first; the returned Bounds is the
// rectangle actually captured. An empty region grabs the whole screen.
func (screenGrabber) Grab(ctx context.Context, region contract.Bounds) (*image.RGBA, contract.Bounds, error) {
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

// ScreenBounds reports the full root-window rectangle.
func (screenGrabber) ScreenBounds(ctx context.Context) (contract.Bounds, error) {
	if err := ctx.Err(); err != nil {
		return contract.Bounds{}, contract.Cancelled("screen bounds cancelled")
	}
	d, err := x11.Open()
	if err != nil {
		return contract.Bounds{}, err
	}
	defer d.Close()
	return x11.ScreenBounds(d), nil
}

// Monitors enumerates physical monitors via RandR. Returns an empty slice
// (not an error) when RandR is unavailable so the service can synthesize a
// single root monitor.
func (screenGrabber) Monitors(ctx context.Context) ([]contract.MonitorInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, contract.Cancelled("monitor enumeration cancelled")
	}
	d, err := x11.Open()
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return collectMonitors(d), nil
}

// CursorPos reports the pointer position in screen space.
func (screenGrabber) CursorPos(ctx context.Context) (contract.Point, error) {
	if err := ctx.Err(); err != nil {
		return contract.Point{}, contract.Cancelled("cursor query cancelled")
	}
	d, err := x11.Open()
	if err != nil {
		return contract.Point{}, err
	}
	defer d.Close()
	reply, err := xproto.QueryPointer(d.Conn, d.Screen.Root).Reply()
	if err != nil {
		return contract.Point{}, contract.Dependency("POINTER_QUERY_FAILED", "failed to query pointer position", map[string]any{"error": err.Error()})
	}
	return contract.Point{X: int(reply.RootX), Y: int(reply.RootY), Space: contract.CoordSpaceScreen}, nil
}

// CursorImage returns the current cursor sprite via XFixes. ok=false (nil
// error) when XFixes is unavailable or no cursor image is present, so the
// service skips the overlay rather than failing the screenshot.
func (screenGrabber) CursorImage(ctx context.Context) (*platform.CursorImage, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, contract.Cancelled("cursor image cancelled")
	}
	d, err := x11.Open()
	if err != nil {
		return nil, false, err
	}
	defer d.Close()
	if err := xfixes.Init(d.Conn); err != nil {
		return nil, false, nil
	}
	if _, err := xfixes.QueryVersion(d.Conn, 4, 0).Reply(); err != nil {
		return nil, false, nil
	}
	reply, err := xfixes.GetCursorImage(d.Conn).Reply()
	if err != nil || reply == nil || reply.Width == 0 || reply.Height == 0 {
		return nil, false, nil
	}
	w, h := int(reply.Width), int(reply.Height)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			pixel := reply.CursorImage[y*w+x]
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(pixel >> 16),
				G: uint8(pixel >> 8),
				B: uint8(pixel),
				A: uint8(pixel >> 24),
			})
		}
	}
	return &platform.CursorImage{
		Image: img,
		HotX:  int(reply.Xhot),
		HotY:  int(reply.Yhot),
		X:     int(reply.X),
		Y:     int(reply.Y),
	}, true, nil
}

// --- helpers migrated from internal/screen ---

func clamp(region, screen contract.Bounds) contract.Bounds {
	x1 := max(region.X, screen.X)
	y1 := max(region.Y, screen.Y)
	x2 := min(region.X+region.Width, screen.X+screen.Width)
	y2 := min(region.Y+region.Height, screen.Y+screen.Height)
	return contract.Bounds{X: x1, Y: y1, Width: x2 - x1, Height: y2 - y1}
}

func collectMonitors(d *x11.Display) []contract.MonitorInfo {
	if err := randr.Init(d.Conn); err != nil {
		return nil
	}
	reply, err := randr.GetMonitors(d.Conn, d.Screen.Root, true).Reply()
	if err != nil {
		return nil
	}
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
