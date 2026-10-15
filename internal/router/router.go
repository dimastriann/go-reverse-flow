// Package router dispatches requests to per-pool handlers by longest path
// prefix, falling back to a default handler when no route matches.
//
// The router is deliberately dumb about everything else — health, weighting,
// retries, metrics all live inside the per-pool proxy handlers it delegates
// to — so route tables compose with the rest of the chain unchanged.
package router

import (
	"net/http"
	"sort"
	"strings"
)

// Handler dispatches by longest matching prefix.
type Handler struct {
	fallback http.Handler
	prefixes []string // sorted by length desc; longest match wins
	routes   map[string]http.Handler
}

// New builds the dispatch table. Prefixes must start with "/"; duplicate
// prefixes overwrite silently here because config validation already
// rejects them — New stays friendly for tests.
func New(fallback http.Handler, routes map[string]http.Handler) *Handler {
	prefixes := make([]string, 0, len(routes))
	for p := range routes {
		prefixes = append(prefixes, p)
	}
	sort.Slice(prefixes, func(i, j int) bool {
		return len(prefixes[i]) > len(prefixes[j])
	})
	return &Handler{
		fallback: fallback,
		prefixes: prefixes,
		routes:   routes,
	}
}

// ServeHTTP implements http.Handler.
func (rt *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	for _, prefix := range rt.prefixes {
		if matchPrefix(path, prefix) {
			rt.routes[prefix].ServeHTTP(w, r)
			return
		}
	}
	rt.fallback.ServeHTTP(w, r)
}

// matchPrefix reports whether path starts with the prefix on a path-segment
// boundary: /api matches /api and /api/users, but not /april. The root
// prefix "/" matches everything.
func matchPrefix(path, prefix string) bool {
	if prefix == "/" {
		return true
	}
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	if len(path) == len(prefix) {
		return true
	}
	return path[len(prefix)] == '/'
}
