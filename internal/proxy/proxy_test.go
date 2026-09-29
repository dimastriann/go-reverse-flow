package proxy

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/dimastriann/go-reverse-flow/internal/balancer"
)

// newBackend starts an httptest server that responds with its own name so
// tests can see which backend served the request. Its URL must be released
// with the returned close function.
func newBackend(t *testing.T, name string) (*url.URL, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, name)
	}))
	u, err := url.Parse(srv.URL)
	if err != nil {
		srv.Close()
		t.Fatalf("parse backend URL %s: %v", srv.URL, err)
	}
	return u, srv.Close
}

func newHandler(t *testing.T, backends []*url.URL) *Handler {
	t.Helper()
	h, err := New(backends, &balancer.Balancer{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

// TestRoundRobinAlternation hits the handler 3n times over n backends and
// asserts each backend is hit exactly n times.
func TestRoundRobinAlternation(t *testing.T) {
	const n = 3

	names := []string{"backend-1", "backend-2", "backend-3"}
	urls := make([]*url.URL, n)
	for i, name := range names {
		u, closeFn := newBackend(t, name)
		urls[i] = u
		defer closeFn()
	}

	h := newHandler(t, urls)
	hits := make(map[string]int, n)
	for i := 0; i < 3*n; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 (body: %q)", i, rec.Code, rec.Body.String())
		}
		hits[rec.Body.String()]++
	}

	for _, name := range names {
		if hits[name] != n {
			t.Errorf("backend %s hit %d times, want %d", name, hits[name], n)
		}
	}
}

// TestPathForwarded verifies the incoming path is preserved by the single-host
// proxy.
func TestPathForwarded(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	h := newHandler(t, []*url.URL{u})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/some/path?q=1", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotPath != "/some/path" {
		t.Errorf("backend saw path %q, want /some/path", gotPath)
	}
}

// TestBackendUnavailable checks that a dead backend surfaces as 502 Bad
// Gateway instead of hanging or panicking.
func TestBackendUnavailable(t *testing.T) {
	u, closeFn := newBackend(t, "doomed")
	closeFn() // kill the backend before any request

	h := newHandler(t, []*url.URL{u})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (body: %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "backend unavailable") {
		t.Errorf("body = %q, want backend-unavailable message", rec.Body.String())
	}
}

// TestNewRejectsBadInput guards constructor programming errors.
func TestNewRejectsBadInput(t *testing.T) {
	u, closeFn := newBackend(t, "x")
	defer closeFn()

	if _, err := New(nil, &balancer.Balancer{}); !errors.Is(err, errNoBackends) {
		t.Errorf("New(nil): err = %v, want errNoBackends", err)
	}
	if _, err := New([]*url.URL{u}, nil); !errors.Is(err, errNilBalancer) {
		t.Errorf("New(nil balancer): err = %v, want errNilBalancer", err)
	}
	if _, err := New([]*url.URL{nil}, &balancer.Balancer{}); !errors.Is(err, errNilBackend) {
		t.Errorf("New(nil backend): err = %v, want errNilBackend", err)
	}
}

// TestConcurrentRequests forwards 200 requests from 20 goroutines and checks
// all succeed and every backend is exercised.
func TestConcurrentRequests(t *testing.T) {
	const backends = 3
	const goroutines = 20
	const perGoroutine = 10 // 200 requests total

	names := make([]string, backends)
	urls := make([]*url.URL, backends)
	closes := make([]func(), backends)
	for i := 0; i < backends; i++ {
		names[i] = fmt.Sprintf("backend-%d", i)
		urls[i], closes[i] = newBackend(t, names[i])
		defer closes[i]()
	}
	h := newHandler(t, urls)

	hits := make(map[string]int, backends)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(localHits map[string]int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
				if rec.Code != http.StatusOK {
					t.Errorf("status = %d, want 200", rec.Code)
					return
				}
				localHits[rec.Body.String()]++
			}
			mu.Lock()
			defer mu.Unlock()
			for name, c := range localHits {
				hits[name] += c
			}
		}(make(map[string]int, backends))
	}
	wg.Wait()

	total := goroutines * perGoroutine
	sum := 0
	for _, name := range names {
		if hits[name] == 0 {
			t.Errorf("backend %s never served a request", name)
		}
		sum += hits[name]
	}
	if sum != total {
		t.Errorf("total answered requests = %d, want %d", sum, total)
	}
}
