package logging

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRingBoundedAndOrdered fills past the cap and checks the buffer keeps
// only the newest entries, oldest-first.
func TestRingBoundedAndOrdered(t *testing.T) {
	r := NewRing(3)
	for i := 0; i < 5; i++ {
		r.add(Entry{Remote: "1.1.1.1", Path: string(rune('a' + i)), Status: http.StatusOK})
	}

	got := r.Tail(10)
	if len(got) != 3 {
		t.Fatalf("tail = %d entries, want 3", len(got))
	}
	wantPaths := []string{"c", "d", "e"} // oldest of the survivors first
	for i, e := range got {
		if e.Path != wantPaths[i] {
			t.Errorf("entry %d path = %q, want %q", i, e.Path, wantPaths[i])
		}
	}
}

// TestTailFewerThanRequested: Tail(n) with n larger than the buffer returns
// everything available.
func TestTailFewerThanRequested(t *testing.T) {
	r := NewRing(8)
	r.add(Entry{Path: "/only"})
	if got := r.Tail(5); len(got) != 1 {
		t.Errorf("tail = %d entries, want 1", len(got))
	}
}

// TestTailZeroAndNegative: degenerate limits return nothing and do not panic.
func TestTailZeroAndNegative(t *testing.T) {
	r := NewRing(4)
	r.add(Entry{Path: "/x"})
	if got := r.Tail(0); len(got) != 0 {
		t.Errorf("Tail(0) = %d entries, want 0", len(got))
	}
	if got := r.Tail(-1); len(got) != 0 {
		t.Errorf("Tail(-1) = %d entries, want 0", len(got))
	}
}

// TestMiddlewareFeedsRing: requests through a ring-wired middleware are
// recorded with method, path, backend attribution, status, and duration.
func TestMiddlewareFeedsRing(t *testing.T) {
	r := NewRing(16)
	inner := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if info := From(req.Context()); info != nil {
			info.Backend = "http://recorded:1"
		}
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("brew"))
	})
	m := New(inner, WithRing(r))

	req := httptest.NewRequest(http.MethodGet, "/brew", nil)
	req.RemoteAddr = "10.9.8.7:1234"
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, req)

	got := r.Tail(1)
	if len(got) != 1 {
		t.Fatalf("ring = %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Method != http.MethodGet || e.Path != "/brew" || e.Status != http.StatusTeapot {
		t.Errorf("entry = %+v, want GET /brew 418", e)
	}
	if e.Backend != "http://recorded:1" {
		t.Errorf("backend = %q, want http://recorded:1", e.Backend)
	}
	if e.Duration < 0 {
		t.Errorf("duration = %v, want non-negative", e.Duration)
	}
	if e.Remote != "10.9.8.7:1234" {
		t.Errorf("remote = %q, want 10.9.8.7:1234", e.Remote)
	}
}

// TestMiddlewareWithoutRingStillLogs: the middleware must work unchanged
// when no ring is wired.
func TestMiddlewareWithoutRingStillLogs(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	m := New(inner)
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plain", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// TestRingConcurrentAdds: concurrent writers never race (detected by -race
// in CI) and the buffer stays within its cap.
func TestRingConcurrentAdds(t *testing.T) {
	r := NewRing(50)
	done := make(chan struct{})
	for g := 0; g < 10; g++ {
		go func(g int) {
			for i := 0; i < 20; i++ {
				r.add(Entry{Path: "/c", Status: http.StatusOK})
			}
			done <- struct{}{}
		}(g)
	}
	for g := 0; g < 10; g++ {
		<-done
	}
	if got := len(r.Tail(1000)); got > 50 {
		t.Errorf("ring grew past cap: %d entries", got)
	}
}

// TestTailReturnsCopies guards against callers mutating ring internals.
func TestTailReturnsCopies(t *testing.T) {
	r := NewRing(2)
	r.add(Entry{Path: "/original", Status: http.StatusOK})
	got := r.Tail(1)
	got[0].Path = "/mutated"

	if again := r.Tail(1); again[0].Path != "/original" {
		t.Errorf("tail aliases ring internals: %q", again[0].Path)
	}
}
