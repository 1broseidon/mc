package mcpserver

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/1broseidon/mc/internal/a11y"
	"github.com/1broseidon/mc/internal/browser"
	"github.com/1broseidon/mc/internal/clipboard"
	"github.com/1broseidon/mc/internal/config"
	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/diagnostic"
	"github.com/1broseidon/mc/internal/input"
	"github.com/1broseidon/mc/internal/pipeline"
	"github.com/1broseidon/mc/internal/screen"
	"github.com/1broseidon/mc/internal/wait"
	"github.com/1broseidon/mc/internal/window"
)

type Options struct {
	Version contract.VersionInfo
	Config  config.Effective
}

// ToolInfo describes one registered MCP tool: its wire name, one-line
// description, and the read_only / destructive annotations that come
// from the add() call site. The conventions emit subcommand and the
// doctor.available_tools field consume this catalog so the tool list
// has one source of truth (the add() calls inside New()).
type ToolInfo struct {
	Name        string
	Description string
	ReadOnly    bool
	Destructive bool
}

// toolCatalog is the slice of tools registered during the most recent
// New() call. Populated by add() in registration order. Read via
// Catalog() — never mutate after New() returns.
var toolCatalog []ToolInfo

// Catalog returns a defensive copy of the tools registered by the most
// recent call to New(). If New() has never been invoked the result is
// empty. Used by `mycomputer conventions emit` to derive the MCP tool
// inventory from the live binary surface.
func Catalog() []ToolInfo {
	out := make([]ToolInfo, len(toolCatalog))
	copy(out, toolCatalog)
	return out
}

// SupportedSchemaVersions returns the action-payload schema versions
// this server can accept. Re-exported here so MCP-side callers don't
// need to depend on the contract package directly. Mirrors
// contract.SupportedSchemaVersions exactly.
func SupportedSchemaVersions() []string {
	return contract.SupportedSchemaVersions()
}

// ServerInfo is the server-identity envelope returned to MCP clients
// during initialize. SchemaVersions advertises every action-payload
// schema version the server can accept.
type ServerInfo struct {
	Name           string   `json:"name"`
	Version        string   `json:"version"`
	SchemaVersions []string `json:"schema_versions"`
}

// GetServerInfo returns the server identity plus the supported
// action-payload schema versions. Called by serve handlers that want
// to advertise version negotiation metadata.
func GetServerInfo(version contract.VersionInfo) ServerInfo {
	return ServerInfo{
		Name:           "my-computer",
		Version:        version.Version,
		SchemaVersions: contract.SupportedSchemaVersions(),
	}
}

