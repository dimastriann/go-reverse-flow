// Package admin serves the management surface: a Basic-Auth-protected JSON
// API and (in later releases) the embedded dashboard, on its own listener —
// deliberately outside the proxy chain, so admin traffic is never throttled,
// cached, or routed.
//
// The credentials live in the caller's config; the middleware compares them
// in constant time and every handler behind Handler requires them.
package admin

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"runtime"
	"time"
)

// BackendStatus is one row of the status payload.
type BackendStatus struct {
	URL      string `json:"url"`
	Healthy  bool   `json:"healthy"`
	Requests uint64 `json:"requests"`
}

// Status is the full GET /api/status payload.
type Status struct {
	Version     string          `json:"version"`
	GoVersion   string          `json:"go_version"`
	Uptime      string          `json:"uptime"`
	Listen      string          `json:"listen"`
	AdminListen string          `json:"admin_listen"`
	StartedAt   time.Time       `json:"started_at"`
	Backends    []BackendStatus `json:"backends"`
	CacheTTL    string          `json:"cache_ttl"`
	RateLimit   float64         `json:"rate_limit"`
	RateBurst   int             `json:"rate_burst"`
}

// StatusSource builds the current payload; injected so the server package
// stays decoupled from config/proxy/health details.
type StatusSource func() Status

// Server is the admin HTTP server.
type Server struct {
	status    StatusSource
	user      string
	pass      string
	startedAt time.Time
	now       func() time.Time
}

// Option configures a Server.
type Option func(*Server)

// WithClock replaces the time source (tests).
func WithClock(fn func() time.Time) Option {
	return func(s *Server) {
		s.now = fn
	}
}

// New builds the admin Server. Credentials come from the caller's config;
// callers must gate construction on them being non-empty (a password is the
// documented on-switch).
func New(username, password string, status StatusSource, opts ...Option) *Server {
	s := &Server{
		status:    status,
		user:      username,
		pass:      password,
		startedAt: time.Now(),
		now:       time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Handler returns the fully authenticated API mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	return s.requireAuth(mux)
}

// handleStatus writes the current payload.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	st := s.status()
	st.GoVersion = runtime.Version()
	st.Uptime = s.now().Sub(s.startedAt).Round(time.Second).String()
	writeJSON(w, http.StatusOK, st)
}

// requireAuth wraps a handler with Basic Auth checks; comparison is constant
// time per credential part. The 401 deliberately carries no
// WWW-Authenticate challenge: the dashboard owns the login UX with its own
// form, and a challenge would make browsers pop their native dialog on top
// of it (curl users pass -u regardless).
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(s.user)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(s.pass)) == 1
		if !ok || !userOK || !passOK {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeJSON emits v as a JSON body with the right content type.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
