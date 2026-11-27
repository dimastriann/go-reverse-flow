package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// TestApplyEnvOverrideListenAddr pins the LISTEN_ADDR precedence contract:
// the environment beats the file's listen_addr only when set non-empty, and
// the file value (explicit or default) stands otherwise. The lookup key is
// asserted too so the variable name can't drift.
func TestApplyEnvOverrideListenAddr(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		env      string
		wantAddr string
	}{
		{
			name:     "env wins over file value",
			json:     `{"listen_addr":":9090","backends":["http://x:1"]}`,
			env:      ":7777",
			wantAddr: ":7777",
		},
		{
			name:     "env wins over default value",
			json:     `{"backends":["http://x:1"]}`,
			env:      ":7777",
			wantAddr: ":7777",
		},
		{
			name:     "empty env falls back to file value",
			json:     `{"listen_addr":":9090","backends":["http://x:1"]}`,
			env:      "",
			wantAddr: ":9090",
		},
		{
			name:     "empty env falls back to default value",
			json:     `{"backends":["http://x:1"]}`,
			env:      "",
			wantAddr: DefaultListenAddr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeFixture(t, tt.json))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			var queriedKeys []string
			lookup := func(key string) string {
				queriedKeys = append(queriedKeys, key)
				if key == EnvListenAddr {
					return tt.env
				}
				return ""
			}

			cfg.ApplyEnvOverride(lookup)
			if len(queriedKeys) < 1 || queriedKeys[0] != EnvListenAddr {
				t.Errorf("lookup queried %v, want %q first", queriedKeys, EnvListenAddr)
			}
			if cfg.ListenAddr != tt.wantAddr {
				t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, tt.wantAddr)
			}
		})
	}
}

// TestAdminSettingsResolution pins the admin knob semantics: loopback listen
// and "admin" user by default, password as the on-switch, env-friendly shape.
func TestAdminSettingsResolution(t *testing.T) {
	tests := []struct {
		name        string
		json        string
		wantListen  string
		wantUser    string
		wantPass    string
		wantEnabled bool
	}{
		{
			name:       "absent disables admin",
			json:       `{"backends":["http://x:1"]}`,
			wantListen: DefaultAdminListen,
			wantUser:   DefaultAdminUser,
		},
		{
			name:        "full config enables",
			json:        `{"backends":["http://x:1"],"admin":{"listen":"0.0.0.0:8090","username":"ops","password":"s3cret"}}`,
			wantListen:  "0.0.0.0:8090",
			wantUser:    "ops",
			wantPass:    "s3cret",
			wantEnabled: true,
		},
		{
			name:       "listen without password stays disabled",
			json:       `{"backends":["http://x:1"],"admin":{"listen":"0.0.0.0:8090"}}`,
			wantListen: "0.0.0.0:8090",
			wantUser:   DefaultAdminUser,
		},
		{
			name:        "password without user defaults the user",
			json:        `{"backends":["http://x:1"],"admin":{"password":"pw"}}`,
			wantListen:  DefaultAdminListen,
			wantUser:    DefaultAdminUser,
			wantPass:    "pw",
			wantEnabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeFixture(t, tt.json))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			listen, user, pass, enabled := cfg.Admin.Values()
			if listen != tt.wantListen || user != tt.wantUser || pass != tt.wantPass || enabled != tt.wantEnabled {
				t.Errorf("Values() = (%q, %q, %q, %v), want (%q, %q, %q, %v)",
					listen, user, pass, enabled, tt.wantListen, tt.wantUser, tt.wantPass, tt.wantEnabled)
			}
		})
	}
}