func New(opts Options) *mcp.Server {
	instructions := "MyComputer drives an X11 desktop for agents. computer_actions payloads require schema_version=\"" +
		contract.SchemaVersion + "\". Supported schema_versions: " +
		strings.Join(contract.SupportedSchemaVersions(), ", ") + "."
	s := mcp.NewServer(
		&mcp.Implementation{Name: "my-computer", Version: opts.Version.Version},
		&mcp.ServerOptions{Instructions: instructions},
	)
	// Reset the package-level tool catalog so a fresh New() call yields
	// a clean inventory. add() appends in registration order below.
	toolCatalog = toolCatalog[:0]

	add(s, "doctor", "Return MyComputer readiness, session, and backend diagnostics.", true, false, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, contract.DoctorReport, error) {
		// Derive AvailableTools from the live catalog rather than a
		// hardcoded slice in diagnostic — this is the doctor handler
		// itself, so toolCatalog is guaranteed populated by the
		// surrounding New() call.
		cat := Catalog()
		toolNames := make([]string, len(cat))
		for i, t := range cat {
			toolNames[i] = t.Name
		}
		return nil, diagnostic.Doctor(opts.Version, opts.Config, toolNames), nil
	})
	add(s, "get_screen_info", "Return X11 screen dimensions, monitor bounds, and coordinate space.", true, false, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, contract.ScreenInfo, error) {
		out, err := screen.Info(ctx)
		return nil, out, err
	})
	add(s, "list_windows", "List visible top-level windows with title, class, PID, focus state, and bounds.", true, false, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct {
		Windows []contract.WindowInfo `json:"windows"`
	}, error) {
		wins, err := window.List(ctx)
		return nil, struct {
			Windows []contract.WindowInfo `json:"windows"`
		}{Windows: wins}, err
	})
	add(s, "focused_window", "Return the currently focused window.", true, false, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct {
		Window *contract.WindowInfo `json:"window,omitempty"`
	}, error) {
		win, err := window.Focused(ctx)
		return nil, struct {
			Window *contract.WindowInfo `json:"window,omitempty"`
		}{Window: win}, err
	})
	add(s, "observe", "Return desktop state: screen, windows, cursor, optional accessibility, and optional screenshot.", true, false, func(ctx context.Context, _ *mcp.CallToolRequest, in ObserveInput) (*mcp.CallToolResult, contract.ObserveResult, error) {
		out, err := pipeline.Observe(ctx, in.Screenshot)
		return nil, out, err
	})
	add(s, "screenshot", "Capture full screen, region, or zoom crop and return image metadata plus coord_map. Region accepts bare {x,y,width,height} (screen-space, v0.1/v0.2 compat) or extended {...,space:'window'|'window_frame'|'monitor',target?,monitor_index?} to capture in-window or per-monitor crops without computing offsets. coord_map always reflects absolute screen-space.", true, false, func(ctx context.Context, _ *mcp.CallToolRequest, in screen.CaptureRequest) (*mcp.CallToolResult, contract.ScreenshotResult, error) {
		if in.MaxEdge == 0 {
			in.MaxEdge = 1568
		}
		out, err := screen.Capture(ctx, in)
		return nil, out, err
	})
	add(s, "focus_window", "Focus a window by id, title, class, or PID.", false, false, func(ctx context.Context, _ *mcp.CallToolRequest, in window.Target) (*mcp.CallToolResult, contract.WindowInfo, error) {
		out, err := window.Focus(ctx, in)
		return nil, out, err
	})
	add(s, "move_mouse", "Move the pointer to a physical or screenshot-mapped coordinate.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in contract.Point) (*mcp.CallToolResult, contract.ActionResult, error) {
		err := input.Move(ctx, in)
		return nil, contract.ActionResult{Action: "move_mouse", OK: err == nil, Backend: "XTest"}, err
	})
	add(s, "click", "Click by coordinate with left, middle, or right button.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in input.ClickRequest) (*mcp.CallToolResult, contract.ActionResult, error) {
		err := input.Click(ctx, in)
		return nil, contract.ActionResult{Action: "click", OK: err == nil, Backend: "XTest"}, err
	})
	add(s, "drag", "Drag from one coordinate to another.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in input.DragRequest) (*mcp.CallToolResult, contract.ActionResult, error) {
		err := input.Drag(ctx, in)
		return nil, contract.ActionResult{Action: "drag", OK: err == nil, Backend: "XTest"}, err
	})
	add(s, "scroll", "Scroll vertically or horizontally at a coordinate.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in input.ScrollRequest) (*mcp.CallToolResult, contract.ActionResult, error) {
		err := input.Scroll(ctx, in)
		return nil, contract.ActionResult{Action: "scroll", OK: err == nil, Backend: "XTest"}, err
	})
	add(s, "type_text", "Type literal text into the focused target. via:auto (default) picks paste for len>64 or non-ASCII or control chars; otherwise xtest. via:xtest is layout-aware (refuses INPUT_LAYOUT_UNREACHABLE chars). via:paste saves+writes+ctrl+v+restores clipboard. With an active IME, xtest is rejected with INPUT_IME_ACTIVE; auto silently routes to paste.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in TypeTextInput) (*mcp.CallToolResult, contract.ActionResult, error) {
		res, err := input.TypeTextWith(ctx, input.TypeTextRequest{Text: in.Text, Via: in.Via})
		backend := "XTest"
		if res.Via == input.TypeTextViaPaste {
			backend = "x11.clipboard"
		}
		details := map[string]any{"via": res.Via}
		if res.Via == input.TypeTextViaPaste {
			details["clipboard_restored"] = res.ClipboardRestored
		}
		if res.IMEActive {
			details["ime_active"] = true
			if res.IMEEngine != "" {
				details["ime_engine"] = res.IMEEngine
			}
		}
		return nil, contract.ActionResult{Action: "type_text", OK: err == nil, Backend: backend, Details: details}, err
	})
	add(s, "clipboard_read", "Read the current value of an X11 selection (clipboard or primary) via native selection protocol.", true, false, func(ctx context.Context, _ *mcp.CallToolRequest, in ClipboardReadInput) (*mcp.CallToolResult, clipboard.ReadResult, error) {
		out, err := clipboard.Read(ctx, in.Selection, in.Mime)
		return nil, out, err
	})
	add(s, "clipboard_write", "Write content to an X11 selection (clipboard, primary, or both). MIME may be text/plain (default) or text/uri-list. Ownership is held by the MyComputer process for its lifetime.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in ClipboardWriteInput) (*mcp.CallToolResult, clipboard.WriteResult, error) {
		out, err := clipboard.Write(ctx, in.Selection, in.Content, in.Mime)
		return nil, out, err
	})
	add(s, "paste", "Send a paste shortcut (ctrl+v by default, or shift+insert with method:insert) to the focused target. Assumes the clipboard already has content.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in PasteInput) (*mcp.CallToolResult, contract.ActionResult, error) {
		method := in.Method
		if method == "" {
			method = "key"
		}
		var chord string
		switch method {
		case "key":
			chord = "ctrl+v"
		case "insert":
			chord = "shift+insert"
		default:
			err := contract.Validation("PASTE_METHOD_INVALID", "paste method must be key or insert", map[string]any{"method": method})
			return nil, contract.ActionResult{Action: "paste", OK: false, Backend: "XTest"}, err
		}
		if in.Selection != "" && in.Selection != clipboard.SelectionClipboard {
			err := contract.Validation("CLIPBOARD_SELECTION_INVALID", "paste only supports the clipboard selection", map[string]any{"selection": in.Selection})
			return nil, contract.ActionResult{Action: "paste", OK: false, Backend: "XTest"}, err
		}
		err := input.PressKey(ctx, chord)
		return nil, contract.ActionResult{Action: "paste", OK: err == nil, Backend: "XTest", Details: map[string]any{"method": method, "chord": chord}}, err
	})
	add(s, "press_key", "Press a key or key chord such as enter, ctrl+l, or alt+tab.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in KeyInput) (*mcp.CallToolResult, contract.ActionResult, error) {
		err := input.PressKey(ctx, in.Key)
		return nil, contract.ActionResult{Action: "press_key", OK: err == nil, Backend: "XTest"}, err
	})
	add(s, "set_text", "Set element text through AT-SPI when available.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in TextInput) (*mcp.CallToolResult, contract.ActionResult, error) {
		if in.ElementID == "" {
			err := contract.Validation("ELEMENT_ID_REQUIRED", "set_text requires an AT-SPI element_id", nil)
			return nil, contract.ActionResult{Action: "set_text", OK: false, Backend: "at-spi"}, err
		}
		err := a11y.SetText(ctx, in.ElementID, in.Text)
		return nil, contract.ActionResult{Action: "set_text", OK: err == nil, Backend: "at-spi"}, err
	})
	add(s, "perform_action", "Invoke an AT-SPI semantic action when available.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in SemanticActionInput) (*mcp.CallToolResult, contract.ActionResult, error) {
		if in.ElementID == "" {
			err := contract.Validation("ELEMENT_ID_REQUIRED", "perform_action requires an AT-SPI element_id", nil)
			return nil, contract.ActionResult{Action: "perform_action", OK: false, Backend: "at-spi"}, err
		}
		err := a11y.PerformAction(ctx, in.ElementID, in.Action)
		return nil, contract.ActionResult{Action: "perform_action", OK: err == nil, Backend: "at-spi"}, err
	})
	add(s, "computer_actions", "Execute an ordered batch of desktop actions. Requires schema_version=\""+contract.SchemaVersion+"\" at the top level.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in pipeline.ActionBatch) (*mcp.CallToolResult, pipeline.BatchResult, error) {
		out, err := pipeline.Run(ctx, in)
		if err != nil {
			// Surface the full contract error envelope (code + message
			// + details) in the tool-result body so callers can branch
			// on the structured code, not free-text matching. We return
			// the result directly with nil error so the SDK does not
			// overwrite the content with err.Error() alone — see
			// go-sdk/mcp/server.go: errRes.SetError on non-nil err.
			res := &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: string(contract.MarshalError(err))}},
			}
			return res, pipeline.BatchResult{}, nil
		}
		return nil, out, nil
	})
	add(s, "find_text", "Run OCR over a screen region and return text candidates matching a query. Region accepts bare {x,y,width,height} (screen-space) or extended {...,space:'window'|'window_frame'|'monitor',target?,monitor_index?} for in-window or per-monitor OCR. Auto preprocessing inverts dark-theme regions before OCR so white-on-dark UIs (gnome-calculator, dark-mode Electron, terminals) work without manual flags; opt out with preprocess=\"none\" or force with preprocess=\"invert\"|\"binarize\".", true, false, func(ctx context.Context, _ *mcp.CallToolRequest, in FindTextInput) (*mcp.CallToolResult, contract.FindResult, error) {
		out, err := pipeline.RunFindText(ctx, in.toAction())
		return nil, out, err
	})
	add(s, "find_image", "Locate a template image inside a screen region by template matching. Region accepts bare {x,y,width,height} (screen-space) or extended {...,space:'window'|'window_frame'|'monitor',target?,monitor_index?} for in-window or per-monitor template search.", true, false, func(ctx context.Context, _ *mcp.CallToolRequest, in FindImageInput) (*mcp.CallToolResult, contract.FindResult, error) {
		out, err := pipeline.RunFindImage(ctx, in.toAction())
		return nil, out, err
	})
	add(s, "find_color", "Sample a screen pixel or locate contiguous color blobs within tolerance.", true, false, func(ctx context.Context, _ *mcp.CallToolRequest, in FindColorInput) (*mcp.CallToolResult, contract.FindResult, error) {
		out, err := pipeline.RunFindColor(ctx, in.toAction())
		return nil, out, err
	})
	add(s, "click_text", "OCR a screen region for matching text and click the highest-confidence hit.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in ClickTextInput) (*mcp.CallToolResult, contract.ActionResult, error) {
		res, err := pipeline.RunClickText(ctx, in.toAction())
		return nil, res, err
	})
	add(s, "click_image", "Locate a template image on screen and click the highest-confidence hit.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in ClickImageInput) (*mcp.CallToolResult, contract.ActionResult, error) {
		res, err := pipeline.RunClickImage(ctx, in.toAction())
		return nil, res, err
	})
	add(s, "window_move", "Move a window to (x, y) screen coordinates via EWMH _NET_MOVERESIZE_WINDOW. On tiling WMs the request may be refused; the result then carries a WINDOW_GEOMETRY_REFUSED warning under details.warning.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in window.MoveRequest) (*mcp.CallToolResult, contract.ActionResult, error) {
		res, err := window.Move(ctx, in)
		return nil, windowMCPResult("window_move", res, err), err
	})
	add(s, "window_resize", "Resize a window to width x height via EWMH _NET_MOVERESIZE_WINDOW. May be refused by tiling WMs; check details.warning.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in window.ResizeRequest) (*mcp.CallToolResult, contract.ActionResult, error) {
		res, err := window.Resize(ctx, in)
		return nil, windowMCPResult("window_resize", res, err), err
	})
	add(s, "window_raise", "Activate and stack a window above its siblings via _NET_ACTIVE_WINDOW with ConfigureWindow fallback.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in window.Target) (*mcp.CallToolResult, contract.ActionResult, error) {
		res, err := window.Raise(ctx, in)
		return nil, windowMCPResult("window_raise", res, err), err
	})
	add(s, "window_minimize", "Iconify a window via WM_CHANGE_STATE (IconicState).", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in window.Target) (*mcp.CallToolResult, contract.ActionResult, error) {
		res, err := window.Minimize(ctx, in)
		return nil, windowMCPResult("window_minimize", res, err), err
	})
	add(s, "window_maximize", "Toggle the maximized state of a window via _NET_WM_STATE. axis may be both (default), horz, or vert.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in window.MaximizeRequest) (*mcp.CallToolResult, contract.ActionResult, error) {
		res, err := window.Maximize(ctx, in)
		return nil, windowMCPResult("window_maximize", res, err), err
	})
	add(s, "window_workspace", "Move a window to the zero-based workspace index via _NET_WM_DESKTOP.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in window.WorkspaceRequest) (*mcp.CallToolResult, contract.ActionResult, error) {
		res, err := window.Workspace(ctx, in)
		return nil, windowMCPResult("window_workspace", res, err), err
	})
	add(s, "window_close", "Request that a window close via _NET_CLOSE_WINDOW. REQUIRES the server to be started with --allow-close; otherwise returns PRECONDITION_CLOSE_NOT_ALLOWED.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in window.Target) (*mcp.CallToolResult, contract.ActionResult, error) {
		if !opts.Config.AllowClose {
			err := contract.Precondition("PRECONDITION_CLOSE_NOT_ALLOWED", "window_close requires the server to be started with --allow-close", map[string]any{"target": in})
			return nil, contract.ActionResult{Action: "window_close", OK: false, Backend: "x11.EWMH"}, err
		}
		res, err := window.Close(ctx, in)
		return nil, windowMCPResult("window_close", res, err), err
	})
	add(s, "wait_for_window", "Wait until a window matching the supplied selector appears (or disappears when present:false). Returns matched window plus polls and elapsed_ms; times out with WAIT_TIMEOUT (exit 5) when the condition is not met.", true, false, func(ctx context.Context, _ *mcp.CallToolRequest, in wait.WindowRequest) (*mcp.CallToolResult, wait.WindowResult, error) {
		out, err := pipeline.RunWaitForWindow(ctx, in)
		return nil, out, err
	})
	add(s, "wait_for_pixel_change", "Wait until pixels in a region change (mode:any, default) or stop changing for stable_ms (mode:stable). Region accepts bare {x,y,width,height} (screen-space) or extended {...,space:'window'|'window_frame'|'monitor',target?,monitor_index?} so callers can poll a window's client area without computing offsets. Uses dhash 16x16 perceptual diff. Times out with WAIT_TIMEOUT (exit 5).", true, false, func(ctx context.Context, _ *mcp.CallToolRequest, in wait.PixelRequest) (*mcp.CallToolResult, wait.PixelResult, error) {
		out, err := pipeline.RunWaitForPixelChange(ctx, in)
		return nil, out, err
	})
	add(s, "wait_for_text", "Wait until OCR finds (or, with present:false, stops finding) text matching the query in the requested region. Region accepts bare {x,y,width,height} (screen-space) or extended {...,space:'window'|'window_frame'|'monitor',target?,monitor_index?}. Reuses find_text; default poll cadence is 250ms to amortize OCR cost. Times out with WAIT_TIMEOUT (exit 5).", true, false, func(ctx context.Context, _ *mcp.CallToolRequest, in wait.TextRequest) (*mcp.CallToolResult, wait.TextResult, error) {
		out, err := pipeline.RunWaitForText(ctx, in)
		return nil, out, err
	})
	add(s, "browser_session", "Report browser CDP readiness or launch a Chromium-family browser when launch is true.", false, false, func(ctx context.Context, _ *mcp.CallToolRequest, in BrowserSessionInput) (*mcp.CallToolResult, contract.BackendStatus, error) {
		if in.Launch {
			out, err := browser.LaunchSession(ctx, in.BrowserBin, in.Headless)
			return nil, out, err
		}
		return nil, browser.Probe(in.BrowserBin, in.Endpoint), nil
	})
	add(s, "browser_pipeline", "Execute an ordered browser pipeline through Chrome DevTools Protocol.", false, true, func(ctx context.Context, _ *mcp.CallToolRequest, in browser.PipelineRequest) (*mcp.CallToolResult, contract.BrowserPipelineResult, error) {
		if in.BrowserBin == "" {
			in.BrowserBin = opts.Config.BrowserBin
		}
		if in.Endpoint == "" {
			in.Endpoint = opts.Config.BrowserEndpoint
		}
		if in.ScreenshotDir == "" {
			in.ScreenshotDir = opts.Config.ScreenshotDir
		}
		out, err := browser.RunPipeline(ctx, in)
		return nil, out, err
	})
	return s
}

