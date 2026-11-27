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
	"io"
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

	configSource func() any         // optional: GET /api/config
	applyFn      func([]byte) error // optional: PUT /api/config
	reloadFn     func() error       // optional: POST /api/reload
}

// Option configures a Server.
type Option func(*Server)

// WithClock replaces the time source (tests).
func WithClock(fn func() time.Time) Option {
	return func(s *Server) {
		s.now = fn
	}
}

// WithConfigSource registers the GET /api/config payload provider. The
// returned value is serialized with its own JSON tags.
func WithConfigSource(fn func() any) Option {
	return func(s *Server) {
		s.configSource = fn
	}
}

// WithApply registers the PUT /api/config handler: it receives the raw JSON
// body (bounded), validates + applies + persists it, and returns an error to
// reject with 400 while the old configuration stays live.
func WithApply(fn func([]byte) error) Option {
	return func(s *Server) {
		s.applyFn = fn
	}
}

// WithReload registers the POST /api/reload handler: re-reads the config
// file, applies it, and returns an error to reject with 400.
func WithReload(fn func() error) Option {
	return func(s *Server) {
		s.reloadFn = fn
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

// Handler returns the fully authenticated API mux. Management endpoints are
// registered only when their hooks were provided.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	if s.configSource != nil || s.applyFn != nil {
		mux.HandleFunc("/api/config", s.handleConfig)
	}
	if s.reloadFn != nil {
		mux.HandleFunc("/api/reload", s.handleReload)
	}
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

// handleConfig serves GET /api/config (the current configuration) and
// PUT /api/config (validate + apply + persist a new one; failures leave the
// live configuration untouched).
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if s.configSource == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, s.configSource())
	case http.MethodPut:
		if s.applyFn == nil {
			http.NotFound(w, r)
			return
		}
		body := http.MaxBytesReader(w, r.Body, 1<<20)
		if err := s.applyFn(readAll(body)); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "applied"})
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleReload serves POST /api/reload.
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.reloadFn(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

// readAll bounds and drains a request body.
func readAll(r io.Reader) []byte {
	body, err := io.ReadAll(r)
	if err != nil {
		return nil
	}
	return body
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
