// Package config loads the proxy configuration from a JSON or YAML file and
// applies defaults and validation.
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultListenAddr = ":8080"

	// EnvListenAddr, when set non-empty, overrides ListenAddr from the file
	// config (ops run the same config file at different ports per replica).
	EnvListenAddr = "LISTEN_ADDR"

	// EnvAdminUser / EnvAdminPassword override the admin credentials from the
	// file config (secrets should not live in committed files).
	EnvAdminUser     = "ADMIN_USER"
	EnvAdminPassword = "ADMIN_PASSWORD"

	// DefaultAdminListen binds the admin surface to loopback by default:
	// exposing it is always an explicit choice.
	DefaultAdminListen = "127.0.0.1:8090"

	// DefaultAdminUser is the username when only a password is configured.
	DefaultAdminUser = "admin"

	// Default health-check settings, used when the JSON values are absent.
	DefaultHealthInterval = 5 * time.Second
	DefaultHealthTimeout  = time.Second

	// Default rate-limit settings: throttling starts disabled.
	DefaultRateBurst = 1
)

// Duration is time.Duration with human-friendly JSON parsing: accepts Go
// duration strings like "500ms", "5s", or "2m" (plain integers are treated as
// whole seconds for convenience). It marshals back as a duration string in
// both JSON and YAML, so persisted configs stay human-readable.
type Duration time.Duration

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("duration %s: %w", data, err)
	}
	switch value := v.(type) {
	case string:
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("duration %q: %w", value, err)
		}
		*d = Duration(parsed)
	case float64:
		*d = Duration(time.Duration(value * float64(time.Second)))
	default:
		return fmt.Errorf("duration %s: must be a string like \"5s\" or a number of seconds", data)
	}
	return nil
}

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

// Config is the top-level proxy configuration.
type Config struct {
	ListenAddr string         `json:"listen_addr" yaml:"listen_addr"`
	Backends   []string       `json:"backends" yaml:"backends"`
	Weights    []int          `json:"backend_weights,omitempty" yaml:"backend_weights,omitempty"` // optional, aligned with backends
	Routes     []Route        `json:"routes,omitempty" yaml:"routes,omitempty"`                   // optional path-prefix routing
	Health     HealthSettings `json:"health,omitempty" yaml:"health,omitempty"`
	RateLimit  RateSettings   `json:"rate_limiter,omitempty" yaml:"rate_limiter,omitempty"`
	Cache      CacheSettings  `json:"cache,omitempty" yaml:"cache,omitempty"`
	Admin      AdminSettings  `json:"admin,omitempty" yaml:"admin,omitempty"`
}

// JSON renders the config as indented JSON (the /api/config view and the
// persistence format for .json files).
func (c *Config) JSON() ([]byte, error) {
	return json.MarshalIndent(c, "", "    ")
}

// YAML renders the config as indented YAML (the persistence format for
// .yaml/.yml files).
func (c *Config) YAML() ([]byte, error) {
	return yaml.Marshal(c)
}

// AdminSettings configures the management surface. Throttling and caching
// never apply to it; auth is active only when a password is present.
type AdminSettings struct {
	Listen   string `json:"listen,omitempty" yaml:"listen,omitempty"`     // defaults to 127.0.0.1:8090
	Username string `json:"username,omitempty" yaml:"username,omitempty"` // defaults to "admin"
	Password string `json:"password,omitempty" yaml:"password,omitempty"` // empty password disables the admin surface
}

// Values resolves the knobs with defaults applied. Enabled reports whether
// the admin surface should start at all (a password is the on-switch).
func (a AdminSettings) Values() (listen, username, password string, enabled bool) {
	listen = DefaultAdminListen
	username = DefaultAdminUser
	if a.Listen != "" {
		listen = a.Listen
	}
	if a.Username != "" {
		username = a.Username
	}
	if a.Password != "" {
		password = a.Password
		enabled = true
	}
	return listen, username, password, enabled
}

// Route sends requests whose path starts with Prefix to its own backend
// pool instead of the default one. The path is forwarded as-is.
type Route struct {
	Prefix   string   `json:"prefix" yaml:"prefix"`
	Backends []string `json:"backends" yaml:"backends"`
}

