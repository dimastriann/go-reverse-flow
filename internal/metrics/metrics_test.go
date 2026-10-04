package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// renderThrough reaches the exposition exactly like a real scraper would.
func renderThrough(t *testing.T, c *Collector) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	c.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("content type = %q", got)
	}
	return rec.Body.String()
}

// TestCountAndRender checks counter values and the stable sorted order.
func TestCountAndRender(t *testing.T) {
	c := New()
	c.Count("http://b:2")
	c.Count("http://b:2")
	c.Count("http://a:1")

	body := renderThrough(t, c)
	want := "# HELP grf_backend_requests_total Requests the proxy forwarded to this backend.\n" +
		"# TYPE grf_backend_requests_total counter\n" +
		"grf_backend_requests_total{backend=\"http://a:1\"} 1\n" +
		"grf_backend_requests_total{backend=\"http://b:2\"} 2\n"
	if body != want {
		t.Errorf("render mismatch:\n--- got\n%q\n--- want\n%q", body, want)
	}
}

// TestConcurrentCounting hammers Count from goroutines; totals must land
// exactly (atomics, no lost updates).
func TestConcurrentCounting(t *testing.T) {
	c := New()
	const goroutines = 20
	const per = 100

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				c.Count("http://hot:1")
			}
		}()
	}
	wg.Wait()

	want := "grf_backend_requests_total{backend=\"http://hot:1\"} 2000\n"
	if !strings.Contains(renderThrough(t, c), want) {
		t.Errorf("body missing %q", want)
	}
}

// TestHealthyGaugeRenders checks the gauge family mirrors the status source
// and covers down-but-zero-traffic backends.
func TestHealthyGaugeRenders(t *testing.T) {
	status := func() map[string]bool {
		return map[string]bool{"http://a:1": true, "http://b:2": false} // b down, no traffic
	}
	c := New(WithStatusSource(status))

	body := renderThrough(t, c)
	if !strings.Contains(body, "grf_backend_healthy{backend=\"http://a:1\"} 1\n") ||
		!strings.Contains(body, "grf_backend_healthy{backend=\"http://b:2\"} 0\n") ||
		!strings.Contains(body, "grf_backend_requests_total{backend=\"http://a:1\"} 0\n") ||
		!strings.Contains(body, "grf_backend_requests_total{backend=\"http://b:2\"} 0\n") {
		t.Errorf("body missing expected gauge/counter block:\n%s", body)
	}
}

// TestGaugeFollowsHealthFlips checks the source function is consulted per
// scrape, so state changes show up on the next scrape.
func TestGaugeFollowsHealthFlips(t *testing.T) {
	status := map[string]bool{"http://a:1": true, "http://b:2": false}
	c := New(WithStatusSource(func() map[string]bool { return status }))

	if before := renderThrough(t, c); !strings.Contains(before, "grf_backend_healthy{backend=\"http://a:1\"} 1\n") {
		t.Fatalf("pre-flip scrape missing up gauge:\n%s", before)
	}

	status = map[string]bool{"http://a:1": false, "http://b:2": true} // simulated checker flip
	if after := renderThrough(t, c); !strings.Contains(after, "grf_backend_healthy{backend=\"http://a:1\"} 0\n") ||
		!strings.Contains(after, "grf_backend_healthy{backend=\"http://b:2\"} 1\n") {
		t.Errorf("post-flip scrape did not follow the state change:\n%s", after)
	}
}
