// Package wait provides condition-based waits that replace the
// hand-tuned `wait { duration_ms }` heuristics that v0.1 batches relied
// on. Three primitives are exposed:
//
//   - WaitForWindow: poll EWMH _NET_CLIENT_LIST until a window matching
//     a WindowTarget appears (or disappears, when Present is false).
//   - WaitForPixelChange: capture a baseline of a screen region and
//     poll until pixel content changes (mode "any") or stops changing
//     (mode "stable").
//   - WaitForText: poll an OCR-driven find_text query until the text
//     appears (or vanishes, when Present is false).
//
// Each primitive honors ctx.Done() (batch-level cancel) and its own
// timeout. Timeouts return contract.Precondition with code
// WAIT_TIMEOUT and a last_state snapshot under details so callers can
// debug from the response alone. Cancellation via ctx.Done() returns
// contract.Cancelled (exit 6).
//
// Design choices:
//
//   - WaitForWindow uses a polling loop, not a PropertyNotify
//     subscription. Polling at 100ms is sufficient for desktop launch
//     and close events; subscription would require a long-lived X11
//     connection plus an event pump, doubling the implementation size
//     for marginal latency wins. The cadence is documented in the
//     result so debugging is straightforward.
//   - WaitForPixelChange uses a 16x16 dhash signature for the baseline
//     and a Hamming-distance ratio as the diff metric. dhash is cheap
//     to compute on a captured RGBA buffer and robust to anti-aliasing
//     jitter at idle.
//   - WaitForText reuses imageutil.FindText and screen.CaptureRGBA so
//     OCR logic is not duplicated.
package wait

import (
	"context"
	"image"
	"time"

	"github.com/1broseidon/mc/internal/contract"
	imageutil "github.com/1broseidon/mc/internal/image"
	"github.com/1broseidon/mc/internal/screen"
	"github.com/1broseidon/mc/internal/window"
)

// Default cadences and timeouts.
const (
	DefaultWindowTimeoutMS = 5000
	DefaultWindowPollMS    = 100

	DefaultPixelTimeoutMS = 5000
	DefaultPixelPollMS    = 100
	DefaultPixelThreshold = 0.02
	DefaultPixelStableMS  = 300
	PixelModeAny          = "any"
	PixelModeStable       = "stable"

	DefaultTextTimeoutMS     = 8000
	DefaultTextPollMS        = 250
	DefaultTextMinConfidence = 0.6
)

// WindowRequest carries the inputs for WaitForWindow.
type WindowRequest struct {
	Match     contract.WindowTarget `json:"match"`
	Present   *bool                 `json:"present,omitempty"`
	Focused   *bool                 `json:"focused,omitempty"`
	TimeoutMS int                   `json:"timeout_ms,omitempty"`
	PollMS    int                   `json:"poll_ms,omitempty"`
}

// WindowResult is the success envelope for WaitForWindow.
type WindowResult struct {
	Matched   *contract.WindowInfo `json:"matched"`
	ElapsedMS int                  `json:"elapsed_ms"`
	Polls     int                  `json:"polls"`
	LastState map[string]any       `json:"last_state"`
}

// PixelRequest carries the inputs for WaitForPixelChange. Region
// accepts both bare {x,y,width,height} (v0.1/v0.2 screen-space) and the
// task-12 extended shape ({...,space:"window"|"window_frame"|"monitor",
// target?,monitor_index?}). The region is resolved to screen-space once
// per call before pixel capture begins.
type PixelRequest struct {
	Region    contract.RegionRef `json:"region" jsonschema:"poll region; bare {x,y,width,height} is screen-space, extended shape adds space/target/monitor_index"`
	Threshold float64            `json:"threshold,omitempty"`
	TimeoutMS int                `json:"timeout_ms,omitempty"`
	PollMS    int                `json:"poll_ms,omitempty"`
	Mode      string             `json:"mode,omitempty"`
	StableMS  int                `json:"stable_ms,omitempty"`
}

// PixelResult is the success envelope for WaitForPixelChange.
type PixelResult struct {
	Changed   bool           `json:"changed"`
	Diff      float64        `json:"diff"`
	ElapsedMS int            `json:"elapsed_ms"`
	Polls     int            `json:"polls"`
	LastState map[string]any `json:"last_state"`
}