// AllBackends returns the union of default and route backends in first-seen
// order (for the shared health checker).
func (c *Config) AllBackends() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(c.Backends))
	for _, u := range c.Backends {
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	for _, r := range c.Routes {
		for _, u := range r.Backends {
			if !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	}
	return out
}

// CacheSettings enables the TTL response cache when ttl > 0.
type CacheSettings struct {
	TTL *Duration `json:"ttl,omitempty" yaml:"ttl,omitempty"`
}

// Value resolves the knob; absent or non-positive ttl disables caching.
func (s CacheSettings) Value() time.Duration {
	if s.TTL != nil && *s.TTL > 0 {
		return time.Duration(*s.TTL)
	}
	return 0
}

// HealthSettings controls the periodic backend probe.
type HealthSettings struct {
	Interval *Duration `json:"interval,omitempty" yaml:"interval,omitempty"` // sweep cadence
	Timeout  *Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`   // per-probe request timeout
}

// Values resolves pointers into concrete durations with defaults applied.
func (h HealthSettings) Values() (interval, timeout time.Duration) {
	interval = DefaultHealthInterval
	timeout = DefaultHealthTimeout
	if h.Interval != nil && *h.Interval > 0 {
		interval = time.Duration(*h.Interval)
	}
	if h.Timeout != nil && *h.Timeout > 0 {
		timeout = time.Duration(*h.Timeout)
	}
	return interval, timeout
}

// RateSettings enables per-client token-bucket throttling when rate > 0.
type RateSettings struct {
	Rate  *float64 `json:"rate,omitempty" yaml:"rate,omitempty"`   // tokens per second, 0 disables
	Burst *int     `json:"burst,omitempty" yaml:"burst,omitempty"` // bucket ceiling, needed when rate > 0
}

// Values resolves the knobs with defaults applied (nothing set = disabled).
func (s RateSettings) Values() (rate float64, burst int) {
	if s.Rate == nil || *s.Rate <= 0 {
		return 0, 0
	}
	rate = *s.Rate
	burst = 1
	if s.Burst != nil && *s.Burst > 0 {
		burst = *s.Burst
	}
	return rate, burst
}

// ApplyEnvOverride applies runtime environment overrides on top of the
// loaded file config. lookup is injected (typically os.Getenv) so callers
// and tests stay off the process environment. Each override wins only when
// it resolves non-empty; unset/empty means the file value stands.
func (c *Config) ApplyEnvOverride(lookup func(string) string) {
	if v := lookup(EnvListenAddr); v != "" {
		c.ListenAddr = v
	}
	if v := lookup(EnvAdminUser); v != "" {
		c.Admin.Username = v
	}
	if v := lookup(EnvAdminPassword); v != "" {
		c.Admin.Password = v
	}
}

// Load reads the config file at path and validates it. The format is
// detected by file extension: .yaml/.yml parse as YAML, everything else
// (including .json) as JSON. Both formats share one semantics path — the
// YAML bytes are bridged to their JSON equivalent, so Duration parsing,
// defaults, validation, and env overrides behave identically.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	data = stripBOM(data)

	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") {
		data, err = yamlToJSON(data)
		if err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}

	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// Parse decodes JSON config bytes, applies defaults, and validates. It is
// the shared tail of Load and the entry point for the admin config API.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// stripBOM removes a leading UTF-8 byte-order mark, which Windows editors
// add silently and both the JSON and YAML decoders reject.
func stripBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}

// yamlToJSON converts YAML bytes to their JSON equivalent.
func yamlToJSON(data []byte) ([]byte, error) {
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// applyDefaults fills in sane defaults for empty fields.
func (c *Config) applyDefaults() {
	if c.ListenAddr == "" {
		c.ListenAddr = DefaultListenAddr
	}
}

// Validate checks that required fields are present and well-formed.
// An empty/invalid ListenAddr or zero backends renders the config unusable.
func (c *Config) Validate() error {
	if c.ListenAddr == "" {
		return fmt.Errorf("listen_addr is empty")
	}
	if len(c.Backends) == 0 {
		return fmt.Errorf("backends is empty: at least one backend URL is required")
	}
	if len(c.Weights) != 0 && len(c.Weights) != len(c.Backends) {
		return fmt.Errorf("backend_weights has %d entries for %d backends: must match or be omitted", len(c.Weights), len(c.Backends))
	}
	for i, w := range c.Weights {
		if w < 1 {
			return fmt.Errorf("backend_weights[%d] = %d: must be >= 1", i, w)
		}
	}
	for i, b := range c.Backends {
		u, err := url.Parse(b)
		if err != nil {
			return fmt.Errorf("backends[%d] %q: %w", i, b, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("backends[%d] %q: scheme must be http or https", i, b)
		}
		if u.Host == "" {
			return fmt.Errorf("backends[%d] %q: missing host", i, b)
		}
	}

	prefixes := map[string]bool{}
	for i, r := range c.Routes {
		if !strings.HasPrefix(r.Prefix, "/") {
			return fmt.Errorf("routes[%d] %q: prefix must start with /", i, r.Prefix)
		}
		if prefixes[r.Prefix] {
			return fmt.Errorf("routes[%d] %q: duplicate prefix", i, r.Prefix)
		}
		prefixes[r.Prefix] = true
		if len(r.Backends) == 0 {
			return fmt.Errorf("routes[%d] %q: backends is empty", i, r.Prefix)
		}
		for j, b := range r.Backends {
			u, err := url.Parse(b)
			if err != nil {
				return fmt.Errorf("routes[%d] %q backends[%d] %q: %w", i, r.Prefix, j, b, err)
			}
			if u.Scheme != "http" && u.Scheme != "https" {
				return fmt.Errorf("routes[%d] %q backends[%d] %q: scheme must be http or https", i, r.Prefix, j, b)
			}
			if u.Host == "" {
				return fmt.Errorf("routes[%d] %q backends[%d] %q: missing host", i, r.Prefix, j, b)
			}
		}
	}
	return nil
}
