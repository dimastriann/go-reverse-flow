// Package ratelimit throttles requests per client IP with a token bucket.
//
// Each client (host part of RemoteAddr) owns a bucket refilling at a fixed
// rate up to a burst ceiling; when the bucket is empty the middleware answers
// 429 with a Retry-After hint instead of forwarding. The clock is injectable
// for deterministic tests. Disabled rate limiting is expressed by a rate of 0.
package ratelimit

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// pruneEvery controls how often stale buckets are swept in an
	// opportunistic, goroutine-free way: every Nth request.
	pruneEvery = 64
	// pruneIdle drops buckets with no refill movement for this long.
	pruneIdle = 10 * time.Minute
)

// Limiter is the middleware.
type Limiter struct {
	next  http.Handler
	rate  float64 // tokens per second
	burst float64

	mu      sync.Mutex
	buckets map[string]*bucket
	reqN    uint64

	now func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// Option configures a Limiter.
type Option func(*Limiter)

// WithClock replaces the time source (tests).
func WithClock(fn func() time.Time) Option {
	return func(l *Limiter) {
		l.now = fn
	}
}

// New wraps next with per-client token-bucket rate limiting. rate<=0 or
// burst<=1 disables limiting and returns next unchanged.
func New(next http.Handler, rate float64, burst int, opts ...Option) http.Handler {
	if rate <= 0 || burst <= 0 {
		return next
	}
	l := &Limiter{
		next:    next,
		rate:    rate,
		burst:   float64(burst),
		buckets: map[string]*bucket{},
		now:     time.Now,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// ServeHTTP implements http.Handler.
func (l *Limiter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	client := hostPart(r.RemoteAddr)

	l.mu.Lock()
	b := l.buckets[client]
	if b == nil {
		b = &bucket{tokens: l.burst, last: l.now()}
		l.buckets[client] = b
	} else {
		elapsed := l.now().Sub(b.last).Seconds()
		if elapsed > 0 {
			b.tokens = math.Min(l.burst, b.tokens+elapsed*l.rate)
			b.last = l.now()
		}
	}
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	l.reqN++
	pruneDue := l.reqN%pruneEvery == 0
	if pruneDue {
		l.pruneLocked()
	}
	l.mu.Unlock()

	if !allowed {
		retryIn := time.Duration((1 / l.rate) * float64(time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(ceilSeconds(retryIn)))
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}

	l.next.ServeHTTP(w, r)
}

// pruneLocked drops long-idle buckets. Caller holds mu.
func (l *Limiter) pruneLocked() {
	for client, b := range l.buckets {
		if l.now().Sub(b.last) > pruneIdle {
			delete(l.buckets, client)
		}
	}
}

// Len exports the tracked-client count for tests.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

func hostPart(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return strings.TrimSpace(remoteAddr)
	}
	return host
}

func ceilSeconds(d time.Duration) int {
	s := int(math.Ceil(d.Seconds()))
	if s < 1 {
		return 1
	}
	return s
}
