package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
// right ones. The 401 deliberately carries no WWW-Authenticate challenge so
// the browser uses the dashboard's own login form instead of its native
// popup.
func TestAuthEnforced(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	// No credentials.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no-credentials status = %d, want 401", rec.Code)
	}
	if ch := rec.Header().Get("WWW-Authenticate"); ch != "" {
		t.Errorf("WWW-Authenticate = %q, want empty (native dialog would fight the form)", ch)
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

// TestConfigEndpoints covers GET/PUT /api/config with the injected hooks:
// view returns the provided payload, PUT feeds the body to the applier,
// applier errors become 400 with the message, and missing hooks 404.
func TestConfigEndpoints(t *testing.T) {
	current := map[string]any{"listen_addr": ":8080"}
	var applied []byte

	s := New("admin", "s3cret", fixedStatus,
		WithConfigSource(func() any { return current }),
		WithApply(func(body []byte) error {
			if string(body) != `{"listen_addr":":9090"}` {
				return fmt.Errorf("unexpected body %q", body)
			}
			applied = body
			current["listen_addr"] = ":9090"
			return nil
		}),
	)
	h := s.Handler()

	// GET view.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.SetBasicAuth("admin", "s3cret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rec.Code)
	}
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("view not JSON: %v", err)
	}
	if view["listen_addr"] != ":8080" {
		t.Errorf("view listen_addr = %v", view["listen_addr"])
	}

	// PUT applied.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"listen_addr":":9090"}`))
	req.SetBasicAuth("admin", "s3cret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if applied == nil {
		t.Error("applier was never invoked")
	}

	// Applier rejection becomes 400 with the message.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"bad":true}`))
	req.SetBasicAuth("admin", "s3cret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("rejected PUT status = %d, want 400", rec.Code)
	}
	var errPayload struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &errPayload)
	if !strings.Contains(errPayload.Error, "unexpected body") {
		t.Errorf("400 body = %q, want applier message", rec.Body.String())
	}

	// Unauthed PUT is still 401 (auth wraps the management surface).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauth PUT status = %d, want 401", rec.Code)
	}
}

// TestReloadEndpoint covers POST /api/reload success and error mapping, and
// that the route disappears without the hook.
func TestReloadEndpoint(t *testing.T) {
	result := error(nil)
	s := New("admin", "s3cret", fixedStatus,
		WithReload(func() error { return result }),
	)
	h := s.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/reload", nil)
	req.SetBasicAuth("admin", "s3cret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reload status = %d, want 200", rec.Code)
	}

	result = fmt.Errorf("config file invalid")
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/reload", nil)
	req.SetBasicAuth("admin", "s3cret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("failed reload status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "config file invalid") {
		t.Errorf("body = %q, want the reload error", rec.Body.String())
	}

	// GET is refused (POST-only endpoint).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/reload", nil)
	req.SetBasicAuth("admin", "s3cret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET reload status = %d, want 405", rec.Code)
	}

	// Without the hook the route is not registered at all (auth passes, then
	// the mux answers 404).
	bare := New("admin", "s3cret", fixedStatus).Handler()
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/reload", nil)
	req.SetBasicAuth("admin", "s3cret")
	bare.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("bare server reload status = %d, want 404", rec.Code)
	}
}

// TestLogsEndpoint covers GET /api/logs: entries pass through, ?limit=
// bounds the request, non-GET is refused, and the route disappears without
// the hook.
func TestLogsEndpoint(t *testing.T) {
	var lastLimit int
	s := New("admin", "s3cret", fixedStatus, WithLogTail(func(limit int) any {
		lastLimit = limit
		return []map[string]any{{"path": "/x", "status": 200}}
	}))
	h := s.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/logs?limit=25", nil)
	req.SetBasicAuth("admin", "s3cret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if lastLimit != 25 {
		t.Errorf("limit passed = %d, want 25", lastLimit)
	}
	var entries []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if len(entries) != 1 || entries[0]["path"] != "/x" {
		t.Errorf("entries = %v", entries)
	}

	// Default limit when absent.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	req.SetBasicAuth("admin", "s3cret")
	h.ServeHTTP(rec, req)
	if lastLimit != 100 {
		t.Errorf("default limit = %d, want 100", lastLimit)
	}

	// POST refused.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/logs", nil)
	req.SetBasicAuth("admin", "s3cret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST logs status = %d, want 405", rec.Code)
	}

	// Bare server: no route.
	bare := New("admin", "s3cret", fixedStatus).Handler()
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	req.SetBasicAuth("admin", "s3cret")
	bare.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("bare logs status = %d, want 404", rec.Code)
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
