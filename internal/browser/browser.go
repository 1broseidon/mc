package browser

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"

	"github.com/1broseidon/mc/internal/contract"
)

type PipelineRequest struct {
	Endpoint      string `json:"endpoint,omitempty" jsonschema:"existing Chrome DevTools websocket endpoint"`
	BrowserBin    string `json:"browser_bin,omitempty" jsonschema:"explicit Chromium or Chrome binary path"`
	Headless      bool   `json:"headless,omitempty"`
	ScreenshotDir string `json:"screenshot_dir,omitempty"`
	Steps         []Step `json:"steps"`
}

type Step struct {
	Action     string `json:"action"`
	URL        string `json:"url,omitempty"`
	Selector   string `json:"selector,omitempty"`
	Text       string `json:"text,omitempty"`
	Key        string `json:"key,omitempty"`
	Path       string `json:"path,omitempty"`
	FullPage   bool   `json:"full_page,omitempty"`
	DurationMS int    `json:"duration_ms,omitempty"`
}

func Probe(browserBin, endpoint string) contract.BackendStatus {
	now := time.Now().UTC()
	if endpoint != "" {
		return contract.BackendStatus{Name: "browser", Ready: true, Required: false, Message: "configured CDP endpoint", Details: map[string]any{"endpoint": endpoint}, CheckedAt: now}
	}
	if browserBin == "" {
		browserBin = FindBrowser()
	}
	if browserBin == "" {
		return contract.BackendStatus{Name: "browser", Ready: false, Required: false, Message: "no Chromium-family browser found on PATH", CheckedAt: now}
	}
	return contract.BackendStatus{Name: "browser", Ready: true, Required: false, Message: "browser binary found", Details: map[string]any{"browser_bin": browserBin}, CheckedAt: now}
}

func LaunchSession(ctx context.Context, browserBin string, headless bool) (contract.BackendStatus, error) {
	now := time.Now().UTC()
	if err := ctx.Err(); err != nil {
		return contract.BackendStatus{}, contract.Cancelled("browser session launch cancelled")
	}
	if browserBin == "" {
		browserBin = FindBrowser()
	}
	if browserBin == "" {
		return contract.BackendStatus{}, contract.Dependency("BROWSER_NOT_FOUND", "no Chromium-family browser binary found; set MYCOMPUTER_BROWSER_BIN or browser_bin", nil)
	}
	url, err := launcher.New().Bin(browserBin).Headless(headless).Leakless(false).Launch()
	if err != nil {
		return contract.BackendStatus{}, contract.Dependency("BROWSER_LAUNCH_FAILED", "failed to launch browser through CDP", map[string]any{"browser_bin": browserBin, "error": err.Error()})
	}
	return contract.BackendStatus{
		Name:      "browser",
		Ready:     true,
		Required:  false,
		Message:   "browser launched",
		Details:   map[string]any{"browser_bin": browserBin, "browser_url": url, "headless": headless},
		CheckedAt: now,
	}, nil
}