type ObserveInput struct {
	Screenshot bool `json:"screenshot,omitempty" jsonschema:"include a screenshot in the observation"`
}

type TextInput struct {
	ElementID string `json:"element_id,omitempty" jsonschema:"AT-SPI element id returned by observe"`
	Text      string `json:"text" jsonschema:"literal text to type or set"`
}

type TypeTextInput struct {
	Text string `json:"text" jsonschema:"literal text to type"`
	Via  string `json:"via,omitempty" jsonschema:"routing: xtest, paste, or auto (default auto)"`
}

type ClipboardReadInput struct {
	Selection string `json:"selection,omitempty" jsonschema:"clipboard (default) or primary"`
	Mime      string `json:"mime,omitempty" jsonschema:"text/plain (default) or text/uri-list"`
}

type ClipboardWriteInput struct {
	Content   string `json:"content" jsonschema:"content to write"`
	Selection string `json:"selection,omitempty" jsonschema:"clipboard (default), primary, or both"`
	Mime      string `json:"mime,omitempty" jsonschema:"text/plain (default) or text/uri-list"`
}

type PasteInput struct {
	Selection string `json:"selection,omitempty" jsonschema:"clipboard (default)"`
	Method    string `json:"method,omitempty" jsonschema:"key (ctrl+v, default) or insert (shift+insert)"`
}

