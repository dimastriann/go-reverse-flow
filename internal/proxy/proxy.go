// Package proxy wires a round-robin balancer to a set of backend servers.
//
// Handler forwards each incoming request to the next backend in
// round-robin order using net/http/httputil.ReverseProxy, which streams the
// response and handles hop-by-hop headers and X-Forwarded-For for us.
package proxy

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/dimastriann/go-reverse-flow/internal/balancer"
	"github.com/dimastriann/go-reverse-flow/internal/health"
	"github.com/dimastriann/go-reverse-flow/internal/logging"
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
	health  *health.Checker // optional; nil means route blindly
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
		rp := httputil.NewSingleHostReverseProxy(target)
		rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy: backend %s: %v", target, err)
			http.Error(w, "backend unavailable", http.StatusBadGateway)
		}
		h.proxies[i] = rp
		h.urls[i] = target.String()
		h.index[target.String()] = i
	}
	for _, opt := range opts {
		opt(h)
	}
	return h, nil
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
	// Report the chosen backend to an outer logging middleware when present.
	if info := logging.From(r.Context()); info != nil {
		info.Backend = h.urls[idx]
	}
	h.proxies[idx].ServeHTTP(w, r)
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
