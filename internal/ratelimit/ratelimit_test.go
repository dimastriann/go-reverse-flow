package ratelimit

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeClock gives tests full control over token refill timing.
type fakeClock struct {
	t time.Time
}

func (fc *fakeClock) now() time.Time      { return fc.t }
func (fc *fakeClock) adv(d time.Duration) { fc.t = fc.t.Add(d) }

// echoBody handler marks inner calls.
var echoBody = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "passed")
})

func throttleRequest(t *testing.T, l http.Handler, ip string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = ip + ":55555"
	l.ServeHTTP(rec, req)
	return rec
}

// limiterOf asserts the enabled middleware back into its concrete type.
func limiterOf(t *testing.T, l http.Handler) *Limiter {
	t.Helper()
	lm, ok := l.(*Limiter)
	if !ok {
		t.Fatal("enabled limiter must be a *Limiter")
	}
	return lm
}

// TestBurstThenRejectAndRefill covers the token-bucket lifecycle: burst
// allowed, empty bucket rejected with Retry-After, and full refill after a
// rate-scaled sleep on the injected clock.
func TestBurstThenRejectAndRefill(t *testing.T) {
	fc := &fakeClock{t: time.Now()}
	l := New(echoBody, 1, 3, WithClock(fc.now)) // 1 token/sec, burst 3

	// Burst of 3 passes.
	for i := 0; i < 3; i++ {
		if rec := throttleRequest(t, l, "10.0.0.1"); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200", i, rec.Code)
		}
	}

	// Bucket empty: 429 with a Retry-After hint.
	rec := throttleRequest(t, l, "10.0.0.1")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "1" {
		t.Errorf("Retry-After = %q, want 1", ra)
	}
	if rec.Body.String() != "rate limited\n" {
		t.Errorf("body = %q, want rate-limited message", rec.Body.String())
	}

	// Half-refilled (still under 1 token): still rejected.
	fc.adv(500 * time.Millisecond)
	if rec := throttleRequest(t, l, "10.0.0.1"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after 0.5s: status %d, want 429", rec.Code)
	}

	// Full refill: passes again.
	fc.adv(500 * time.Millisecond)
	if rec := throttleRequest(t, l, "10.0.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("after 1s: status %d, want 200", rec.Code)
	}
}

// TestDisabledLimiterPassesThrough pins rate<=0/burst<=1 returning next:
// requests flow straight to the inner handler with no bucket bookkeeping.
func TestDisabledLimiterPassesThrough(t *testing.T) {
	var buf bytes.Buffer
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(&buf, "passed")
	})

	for _, tc := range []struct {
		rate  float64
		burst int
	}{
		{rate: 0, burst: 10},
		{rate: 10, burst: 0},
		{rate: -1, burst: 5},
	} {
		buf.Reset()
		got := New(inner, tc.rate, tc.burst)
		if _, ok := got.(*Limiter); ok {
			t.Errorf("rate %v burst %d wrapped a disabled limiter", tc.rate, tc.burst)
		}
		rec := httptest.NewRecorder()
		got.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK || buf.String() != "passed" {
			t.Errorf("rate %v burst %d: inner never ran (status %d)", tc.rate, tc.burst, rec.Code)
		}
	}
}

// TestPerClientIsolation ensures one drained bucket does not affect another.
func TestPerClientIsolation(t *testing.T) {
	fc := &fakeClock{t: time.Now()}
	l := New(echoBody, 1, 1, WithClock(fc.now)) // 1/s, burst 1

	if rec := throttleRequest(t, l, "10.0.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("client A first: status %d", rec.Code)
	}
	if rec := throttleRequest(t, l, "10.0.0.1"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("client A second: status %d, want 429", rec.Code)
	}
	if rec := throttleRequest(t, l, "10.0.0.2"); rec.Code != http.StatusOK {
		t.Fatalf("client B first: status %d, want 200 (fresh bucket)", rec.Code)
	}
	if got := limiterOf(t, l).Len(); got != 2 {
		t.Errorf("tracked buckets = %d, want 2", got)
	}
}

// TestPruneIdleBuckets verifies the goroutine-free sweep drops both idle
// clients while keeping the active one.
func TestPruneIdleBuckets(t *testing.T) {
	fc := &fakeClock{t: time.Now()}
	l := New(echoBody, 1, 2, WithClock(fc.now)) // burst 2 -> both hands

	_, _ = throttleRequest(t, l, "10.0.0.1"), throttleRequest(t, l, "10.0.0.2")
	if got := limiterOf(t, l).Len(); got != 2 {
		t.Fatalf("setup: %d buckets, want 2", got)
	}

	// Jump past the idle horizon plus enough requests to trigger the sweep.
	fc.adv(11 * time.Minute)
	for i := 0; i < 64; i++ {
		// 10.0.0.3 stays active so its fresh timestamp keeps its bucket.
		if rec := throttleRequest(t, l, "10.0.0.3"); rec.Code == http.StatusTooManyRequests {
			// burst consumed; refill 1/s for many seconds -> keep cycling.
			fc.adv(time.Second)
		}
	}
	if got := limiterOf(t, l).Len(); got != 1 {
		t.Errorf("after prune: %d buckets, want 1 (only the active client)", got)
	}
}

// TestHostPartFallback covers the portless RemoteAddr edge defensively.
func TestHostPartFallback(t *testing.T) {
	if got := hostPart("unix-socket"); got != "unix-socket" {
		t.Errorf("hostPart(no port) = %q, want unchanged", got)
	}
}