type KeyInput struct {
	Key string `json:"key" jsonschema:"key or chord such as enter, ctrl+l, alt+tab"`
}

type SemanticActionInput struct {
	ElementID string `json:"element_id" jsonschema:"AT-SPI element id returned by observe"`
	Action    string `json:"action,omitempty" jsonschema:"semantic accessibility action name; empty uses the first exposed action"`
}

type FindTextInput struct {
	Region        contract.RegionRef `json:"region,omitempty" jsonschema:"OCR region; bare {x,y,width,height} is screen-space (defaults to focused window or full screen when empty). Extended shape adds space=window|window_frame|monitor plus target or monitor_index for in-window or per-monitor OCR."`
	Query         string             `json:"query" jsonschema:"text or regex pattern to look for"`
	Lang          string             `json:"lang,omitempty" jsonschema:"Tesseract language code; defaults to eng"`
	CaseSensitive bool               `json:"case_sensitive,omitempty"`
	Regex         bool               `json:"regex,omitempty" jsonschema:"interpret query as a regular expression"`
	MinConfidence float64            `json:"min_confidence,omitempty" jsonschema:"drop candidates below this OCR confidence (0..1)"`
	Preprocess    string             `json:"preprocess,omitempty" jsonschema:"OCR preprocess mode: auto (default; inverts dark-theme regions before OCR), invert, binarize, or none"`
	PSM           int                `json:"psm,omitempty" jsonschema:"Tesseract page segmentation mode (0..13); 0 omits the flag and uses tesseract default"`
	OEM           int                `json:"oem,omitempty" jsonschema:"Tesseract OCR engine mode (0..3); 0 omits the flag and uses tesseract default"`
}

