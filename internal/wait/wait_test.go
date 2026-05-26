package wait

import (
	"context"
	"errors"
	"image"
	"image/color"
	"os"
	"testing"
	"time"

	"github.com/1broseidon/mc/internal/contract"
)

// TestPresentValue verifies the default-true behavior of presentValue
// and explicit overrides.
func TestPresentValue(t *testing.T) {
	if !presentValue(nil) {
		t.Fatalf("nil pointer must default to true")
	}
	yes := true
	no := false
	if !presentValue(&yes) {
		t.Fatalf("explicit true should return true")
	}
	if presentValue(&no) {
		t.Fatalf("explicit false should return false")
	}
}

// TestResolveDurations covers default fallback and explicit overrides.
func TestResolveDurations(t *testing.T) {
	timeout, poll := resolveDurations(0, 0, 5000, 100)
	if timeout != 5*time.Second {
		t.Fatalf("expected default timeout 5s, got %v", timeout)
	}
	if poll != 100*time.Millisecond {
		t.Fatalf("expected default poll 100ms, got %v", poll)
	}
	timeout, poll = resolveDurations(250, 50, 5000, 100)
	if timeout != 250*time.Millisecond {
		t.Fatalf("expected explicit timeout 250ms, got %v", timeout)
	}
	if poll != 50*time.Millisecond {
		t.Fatalf("expected explicit poll 50ms, got %v", poll)
	}
}

// TestFindMatch exercises the helper that combines MatchWindowInfo
// with the optional focused filter.
func TestFindMatch(t *testing.T) {
	wins := []contract.WindowInfo{
		{ID: "0x1", XID: 1, Class: "fam-ui", Focused: false},
		{ID: "0x2", XID: 2, Class: "term", Focused: true},
	}
	t.Run("matches by class", func(t *testing.T) {
		got := findMatch(wins, contract.WindowTarget{Class: "fam-ui"}, nil)
		if got == nil || got.ID != "0x1" {
			t.Fatalf("expected 0x1, got %+v", got)
		}
	})
	t.Run("no match returns nil", func(t *testing.T) {
		got := findMatch(wins, contract.WindowTarget{Class: "missing"}, nil)
		if got != nil {
			t.Fatalf("expected nil, got %+v", got)
		}
	})
	t.Run("focused filter requires match", func(t *testing.T) {
		yes := true
		got := findMatch(wins, contract.WindowTarget{Class: "fam-ui"}, &yes)
		if got != nil {
			t.Fatalf("fam-ui is not focused; expected nil, got %+v", got)
		}
	})
	t.Run("focused filter accepts match", func(t *testing.T) {
		yes := true
		got := findMatch(wins, contract.WindowTarget{Class: "term"}, &yes)
		if got == nil || got.ID != "0x2" {
			t.Fatalf("expected 0x2, got %+v", got)
		}
	})
}

// TestDhashIdentical: identical buffers must hash to zero distance.
func TestDhashIdentical(t *testing.T) {
	a := makeRGBA(64, 64, color.RGBA{R: 100, G: 150, B: 200, A: 255})
	hA := dhash16(a)
	hB := dhash16(a)
	if diff := hashDiff(hA, hB); diff != 0 {
		t.Fatalf("identical images must produce diff=0, got %v", diff)
	}
}

// TestDhashDifferent: a fully different pattern must produce a
// substantial diff.
func TestDhashDifferent(t *testing.T) {
	uniform := makeRGBA(64, 64, color.RGBA{R: 10, G: 10, B: 10, A: 255})
	gradient := makeGradient(64, 64)
	diff := hashDiff(dhash16(uniform), dhash16(gradient))
	if diff < 0.1 {
		t.Fatalf("a uniform vs gradient image must produce diff >= 0.1, got %v", diff)
	}
}

