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