// TextRequest carries the inputs for WaitForText. Region accepts the
// same dual shape as PixelRequest.Region: bare bounds for screen-space
// (v0.1/v0.2 wire compat) or the extended {space,target?,monitor_index?}
// envelope for window-space / monitor-space polling.
type TextRequest struct {
	Query         string             `json:"query"`
	Region        contract.RegionRef `json:"region,omitzero" jsonschema:"OCR region; bare {x,y,width,height} is screen-space, extended shape adds space/target/monitor_index"`
	Present       *bool              `json:"present,omitempty"`
	TimeoutMS     int                `json:"timeout_ms,omitempty"`
	PollMS        int                `json:"poll_ms,omitempty"`
	MinConfidence float64            `json:"min_confidence,omitempty"`
	Lang          string             `json:"lang,omitempty"`
	CaseSensitive bool               `json:"case_sensitive,omitempty"`
	Regex         bool               `json:"regex,omitempty"`
}

// TextResult is the success envelope for WaitForText.
type TextResult struct {
	Found     bool                    `json:"found"`
	Candidate *contract.FindCandidate `json:"candidate"`
	ElapsedMS int                     `json:"elapsed_ms"`
	Polls     int                     `json:"polls"`
	LastState map[string]any          `json:"last_state"`
}

// presentValue returns the effective Present setting (default true).
func presentValue(p *bool) bool {
	if p == nil {
		return true
	}
	return *p
}

// resolveDurations applies defaults and converts to time.Duration. A
// negative value is treated as zero. Caller-supplied values must be
// non-negative; validation is done at the request boundary.
func resolveDurations(timeoutMS, pollMS, defaultTimeoutMS, defaultPollMS int) (time.Duration, time.Duration) {
	if timeoutMS <= 0 {
		timeoutMS = defaultTimeoutMS
	}
	if pollMS <= 0 {
		pollMS = defaultPollMS
	}
	return time.Duration(timeoutMS) * time.Millisecond, time.Duration(pollMS) * time.Millisecond
}

// WaitForWindow polls EWMH _NET_CLIENT_LIST until a window matching
// req.Match appears (Present=true) or disappears (Present=false). The
// matcher is contract.MatchWindowInfo so behavior is identical to the
// focus_window and window_* verbs. When req.Focused is set, the matched
// window must additionally satisfy the requested focused state.
func WaitForWindow(ctx context.Context, req WindowRequest) (WindowResult, error) {
	if req.Match.Empty() {
		return WindowResult{}, contract.Validation("WAIT_MATCH_REQUIRED", "wait_for_window requires a non-empty match selector", nil)
	}
	timeout, poll := resolveDurations(req.TimeoutMS, req.PollMS, DefaultWindowTimeoutMS, DefaultWindowPollMS)
	want := presentValue(req.Present)
	var wantFocused *bool
	if req.Focused != nil {
		v := *req.Focused
		wantFocused = &v
	}

	start := time.Now()
	deadline := start.Add(timeout)
	polls := 0
	var lastCandidates []contract.WindowInfo
	for {
		polls++
		wins, err := window.List(ctx)
		if err != nil {
			return WindowResult{}, err
		}
		lastCandidates = wins
		hit := findMatch(wins, req.Match, wantFocused)
		if want {
			if hit != nil {
				return WindowResult{
					Matched:   hit,
					ElapsedMS: msSince(start),
					Polls:     polls,
					LastState: windowLastState(wins, hit, want, wantFocused),
				}, nil
			}
		} else {
			if hit == nil {
				return WindowResult{
					Matched:   nil,
					ElapsedMS: msSince(start),
					Polls:     polls,
					LastState: windowLastState(wins, nil, want, wantFocused),
				}, nil
			}
		}
		if remaining := time.Until(deadline); remaining <= 0 {
			return WindowResult{}, timeoutError("wait_for_window", windowTimeoutDetails(req, polls, msSince(start), lastCandidates, hit, want, wantFocused))
		}
		if err := sleep(ctx, poll, deadline); err != nil {
			return WindowResult{}, err
		}
	}
}

// findMatch returns the first window that satisfies the matcher (and
// the optional focused filter), or nil when no candidate matches.
func findMatch(wins []contract.WindowInfo, target contract.WindowTarget, wantFocused *bool) *contract.WindowInfo {
	for i := range wins {
		if !contract.MatchWindowInfo(wins[i], target) {
			continue
		}
		if wantFocused != nil && wins[i].Focused != *wantFocused {
			continue
		}
		w := wins[i]
		return &w
	}
	return nil
}

