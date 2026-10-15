package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// echoHandler responds with its own name.
func echoHandler(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, name)
	})
}

func get(t *testing.T, h http.Handler, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Body.String()
}

// TestLongestPrefixWins checks /api/users goes to the longer route over /api,
// and /api to /api over the fallback.
func TestLongestPrefixWins(t *testing.T) {
	rt := New(echoHandler("default"), map[string]http.Handler{
		"/api":       echoHandler("api"),
		"/api/admin": echoHandler("admin"),
	})

	cases := map[string]string{
		"/api/admin/x": "admin",
		"/api/users":   "api",
		"/api":         "api",
		"/other":       "default",
		"/":            "default",
		"/april":       "default", // segment boundary: /ap does not match /api
	}
	for path, want := range cases {
		if got := get(t, rt, path); got != want {
			t.Errorf("path %s: got %q, want %q", path, got, want)
		}
	}
}

// TestMethodAndQueryPreserved checks the router never mutates the request:
// POST bodies, queries, and headers flow to the chosen handler untouched.
func TestMethodAndQueryPreserved(t *testing.T) {
	var seenMethod, seenQuery, seenPath string
	rt := New(echoHandler("default"), map[string]http.Handler{
		"/api": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seenMethod, seenQuery, seenPath = r.Method, r.URL.RawQuery, r.URL.Path
			w.WriteHeader(http.StatusOK)
		}),
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/items?page=2", nil)
	rt.ServeHTTP(rec, req)

	if seenMethod != http.MethodPost || seenQuery != "page=2" || seenPath != "/api/items" {
		t.Errorf("request mutated: method=%s query=%s path=%s", seenMethod, seenQuery, seenPath)
	}
}

// TestFallbackOnlyRouter degenerates gracefully with an empty route table.
func TestFallbackOnlyRouter(t *testing.T) {
	rt := New(echoHandler("default"), nil)
	if got := get(t, rt, "/anything"); got != "default" {
		t.Errorf("got %q, want default", got)
	}
}

// TestRootPrefixMatchesEverything verifies "/" as a catch-all route.
func TestRootPrefixMatchesEverything(t *testing.T) {
	rt := New(echoHandler("default"), map[string]http.Handler{
		"/": echoHandler("root"),
	})
	for _, path := range []string{"/", "/x", "/deep/nested"} {
		if got := get(t, rt, path); got != "root" {
			t.Errorf("path %s: got %q, want root", path, got)
		}
	}
}
