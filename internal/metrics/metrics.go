// Package metrics collects per-backend counters and exposes them as a
// Prometheus-style text endpoint.
//
// It intentionally keeps to the stdlib: the format below is what the
// Prometheus text exposition looks like, and /metrics responses carry
// Content-Type: text/plain; version=0.0.4, so a real Prometheus server can
// scrape it (only the counter and gauge families are used here).
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
)

// Collector owns all counters for one running proxy.
type Collector struct {
	mu       sync.Mutex
	counters map[string]*atomic.Uint64

	// statusFn reports the latest known verdict per tracked backend; nil disables the gauge family.
	statusFn func() map[string]bool
}

// Option configures a Collector.
type Option func(*Collector)

// WithStatusSource plugs in a full per-backend verdict function (see
// health.Statuses) so the exposition can render a grf_backend_healthy gauge
// for every tracked backend, up and down alike.
func WithStatusSource(fn func() map[string]bool) Option {
	return func(c *Collector) {
		c.statusFn = fn
	}
}

// New creates an empty Collector; counters appear lazily as backends are
// counted so a backend that never receives traffic still shows up through
// WithHealthySource gauges.
func New(opts ...Option) *Collector {
	c := &Collector{counters: map[string]*atomic.Uint64{}}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Count bumps the request counter for the given backend URL.
// Count is safe for concurrent use.
func (c *Collector) Count(backend string) {
	c.mu.Lock()
	counter := c.counters[backend]
	if counter == nil {
		counter = &atomic.Uint64{}
		c.counters[backend] = counter
	}
	c.mu.Unlock()
	counter.Add(1)
}

// render produces the exposition body. Backends known to either the counter
// map or the health source are all listed, so down-but-never-proxied
// backends still show up with zeroed gauges.
func (c *Collector) render() []byte {
	c.mu.Lock()
	activity := make(map[string]uint64, len(c.counters))
	for url, counter := range c.counters {
		activity[url] = counter.Load()
	}
	c.mu.Unlock()

	healthy := map[string]bool{}
	union := make([]string, 0, max(len(activity), 4))
	for u := range activity {
		union = append(union, u)
	}
	if c.statusFn != nil {
		healthy = c.statusFn()
		for u := range healthy {
			if _, counted := activity[u]; !counted {
				union = append(union, u)
			}
		}
	}
	sort.Strings(union)

	out := "# HELP grf_backend_requests_total Requests the proxy forwarded to this backend.\n" +
		"# TYPE grf_backend_requests_total counter\n"
	for _, u := range union {
		out += fmt.Sprintf("grf_backend_requests_total{backend=%q} %d\n", u, activity[u])
	}

	if c.statusFn != nil {
		out += "# HELP grf_backend_healthy Latest health-check verdict per backend.\n" +
			"# TYPE grf_backend_healthy gauge\n"
		for _, u := range union {
			up := uint64(0)
			if healthy[u] {
				up = 1
			}
			out += fmt.Sprintf("grf_backend_healthy{backend=%q} %d\n", u, up)
		}
	}
	return []byte(out)
}

// Handler serves the exposition endpoint.
func (c *Collector) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write(c.render())
	})
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
