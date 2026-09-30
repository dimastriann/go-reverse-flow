// Package health tracks backend availability by probing each backend at a
// fixed interval and reporting the latest known status.
//
// A probe is an HTTP GET with a short timeout; any response with a status
// code below 500 counts as healthy, so backends that return 404 on the probe
// path are still considered up. Status transitions are stored under an
// RWMutex, making Healthy cheap and safe on request paths.
package health

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Default probe settings.
const (
	DefaultInterval = 5 * time.Second
	DefaultTimeout  = time.Second
)

// Checker periodically probes backends and exposes their liveness.
type Checker struct {
	client *http.Client
	probe  func(url string) bool // indirection for tests

	mu      sync.RWMutex
	healthy map[string]bool

	onTransition func(backend string, up bool) // optional, runs without mu held
}

// Option configures a Checker.
type Option func(*Checker)

// WithTransitionHook registers a callback fired on every up/down flip.
// The callback must not call back into the Checker's exported methods while
// a sweep applies transitions; it receives the backend URL and new state.
func WithTransitionHook(fn func(backend string, up bool)) Option {
	return func(c *Checker) {
		c.onTransition = fn
	}
}

// New creates a Checker for the given backend URLs. Every backend starts
// healthy so the first sweep's transitions are authoritative, not a guess.
func New(urls []string, timeout time.Duration, opts ...Option) *Checker {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	c := &Checker{
		client:  &http.Client{Timeout: timeout},
		healthy: make(map[string]bool, len(urls)),
	}
	for _, u := range urls {
		c.healthy[u] = true
	}
	c.probe = func(u string) bool { return probe(c.client, u) }
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// probe performs one GET and reports whether the backend should be
// considered healthy.
func probe(client *http.Client, url string) bool {
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500
}

// Start launches the periodic sweep goroutine: one immediate sweep, then one
// per tick, until ctx is cancelled. Start must be called once.
func (c *Checker) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		c.sweep()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.sweep()
			}
		}
	}()
}

// sweep probes every backend once and applies any transitions.
func (c *Checker) sweep() {
	c.mu.RLock()
	targets := make([]string, 0, len(c.healthy))
	for u := range c.healthy {
		targets = append(targets, u)
	}
	c.mu.RUnlock()

	for _, u := range targets {
		up := c.probe(u)

		flipped := false
		c.mu.Lock()
		if prev, ok := c.healthy[u]; ok && prev != up {
			c.healthy[u] = up
			flipped = true
		}
		c.mu.Unlock()

		if flipped && c.onTransition != nil {
			c.onTransition(u, up)
		}
	}
}

// Healthy reports the latest known status of the given backend URL.
// Unknown backends count as unhealthy.
func (c *Checker) Healthy(url string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.healthy[url]
}

// HealthyOf returns the URLs currently known to be up, sorted
// lexicographically so successive snapshots have stable order — the
// round-robin counter in the proxy assumes member-position parity between
// calls while the healthy set stays unchanged.
func (c *Checker) HealthyOf() []string {
	c.mu.RLock()
	out := make([]string, 0, len(c.healthy))
	for u, up := range c.healthy {
		if up {
			out = append(out, u)
		}
	}
	c.mu.RUnlock()

	sort.Strings(out)
	return out
}

// String implements fmt.Stringer for diagnostics, e.g. logs.
func (c *Checker) String() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := "health:"
	for u, ok := range c.healthy {
		state := "down"
		if ok {
			state = "up"
		}
		out += fmt.Sprintf(" %s=%s", u, state)
	}
	return out
}
