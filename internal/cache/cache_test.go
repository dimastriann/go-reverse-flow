package cache

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeClock drives deterministic TTL behavior.
type fakeClock struct{ t time.Time }

func (fc *fakeClock) now() time.Time      { return fc.t }
func (fc *fakeClock) adv(d time.Duration) { fc.t = fc.t.Add(d) }

// countingHandler serves a unique body per call and tracks invocations.
func countingHandler(t *testing.T) (*int, http.Handler) {
	t.Helper()
	n := 0
	return &n, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		body := fmt.Sprintf("call-%d", n)
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		fmt.Fprint(w, body)
	})
}

func doGet(t *testing.T, h http.Handler, path string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	h.ServeHTTP(rec, req)
	return rec
}

// TestMissThenHitLifecycle: first request passes through (miss), second is
// served from the cache (hit) without touching the inner handler.
func TestMissThenHitLifecycle(t *testing.T) {
	n, inner := countingHandler(t)
	fc := &fakeClock{t: time.Now()}
	h := New(inner, 30*time.Second, WithClock(fc.now))

	first := doGet(t, h, "/x")
	if first.Code != http.StatusOK || first.Body.String() != "call-1" {
		t.Fatalf("first: status %d body %q", first.Code, first.Body.String())
	}
	if got := first.Header().Get("X-Cache"); got != "miss" {
		t.Errorf("first X-Cache = %q, want miss", got)
	}

	second := doGet(t, h, "/x")
	if second.Body.String() != "call-1" {
		t.Errorf("second body = %q, want cached call-1", second.Body.String())
	}
	if got := second.Header().Get("X-Cache"); got != "hit" {
		t.Errorf("second X-Cache = %q, want hit", got)
	}
	if *n != 1 {
		t.Errorf("inner invocations = %d, want 1", *n)
	}
	if c := h.(*Cache); c.Len() != 1 {
		t.Errorf("entries = %d, want 1", c.Len())
	}
}

// TestTTLExpiryAndRefresh: after the TTL the entry stops serving and the
// next pass re-stores a fresh one.
func TestTTLExpiryAndRefresh(t *testing.T) {
	n, inner := countingHandler(t)
	fc := &fakeClock{t: time.Now()}
	h := New(inner, 10*time.Second, WithClock(fc.now))

	_ = doGet(t, h, "/x")
	fc.adv(11 * time.Second)
	expired := doGet(t, h, "/x")
	if expired.Header().Get("X-Cache") != "miss" || expired.Body.String() != "call-2" {
		t.Errorf("after TTL: X-Cache %q body %q; want miss with fresh content", expired.Header().Get("X-Cache"), expired.Body.String())
	}
	if *n != 2 {
		t.Errorf("inner invocations = %d, want 2", *n)
	}
}

// TestPerPathKeys: /a and /b are distinct entries.
func TestPerPathKeys(t *testing.T) {
	_, inner := countingHandler(t)
	fc := &fakeClock{t: time.Now()}
	h := New(inner, 30*time.Second, WithClock(fc.now))

	_ = doGet(t, h, "/a")
	if got := doGet(t, h, "/b"); got.Header().Get("X-Cache") != "miss" {
		t.Error("/b served from /a's entry: keys collided")
	}
}

// TestEligibilityBypasses covers method, request Cache-Control, and the
// X-Cache-Bypass escape hatch.
func TestEligibilityBypasses(t *testing.T) {
	n, inner := countingHandler(t)
	fc := &fakeClock{t: time.Now()}
	h := New(inner, 30*time.Second, WithClock(fc.now))

	_ = doGet(t, h, "/x") // prime the entry: 1 call

	if rec := doGet(t, h, "/x", "Cache-Control", "no-store"); rec.Header().Get("X-Cache") == "hit" {
		t.Error("no-store request got a cached hit")
	}
	if rec := doGet(t, h, "/x", "X-Cache-Bypass", "1"); rec.Header().Get("X-Cache") == "hit" {
		t.Error("X-Cache-Bypass request got a cached hit")
	}

	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/x", nil))
	if post.Header().Get("X-Cache") != "" {
		t.Error("POST went through the cache")
	}
	if *n < 3 {
		t.Errorf("inner invocations = %d, want >=3 (bypassed requests must hit inner)", *n)
	}
}