func (in FindTextInput) toAction() pipeline.Action {
	return pipeline.Action{Region: in.Region, Query: in.Query, Lang: in.Lang, CaseSensitive: in.CaseSensitive, Regex: in.Regex, MinConfidence: in.MinConfidence, Preprocess: in.Preprocess, PSM: in.PSM, OEM: in.OEM}
}

type FindImageInput struct {
	Region       contract.RegionRef `json:"region,omitempty" jsonschema:"search region; bare {x,y,width,height} is screen-space (defaults to focused window or full screen when empty). Extended shape adds space=window|window_frame|monitor plus target or monitor_index."`
	TemplatePath string             `json:"template_path" jsonschema:"path to a PNG or JPEG template"`
	Threshold    float64            `json:"threshold,omitempty" jsonschema:"match threshold; defaults to 0.9"`
	Scales       []float64          `json:"scales,omitempty" jsonschema:"scale factors to try; gocv backend only"`
}

func (in FindImageInput) toAction() pipeline.Action {
	return pipeline.Action{Region: in.Region, TemplatePath: in.TemplatePath, Threshold: in.Threshold, Scales: in.Scales}
}

type FindColorInput struct {
	Point     *contract.Point    `json:"point,omitempty" jsonschema:"sample this single pixel"`
	Color     string             `json:"color,omitempty" jsonschema:"hex color #rrggbb to search for"`
	Region    contract.RegionRef `json:"region,omitempty" jsonschema:"search region; bare {x,y,width,height} is screen-space. Extended shape adds space=window|window_frame|monitor plus target or monitor_index."`
	Tolerance int                `json:"tolerance,omitempty" jsonschema:"per-channel tolerance (default 8)"`
	MinArea   int                `json:"min_area,omitempty" jsonschema:"minimum blob area in pixels (default 4)"`
}

