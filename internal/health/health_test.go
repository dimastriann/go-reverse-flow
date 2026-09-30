package health

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testPort = 18081

// fixedPortListener re-creates a listener on the same port so tests can take
// a backend down and bring a replacement back up at the identical URL — the
// exact lifecycle a health checker must observe.
func fixedPortListener(t *testing.T, port int) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("listen on fixed port %d: %v", port, err)
	}
	return l
}

// newBackendOn serves GET / with a 200 until stopped.
func newBackendOn(t *testing.T, port int) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.Listener = fixedPortListener(t, port)
	srv.Start()
	return srv
}

// waitHealthy polls until the checker reports want for raw or times out.
func waitHealthy(t *testing.T, c *Checker, raw string, want bool, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if got := c.Healthy(raw); got == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("status did not reach want=%v for %s within %v", want, raw, d)
}

// TestStatusTransitions covers the lifecycle up -> down -> up again, the
// immediate first sweep, and the transition hook firing on flips.
func TestStatusTransitions(t *testing.T) {
	const interval = 300 * time.Millisecond

	raw := "http://127.0.0.1:" + strconv.Itoa(testPort)
	srv := newBackendOn(t, testPort)

	var flips []string
	var mu sync.Mutex
	c := New([]string{raw}, 200*time.Millisecond, WithTransitionHook(func(b string, up bool) {
		mu.Lock()
		defer mu.Unlock()
		flips = append(flips, b)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(ctx, interval)

	// The immediate first sweep must see the server up.
	if !c.Healthy(raw) {
		t.Fatal("initial status: Healthy = false, want true")
	}

	// Kill it; the next tick must demote and fire the hook.
	srv.Close()
	waitHealthy(t, c, raw, false, 2*time.Second)
	mu.Lock()
	flipped := len(flips) == 1 && flips[0] == raw
	mu.Unlock()
	if !flipped {
		t.Errorf("hook saw flips %v, want one down-flip for %s", flips, raw)
	}

	// A replacement on the same port must promote again.
	srv2 := newBackendOn(t, testPort)
	defer srv2.Close()
	waitHealthy(t, c, raw, true, 2*time.Second)
}

// TestProbeClassification checks the "below 500 is healthy" rule directly.
func TestProbeClassification(t *testing.T) {
	client := &http.Client{Timeout: time.Second}

	tests := []struct {
		status int
		want   bool
	}{
		{http.StatusOK, true},
		{http.StatusNotFound, true}, // 404 on / still means the process is alive
		{http.StatusInternalServerError, false},
		{http.StatusServiceUnavailable, false},
	}

	for _, tt := range tests {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tt.status)
		}))
		if got := probe(client, srv.URL); got != tt.want {
			t.Errorf("status %d: Healthy = %v, want %v", tt.status, got, tt.want)
		}
		srv.Close()
	}
}

// TestClientErrorMeansDown documents that transport failures demote quickly.
func TestClientErrorMeansDown(t *testing.T) {
	client := &http.Client{Timeout: 200 * time.Millisecond}
	// Port 1 on loopback has no accepted listener, so this fails fast.
	if probe(client, "http://127.0.0.1:1/") {
		t.Error("probe on refused connection = true, want false")
	}
}

// TestUnknownBackendReportsUnhealthy guards the lookup default and that
// cancelling the context stops the goroutine (detected via race detector / vet).
func TestUnknownBackendReportsUnhealthy(t *testing.T) {
	c := New([]string{"http://127.0.0.1:1/"}, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx, time.Hour) // no churn wanted in this test
	cancel()

	if c.Healthy("http://127.0.0.1:59999/") {
		t.Error("Healthy(unknown) = true, want false")
	}
}

// TestHealthyOfOnlyReturnsUp checks the summary helper.
func TestHealthOfOnlyReturnsUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New([]string{srv.URL}, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(ctx, time.Hour)

	got := c.HealthyOf()
	if len(got) != 1 || got[0] != srv.URL {
		t.Errorf("HealthyOf() = %v, want [%s]", got, srv.URL)
	}
}

// TestHealthyOfOrderIsStable pins the contract HealthyOf promises to the
// proxy's round-robin parity: repeated snapshots over an unchanged healthy
// set must be byte-identical (map iteration alone would shuffle them).
func TestHealthyOfOrderIsStable(t *testing.T) {
	c := New([]string{"http://c:3", "http://a:1", "http://b:2"}, 50*time.Millisecond)

	first := c.HealthyOf()
	for i := 0; i < 200; i++ {
		again := c.HealthyOf()
		if len(again) != 3 || again[0] != "http://a:1" || again[1] != "http://b:2" || again[2] != "http://c:3" {
			t.Fatalf("iteration %d: HealthyOf() = %v, want fixed order [a b c], first snapshot was %v", i, again, first)
		}
	}
}

// TestStringShowsStates checks the diagnostics formatter.
func TestStringShowsStates(t *testing.T) {
	c := New([]string{"http://ok:1234", "http://down:4321"}, 50*time.Millisecond)
	c.mu.Lock()
	c.healthy["http://down:4321"] = false
	c.mu.Unlock()

	s := c.String()
	if !strings.Contains(s, "http://ok:1234=up") || !strings.Contains(s, "http://down:4321=down") {
		t.Errorf("String() = %q, want both states visible", s)
	}
}
