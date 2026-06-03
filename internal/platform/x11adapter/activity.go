//go:build linux

package x11adapter

import (
	"context"
	"time"

	"github.com/1broseidon/mc/internal/platform"
	"github.com/1broseidon/mc/internal/yield"
)

// xinputActivity implements platform.UserActivityWatcher over the existing
// XInput2 text-stream watcher. It stays adapter-side so the pipeline can
// depend only on platform.ActivityEvent rather than the Linux-specific
// yield package.
type xinputActivity struct{}

func (xinputActivity) Available() (bool, string) {
	return yield.Available()
}

func (xinputActivity) Start(ctx context.Context) (<-chan platform.ActivityEvent, func(), error) {
	w := &yield.Watcher{Buffer: 64}
	if err := w.Start(ctx); err != nil {
		return nil, nil, err
	}
	out := make(chan platform.ActivityEvent, 64)
	go func() {
		defer close(out)
		for ev := range w.Events() {
			out <- platform.ActivityEvent{
				Kind:     string(ev.Kind),
				DeviceID: ev.Master,
				SourceID: ev.Slave,
				Detail:   ev.Detail,
				TS:       ev.TS,
			}
		}
	}()
	return out, w.Stop, nil
}

func (xinputActivity) Sample(ctx context.Context, d time.Duration) (int, error) {
	return yield.SampleUserEvents(ctx, d)
}