func (in FindColorInput) toAction() pipeline.Action {
	a := pipeline.Action{Region: in.Region, Color: in.Color, Tolerance: in.Tolerance, MinArea: in.MinArea}
	if in.Point != nil {
		a.Point = *in.Point
	}
	return a
}

type ClickTextInput struct {
	FindTextInput
	Button string `json:"button,omitempty"`
	Count  int    `json:"count,omitempty"`
	Strict bool   `json:"strict,omitempty" jsonschema:"fail with TARGET_AMBIGUOUS when multiple candidates pass min_confidence"`
}

func (in ClickTextInput) toAction() pipeline.Action {
	a := in.FindTextInput.toAction()
	a.Button = in.Button
	a.Count = in.Count
	a.Strict = in.Strict
	return a
}

type ClickImageInput struct {
	FindImageInput
	Button        string  `json:"button,omitempty"`
	Count         int     `json:"count,omitempty"`
	MinConfidence float64 `json:"min_confidence,omitempty"`
	Strict        bool    `json:"strict,omitempty"`
}

func (in ClickImageInput) toAction() pipeline.Action {
	a := in.FindImageInput.toAction()
	a.Button = in.Button
	a.Count = in.Count
	a.MinConfidence = in.MinConfidence
	a.Strict = in.Strict
	return a
}

