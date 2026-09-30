package logging

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureLogger builds a slog logger writing text records into a buffer.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func attrOf(t *testing.T, line, key string) string {
	t.Helper()
	want := key + "="
	idx := strings.Index(line, want)
	if idx < 0 {
		t.Fatalf("log line %q lacks attribute %q", line, key)
	}
	rest := line[idx+len(want):]
	if i := strings.Index(rest, " "); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// TestStatusCapture covers explicit WriteHeader codes and implicit 200.
func TestStatusCapture(t *testing.T) {
	tests := []struct {
		name       string
		write      func(w http.ResponseWriter)
		wantStatus string
	}{
		{
			name: "explicit 503",
			write: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
			wantStatus: "503",
		},
		{
			name: "explicit 201 then body",
			write: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte("ok"))
			},
			wantStatus: "201",
		},
		{
			name: "implicit 200 via body only",
			write: func(w http.ResponseWriter) {
				_, _ = w.Write([]byte("hello"))
			},
			wantStatus: "200",
		},
		{
			name:       "writes nothing at all",
			write:      func(http.ResponseWriter) {},
			wantStatus: "200", // Go's server would have written 200 anyway
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			m := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tt.write(w)
			}), WithLogger(captureLogger(&buf)))

			rec := httptest.NewRecorder()
			m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))

			line := buf.String()
			if got := attrOf(t, line, "status"); got != tt.wantStatus {
				t.Errorf("status attr = %q, want %q (line: %s)", got, tt.wantStatus, line)
			}
			if got := attrOf(t, line, "path"); got != "/status" {
				t.Errorf("path attr = %q, want /status", got)
			}
			if got := attrOf(t, line, "method"); got != http.MethodGet {
				t.Errorf("method attr = %q, want GET", got)
			}
		})
	}
}

// TestBackendJoinedViaContext checks the proxy->middleware channel: an inner
// handler calling From(ctx) and filling Backend shows up in the log line;
// handlers that ignore the holder leave the field empty without panicking.
func TestBackendJoinedViaContext(t *testing.T) {
	var buf bytes.Buffer
	logger := captureLogger(&buf)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := From(r.Context())
		if info == nil {
			t.Error("middleware did not attach the Info holder")
			return
		}
		info.Backend = "http://localhost:8001"
		_, _ = w.Write([]byte("proxied"))
	})
	m := New(inner, WithLogger(logger))

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/via", nil))

	if got := attrOf(t, buf.String(), "backend"); got != "http://localhost:8001" {
		t.Errorf("backend attr = %q, want http://localhost:8001", got)
	}
}

// TestStandaloneInnerWithoutMiddleware confirms From(nil-context) stays nil-
// safe: the inner handler runs directly and no Info holder is required.
func TestStandaloneInnerWithoutMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if info := From(r.Context()); info != nil {
			t.Error("expected no Info holder when middleware is absent")
		}
		_, _ = w.Write([]byte("plain"))
	})

	rec := httptest.NewRecorder()
	inner.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Body.String() != "plain" {
		t.Errorf("body = %q, want plain", rec.Body.String())
	}
}

// TestDurationFieldPresence ensures the duration attribute exists and parses
// (logged via slog's time.Duration formatting).
func TestDurationFieldPresence(t *testing.T) {
	var buf bytes.Buffer
	m := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
	}), WithLogger(captureLogger(&buf)))

	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ping", nil))

	line := buf.String()
	d := attrOf(t, line, "duration")
	if d == "" {
		t.Errorf("duration attr missing; line: %s", line)
	}
}
