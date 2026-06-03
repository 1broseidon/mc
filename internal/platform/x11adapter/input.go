//go:build linux

package x11adapter

import (
	"context"

	"github.com/jezek/xgb/xtest"

	"github.com/1broseidon/mc/internal/contract"
	"github.com/1broseidon/mc/internal/x11"
)

// withInputDisplay opens a short-lived X11 connection with the XTest
// extension initialized, matching the per-call connection model used
// elsewhere in the adapter. The pointer and keyboard primitives both
// route through here so XTest readiness (XTEST_UNAVAILABLE) is reported
// consistently.
func withInputDisplay(ctx context.Context, fn func(*x11.Display) error) error {
	if err := ctx.Err(); err != nil {
		return contract.Cancelled("input operation cancelled")
	}
	d, err := x11.Open()
	if err != nil {
		return err
	}
	defer d.Close()
	if err := xtest.Init(d.Conn); err != nil {
		return contract.Dependency("XTEST_UNAVAILABLE", "XTest extension is not available", map[string]any{"error": err.Error()})
	}
	return fn(d)
}
