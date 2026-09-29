// Package config loads the proxy configuration from a JSON file and applies
// defaults and validation.
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
)

const (
	DefaultListenAddr = ":8080"
)

// Config is the top-level proxy configuration.
type Config struct {
	ListenAddr string   `json:"listen_addr"`
	Backends   []string `json:"backends"`
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