func windowLastState(wins []contract.WindowInfo, matched *contract.WindowInfo, present bool, wantFocused *bool) map[string]any {
	out := map[string]any{
		"present":          present,
		"candidate_count":  len(wins),
		"window_summaries": summarizeWindows(wins),
	}
	if matched != nil {
		out["matched"] = map[string]any{
			"id":      matched.ID,
			"title":   matched.Title,
			"class":   matched.Class,
			"pid":     matched.PID,
			"focused": matched.Focused,
		}
	}
	if wantFocused != nil {
		out["required_focused"] = *wantFocused
	}
	return out
}

func windowTimeoutDetails(req WindowRequest, polls, elapsedMS int, wins []contract.WindowInfo, matched *contract.WindowInfo, present bool, wantFocused *bool) map[string]any {
	return map[string]any{
		"condition":  "wait_for_window",
		"match":      req.Match,
		"present":    present,
		"polls":      polls,
		"elapsed_ms": elapsedMS,
		"last_state": windowLastState(wins, matched, present, wantFocused),
	}
}

func summarizeWindows(wins []contract.WindowInfo) []map[string]any {
	out := make([]map[string]any, 0, len(wins))
	for _, w := range wins {
		out = append(out, map[string]any{
			"id":      w.ID,
			"title":   w.Title,
			"class":   w.Class,
			"pid":     w.PID,
			"focused": w.Focused,
		})
	}
	return out
}

// WaitForPixelChange captures a baseline of the requested screen
// region, then polls until the region's dhash signature diverges from
// the baseline (mode "any") or has been stable for StableMS (mode
// "stable").
func WaitForPixelChange(ctx context.Context, req PixelRequest) (PixelResult, error) {
	if req.Region.Empty() {
		return PixelResult{}, contract.Validation("WAIT_REGION_REQUIRED", "wait_for_pixel_change requires a non-empty region", map[string]any{"region": req.Region})
	}
	// Resolve the coord-space-aware region once. Resolution must happen
	// before the polling loop so window/monitor-space failures surface
	// as a single validation/precondition error instead of recurring on
	// every poll. The resolved Bounds drives all subsequent CaptureRGBA
	// calls — last_state and timeout details continue to report the
	// resolved screen-space rectangle so agents see what was actually
	// polled.
	resolvedRegion, err := screen.ResolveCaptureRegion(ctx, req.Region)
	if err != nil {
		return PixelResult{}, err
	}
	timeout, poll := resolveDurations(req.TimeoutMS, req.PollMS, DefaultPixelTimeoutMS, DefaultPixelPollMS)
	threshold := req.Threshold
	if threshold <= 0 {
		threshold = DefaultPixelThreshold
	}
	mode := req.Mode
	if mode == "" {
		mode = PixelModeAny
	}
	if mode != PixelModeAny && mode != PixelModeStable {
		return PixelResult{}, contract.Validation("WAIT_MODE_INVALID", "wait_for_pixel_change mode must be any or stable", map[string]any{"mode": req.Mode})
	}
	stableMS := req.StableMS
	if stableMS <= 0 {
		stableMS = DefaultPixelStableMS
	}
	stable := time.Duration(stableMS) * time.Millisecond

	start := time.Now()
	deadline := start.Add(timeout)

	baselineImg, _, err := screen.CaptureRGBA(ctx, resolvedRegion)
	if err != nil {
		return PixelResult{}, err
	}
	baseHash := dhash16(baselineImg)

	polls := 0
	// For stable mode we track the most recent "still" timestamp — the
	// last time we observed no change relative to the previous snapshot.
	prevHash := baseHash
	stableSince := time.Now()
	var lastDiff float64
	for {
		polls++
		if err := sleep(ctx, poll, deadline); err != nil {
			return PixelResult{}, err
		}
		img, _, err := screen.CaptureRGBA(ctx, resolvedRegion)
		if err != nil {
			return PixelResult{}, err
		}
		curHash := dhash16(img)
		diff := hashDiff(baseHash, curHash)
		lastDiff = diff
		switch mode {
		case PixelModeAny:
			if diff >= threshold {
				return PixelResult{
					Changed:   true,
					Diff:      diff,
					ElapsedMS: msSince(start),
					Polls:     polls,
					LastState: pixelLastState(diff, threshold, mode, stable),
				}, nil
			}
		case PixelModeStable:
			// Compare to previous sample, not the baseline; "stable"
			// means consecutive frames don't change beyond threshold.
			frameDiff := hashDiff(prevHash, curHash)
			if frameDiff < threshold {
				if time.Since(stableSince) >= stable {
					return PixelResult{
						Changed:   true,
						Diff:      diff,
						ElapsedMS: msSince(start),
						Polls:     polls,
						LastState: pixelLastStateStable(diff, frameDiff, threshold, stable, time.Since(stableSince)),
					}, nil
				}
			} else {
				stableSince = time.Now()
			}
			prevHash = curHash
		}
		if time.Until(deadline) <= 0 {
			details := pixelTimeoutDetails(req, polls, msSince(start), lastDiff, mode, stable)
			details["resolved_region"] = resolvedRegion
			return PixelResult{}, timeoutError("wait_for_pixel_change", details)
		}
	}
}

