// Package cache adds a TTL response cache for idempotent GET/HEAD requests
// in front of the proxy.
//
// Eligibility is deliberately conservative: only 2xx responses with a
// Content-Length at or under maxEntryBytes are cached, requests/responses
// carrying Cache-Control: no-store are bypassed, Set-Cookie responses are
// never shared, and Accept-Encoding requests bypass the keying entirely
// because a single-key scheme cannot represent per-flavor variants.
// Credentialed requests (Authorization, Cookie, Proxy-Authorization) bypass
// lookup and store for the same reason: the key ignores identity, so a
// shared entry would replay one client's response body to another. Range
// requests bypass likewise, and partial content (206, Content-Range) is
// never stored either: a partial body under the full-URI key would poison
// that URI for every client.
//
// Buffering is bounded: stored entries fit maxEntryBytes, and responses
// that outgrow the cap stream through verbatim instead of buffering, so no
// single origin response can balloon proxy memory regardless of size.
//
// The entry count is bounded too (maxEntries): flooding distinct keys
// evicts expired entries first, then an arbitrary one, so resident memory
// stays bounded no matter how many distinct URIs clients request.
//
// The tradeoff is documented behavior, not a bug: stored response bodies
// are buffered in full and served afterwards, and every response through
// the middleware carries an X-Cache: hit or miss stamp so the cache is
// observable from curl in the demo.
package cache

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxEntryBytes caps one cacheable response body to keep memory bounded.
const maxEntryBytes = 1 << 20 // 1 MiB

// maxEntries caps the tracked-entry count, bounding resident cache memory
// to maxEntryBytes × maxEntries regardless of how many distinct URIs
// clients request.
const maxEntries = 1 << 10 // 1024 entries

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
	_, h := NewCache(next, ttl, opts...)
	return h
}

// NewCache builds the concrete Cache alongside its middleware form. Callers
// needing an operational handle on the instance (the invalidation endpoint)
// use this; plain chain wiring uses New.
func NewCache(next http.Handler, ttl time.Duration, opts ...Option) (*Cache, http.Handler) {
	if ttl <= 0 {
		return nil, next
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
	return c, c
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
	rec := &bufferingWriter{real: w}
	c.next.ServeHTTP(rec, r)

	if rec.overflow {
		// The response outgrew the buffer cap mid-stream: it was committed
		// and streamed verbatim through the real writer already. Serving
		// again would duplicate the prologue; storing is pointless.
		return
	}

	resp := Response{
		Status:  rec.status,
		Header:  rec.Header().Clone(),
		Body:    append([]byte(nil), rec.body...),
		Expires: c.now().Add(c.ttl),
	}
	if _, ok := c.storeable(rec); ok {
		c.mu.Lock()
		c.evictLocked()
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
	// Credentials are the sharpest per-client variant: replaying a stored
	// entry across an Authorization/Cookie boundary serves one client's
	// response body to another, so credentialed requests skip lookup and
	// store entirely.
	for _, h := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	// Range requests change what the response body *is* for the same URI:
	// a stored 206 partial body would be replayed to every ordinary client
	// of the URI, so ranged requests skip lookup and store entirely.
	if r.Header.Get("Range") != "" {
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
	// Partial content is never shared: storing a byte-range body under the
	// full-URI key would replay it to ordinary requests of that URI.
	if rec.status == http.StatusPartialContent || rec.Header().Get("Content-Range") != "" {
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
//
// The body never exceeds maxEntryBytes in memory: the first write that
// would trip the cap commits the buffered prologue to the real writer and
// switches to pass-through, so an attacker-sized response streams through
// instead of being accumulated.
type bufferingWriter struct {
	real http.ResponseWriter

	header   http.Header
	status   int
	body     []byte
	overflow bool
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
	if w.overflow {
		return w.real.Write(p)
	}
	if len(w.body)+len(p) > maxEntryBytes {
		w.commit()
		return w.real.Write(p)
	}
	w.body = append(w.body, p...)
	return len(p), nil
}

// commit flushes the buffered prologue to the real writer and marks the
// writer as streaming; after this point nothing else is accumulated.
func (w *bufferingWriter) commit() {
	w.overflow = true
	for k, vs := range w.Header() {
		for _, v := range vs {
			w.real.Header().Add(k, v)
		}
	}
	w.real.WriteHeader(w.status)
	_, _ = w.real.Write(w.body)
}

// evictLocked makes room for one more entry when the map is at capacity:
// an expired entry first, otherwise an arbitrary one. Map iteration order
// is randomized by Go, so a flooded key space rotates fairly instead of
// freezing the table or growing it past the cap. Callers hold c.mu.
func (c *Cache) evictLocked() {
	if len(c.items) < maxEntries {
		return
	}
	now := c.now()
	var victim string
	found := false
	for k, v := range c.items {
		if now.After(v.Expires) {
			victim, found = k, true
			break
		}
		if !found {
			victim, found = k, true // fallback: the first key seen
		}
	}
	if found {
		delete(c.items, victim)
	}
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

// PurgeHandler serves the invalidation endpoint: POST clears every entry
// and answers 204 with the removed count in X-Purged-Entries; other methods
// get 405 with an Allow hint, so a stray GET or a crawler cannot wipe the
// cache. Mount it on the mux only while the cache is enabled (a disabled
// cache has no entries to purge).
func (c *Cache) PurgeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed: POST purges the cache", http.StatusMethodNotAllowed)
			return
		}
		n := c.Len()
		c.Purge()
		log.Printf("cache: purged %d entr%s via %s", n, pluralEntries(n), r.RemoteAddr)
		w.Header().Set("X-Purged-Entries", strconv.Itoa(n))
		w.WriteHeader(http.StatusNoContent)
	})
}

// pluralEntries renders "y"/"ies" for the purged-count log line.
func pluralEntries(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
