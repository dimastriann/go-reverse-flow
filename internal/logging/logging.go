// Package logging provides the request-logging middleware for the proxy.
//
// New wraps an inner handler and emits one structured log line per request
// after it returns: remote address, method, path, chosen backend, final
// status, and duration. The chosen backend is written by the inner handler
// (via From on the amended context) when the proxy runs underneath; requests
// served without the proxy simply log an empty backend field.
package logging

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// Info carries per-request facts from inner handlers up to the middleware.
type Info struct {
	Backend string
}

// key is the context key under which Info travels.
type key struct{}

// NewContext attaches a fresh Info holder to ctx.
func NewContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, key{}, &Info{})
}

// From returns the Info holder stored in ctx, or nil when NewContext was
// never called (plain use of the inner handler outside the middleware).
func From(ctx context.Context) *Info {
	info, _ := ctx.Value(key{}).(*Info)
	return info
}

// recordingWriter records the final response status.
type recordingWriter struct {
	http.ResponseWriter
	status int
}

func (w *recordingWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *recordingWriter) Write(b []byte) (int, error) {
	// Implicit 200s never call WriteHeader explicitly.
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Middleware wraps the inner handler with one access-log line per request.
type Middleware struct {
	next   http.Handler
	logger *slog.Logger
}

// Option configures the Middleware.
type Option func(*Middleware)

// WithLogger overrides the slog destination (defaults to slog.Default()).
func WithLogger(l *slog.Logger) Option {
	return func(m *Middleware) {
		m.logger = l
	}
}

// New instruments next with the access-log middleware.
func New(next http.Handler, opts ...Option) http.Handler {
	m := &Middleware{next: next, logger: slog.Default()}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// ServeHTTP implements http.Handler.
func (m *Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	rec := &recordingWriter{ResponseWriter: w}
	inner := r.WithContext(NewContext(r.Context()))

	m.next.ServeHTTP(rec, inner)

	info := From(inner.Context())
	backend := ""
	if info != nil {
		backend = info.Backend
	}

	status := rec.status
	if status == 0 {
		// No WriteHeader and no body: Go's http server would have written 200.
		status = http.StatusOK
	}

	m.logger.Info("request",
		"remote_addr", r.RemoteAddr,
		"method", r.Method,
		"path", r.URL.Path,
		"backend", backend,
		"status", status,
		"duration", time.Since(start).Round(time.Microsecond),
	)
}