func pixelLastState(diff, threshold float64, mode string, stable time.Duration) map[string]any {
	return map[string]any{
		"diff":      diff,
		"threshold": threshold,
		"mode":      mode,
		"stable_ms": int(stable / time.Millisecond),
	}
}

func pixelLastStateStable(baseDiff, frameDiff, threshold float64, requiredStable, observedStable time.Duration) map[string]any {
	return map[string]any{
		"diff":               baseDiff,
		"frame_diff":         frameDiff,
		"threshold":          threshold,
		"mode":               PixelModeStable,
		"stable_ms":          int(requiredStable / time.Millisecond),
		"observed_stable_ms": int(observedStable / time.Millisecond),
	}
}

func pixelTimeoutDetails(req PixelRequest, polls, elapsedMS int, lastDiff float64, mode string, stable time.Duration) map[string]any {
	return map[string]any{
		"condition":  "wait_for_pixel_change",
		"region":     req.Region,
		"mode":       mode,
		"threshold":  req.Threshold,
		"polls":      polls,
		"elapsed_ms": elapsedMS,
		"last_state": pixelLastState(lastDiff, req.Threshold, mode, stable),
	}
}

// WaitForText polls find_text until the requested query is found
// (Present=true) or vanishes (Present=false). Reuses imageutil.FindText
// — no OCR logic is duplicated here.
func WaitForText(ctx context.Context, req TextRequest) (TextResult, error) {
	if req.Query == "" {
		return TextResult{}, contract.Validation("WAIT_QUERY_REQUIRED", "wait_for_text requires a query", nil)
	}
	timeout, poll := resolveDurations(req.TimeoutMS, req.PollMS, DefaultTextTimeoutMS, DefaultTextPollMS)
	minConf := req.MinConfidence
	if minConf <= 0 {
		minConf = DefaultTextMinConfidence
	}
	want := presentValue(req.Present)

	start := time.Now()
	deadline := start.Add(timeout)
	polls := 0
	// Use a fresh BatchContext per call; per-poll captures vary so the
	// OCR cache would never hit, but a non-nil batch keeps the API
	// shape consistent.
	bctx := imageutil.NewBatchContext()
	var lastCand *contract.FindCandidate
	for {
		polls++
		region, err := resolveTextRegion(ctx, req.Region)
		if err != nil {
			return TextResult{}, err
		}
		img, capture, err := screen.CaptureRGBA(ctx, region)
		if err != nil {
			return TextResult{}, err
		}
		result, err := imageutil.FindText(ctx, bctx, capture, img, imageutil.FindTextRequest{
			Region:        capture,
			Query:         req.Query,
			Lang:          req.Lang,
			CaseSensitive: req.CaseSensitive,
			MinConfidence: minConf,
			Regex:         req.Regex,
		})
		if err != nil {
			// FindText dependency failures (e.g. tesseract missing)
			// must surface verbatim; they are not timeouts.
			return TextResult{}, err
		}
		var hit *contract.FindCandidate
		if len(result.Candidates) > 0 {
			c := result.Candidates[0]
			hit = &c
		}
		lastCand = hit
		if want {
			if hit != nil {
				return TextResult{
					Found:     true,
					Candidate: hit,
					ElapsedMS: msSince(start),
					Polls:     polls,
					LastState: textLastState(req.Query, hit, want, len(result.Candidates)),
				}, nil
			}
		} else {
			if hit == nil {
				return TextResult{
					Found:     false,
					Candidate: nil,
					ElapsedMS: msSince(start),
					Polls:     polls,
					LastState: textLastState(req.Query, nil, want, 0),
				}, nil
			}
		}
		if time.Until(deadline) <= 0 {
			return TextResult{}, timeoutError("wait_for_text", textTimeoutDetails(req, polls, msSince(start), lastCand, want))
		}
		if err := sleep(ctx, poll, deadline); err != nil {
			return TextResult{}, err
		}
	}
}

