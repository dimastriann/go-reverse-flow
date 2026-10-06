// Package config loads the proxy configuration from a JSON file and applies
// defaults and validation.
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"time"
)

const (
	DefaultListenAddr = ":8080"

	// Default health-check settings, used when the JSON values are absent.
	DefaultHealthInterval = 5 * time.Second
	DefaultHealthTimeout  = time.Second

	// Default rate-limit settings: throttling starts disabled.
	DefaultRateBurst = 1
)

// Duration is time.Duration with human-friendly JSON parsing: accepts Go
// duration strings like "500ms", "5s", or "2m" (plain integers are treated as
// whole seconds for convenience).
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

// Config is the top-level proxy configuration.
type Config struct {
	ListenAddr string         `json:"listen_addr"`
	Backends   []string       `json:"backends"`
	Weights    []int          `json:"backend_weights,omitempty"` // optional, aligned with backends
	Health     HealthSettings `json:"health,omitempty"`
	RateLimit  RateSettings   `json:"rate_limiter,omitempty"`
}

// HealthSettings controls the periodic backend probe.
type HealthSettings struct {
	Interval *Duration `json:"interval,omitempty"` // sweep cadence
	Timeout  *Duration `json:"timeout,omitempty"`  // per-probe request timeout
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
	Rate  *float64 `json:"rate,omitempty"`  // tokens per second, 0 disables
	Burst *int     `json:"burst,omitempty"` // bucket ceiling, needed when rate > 0
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

// Load reads the JSON config file at path and validates it.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &cfg, nil
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
	return nil
}