// TestPopcount sanity-checks the bit population counter.
func TestPopcount(t *testing.T) {
	cases := []struct {
		in   byte
		want int
	}{
		{0x00, 0}, {0xff, 8}, {0x0f, 4}, {0xaa, 4}, {0x01, 1},
	}
	for _, c := range cases {
		if got := popcount(c.in); got != c.want {
			t.Fatalf("popcount(%#x) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestSleepRespectsContext verifies that sleep returns a
// contract.Cancelled (exit 6) when ctx is cancelled mid-wait. No X11
// connection is opened.
func TestSleepRespectsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- sleep(ctx, time.Hour, time.Now().Add(time.Hour))
	}()
	time.AfterFunc(10*time.Millisecond, cancel)
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("expected cancellation error, got nil")
		}
		if code := contract.ErrorCode(err); code != contract.ExitCancelled {
			t.Fatalf("expected exit 6 (cancelled), got %d (%v)", code, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("sleep did not return after context cancel")
	}
}

// TestSleepDeadlineShortens ensures the helper clamps to the wait
// deadline so we never sleep past the timeout.
func TestSleepDeadlineShortens(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	deadline := start.Add(20 * time.Millisecond)
	err := sleep(ctx, time.Hour, deadline)
	if err != nil {
		t.Fatalf("sleep returned error: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Fatalf("sleep should have honored 20ms deadline; took %v", elapsed)
	}
}

// TestTimeoutErrorShape: WAIT_TIMEOUT must be a precondition (exit 5)
// with last_state attached.
func TestTimeoutErrorShape(t *testing.T) {
	err := timeoutError("wait_for_window", map[string]any{
		"polls":      3,
		"elapsed_ms": 600,
		"last_state": map[string]any{"candidate_count": 0},
	})
	if contract.ErrorCode(err) != contract.ExitPrecondition {
		t.Fatalf("expected exit 5, got %d", contract.ErrorCode(err))
	}
	var app *contract.AppError
	if !errors.As(err, &app) {
		t.Fatalf("expected contract.AppError, got %T", err)
	}
	if app.Code != "WAIT_TIMEOUT" {
		t.Fatalf("expected code WAIT_TIMEOUT, got %s", app.Code)
	}
	if _, ok := app.Details["last_state"]; !ok {
		t.Fatalf("expected last_state in details, got %+v", app.Details)
	}
	if _, ok := app.Details["polls"]; !ok {
		t.Fatalf("expected polls in details, got %+v", app.Details)
	}
}

// TestWaitForWindowValidation: empty match selector is a validation
// error (exit 2). No X11 work is done.
func TestWaitForWindowValidation(t *testing.T) {
	_, err := WaitForWindow(context.Background(), WindowRequest{})
	if err == nil {
		t.Fatalf("expected validation error for empty match")
	}
	if contract.ErrorCode(err) != contract.ExitValidation {
		t.Fatalf("expected exit 2, got %d", contract.ErrorCode(err))
	}
}

// TestWaitForTextValidation: empty query is validation (exit 2). No
// OCR work is done.
func TestWaitForTextValidation(t *testing.T) {
	_, err := WaitForText(context.Background(), TextRequest{})
	if err == nil {
		t.Fatalf("expected validation error for empty query")
	}
	if contract.ErrorCode(err) != contract.ExitValidation {
		t.Fatalf("expected exit 2, got %d", contract.ErrorCode(err))
	}
}

// TestWaitForPixelChangeValidation: empty region is validation (exit
// 2); invalid mode is validation. No X11 work is done.
func TestWaitForPixelChangeValidation(t *testing.T) {
	_, err := WaitForPixelChange(context.Background(), PixelRequest{})
	if contract.ErrorCode(err) != contract.ExitValidation {
		t.Fatalf("expected exit 2 for empty region, got %d (%v)", contract.ErrorCode(err), err)
	}
	_, err = WaitForPixelChange(context.Background(), PixelRequest{
		Region: contract.RegionRefFromBounds(contract.Bounds{X: 0, Y: 0, Width: 10, Height: 10}),
		Mode:   "bogus",
	})
	if contract.ErrorCode(err) != contract.ExitValidation {
		t.Fatalf("expected exit 2 for invalid mode, got %d (%v)", contract.ErrorCode(err), err)
	}
}

// TestWaitForWindowTimeoutNoDisplay covers the live-X11 path when DISPLAY
// is set. We pick a deliberately impossible class so the wait must time
// out; the resulting error must be exit 5 / WAIT_TIMEOUT with polls,
// elapsed_ms, last_state populated. Skipped when DISPLAY is unset.
func TestWaitForWindowTimeoutLive(t *testing.T) {
	if os.Getenv("DISPLAY") == "" {
		t.Skip("DISPLAY not set; skipping live X11 wait test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := WaitForWindow(ctx, WindowRequest{
		Match:     contract.WindowTarget{Class: "__definitely_not_a_real_window_class__"},
		TimeoutMS: 200,
		PollMS:    50,
	})
	if err == nil {
		t.Fatalf("expected WAIT_TIMEOUT, got nil error")
	}
	if contract.ErrorCode(err) != contract.ExitPrecondition {
		t.Fatalf("expected exit 5 (precondition), got %d", contract.ErrorCode(err))
	}
	var app *contract.AppError
	if !errors.As(err, &app) || app.Code != "WAIT_TIMEOUT" {
		t.Fatalf("expected WAIT_TIMEOUT, got %v", err)
	}
	for _, key := range []string{"polls", "elapsed_ms", "last_state"} {
		if _, ok := app.Details[key]; !ok {
			t.Fatalf("expected %q in details, got %+v", key, app.Details)
		}
	}
}

// --- test helpers ----------------------------------------------------

func makeRGBA(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func makeGradient(w, h int) *image.RGBA {
	// Checkerboard pattern: alternating dark/light squares produce
	// many left-vs-right intensity flips, exercising dhash bit set.
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			cell := ((x / 4) + (y / 4)) % 2
			c := color.RGBA{R: 20, G: 20, B: 20, A: 255}
			if cell == 1 {
				c = color.RGBA{R: 220, G: 220, B: 220, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}