// TestCredentialedRequestsBypassCache: Authorization and Cookie requests
// bypass lookup and store. The key ignores identity, so without this both
// leak directions exist: a credentialed client must not receive an
// anonymously cached body, and its own body must never be replayed to
// other clients of the URI. Regression for the cross-client leak found in
// the security review.
func TestCredentialedRequestsBypassCache(t *testing.T) {
	n, inner := countingHandler(t)
	fc := &fakeClock{t: time.Now()}
	h := New(inner, 30*time.Second, WithClock(fc.now))

	if rec := doGet(t, h, "/x"); rec.Header().Get("X-Cache") != "miss" {
		t.Fatalf("anonymous prime: X-Cache %q, want miss", rec.Header().Get("X-Cache"))
	}

	authed := doGet(t, h, "/x", "Authorization", "Bearer secret")
	if got := authed.Header().Get("X-Cache"); got != "" {
		t.Errorf("credentialed X-Cache = %q, want unstamped pass-through", got)
	}
	if authed.Body.String() != "call-2" {
		t.Errorf("credentialed body %q, want fresh call-2, not the anonymous call-1", authed.Body.String())
	}
	if rec := doGet(t, h, "/x", "Cookie", "session=1"); rec.Body.String() != "call-3" {
		t.Errorf("cookie body %q, want fresh call-3", rec.Body.String())
	}

	// Still untouched for anonymous clients: no credentialed body leaked
	// into the entry and no second entry was stored.
	if rec := doGet(t, h, "/x"); rec.Header().Get("X-Cache") != "hit" || rec.Body.String() != "call-1" {
		t.Errorf("anonymous replay X-Cache %q body %q, want hit with call-1", rec.Header().Get("X-Cache"), rec.Body.String())
	}
	if c := h.(*Cache); c.Len() != 1 {
		t.Errorf("entries = %d, want 1 (credentialed responses must not store)", c.Len())
	}
	if *n != 3 {
		t.Errorf("inner invocations = %d, want 3", *n)
	}
}

// rangeBackend behaves like a real static origin: byte-range requests get
// a 206 slice with Content-Range, everything else gets the full body.
func rangeBackend(t *testing.T) (*int, http.Handler) {
	t.Helper()
	body := bytes.Repeat([]byte("abcdefg"), 8) // 56B, well under the entry cap
	n := 0
	return &n, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		rng := strings.TrimPrefix(r.Header.Get("Range"), "bytes=")
		if dash := strings.Index(rng, "-"); dash >= 0 {
			start, _ := strconv.Atoi(rng[:dash])
			end, _ := strconv.Atoi(rng[dash+1:])
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
			w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(body[start : end+1])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	})
}

// TestRangedRequestsNeverCached: a stored 206 partial body would poison the
// whole URI — later ordinary clients of `GET /file` would receive the byte
// range instead of the file. The key cannot represent the Range variant,
// so ranged requests bypass lookup and store. Regression for the
// poisoning vector found in the security review.
func TestRangedRequestsNeverCached(t *testing.T) {
	hits, inner := rangeBackend(t)
	fc := &fakeClock{t: time.Now()}
	h := New(inner, 30*time.Second, WithClock(fc.now))

	// Ranged request on a cold key: served verbatim (206 of the origin),
	// unstamped, and nothing stored.
	ranged := doGet(t, h, "/file", "Range", "bytes=0-9")
	if got := ranged.Header().Get("X-Cache"); got != "" {
		t.Errorf("ranged X-Cache = %q, want unstamped pass-through", got)
	}
	if ranged.Code != http.StatusPartialContent || ranged.Header().Get("Content-Range") == "" {
		t.Fatalf("ranged response status %d, want 206 with Content-Range", ranged.Code)
	}
	if c := h.(*Cache); c.Len() != 0 {
		t.Errorf("entries = %d, want 0 (partial content must not store)", c.Len())
	}

	// The URI is unpoisoned: an ordinary client gets the full 200 body.
	full := doGet(t, h, "/file")
	if got := full.Header().Get("X-Cache"); got != "miss" {
		t.Errorf("full response X-Cache = %q, want fresh miss", got)
	}
	if full.Code != http.StatusOK || full.Body.Len() != 56 || !bytes.HasPrefix(full.Body.Bytes(), []byte("abcdefg")) {
		t.Errorf("full body served %d/%d status %d; want 200 full file", full.Body.Len(), 56, full.Code)
	}

	// After priming, ranged requests still reach the origin — the cached
	// full body must not be replayed to them either (206 contract).
	ranged2 := doGet(t, h, "/file", "Range", "bytes=0-9")
	if got := ranged2.Header().Get("X-Cache"); got != "" || ranged2.Body.Len() != 10 {
		t.Errorf("second ranged response X-Cache %q len %d; want unstamped 10B pass-through", got, ranged2.Body.Len())
	}
	if *hits != 3 {
		t.Errorf("inner invocations = %d, want 3", *hits)
	}
}

