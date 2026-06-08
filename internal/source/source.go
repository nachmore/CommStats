// Package source defines the comm-source plugin seam. Each communication
// medium (Slack, Email, Zoom, ...) implements Source and self-registers via
// Register, so adding a medium needs no changes to the orchestration code.
package source

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// TimeWindow is the half-open interval [Start, End) a collection covers.
type TimeWindow struct {
	Start time.Time
	End   time.Time
}

// Metric is a single source-agnostic measurement. The store and report layers
// operate purely on Metric and never know which Source produced it.
//
// Value is the absolute count for the window (not a delta). Dimensions carry
// source-specific breakdowns (e.g. {"channel_type":"im"}); they participate in
// a metric's identity so, say, per-channel-type counts coexist as sibling rows.
type Metric struct {
	Source     string
	Name       string
	Value      float64
	Window     TimeWindow
	Dimensions map[string]string
}

// Source collects metrics for a single communication medium over a window.
//
// Collect must be idempotent: invoking it repeatedly for overlapping windows
// must report absolute counts for the window, never deltas, so the store can
// reconcile re-runs without double-counting.
type Source interface {
	Name() string
	Collect(ctx context.Context, window TimeWindow) ([]Metric, error)
}

var (
	mu       sync.RWMutex
	registry = map[string]Source{}
)

// Register adds a Source to the global registry, keyed by its Name. Intended to
// be called from a package init() so importing the package wires it in. Panics
// on duplicate names to surface wiring mistakes at startup.
func Register(s Source) {
	mu.Lock()
	defer mu.Unlock()
	name := s.Name()
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("source: duplicate registration for %q", name))
	}
	registry[name] = s
}

// Registered returns all registered sources sorted by name for deterministic
// iteration order.
func Registered() []Source {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Source, 0, len(registry))
	for _, s := range registry {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Lookup returns the registered source with the given name, if any.
func Lookup(name string) (Source, bool) {
	mu.RLock()
	defer mu.RUnlock()
	s, ok := registry[name]
	return s, ok
}

type interactiveKey struct{}

// WithInteractive marks ctx as a foreground (user-attended) invocation. Sources
// may use this to decide whether prompting the user — e.g. an interactive
// login — is appropriate, versus a scheduled/headless run where it is not.
func WithInteractive(ctx context.Context) context.Context {
	return context.WithValue(ctx, interactiveKey{}, true)
}

// IsInteractive reports whether ctx was marked interactive via WithInteractive.
func IsInteractive(ctx context.Context) bool {
	v, _ := ctx.Value(interactiveKey{}).(bool)
	return v
}