func FindBrowser() string {
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "brave-browser"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

func RunPipeline(ctx context.Context, req PipelineRequest) (contract.BrowserPipelineResult, error) {
	if err := ctx.Err(); err != nil {
		return contract.BrowserPipelineResult{}, contract.Cancelled("browser pipeline cancelled")
	}
	if len(req.Steps) == 0 {
		return contract.BrowserPipelineResult{}, contract.Validation("EMPTY_BROWSER_PIPELINE", "browser pipeline requires at least one step", nil)
	}
	controlURL := req.Endpoint
	if controlURL == "" {
		bin := req.BrowserBin
		if bin == "" {
			bin = FindBrowser()
		}
		if bin == "" {
			return contract.BrowserPipelineResult{}, contract.Dependency("BROWSER_NOT_FOUND", "no Chromium-family browser binary found; set MYCOMPUTER_BROWSER_BIN or browser_bin", nil)
		}
		url, err := launcher.New().Bin(bin).Headless(req.Headless).Leakless(false).Launch()
		if err != nil {
			return contract.BrowserPipelineResult{}, contract.Dependency("BROWSER_LAUNCH_FAILED", "failed to launch browser through CDP", map[string]any{"browser_bin": bin, "error": err.Error()})
		}
		controlURL = url
	}
	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		return contract.BrowserPipelineResult{}, contract.Dependency("BROWSER_CONNECT_FAILED", "failed to connect to browser CDP endpoint", map[string]any{"endpoint": controlURL, "error": err.Error()})
	}
	defer func() { _ = browser.Close() }()
	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return contract.BrowserPipelineResult{}, contract.Dependency("BROWSER_PAGE_FAILED", "failed to create browser page", map[string]any{"error": err.Error()})
	}

	result := contract.BrowserPipelineResult{BrowserURL: controlURL, Steps: []contract.ActionResult{}, Values: map[string]string{}}
	for _, step := range req.Steps {
		if err := ctx.Err(); err != nil {
			return result, contract.Cancelled("browser pipeline cancelled")
		}
		action := strings.ToLower(step.Action)
		switch action {
		case "navigate":
			if step.URL == "" {
				return result, contract.Validation("BROWSER_URL_REQUIRED", "navigate step requires url", map[string]any{"step": step})
			}
			if err := page.Navigate(step.URL); err != nil {
				return result, contract.Dependency("BROWSER_NAVIGATE_FAILED", "browser navigation failed", map[string]any{"url": step.URL, "error": err.Error()})
			}
			result.Steps = append(result.Steps, contract.ActionResult{Action: action, OK: true, Backend: "cdp", Details: map[string]any{"url": step.URL}})
		case "wait_for_load":
			err := page.WaitLoad()
			if err != nil {
				return result, contract.Dependency("BROWSER_WAIT_FAILED", "browser wait_for_load failed", map[string]any{"error": err.Error()})
			}
			result.Steps = append(result.Steps, contract.ActionResult{Action: action, OK: true, Backend: "cdp"})
		case "wait_for_selector":
			if step.Selector == "" {
				return result, contract.Validation("BROWSER_SELECTOR_REQUIRED", "wait_for_selector step requires selector", map[string]any{"step": step})
			}
			if _, err := page.Element(step.Selector); err != nil {
				return result, contract.Dependency("BROWSER_SELECTOR_FAILED", "selector did not resolve", map[string]any{"selector": step.Selector, "error": err.Error()})
			}
			result.Steps = append(result.Steps, contract.ActionResult{Action: action, OK: true, Backend: "cdp", Details: map[string]any{"selector": step.Selector}})
		case "click_selector":
			el, err := element(page, step)
			if err != nil {
				return result, err
			}
			if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
				return result, contract.Dependency("BROWSER_CLICK_FAILED", "browser click_selector failed", map[string]any{"selector": step.Selector, "error": err.Error()})
			}
			result.Steps = append(result.Steps, contract.ActionResult{Action: action, OK: true, Backend: "cdp", Details: map[string]any{"selector": step.Selector}})
		case "fill_selector":
			el, err := element(page, step)
			if err != nil {
				return result, err
			}
			if err := el.SelectAllText(); err != nil {
				return result, contract.Dependency("BROWSER_FILL_FAILED", "browser fill_selector failed to select text", map[string]any{"selector": step.Selector, "error": err.Error()})
			}
			if err := el.Input(step.Text); err != nil {
				return result, contract.Dependency("BROWSER_FILL_FAILED", "browser fill_selector failed", map[string]any{"selector": step.Selector, "error": err.Error()})
			}
			result.Steps = append(result.Steps, contract.ActionResult{Action: action, OK: true, Backend: "cdp", Details: map[string]any{"selector": step.Selector}})
		case "press_key":
			key := browserKey(step.Key)
			if key == 0 {
				return result, contract.Validation("BROWSER_KEY_UNSUPPORTED", "browser key is unsupported", map[string]any{"key": step.Key})
			}
			if err := page.Keyboard.Press(key); err != nil {
				return result, contract.Dependency("BROWSER_KEY_FAILED", "browser press_key failed", map[string]any{"key": step.Key, "error": err.Error()})
			}
			result.Steps = append(result.Steps, contract.ActionResult{Action: action, OK: true, Backend: "cdp", Details: map[string]any{"key": step.Key}})
		case "scroll":
			_, err := page.Eval(`() => { window.scrollBy(0, window.innerHeight * 0.8); return true }`)
			if err != nil {
				return result, contract.Dependency("BROWSER_SCROLL_FAILED", "browser scroll failed", map[string]any{"error": err.Error()})
			}
			result.Steps = append(result.Steps, contract.ActionResult{Action: action, OK: true, Backend: "cdp"})
		case "screenshot":
			path := step.Path
			if path == "" {
				dir := req.ScreenshotDir
				if dir == "" {
					dir = os.TempDir()
				}
				path = filepath.Join(dir, "mycomputer-browser-"+time.Now().UTC().Format("20060102T150405.000000000")+".png")
			}
			data, err := page.Screenshot(step.FullPage, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
			if err != nil {
				return result, contract.Dependency("BROWSER_SCREENSHOT_FAILED", "browser screenshot failed", map[string]any{"error": err.Error()})
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return result, contract.Dependency("BROWSER_SCREENSHOT_DIR_FAILED", "failed to create browser screenshot directory", map[string]any{"path": filepath.Dir(path), "error": err.Error()})
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				return result, contract.Dependency("BROWSER_SCREENSHOT_WRITE_FAILED", "failed to write browser screenshot", map[string]any{"path": path, "error": err.Error()})
			}
			result.Screenshot = path
			result.Steps = append(result.Steps, contract.ActionResult{Action: action, OK: true, Backend: "cdp", Details: map[string]any{"path": path}})
		case "get_url":
			info, err := page.Info()
			if err != nil {
				return result, contract.Dependency("BROWSER_INFO_FAILED", "failed to read browser page info", map[string]any{"error": err.Error()})
			}
			result.URL = info.URL
			result.Values["url"] = info.URL
			result.Steps = append(result.Steps, contract.ActionResult{Action: action, OK: true, Backend: "cdp"})
		case "get_title":
			info, err := page.Info()
			if err != nil {
				return result, contract.Dependency("BROWSER_INFO_FAILED", "failed to read browser page info", map[string]any{"error": err.Error()})
			}
			result.Title = info.Title
			result.Values["title"] = info.Title
			result.Steps = append(result.Steps, contract.ActionResult{Action: action, OK: true, Backend: "cdp"})
		case "get_dom_text":
			value, err := page.Eval(`() => document.body ? document.body.innerText : ""`)
			if err != nil {
				return result, contract.Dependency("BROWSER_DOM_TEXT_FAILED", "failed to read DOM text", map[string]any{"error": err.Error()})
			}
			result.Text = value.Value.Str()
			result.Values["dom_text"] = result.Text
			result.Steps = append(result.Steps, contract.ActionResult{Action: action, OK: true, Backend: "cdp"})
		case "wait":
			d := time.Duration(step.DurationMS) * time.Millisecond
			if d <= 0 {
				d = 500 * time.Millisecond
			}
			select {
			case <-ctx.Done():
				return result, contract.Cancelled("browser wait cancelled")
			case <-time.After(d):
			}
			result.Steps = append(result.Steps, contract.ActionResult{Action: action, OK: true, Backend: "timer"})
		default:
			return result, contract.Validation("BROWSER_ACTION_UNSUPPORTED", "browser pipeline action is unsupported", map[string]any{"action": step.Action})
		}
	}
	if info, err := page.Info(); err == nil {
		result.URL = info.URL
		result.Title = info.Title
	}
	return result, nil
}

func element(page *rod.Page, step Step) (*rod.Element, error) {
	if step.Selector == "" {
		return nil, contract.Validation("BROWSER_SELECTOR_REQUIRED", "browser step requires selector", map[string]any{"step": step})
	}
	el, err := page.Element(step.Selector)
	if err != nil {
		return nil, contract.Dependency("BROWSER_SELECTOR_FAILED", "selector did not resolve", map[string]any{"selector": step.Selector, "error": err.Error()})
	}
	return el, nil
}

func browserKey(name string) input.Key {
	switch strings.ToLower(name) {
	case "enter", "return":
		return input.Enter
	case "escape", "esc":
		return input.Escape
	case "tab":
		return input.Tab
	case "backspace":
		return input.Backspace
	case "delete":
		return input.Delete
	case "arrowleft", "left":
		return input.ArrowLeft
	case "arrowright", "right":
		return input.ArrowRight
	case "arrowup", "up":
		return input.ArrowUp
	case "arrowdown", "down":
		return input.ArrowDown
	}
	if len([]rune(name)) == 1 {
		return input.Key([]rune(name)[0])
	}
	return 0
}
