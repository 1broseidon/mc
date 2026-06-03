//go:build linux

package x11adapter

import (
	"testing"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/platform"
)

// TestClientBoundsCalculation exercises the pure decoration-inset math
// without touching X11. Verifies that the client_bounds rectangle
// correctly subtracts WM frame extents and that zero-inset (or oversized)
// windows fall back to the outer bounds. Migrated from internal/window
// when the EWMH property readers moved into the adapter.
func TestClientBoundsCalculation(t *testing.T) {
	cases := []struct {
		name   string
		outer  contract.Bounds
		insets contract.DecorationInsets
		want   contract.Bounds
	}{
		{
			name:   "no insets falls back to outer",
			outer:  contract.Bounds{X: 100, Y: 200, Width: 800, Height: 600},
			insets: contract.DecorationInsets{},
			want:   contract.Bounds{X: 100, Y: 200, Width: 800, Height: 600},
		},
		{
			name:   "standard decoration subtracts borders",
			outer:  contract.Bounds{X: 100, Y: 200, Width: 800, Height: 600},
			insets: contract.DecorationInsets{Left: 1, Top: 25, Right: 1, Bottom: 1},
			want:   contract.Bounds{X: 101, Y: 225, Width: 798, Height: 574},
		},
		{
			name:   "oversized insets fall back to outer",
			outer:  contract.Bounds{X: 0, Y: 0, Width: 100, Height: 100},
			insets: contract.DecorationInsets{Left: 200, Top: 200, Right: 200, Bottom: 200},
			want:   contract.Bounds{X: 0, Y: 0, Width: 100, Height: 100},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clientBounds(tc.outer, tc.insets)
			if got != tc.want {
				t.Fatalf("clientBounds(%+v, %+v) = %+v, want %+v", tc.outer, tc.insets, got, tc.want)
			}
		})
	}
}

// TestWindowManagerImplementsWorkspaceReader guards the optional
// capability wiring: the X11 window backend must satisfy
// platform.WindowWorkspaceReader so the service's workspace honored-check
// stays live on Linux.
func TestWindowManagerImplementsWorkspaceReader(t *testing.T) {
	if _, ok := platform.WindowManager(windowManager{}).(platform.WindowWorkspaceReader); !ok {
		t.Fatal("windowManager must implement platform.WindowWorkspaceReader")
	}
}
