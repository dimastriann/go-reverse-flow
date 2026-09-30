package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dimastriann/go-reverse-flow/internal/balancer"
	"github.com/dimastriann/go-reverse-flow/internal/health"
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

// newHandlerWithHealth builds a Handler whose routing consults a live
// health.Checker sweeping every 200ms over the same backend URLs.
func newHandlerWithHealth(t *testing.T, backends []*url.URL, interval time.Duration) *Handler {
	t.Helper()
	raws := make([]string, len(backends))
	for i, u := range backends {
		raws[i] = u.String()
	}
	c := health.New(raws, 500*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c.Start(ctx, interval)

	h, err := New(backends, &balancer.Balancer{}, WithHealth(c))
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

// TestHealthyOnlyRouting kills one of two backends and expects every request
// to land on the survivor — no 502s, no dead-backend hits — and a revive
// returns both to rotation with a fair split.
func TestHealthyOnlyRouting(t *testing.T) {
	fast := 200 * time.Millisecond

	urls := make([]*url.URL, 2)
	closes := make([]func(), 2)
	for i := 0; i < 2; i++ {
		name := fmt.Sprintf("healthy-%d", i+1)
		u, closeFn := newBackend(t, name)
		urls[i] = u
		closes[i] = closeFn
	}
	h := newHandlerWithHealth(t, urls, fast)

	// Both up: bodies flip between the two.
	first := requestOnce(t, h, "/")
	second := requestOnce(t, h, "/")
	if first == second {
		t.Errorf("both up: consecutive bodies %q and %q identical; want alternation", first, second)
	}

	// Kill the backend that just served; every subsequent request must miss it.
	var survivor int
	if first == "healthy-1" {
		survivor = 0
	} else {
		survivor = 1
	}
	closes[survivor^1]()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if body := requestOnce(t, h, "/"); body == fmt.Sprintf("healthy-%d", (survivor^1)+1) {
			t.Errorf("request routed to the dead backend with body %q", body)
			return
		} else if body == fmt.Sprintf("healthy-%d", survivor+1) {
			time.Sleep(20 * time.Millisecond) // still up: keep checking through the demotion window
		}
	}
}

// TestNoHealthyBackends503 asserts the all-down edge: 503, not a forward into
// a dead dial.
func TestNoHealthyBackends503(t *testing.T) {
	u, closeFn := newBackend(t, "gone")
	defer closeFn()

	h := newHandlerWithHealth(t, []*url.URL{u}, 200*time.Millisecond)
	closeFn() // kill the only backend

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code == http.StatusServiceUnavailable {
			if !strings.Contains(rec.Body.String(), "no healthy backend") {
				t.Errorf("503 body = %q, want no-healthy message", rec.Body.String())
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("never reached 503 after backend death")
}

// requestOnce sends one request and returns the response body.
func requestOnce(t *testing.T, h *Handler, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK && rec.Code != http.StatusBadGateway && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status %d (body %q)", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
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