// TestApplyEnvOverrideAdminCredentials pins the admin env precedence.
func TestApplyEnvOverrideAdminCredentials(t *testing.T) {
	cfg, err := Load(writeFixture(t, `{
		"backends":["http://x:1"],
		"admin":{"listen":"127.0.0.1:8090","username":"file-user","password":"file-pass"}
	}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	lookup := func(key string) string {
		switch key {
		case EnvAdminUser:
			return "env-user"
		case EnvAdminPassword:
			return "env-pass"
		}
		return ""
	}
	cfg.ApplyEnvOverride(lookup)

	if cfg.Admin.Username != "env-user" || cfg.Admin.Password != "env-pass" {
		t.Errorf("admin credentials = %q/%q, want env-user/env-pass", cfg.Admin.Username, cfg.Admin.Password)
	}
	listen, _, _, enabled := cfg.Admin.Values()
	if !enabled || listen != "127.0.0.1:8090" {
		t.Errorf("admin listen/enabled = %q/%v, want 127.0.0.1:8090/true", listen, enabled)
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

// TestParseBytes covers the admin-API entry point: valid JSON applies
// defaults, invalid JSON errors.
func TestParseBytes(t *testing.T) {
	cfg, err := Parse([]byte(`{"backends":["http://a:1"],"health":{"interval":"700ms"}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.ListenAddr != DefaultListenAddr {
		t.Errorf("ListenAddr = %q, want default", cfg.ListenAddr)
	}
	interval, _ := cfg.Health.Values()
	if interval != 700*time.Millisecond {
		t.Errorf("interval = %v, want 700ms", interval)
	}

	if _, err := Parse([]byte(`{"backends": []}`)); err == nil {
		t.Error("Parse(empty backends) = nil error, want validation error")
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Error("Parse(not json) = nil error, want error")
	}
}

// TestDurationMarshalForms pins the human-readable rendering: JSON gets a
// string ("45s"), not nanoseconds.
func TestDurationMarshalForms(t *testing.T) {
	cfg, err := Load(writeFixture(t, `{
		"backends":["http://a:1"],
		"cache":{"ttl":"45s"},
		"health":{"interval":"500ms"}
	}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	jsonOut, err := cfg.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !strings.Contains(string(jsonOut), `"ttl": "45s"`) || !strings.Contains(string(jsonOut), `"interval": "500ms"`) {
		t.Errorf("JSON output missing string durations:\n%s", jsonOut)
	}

	// YAML marshal -> bridge back -> parse: must round-trip the same config.
	yamlOut, err := cfg.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	if !strings.Contains(string(yamlOut), "ttl: 45s") {
		t.Errorf("YAML output missing string ttl:\n%s", yamlOut)
	}
	bridged, err := yamlToJSON(yamlOut)
	if err != nil {
		t.Fatalf("yamlToJSON: %v", err)
	}
	parsed, err := Parse(bridged)
	if err != nil {
		t.Fatalf("Parse(bridged): %v", err)
	}
	if !reflect.DeepEqual(cfg, parsed) {
		t.Errorf("round-trip diverged:\norig:  %+v\nparsed: %+v", cfg, parsed)
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

// TestCacheSettingsResolution pins the ttl knob semantics.
func TestCacheSettingsResolution(t *testing.T) {
	tests := []struct {
		name string
		json string
		want time.Duration
	}{
		{name: "absent disables", json: `{"backends":["http://x:1"]}`},
		{name: "zero disables", json: `{"backends":["http://x:1"],"cache":{"ttl":"0s"}}`},
		{name: "string ttl", json: `{"backends":["http://x:1"],"cache":{"ttl":"45s"}}`, want: 45 * time.Second},
		{name: "number ttl seconds", json: `{"backends":["http://x:1"],"cache":{"ttl":2}}`, want: 2 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeFixture(t, tt.json))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Cache.Value(); got != tt.want {
				t.Errorf("Cache.Value() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestValidateRoutes covers the route table rules and AllBackends union.
func TestValidateRoutes(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr bool
	}{
		{
			name: "absent routes, default only",
			json: `{"listen_addr":":1","backends":["http://a:1"]}`,
		},
		{
			name: "valid route",
			json: `{"listen_addr":":1","backends":["http://a:1"],"routes":[{"prefix":"/api","backends":["http://b:2"]}]}`,
		},
		{
			name:    "prefix without slash",
			json:    `{"listen_addr":":1","backends":["http://a:1"],"routes":[{"prefix":"api","backends":["http://b:2"]}]}`,
			wantErr: true,
		},
		{
			name:    "duplicate prefix",
			json:    `{"listen_addr":":1","backends":["http://a:1"],"routes":[{"prefix":"/api","backends":["http://b:2"]},{"prefix":"/api","backends":["http://c:3"]}]}`,
			wantErr: true,
		},
		{
			name:    "empty route backends",
			json:    `{"listen_addr":":1","backends":["http://a:1"],"routes":[{"prefix":"/api","backends":[]}]}`,
			wantErr: true,
		},
		{
			name:    "bad scheme in route backend",
			json:    `{"listen_addr":":1","backends":["http://a:1"],"routes":[{"prefix":"/api","backends":["ftp://b:2"]}]}`,
			wantErr: true,
		},
		{
			name:    "bad scheme in default backend",
			json:    `{"listen_addr":":1","backends":["ftp://a:1"],"routes":[{"prefix":"/api","backends":["http://b:2"]}]}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Load(writeFixture(t, tt.json)); (err != nil) != tt.wantErr {
				t.Errorf("Load error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

// TestAllBackendsUnion checks the health-check URL list includes route
// backends once each, in first-seen order.
func TestAllBackendsUnion(t *testing.T) {
	cfg, err := Load(writeFixture(t, `{
		"listen_addr":":1",
		"backends":["http://a:1","http://b:2"],
		"routes":[{"prefix":"/api","backends":["http://b:2","http://c:3"]}]
	}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"http://a:1", "http://b:2", "http://c:3"}
	if got := cfg.AllBackends(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("AllBackends() = %v, want %v", got, want)
	}
}

// writeFixturePath writes content to a named temp file so format-specific
// extensions can be exercised.
func writeFixturePath(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// TestLoadYAMLEquivalence proves both formats produce the identical Config
// for the same settings, including Duration parsing, weights, and routes.
func TestLoadYAMLEquivalence(t *testing.T) {
	jsonSrc := `{
		"listen_addr": ":9090",
		"backends": ["http://a:1", "http://b:2"],
		"backend_weights": [3, 1],
		"routes": [{"prefix": "/api", "backends": ["http://c:3"]}],
		"health": {"interval": "500ms", "timeout": "2s"},
		"rate_limiter": {"rate": 2.5, "burst": 10},
		"cache": {"ttl": "45s"}
	}`
	yamlSrc := `listen_addr: :9090
backends:
  - http://a:1
  - http://b:2
backend_weights: [3, 1]
routes:
  - prefix: /api
    backends:
      - http://c:3
health:
  interval: 500ms
  timeout: 2s
rate_limiter:
  rate: 2.5
  burst: 10
cache:
  ttl: 45s
`

	fromJSON, err := Load(writeFixturePath(t, "config.json", jsonSrc))
	if err != nil {
		t.Fatalf("Load json: %v", err)
	}
	fromYAML, err := Load(writeFixturePath(t, "config.yaml", yamlSrc))
	if err != nil {
		t.Fatalf("Load yaml: %v", err)
	}
	if !reflect.DeepEqual(fromJSON, fromYAML) {
		t.Errorf("configs diverge:\njson:  %+v\nyaml:  %+v", fromJSON, fromYAML)
	}
	if fromYAML.Cache.Value() != 45*time.Second {
		t.Errorf("yaml cache ttl = %v, want 45s", fromYAML.Cache.Value())
	}
}

// TestLoadYAMLExtensionVariants checks .yml and uppercase .YAML both route to
// the YAML decoder.
func TestLoadYAMLExtensionVariants(t *testing.T) {
	src := "backends:\n  - http://a:1\n"
	for _, name := range []string{"config.yml", "config.YAML", "CONFIG.Yml"} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(writeFixturePath(t, name, src))
			if err != nil {
				t.Fatalf("Load(%s): %v", name, err)
			}
			if cfg.ListenAddr != DefaultListenAddr {
				t.Errorf("ListenAddr = %q, want default", cfg.ListenAddr)
			}
		})
	}
}

// TestLoadInvalidYAML rejects structurally broken YAML with an error.
func TestLoadInvalidYAML(t *testing.T) {
	_, err := Load(writeFixturePath(t, "config.yaml", "listen_addr: [unclosed"))
	if err == nil {
		t.Fatal("Load on invalid YAML: got nil error, want error")
	}
}

// TestLoadBOMStripped pins the Windows-editor scenario: a UTF-8 BOM must not
// break either decoder.
func TestLoadBOMStripped(t *testing.T) {
	bom := "\xEF\xBB\xBF"
	bomJSON := bom + `{"backends":["http://a:1"]}`
	bomYAML := bom + "backends:\n  - http://a:1\n"

	if _, err := Load(writeFixturePath(t, "config.json", bomJSON)); err != nil {
		t.Errorf("Load json-with-BOM: %v", err)
	}
	if _, err := Load(writeFixturePath(t, "config.yaml", bomYAML)); err != nil {
		t.Errorf("Load yaml-with-BOM: %v", err)
	}
}

// TestLoadYAMLEnvOverride proves the env override applies identically on the
// YAML path.
func TestLoadYAMLEnvOverride(t *testing.T) {
	cfg, err := Load(writeFixturePath(t, "config.yaml", "listen_addr: :9090\nbackends:\n  - http://a:1\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.ApplyEnvOverride(func(string) string { return ":7070" })
	if cfg.ListenAddr != ":7070" {
		t.Errorf("ListenAddr = %q, want :7070", cfg.ListenAddr)
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
