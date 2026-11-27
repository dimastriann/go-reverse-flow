package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fixedStatus returns a fully-populated payload for auth tests.
func fixedStatus() Status {
	return Status{
		Version:     "v0.9.0",
		Listen:      ":8080",
		AdminListen: "127.0.0.1:8090",
		Backends: []BackendStatus{
			{URL: "http://a:1", Healthy: true, Requests: 3},
			{URL: "http://b:2", Healthy: false, Requests: 1},
		},
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return New("admin", "s3cret", fixedStatus)
}

// TestAuthEnforced covers 401 without/wrong credentials, and 200 with the
// right ones — including the WWW-Authenticate challenge header.
func TestAuthEnforced(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	// No credentials.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no-credentials status = %d, want 401", rec.Code)
	}
	if ch := rec.Header().Get("WWW-Authenticate"); ch == "" {
		t.Error("missing WWW-Authenticate challenge on 401")
	}

	// Wrong password.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.SetBasicAuth("admin", "wrong")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong-password status = %d, want 401", rec.Code)
	}

	// Wrong username.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.SetBasicAuth("root", "s3cret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong-username status = %d, want 401", rec.Code)
	}

	// Correct credentials.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.SetBasicAuth("admin", "s3cret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid-credentials status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
}

// TestStatusPayloadShape verifies the JSON contract the dashboard depends on:
// status fields, healthy gauges, request counters.
func TestStatusPayloadShape(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.SetBasicAuth("admin", "s3cret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("content type = %q", ct)
	}

	var payload Status
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if payload.Version != "v0.9.0" {
		t.Errorf("version = %q, want v0.9.0", payload.Version)
	}
	if len(payload.Backends) != 2 {
		t.Fatalf("backends = %d, want 2", len(payload.Backends))
	}
	if !payload.Backends[0].Healthy || payload.Backends[0].Requests != 3 {
		t.Errorf("backend[0] = %+v, want healthy with 3 requests", payload.Backends[0])
	}
	if payload.Backends[1].Healthy {
		t.Error("backend[1] reported healthy, want false")
	}
}

// TestStatusMethodGuard refuses non-GET on the status endpoint.
func TestStatusMethodGuard(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/status", nil)
	req.SetBasicAuth("admin", "s3cret")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
		t.Errorf("Allow = %q, want GET", allow)
	}
}

// TestUptimeAdvances checks the uptime reflects the injected clock.
func TestUptimeAdvances(t *testing.T) {
	start := time.Now()
	fc := start
	s := New("admin", "s3cret", fixedStatus, WithClock(func() time.Time { return fc }))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.SetBasicAuth("admin", "s3cret")
	s.Handler().ServeHTTP(rec, req)

	var payload struct {
		Uptime string `json:"uptime"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	if payload.Uptime != "0s" {
		t.Fatalf("uptime immediately after start = %q, want 0s", payload.Uptime)
	}

	// Advance the fake clock by re-anchoring: build a second server whose
	// startedAt predates now by construction.
	old := New("admin", "s3cret", fixedStatus, WithClock(func() time.Time { return start.Add(2 * time.Hour) }))
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.SetBasicAuth("admin", "s3cret")
	old.Handler().ServeHTTP(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("second status not JSON (status %d): %v — body %q", rec.Code, err, rec.Body.String())
	}
	if payload.Uptime != "2h0m0s" {
		t.Errorf("uptime after 2h = %q, want 2h0m0s", payload.Uptime)
	}
}
