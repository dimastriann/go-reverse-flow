// Package proxy wires a round-robin balancer to a set of backend servers.
//
// Handler forwards each incoming request to the next backend in
// round-robin order using net/http/httputil.ReverseProxy, which streams the
// response and handles hop-by-hop headers and X-Forwarded-For for us.
//
// With health routing enabled, an idempotent request whose first attempt
// dies on the wire (transport error before any bytes reach the client) is
// retried once against a different healthy backend; non-idempotent methods
// and half-written responses surface as 502s, never as side-effecting
// replays.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/dimastriann/go-reverse-flow/internal/balancer"
	"github.com/dimastriann/go-reverse-flow/internal/health"
	"github.com/dimastriann/go-reverse-flow/internal/logging"
	"github.com/dimastriann/go-reverse-flow/internal/metrics"
)

var (
	errNoBackends  = errors.New("proxy: at least one backend is required")
	errNilBalancer = errors.New("proxy: balancer must not be nil")
	errNilBackend  = errors.New("proxy: backend must not be nil")
)

// Handler is an http.Handler that load-balances requests over backends.
type Handler struct {
	proxies []*httputil.ReverseProxy
	urls    []string // parallel to proxies, for health lookups
	index   map[string]int
	bal     *balancer.Balancer
	health  *health.Checker    // optional; nil means route blindly
	metrics *metrics.Collector // optional; nil means no counting
}

// Option configures a Handler.
type Option func(*Handler)

// WithHealth enables health-aware routing: the round-robin pointer walks only
// over the backends the checker currently reports up.
func WithHealth(c *health.Checker) Option {
	return func(h *Handler) {
		h.health = c
	}
}

// WithMetrics enables per-backend request counting on every forwarded
// attempt (503 paths do not count: no backend was chosen).
func WithMetrics(c *metrics.Collector) Option {
	return func(h *Handler) {
		h.metrics = c
	}
}

// New builds one ReverseProxy per backend URL. All listeners should exist by
// the time New is called; New itself performs no network I/O.
func New(backends []*url.URL, bal *balancer.Balancer, opts ...Option) (*Handler, error) {
	if len(backends) == 0 {
		return nil, errNoBackends
	}
	if bal == nil {
		return nil, errNilBalancer
	}

	h := &Handler{
		proxies: make([]*httputil.ReverseProxy, len(backends)),
		urls:    make([]string, len(backends)),
		index:   make(map[string]int, len(backends)),
		bal:     bal,
	}
	for i, target := range backends {
		if target == nil {
			return nil, fmt.Errorf("%w (index %d)", errNilBackend, i)
		}
		h.proxies[i] = httputil.NewSingleHostReverseProxy(target)
		h.urls[i] = target.String()
		h.index[target.String()] = i
	}
	h.configureErrorHandlers()
	for _, opt := range opts {
		opt(h)
	}
	return h, nil
}

// attemptState travels per outbound attempt through the request context: the
// per-backend ErrorHandler records wire failures there, so ServeHTTP can
// decide between retrying and surfacing 502 — without globals.
type attemptState struct {
	failed bool
	final  bool
	target string
}

type attemptKey struct{}

// configureErrorHandlers gives each per-backend proxy an ErrorHandler that
// reports the failure to the attempt state instead of writing 502 blindly;
// the caller decides whether a retry (or the single honest 502) follows.
func (h *Handler) configureErrorHandlers() {
	for i, rp := range h.proxies {
		target := h.urls[i]
		rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy: backend %s: %v", target, err)
			as, ok := r.Context().Value(attemptKey{}).(*attemptState)
			if !ok || as == nil {
				// Called outside the attempt machinery: keep the honest 502.
				http.Error(w, "backend unavailable", http.StatusBadGateway)
				return
			}
			as.failed = true
			if as.final {
				http.Error(w, "backend unavailable", http.StatusBadGateway)
			}
		}
	}
}

// writeTracker mirrors the recording behavior needed for the retry rule: a
// response that already reached the client forbids a second attempt.
type writeTracker struct {
	http.ResponseWriter
	written bool
}

func (w *writeTracker) WriteHeader(status int) {
	w.written = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *writeTracker) Write(b []byte) (int, error) {
	w.written = true
	return w.ResponseWriter.Write(b)
}

// retryable reports whether the request may legally replay on a different
// backend: only safe methods, and only with an alternate healthy target.
func (h *Handler) retryable(r *http.Request) bool {
	if h.health == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	return len(h.health.HealthyOf()) > 1
}

// pickAlternate chooses the next backend among healthy backends that are not
// the just-failed URL; returns -1 when nothing else is available.
func (h *Handler) pickAlternate(failedURL string) int {
	ups := h.health.HealthyOf()
	alive := make([]string, 0, len(ups))
	for _, u := range ups {
		if u != failedURL {
			alive = append(alive, u)
		}
	}
	if len(alive) == 0 {
		return -1
	}
	pos := h.bal.Next(len(alive))
	return h.index[alive[pos]]
}

// ServeHTTP forwards the request to the next backend. With health routing
// enabled the round-robin pointer distributes over currently healthy
// backends only; when no backend is up it answers 503 rather than retrying
// blindly into the void.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	idx, status := h.pick()
	if status != nil {
		http.Error(w, status.message, status.code)
		return
	}

	attempts := 1
	if h.retryable(r) {
		attempts = 2
	}

	for attempt := 0; attempt < attempts; attempt++ {
		as := &attemptState{final: attempt == attempts-1, target: h.urls[idx]}
		tracked := &writeTracker{ResponseWriter: w}
		req2 := r.Clone(context.WithValue(r.Context(), attemptKey{}, as))

		// Report the chosen backend to an outer logging middleware when
		// present (each attempt overwrites, so the last one wins).
		if info := logging.From(r.Context()); info != nil {
			info.Backend = h.urls[idx]
		}
		if h.metrics != nil {
			h.metrics.Count(h.urls[idx])
		}

		h.proxies[idx].ServeHTTP(tracked, req2)

		if attempt == attempts-1 {
			return
		}
		if !tracked.written && as.failed {
			// Dead on the wire before the client saw anything: replay once
			// on a different healthy backend.
			if alt := h.pickAlternate(h.urls[idx]); alt != -1 {
				idx = alt
				continue
			}
			return // nothing else up: the final honest failure stands
		}
		return // backend answered, or headers already went to the client
	}
}

// pickStatus bundles the early-return failure modes of routing.
type pickStatus struct {
	code    int
	message string
}

// pick chooses the next backend index and reports failure modes.
func (h *Handler) pick() (int, *pickStatus) {
	if h.health != nil {
		ups := h.health.HealthyOf()
		if len(ups) == 0 {
			return 0, &pickStatus{code: http.StatusServiceUnavailable, message: "no healthy backend available"}
		}
		pos := h.bal.Next(len(ups))
		return h.index[ups[pos]], nil
	}
	return h.bal.Next(len(h.proxies)), nil
}
