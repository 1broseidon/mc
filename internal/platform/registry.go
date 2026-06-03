package platform

import "sync"

// registry holds the process-wide active Provider. It is set once during
// startup by an adapter's build-tagged init (init_linux.go, init_darwin.go)
// and may be overridden in tests via SetProvider. Access is guarded so the
// rare test that swaps providers concurrently with a probe is safe.
var (
	mu      sync.RWMutex
	current Provider = unsupported{}
)

// Current returns the active platform provider. It never returns nil: when
// no adapter has registered (an unsupported OS, or a unit test that has not
// installed a fake), it returns a provider whose every operation fails with
// a canonical PLATFORM_UNSUPPORTED error. Callers therefore never need a
// nil check.
func Current() Provider {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// SetProvider installs the active provider and returns the previous one so
// tests can restore it with a defer. Adapter init functions call this once;
// tests use it to inject a fake provider.
func SetProvider(p Provider) Provider {
	mu.Lock()
	defer mu.Unlock()
	prev := current
	if p == nil {
		p = unsupported{}
	}
	current = p
	return prev
}
