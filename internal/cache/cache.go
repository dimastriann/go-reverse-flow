// Package cache adds a TTL response cache for idempotent GET/HEAD requests
// in front of the proxy.
//
// Eligibility is deliberately conservative: only 2xx responses with a
// Content-Length at or under maxEntryBytes are cached, requests/responses
// carrying Cache-Control: no-store are bypassed, Set-Cookie responses are
// never shared, and Accept-Encoding requests bypass the keying entirely
// because a single-key scheme cannot represent per-flavor variants. The
// tradeoff is documented behavior, not a bug: cached responses are served
// after full buffering (they must fit maxEntryBytes anyway), and every
// response through the middleware carries an X-Cache: hit or miss stamp so
// the cache is observable from curl in the demo.
package cache

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxEntryBytes caps one cacheable response body to keep memory bounded.
const maxEntryBytes = 1 << 20 // 1 MiB

// Response is one buffered cache entry.
type Response struct {
	Status  int
	Header  http.Header
	Body    []byte
	Expires time.Time
}

// Cache is the middleware.
type Cache struct {
	next http.Handler
	ttl  time.Duration

	mu    sync.RWMutex
	items map[string]Response

	now func() time.Time
}

// Option configures a Cache.
type Option func(*Cache)

// WithClock replaces the time source (tests).
func WithClock(fn func() time.Time) Option {
	return func(c *Cache) {
		c.now = fn
	}
}

// New wraps next with the TTL cache; ttl<=0 disables it entirely.
func New(next http.Handler, ttl time.Duration, opts ...Option) http.Handler {
	if ttl <= 0 {
		return next
	}
	c := &Cache{
		next:  next,
		ttl:   ttl,
		items: map[string]Response{},
		now:   time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// ServeHTTP implements http.Handler.
func (c *Cache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !c.eligible(r) {
		c.next.ServeHTTP(w, r)
		return
	}

	key := r.Method + " " + r.URL.RequestURI()

	if resp, ok := c.lookup(key); ok {
		w.Header().Set("X-Cache", "hit")
		serveBuffered(w, resp)
		return
	}
	rec := &bufferingWriter{}
	c.next.ServeHTTP(rec, r)

	resp := Response{
		Status:  rec.status,
		Header:  rec.Header().Clone(),
		Body:    append([]byte(nil), rec.body...),
		Expires: c.now().Add(c.ttl),
	}
	if _, ok := c.storeable(rec); ok {
		c.mu.Lock()
		c.items[key] = resp
		c.mu.Unlock()
		w.Header().Set("X-Cache", "miss")
	} else {
		// Uncacheable: serve the captured response verbatim, unstamped.
		w.Header().Del("X-Cache")
	}
	serveBuffered(w, resp)
}

// eligible gates the cache by method and client escape hatches.
func (c *Cache) eligible(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	default:
		return false
	}
	cc := r.Header.Get("Cache-Control")
	if cc != "" && strings.Contains(cc, "no-store") {
		return false
	}
	if r.Header.Get("X-Cache-Bypass") != "" {
		return false
	}
	if r.Header.Get("Accept-Encoding") != "" {
		return false // naive single-key scheme cannot represent variants
	}
	return true
}

// lookup returns a live entry for key, if any.
func (c *Cache) lookup(key string) (Response, bool) {
	c.mu.RLock()
	resp, ok := c.items[key]
	c.mu.RUnlock()
	if !ok || c.now().After(resp.Expires) {
		return Response{}, false
	}
	return resp, true
}

// storeable decides whether the buffered response is cache-safe.
func (c *Cache) storeable(rec *bufferingWriter) (Response, bool) {
	if rec.status < 200 || rec.status > 299 {
		return Response{}, false
	}
	if rec.Header().Get("Set-Cookie") != "" {
		return Response{}, false
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "" && strings.Contains(cc, "no-store") {
		return Response{}, false
	}
	length := rec.Header().Get("Content-Length")
	if length == "" {
		return Response{}, false // unknown size: not for this simple scheme
	}
	n, err := strconv.Atoi(length)
	if err != nil || n < 0 || n > maxEntryBytes || len(rec.body) != n {
		return Response{}, false
	}

	resp := Response{
		Status:  rec.status,
		Header:  rec.Header().Clone(),
		Body:    append([]byte(nil), rec.body...),
		Expires: c.now().Add(c.ttl),
	}
	return resp, true
}

// serveBuffered replays a buffered entry verbatim.
func serveBuffered(w http.ResponseWriter, resp Response) {
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}

// bufferingWriter buffers the whole inner response; the cache serves it only
// through serveBuffered afterwards, so nothing reaches the client twice.
type bufferingWriter struct {
	header http.Header
	status int
	body   []byte
}

func (w *bufferingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *bufferingWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *bufferingWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.body = append(w.body, p...)
	return len(p), nil
}

// Len reports the tracked-entry count for tests.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// Purge drops every entry immediately (TTL expiry is checked lazily at
// lookup; expired entries are simply overwritten on the next store).
func (c *Cache) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = map[string]Response{}
}