// TestStoreableRejectsPartialContent: even where the eligibility bypass
// does not apply, partial content never becomes a shared entry (defense in
// depth for the Range bypass above).
func TestStoreableRejectsPartialContent(t *testing.T) {
	fc := &fakeClock{t: time.Now()}
	n, inner := countingHandler(t)
	_ = n
	h := New(inner, 30*time.Second, WithClock(fc.now)).(*Cache)

	rec := &bufferingWriter{}
	rec.Header().Set("Content-Length", "2")
	rec.WriteHeader(http.StatusPartialContent)
	_, _ = rec.Write([]byte("ok"))
	if _, ok := h.storeable(rec); ok {
		t.Error("206 response was deemed storeable")
	}
}

// TestSetCookieNeverCached pins response-side storeability rules.
func TestSetCookieNeverCached(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "session=1; HttpOnly")
		w.Header().Set("Content-Length", "2")
		fmt.Fprint(w, "ok")
	})
	fc := &fakeClock{t: time.Now()}
	h := New(inner, 30*time.Second, WithClock(fc.now))

	_ = doGet(t, h, "/login")
	if got := doGet(t, h, "/login"); got.Header().Get("X-Cache") == "hit" {
		t.Error("Set-Cookie response was cached and replayed")
	}
}

// TestUnknownLengthNotCached: chunked streaming responses pass verbatim and
// are not stored.
func TestUnknownLengthNotCached(t *testing.T) {
	var inner http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "chunk") // no Content-Length -> unknown
	})
	fc := &fakeClock{t: time.Now()}
	h := New(inner, 30*time.Second, WithClock(fc.now))

	first := doGet(t, h, "/stream")
	if first.Body.String() != "chunk" {
		t.Fatalf("stream body = %q, want chunk", first.Body.String())
	}
	if c := h.(*Cache); c.Len() != 0 {
		t.Errorf("entries = %d, want 0 (unknown length must not store)", c.Len())
	}
}

// TestOversizedResponseNotCached caps memory: a body beyond maxEntryBytes is
// served but not stored.
func TestOversizedResponseNotCached(t *testing.T) {
	big := bytes.Repeat([]byte("x"), maxEntryBytes+1)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(big)))
		_, _ = w.Write(big)
	})
	fc := &fakeClock{t: time.Now()}
	h := New(inner, 30*time.Second, WithClock(fc.now))

	rec := doGet(t, h, "/big")
	if rec.Body.Len() != len(big) {
		t.Fatalf("big response truncated: %d bytes", rec.Body.Len())
	}
	if c := h.(*Cache); c.Len() != 0 {
		t.Errorf("entries = %d, want 0 (oversized must not store)", c.Len())
	}
}

// TestDisabledCachePassthrough: ttl<=0 returns the inner handler untouched.
func TestDisabledCachePassthrough(t *testing.T) {
	n, inner := countingHandler(t)
	h := New(inner, 0)

	for i := 0; i < 3; i++ {
		rec := doGet(t, h, "/x")
		if rec.Header().Get("X-Cache") != "" {
			t.Error("disabled cache still stamped X-Cache")
		}
	}
	if *n != 3 {
		t.Errorf("inner invocations = %d, want 3", *n)
	}
}

// TestPurge clears the map.
func TestPurge(t *testing.T) {
	_, inner := countingHandler(t)
	fc := &fakeClock{t: time.Now()}
	h := New(inner, time.Minute, WithClock(fc.now))

	_ = doGet(t, h, "/x")
	c := h.(*Cache)
	if c.Len() != 1 {
		t.Fatalf("entries = %d, want 1", c.Len())
	}
	c.Purge()
	if c.Len() != 0 {
		t.Errorf("after purge entries = %d, want 0", c.Len())
	}
}