type BrowserSessionInput struct {
	Endpoint   string `json:"endpoint,omitempty"`
	BrowserBin string `json:"browser_bin,omitempty"`
	Headless   bool   `json:"headless,omitempty"`
	Launch     bool   `json:"launch,omitempty" jsonschema:"start a new Chromium-family browser and return its CDP URL"`
}

// windowMCPResult turns a window.VerbResult into the MCP-facing
// ActionResult, attaching the post-op window snapshot and any warning
// (e.g. WINDOW_GEOMETRY_REFUSED) under details. err short-circuits
// with OK:false so callers see structured failure even though the
// transport layer also surfaces err.
func windowMCPResult(action string, res window.VerbResult, err error) contract.ActionResult {
	if err != nil {
		return contract.ActionResult{Action: action, OK: false, Backend: "x11.EWMH"}
	}
	details := map[string]any{"window": res.Window}
	if res.Warning != nil {
		details["warning"] = res.Warning
	}
	if len(res.Notes) > 0 {
		details["notes"] = res.Notes
	}
	return contract.ActionResult{Action: action, OK: true, Backend: "x11.EWMH", Details: details}
}

func add[In, Out any](s *mcp.Server, name, description string, readOnly bool, destructive bool, handler mcp.ToolHandlerFor[In, Out]) {
	openWorld := true
	destructiveHint := destructive
	mcp.AddTool(s, &mcp.Tool{
		Name:        name,
		Description: description,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    readOnly,
			DestructiveHint: &destructiveHint,
			OpenWorldHint:   &openWorld,
		},
	}, handler)
	toolCatalog = append(toolCatalog, ToolInfo{
		Name:        name,
		Description: description,
		ReadOnly:    readOnly,
		Destructive: destructive,
	})
}
