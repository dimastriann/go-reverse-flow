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
)

var (
	errNoBackends  = errors.New("proxy: at least one backend is required")
	errNilBalancer = errors.New("proxy: balancer must not be nil")
	errNilBackend  = errors.New("proxy: backend must not be nil")
)

// Handler is an http.Handler that load-balances requests over backends.
type Handler struct {
	proxies []*httputil.ReverseProxy
	bal     *balancer.Balancer
}

// New builds one ReverseProxy per backend URL. All listeners should exist by
// the time New is called; New itself performs no network I/O.
func New(backends []*url.URL, bal *balancer.Balancer) (*Handler, error) {
	if len(backends) == 0 {
		return nil, errNoBackends
	}
	if bal == nil {
		return nil, errNilBalancer
	}

	h := &Handler{proxies: make([]*httputil.ReverseProxy, len(backends)), bal: bal}
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
	}
	return h, nil
}

// ServeHTTP forwards the request to the next backend in round-robin order.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.proxies[h.bal.Next(len(h.proxies))].ServeHTTP(w, r)
}
