package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeFixture writes content to a temp file so tests can exercise Load.
func writeFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestLoadOKAndDefaults(t *testing.T) {
	path := writeFixture(t, `{"backends":["http://127.0.0.1:9001"]}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.ListenAddr != DefaultListenAddr {
		t.Errorf("ListenAddr = %q, want default %q", cfg.ListenAddr, DefaultListenAddr)
	}
	if len(cfg.Backends) != 1 || cfg.Backends[0] != "http://127.0.0.1:9001" {
		t.Errorf("Backends = %v, want [http://127.0.0.1:9001]", cfg.Backends)
	}
}

func TestLoadCustomListenAddr(t *testing.T) {
	path := writeFixture(t, `{"listen_addr":":9090","backends":["http://127.0.0.1:9001","http://127.0.0.1:9002"]}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.ListenAddr != ":9090" {
		t.Errorf("ListenAddr = %q, want :9090", cfg.ListenAddr)
	}
	if len(cfg.Backends) != 2 {
		t.Errorf("len(Backends) = %d, want 2", len(cfg.Backends))
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Fatal("Load on missing file: got nil error, want error")
	}
}

func TestLoadBadJSON(t *testing.T) {
	path := writeFixture(t, `{"backends": [`)

	if _, err := Load(path); err == nil {
		t.Fatal("Load on malformed JSON: got nil error, want error")
	}
}

func TestLoadHealthDefaults(t *testing.T) {
	path := writeFixture(t, `{"backends":["http://127.0.0.1:9001"]}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	interval, timeout := cfg.Health.Values()
	if interval != DefaultHealthInterval {
		t.Errorf("interval = %v, want default %v", interval, DefaultHealthInterval)
	}
	if timeout != DefaultHealthTimeout {
		t.Errorf("timeout = %v, want default %v", timeout, DefaultHealthTimeout)
	}
}

func TestLoadHealthSettingsVariants(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantInt time.Duration
		wantTmt time.Duration
		wantErr bool
	}{
		{
			name:    "string durations",
			json:    `{"backends":["http://x:1"],"health":{"interval":"200ms","timeout":"2s"}}`,
			wantInt: 200 * time.Millisecond,
			wantTmt: 2 * time.Second,
		},
		{
			name:    "numbers mean seconds",
			json:    `{"backends":["http://x:1"],"health":{"interval":1,"timeout":0.5}}`,
			wantInt: time.Second,
			wantTmt: 500 * time.Millisecond,
		},
		{
			name:    "empty object keeps defaults",
			json:    `{"backends":["http://x:1"],"health":{}}`,
			wantInt: DefaultHealthInterval,
			wantTmt: DefaultHealthTimeout,
		},
		{
			name:    "bad duration string",
			json:    `{"backends":["http://x:1"],"health":{"interval":"banana"}}`,
			wantErr: true,
		},
		{
			name:    "wrong type",
			json:    `{"backends":["http://x:1"],"health":{"timeout":[1,2]}}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFixture(t, tt.json)
			cfg, err := Load(path)
			if tt.wantErr {
				if err == nil {
					t.Fatal("Load = nil error, want parse error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			interval, timeout := cfg.Health.Values()
			if interval != tt.wantInt {
				t.Errorf("interval = %v, want %v", interval, tt.wantInt)
			}
			if timeout != tt.wantTmt {
				t.Errorf("timeout = %v, want %v", timeout, tt.wantTmt)
			}
		})
	}
}

// TestRateSettingsResolution pins the knob semantics.
func TestRateSettingsResolution(t *testing.T) {
	tests := []struct {
		name      string
		json      string
		wantRate  float64
		wantBurst int
	}{
		{name: "absent means disabled", json: `{"backends":["http://x:1"]}`},
		{name: "explicit zero disables", json: `{"backends":["http://x:1"],"rate_limiter":{"rate":0,"burst":50}}`},
		{name: "negative disables", json: `{"backends":["http://x:1"],"rate_limiter":{"rate":-5,"burst":50}}`},
		{name: "rate without burst defaults burst 1", json: `{"backends":["http://x:1"],"rate_limiter":{"rate":2.5}}`, wantRate: 2.5, wantBurst: 1},
		{name: "rate with burst honored", json: `{"backends":["http://x:1"],"rate_limiter":{"rate":2.5,"burst":100}}`, wantRate: 2.5, wantBurst: 100},
		{name: "burst without rate ignored", json: `{"backends":["http://x:1"],"rate_limiter":{"burst":100}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeFixture(t, tt.json))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			gotRate, gotBurst := cfg.RateLimit.Values()
			if gotRate != tt.wantRate || gotBurst != tt.wantBurst {
				t.Errorf("Values() = (%v, %v), want (%v, %v)", gotRate, gotBurst, tt.wantRate, tt.wantBurst)
			}
		})
	}
}

// TestValidateWeights covers the weights alignment and range rules.
func TestValidateWeights(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr bool
	}{
		{name: "absent weights, default RR", json: `{"listen_addr":":1","backends":["http://a:1","http://b:2"]}`},
		{name: "aligned weights", json: `{"listen_addr":":1","backends":["http://a:1","http://b:2"],"backend_weights":[3,1]}`},
		{name: "length mismatch", json: `{"listen_addr":":1","backends":["http://a:1","http://b:2"],"backend_weights":[3]}`, wantErr: true},
		{name: "zero weight", json: `{"listen_addr":":1","backends":["http://a:1"],"backend_weights":[0]}`, wantErr: true},
		{name: "negative weight", json: `{"listen_addr":":1","backends":["http://a:1","http://b:2"],"backend_weights":[2,-1]}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeFixture(t, tt.json))
			if (err != nil) != tt.wantErr {
				t.Errorf("Load error = %v, wantErr = %v", err, tt.wantErr)
			}
			if err == nil && tt.name == "aligned weights" {
				if len(cfg.Weights) != 2 || cfg.Weights[0] != 3 || cfg.Weights[1] != 1 {
					t.Errorf("Weights = %v, want [3,1]", cfg.Weights)
				}
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid http backend",
			cfg:  Config{ListenAddr: ":8080", Backends: []string{"http://localhost:8001"}},
		},
		{
			name: "valid https backend",
			cfg:  Config{ListenAddr: ":8080", Backends: []string{"https://example.com"}},
		},
		{
			name:    "empty listen_addr",
			cfg:     Config{ListenAddr: "", Backends: []string{"http://localhost:8001"}},
			wantErr: true,
		},
		{
			name:    "no backends",
			cfg:     Config{ListenAddr: ":8080"},
			wantErr: true,
		},
		{
			name:    "unsupported scheme",
			cfg:     Config{ListenAddr: ":8080", Backends: []string{"ftp://localhost:8001"}},
			wantErr: true,
		},
		{
			name:    "missing host",
			cfg:     Config{ListenAddr: ":8080", Backends: []string{"http://"}},
			wantErr: true,
		},
		{
			name:    "unparseable URL",
			cfg:     Config{ListenAddr: ":8080", Backends: []string{"http://exa mple.com"}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