// resolveTextRegion mirrors pipeline.resolveRegion: an explicit region
// wins (and is translated through ResolveCaptureRegion so window/monitor-
// space requests work), otherwise the focused window, otherwise the
// full screen. Done inline here so the wait package doesn't take a
// dependency on the pipeline package.
func resolveTextRegion(ctx context.Context, region contract.RegionRef) (contract.Bounds, error) {
	if !region.Empty() {
		return screen.ResolveCaptureRegion(ctx, region)
	}
	if focused, err := window.Focused(ctx); err == nil && focused != nil && !focused.Bounds.Empty() {
		return focused.Bounds, nil
	}
	info, err := screen.Info(ctx)
	if err != nil {
		return contract.Bounds{}, err
	}
	return info.Bounds, nil
}

func textLastState(query string, cand *contract.FindCandidate, present bool, candidateCount int) map[string]any {
	out := map[string]any{
		"query":           query,
		"present":         present,
		"candidate_count": candidateCount,
	}
	if cand != nil {
		out["candidate"] = cand
	}
	return out
}

func textTimeoutDetails(req TextRequest, polls, elapsedMS int, last *contract.FindCandidate, present bool) map[string]any {
	return map[string]any{
		"condition":  "wait_for_text",
		"query":      req.Query,
		"region":     req.Region,
		"present":    present,
		"polls":      polls,
		"elapsed_ms": elapsedMS,
		"last_state": textLastState(req.Query, last, present, 0),
	}
}

// timeoutError wraps a wait timeout in a precondition error with code
// WAIT_TIMEOUT. Per the task contract, timeouts are exit 5 (Precondition).
func timeoutError(condition string, details map[string]any) error {
	return contract.Precondition("WAIT_TIMEOUT", condition+" timed out before the condition was met", details)
}

// sleep waits for d or for the parent ctx to be cancelled, whichever
// comes first. Returns contract.Cancelled (exit 6) when ctx is done.
// When deadline is set and earlier than now+d the sleep is shortened.
func sleep(ctx context.Context, d time.Duration, deadline time.Time) error {
	if remaining := time.Until(deadline); remaining > 0 && remaining < d {
		d = remaining
	}
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return contract.Cancelled("wait cancelled by parent context")
	case <-t.C:
		return nil
	}
}

func msSince(t time.Time) int {
	return int(time.Since(t) / time.Millisecond)
}

// --- dhash --------------------------------------------------------------

// dhash16 computes a 16x16 difference hash signature for img. The
// algorithm: downscale to 17x16 grayscale (nearest neighbor), then for
// each pixel set a bit to 1 when the left neighbor is brighter than
// the right. The resulting 256-bit signature is robust to small
// translations and intensity shifts while remaining sensitive to
// structural change.
func dhash16(img *image.RGBA) [32]byte {
	const w = 17
	const h = 16
	if img == nil {
		return [32]byte{}
	}
	src := img.Bounds()
	srcW := src.Dx()
	srcH := src.Dy()
	if srcW == 0 || srcH == 0 {
		return [32]byte{}
	}
	gray := make([]uint8, w*h)
	for y := 0; y < h; y++ {
		sy := y * srcH / h
		for x := 0; x < w; x++ {
			sx := x * srcW / w
			c := img.RGBAAt(src.Min.X+sx, src.Min.Y+sy)
			// Rec.601 luma
			lum := (uint32(c.R)*299 + uint32(c.G)*587 + uint32(c.B)*114) / 1000
			gray[y*w+x] = uint8(lum)
		}
	}
	var out [32]byte
	bit := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w-1; x++ {
			if gray[y*w+x] > gray[y*w+x+1] {
				out[bit/8] |= 1 << (uint(bit) % 8)
			}
			bit++
		}
	}
	return out
}

// hashDiff returns the Hamming distance between two 256-bit dhash
// signatures normalized to [0, 1]. 0 = identical, 1 = every bit flipped.
func hashDiff(a, b [32]byte) float64 {
	var bits int
	for i := 0; i < 32; i++ {
		bits += popcount(a[i] ^ b[i])
	}
	return float64(bits) / 256.0
}

func popcount(x byte) int {
	x = x - ((x >> 1) & 0x55)
	x = (x & 0x33) + ((x >> 2) & 0x33)
	return int((x + (x >> 4)) & 0x0f)
}
