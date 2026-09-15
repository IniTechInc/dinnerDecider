package config

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
)

// envFrom builds a lookup over a fixed map. Every test uses one, including the
// empty map, so that the real process environment can never leak into a result
// and make a run depend on the machine it ran on.
func envFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// writeConfig writes body to a temp file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// load runs Load with the file layer pointed at body (or suppressed when body
// is empty), a fixed environment, and the given arguments.
func load(t *testing.T, body string, env map[string]string, args []string) (Config, []string, error) {
	t.Helper()
	if args == nil {
		// A nil Args means "read os.Args" in production. A test must never do
		// that: the test binary's own -test.* flags would land in the FlagSet.
		args = []string{}
	}
	src := Source{
		Env:    envFrom(env),
		Args:   args,
		Output: io.Discard,
	}
	if body == "" {
		src.SkipFile = true
	} else {
		src.ConfigPath = writeConfig(t, body)
	}
	return Load(src)
}

func mustLoad(t *testing.T, body string, env map[string]string, args []string) Config {
	t.Helper()
	cfg, _, err := load(t, body, env, args)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	return cfg
}

func TestDefaultsMatchThePlan(t *testing.T) {
	d := Defaults()

	if d.Bind != "127.0.0.1" {
		t.Errorf("bind = %q, want loopback 127.0.0.1 — reaching the LAN must be a decision, not an accident", d.Bind)
	}
	if d.BindIsLoopback() != true {
		t.Error("the default bind must be loopback")
	}

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"port", d.Port, 8080},
		{"tlsPort", d.TLSPort, 8443},
		{"tls", d.TLS, TLSSelfSigned},
		{"auth", d.Auth, AuthToken},
		{"stateDir", d.StateDir, "/var/lib/dinnerdecider"},
		{"modelsDir", d.ModelsDir, ""},
		{"llamaServerPath", d.LlamaServerPath, ""},
		{"scanCallBudget", d.ScanCallBudget, 4},
		{"contextSize", d.ContextSize, 1024},
		{"batchSize", d.BatchSize, 512},
		{"parallelSlots", d.ParallelSlots, 1},
		{"idleUnloadMinutes", d.IdleUnloadMinutes, 15},
		{"inferQueueTimeout", d.InferQueueTimeout, 10 * time.Minute},
		{"ocr", d.OCR, OCRAuto},
		{"metered", d.Metered, false},
		{"photoRetentionHours", d.PhotoRetentionHours, 24},
		{"maxUploadBytes", d.MaxUploadBytes, int64(48 << 20)},
		{"maxUploadPixels", d.MaxUploadPixels, int64(40_000_000)},
		{"maxConcurrentScans", d.MaxConcurrentScans, 2},
		{"maxSSEPerScan", d.MaxSSEPerScan, 16},
		{"maxSSETotal", d.MaxSSETotal, 64},
		{"acceptLANExposure", d.AcceptLANExposure, false},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("default %s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}

	if d.ScanCallBudget != contract.DefaultScanCallLimit {
		t.Errorf("scanCallBudget = %d, want contract.DefaultScanCallLimit = %d; the budget belongs to the contract, not to config",
			d.ScanCallBudget, contract.DefaultScanCallLimit)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("the built-in defaults must validate: %v", err)
	}
}

// TestPrecedence walks every layer boundary. Each row names which layers speak
// and what the winner must be.
func TestPrecedence(t *testing.T) {
	tests := []struct {
		name   string
		file   string
		env    map[string]string
		args   []string
		get    func(Config) any
		expect any
	}{
		// ---- defaults only -------------------------------------------------
		{
			name:   "defaults only: bind",
			get:    func(c Config) any { return c.Bind },
			expect: "127.0.0.1",
		},
		{
			name:   "defaults only: scanCallBudget",
			get:    func(c Config) any { return c.ScanCallBudget },
			expect: 4,
		},

		// ---- file over defaults --------------------------------------------
		{
			name:   "file over default: bind",
			file:   `{"bind":"0.0.0.0"}`,
			get:    func(c Config) any { return c.Bind },
			expect: "0.0.0.0",
		},
		{
			name:   "file over default: int",
			file:   `{"port":9000}`,
			get:    func(c Config) any { return c.Port },
			expect: 9000,
		},
		{
			name:   "file over default: bool",
			file:   `{"metered":true}`,
			get:    func(c Config) any { return c.Metered },
			expect: true,
		},
		{
			name:   "file over default: duration string",
			file:   `{"inferQueueTimeout":"90s"}`,
			get:    func(c Config) any { return c.InferQueueTimeout },
			expect: 90 * time.Second,
		},
		{
			name:   "file over default: enum",
			file:   `{"tls":"off"}`,
			get:    func(c Config) any { return c.TLS },
			expect: TLSOff,
		},
		{
			// The pointer-field mechanism: a file key whose value happens to be
			// the Go zero value must still win. Without it, "idleUnloadMinutes":0
			// (disable idle unload) is indistinguishable from an absent key and
			// the 15-minute default silently re-asserts itself.
			name:   "file zero value beats a non-zero default",
			file:   `{"idleUnloadMinutes":0}`,
			get:    func(c Config) any { return c.IdleUnloadMinutes },
			expect: 0,
		},
		{
			name:   "file key absent leaves the default alone",
			file:   `{"port":9000}`,
			get:    func(c Config) any { return c.IdleUnloadMinutes },
			expect: 15,
		},

		// ---- env over defaults ---------------------------------------------
		{
			name:   "env over default: string",
			env:    map[string]string{"DD_STATE_DIR": "/srv/dd"},
			get:    func(c Config) any { return c.StateDir },
			expect: "/srv/dd",
		},
		{
			name:   "env over default: int",
			env:    map[string]string{"DD_MAX_SSE_TOTAL": "128"},
			get:    func(c Config) any { return c.MaxSSETotal },
			expect: 128,
		},
		{
			name:   "env over default: int64",
			env:    map[string]string{"DD_MAX_UPLOAD_BYTES": "1048576"},
			get:    func(c Config) any { return c.MaxUploadBytes },
			expect: int64(1048576),
		},
		{
			name:   "env over default: bool",
			env:    map[string]string{"DD_METERED": "true"},
			get:    func(c Config) any { return c.Metered },
			expect: true,
		},
		{
			name:   "env over default: duration",
			env:    map[string]string{"DD_INFER_QUEUE_TIMEOUT": "2m30s"},
			get:    func(c Config) any { return c.InferQueueTimeout },
			expect: 150 * time.Second,
		},

		// ---- env over file -------------------------------------------------
		{
			name:   "env beats file: string",
			file:   `{"bind":"10.0.0.5"}`,
			env:    map[string]string{"DD_BIND": "192.168.1.9"},
			get:    func(c Config) any { return c.Bind },
			expect: "192.168.1.9",
		},
		{
			name:   "env beats file: int",
			file:   `{"port":9000}`,
			env:    map[string]string{"DD_PORT": "9100"},
			get:    func(c Config) any { return c.Port },
			expect: 9100,
		},
		{
			name:   "env beats file: enum",
			file:   `{"auth":"none"}`,
			env:    map[string]string{"DD_AUTH": "token"},
			get:    func(c Config) any { return c.Auth },
			expect: AuthToken,
		},
		{
			name:   "env beats file: duration",
			file:   `{"inferQueueTimeout":"90s"}`,
			env:    map[string]string{"DD_INFER_QUEUE_TIMEOUT": "45s"},
			get:    func(c Config) any { return c.InferQueueTimeout },
			expect: 45 * time.Second,
		},
		{
			name:   "file still wins for keys env does not mention",
			file:   `{"port":9000,"batchSize":1024}`,
			env:    map[string]string{"DD_PORT": "9100"},
			get:    func(c Config) any { return c.BatchSize },
			expect: 1024,
		},

		// ---- flag over env -------------------------------------------------
		{
			name:   "flag beats env: string",
			env:    map[string]string{"DD_BIND": "192.168.1.9"},
			args:   []string{"-bind", "127.0.0.1"},
			get:    func(c Config) any { return c.Bind },
			expect: "127.0.0.1",
		},
		{
			name:   "flag beats env: int",
			env:    map[string]string{"DD_PORT": "9100"},
			args:   []string{"-port=9200"},
			get:    func(c Config) any { return c.Port },
			expect: 9200,
		},
		{
			name:   "flag beats env: bool",
			env:    map[string]string{"DD_METERED": "true"},
			args:   []string{"-metered=false"},
			get:    func(c Config) any { return c.Metered },
			expect: false,
		},
		{
			name:   "flag beats env: duration",
			env:    map[string]string{"DD_INFER_QUEUE_TIMEOUT": "45s"},
			args:   []string{"-infer-queue-timeout=5s"},
			get:    func(c Config) any { return c.InferQueueTimeout },
			expect: 5 * time.Second,
		},
		{
			name:   "flag beats env: enum",
			env:    map[string]string{"DD_OCR": "on"},
			args:   []string{"-ocr=off"},
			get:    func(c Config) any { return c.OCR },
			expect: OCROff,
		},
		{
			name:   "env still wins for keys no flag mentions",
			env:    map[string]string{"DD_PORT": "9100", "DD_TLS_PORT": "9101"},
			args:   []string{"-port=9200"},
			get:    func(c Config) any { return c.TLSPort },
			expect: 9101,
		},
		{
			name:   "double-dash flag form",
			env:    map[string]string{"DD_PORT": "9100"},
			args:   []string{"--port=9200"},
			get:    func(c Config) any { return c.Port },
			expect: 9200,
		},

		// ---- all four layers at once ---------------------------------------
		{
			name:   "all four layers, flag wins",
			file:   `{"port":9000}`,
			env:    map[string]string{"DD_PORT": "9100"},
			args:   []string{"-port=9200"},
			get:    func(c Config) any { return c.Port },
			expect: 9200,
		},
		{
			name:   "all four layers, env wins where no flag speaks",
			file:   `{"port":9000,"tlsPort":9001}`,
			env:    map[string]string{"DD_TLS_PORT": "9101"},
			args:   []string{"-port=9200"},
			get:    func(c Config) any { return c.TLSPort },
			expect: 9101,
		},
		{
			name:   "all four layers, file wins where nothing above speaks",
			file:   `{"port":9000,"maxSSEPerScan":8}`,
			env:    map[string]string{"DD_PORT": "9100"},
			args:   []string{"-port=9200"},
			get:    func(c Config) any { return c.MaxSSEPerScan },
			expect: 8,
		},
		{
			name:   "all four layers, default wins where nothing speaks",
			file:   `{"port":9000}`,
			env:    map[string]string{"DD_PORT": "9100"},
			args:   []string{"-port=9200"},
			get:    func(c Config) any { return c.MaxSSETotal },
			expect: 64,
		},

		// ---- the LAN-exposure acknowledgement ------------------------------
		{
			name:   "acknowledgement from flag",
			args:   []string{"-i-accept-lan-exposure"},
			get:    func(c Config) any { return c.AcceptLANExposure },
			expect: true,
		},
		{
			name:   "acknowledgement from env, for compose and systemd units",
			env:    map[string]string{"DD_I_ACCEPT_LAN_EXPOSURE": "1"},
			get:    func(c Config) any { return c.AcceptLANExposure },
			expect: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := mustLoad(t, tc.file, tc.env, tc.args)
			if got := tc.get(cfg); got != tc.expect {
				t.Errorf("got %v (%T), want %v (%T)", got, got, tc.expect, tc.expect)
			}
		})
	}
}

func TestEveryKeyIsSettableFromEveryLayer(t *testing.T) {
	file := `{
		"bind":"10.1.1.1","port":1001,"tlsPort":1002,"tls":"files",
		"tlsCertFile":"/c.pem","tlsKeyFile":"/k.pem","auth":"none",
		"stateDir":"/s","modelsDir":"/m","llamaServerPath":"/bin/ls",
		"scanCallBudget":8,"contextSize":2048,"batchSize":1024,"parallelSlots":2,
		"idleUnloadMinutes":5,"inferQueueTimeout":"1m","ocr":"off","metered":true,
		"photoRetentionHours":1,"maxUploadBytes":11,"maxUploadPixels":12,
		"maxConcurrentScans":3,"maxSSEPerScan":4,"maxSSETotal":5
	}`
	cfg := mustLoad(t, file, nil, nil)

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"bind", cfg.Bind, "10.1.1.1"},
		{"port", cfg.Port, 1001},
		{"tlsPort", cfg.TLSPort, 1002},
		{"tls", cfg.TLS, TLSFiles},
		{"tlsCertFile", cfg.TLSCertFile, "/c.pem"},
		{"tlsKeyFile", cfg.TLSKeyFile, "/k.pem"},
		{"auth", cfg.Auth, AuthNone},
		{"stateDir", cfg.StateDir, "/s"},
		{"modelsDir", cfg.ModelsDir, "/m"},
		{"llamaServerPath", cfg.LlamaServerPath, "/bin/ls"},
		{"scanCallBudget", cfg.ScanCallBudget, 8},
		{"contextSize", cfg.ContextSize, 2048},
		{"batchSize", cfg.BatchSize, 1024},
		{"parallelSlots", cfg.ParallelSlots, 2},
		{"idleUnloadMinutes", cfg.IdleUnloadMinutes, 5},
		{"inferQueueTimeout", cfg.InferQueueTimeout, time.Minute},
		{"ocr", cfg.OCR, OCROff},
		{"metered", cfg.Metered, true},
		{"photoRetentionHours", cfg.PhotoRetentionHours, 1},
		{"maxUploadBytes", cfg.MaxUploadBytes, int64(11)},
		{"maxUploadPixels", cfg.MaxUploadPixels, int64(12)},
		{"maxConcurrentScans", cfg.MaxConcurrentScans, 3},
		{"maxSSEPerScan", cfg.MaxSSEPerScan, 4},
		{"maxSSETotal", cfg.MaxSSETotal, 5},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("file key %s = %v, want %v", c.name, c.got, c.want)
		}
	}

	env := map[string]string{
		"DD_BIND": "10.2.2.2", "DD_PORT": "2001", "DD_TLS_PORT": "2002",
		"DD_TLS": "off", "DD_TLS_CERT_FILE": "/c2", "DD_TLS_KEY_FILE": "/k2",
		"DD_AUTH": "token", "DD_STATE_DIR": "/s2", "DD_MODELS_DIR": "/m2",
		"DD_LLAMA_SERVER_PATH": "/bin/ls2", "DD_SCAN_CALL_BUDGET": "6",
		"DD_CONTEXT_SIZE": "1536", "DD_BATCH_SIZE": "768", "DD_PARALLEL_SLOTS": "3",
		"DD_IDLE_UNLOAD_MINUTES": "7", "DD_INFER_QUEUE_TIMEOUT": "2m", "DD_OCR": "on",
		"DD_METERED": "false", "DD_PHOTO_RETENTION_HOURS": "48",
		"DD_MAX_UPLOAD_BYTES": "21", "DD_MAX_UPLOAD_PIXELS": "22",
		"DD_MAX_CONCURRENT_SCANS": "9", "DD_MAX_SSE_PER_SCAN": "10", "DD_MAX_SSE_TOTAL": "11",
	}
	e := mustLoad(t, file, env, nil)
	envChecks := []struct {
		name string
		got  any
		want any
	}{
		{"bind", e.Bind, "10.2.2.2"},
		{"port", e.Port, 2001},
		{"tlsPort", e.TLSPort, 2002},
		{"tls", e.TLS, TLSOff},
		{"tlsCertFile", e.TLSCertFile, "/c2"},
		{"tlsKeyFile", e.TLSKeyFile, "/k2"},
		{"auth", e.Auth, AuthToken},
		{"stateDir", e.StateDir, "/s2"},
		{"modelsDir", e.ModelsDir, "/m2"},
		{"llamaServerPath", e.LlamaServerPath, "/bin/ls2"},
		{"scanCallBudget", e.ScanCallBudget, 6},
		{"contextSize", e.ContextSize, 1536},
		{"batchSize", e.BatchSize, 768},
		{"parallelSlots", e.ParallelSlots, 3},
		{"idleUnloadMinutes", e.IdleUnloadMinutes, 7},
		{"inferQueueTimeout", e.InferQueueTimeout, 2 * time.Minute},
		{"ocr", e.OCR, OCROn},
		{"metered", e.Metered, false},
		{"photoRetentionHours", e.PhotoRetentionHours, 48},
		{"maxUploadBytes", e.MaxUploadBytes, int64(21)},
		{"maxUploadPixels", e.MaxUploadPixels, int64(22)},
		{"maxConcurrentScans", e.MaxConcurrentScans, 9},
		{"maxSSEPerScan", e.MaxSSEPerScan, 10},
		{"maxSSETotal", e.MaxSSETotal, 11},
	}
	for _, c := range envChecks {
		if c.got != c.want {
			t.Errorf("env key %s = %v, want %v (env must beat the file)", c.name, c.got, c.want)
		}
	}

	args := []string{
		"-bind=10.3.3.3", "-port=3001", "-tls-port=3002", "-tls=selfsigned",
		"-tls-cert-file=/c3", "-tls-key-file=/k3", "-auth=none", "-state-dir=/s3",
		"-models-dir=/m3", "-llama-server-path=/bin/ls3", "-scan-call-budget=5",
		"-context-size=1024", "-batch-size=512", "-parallel-slots=1",
		"-idle-unload-minutes=9", "-infer-queue-timeout=3m", "-ocr=auto",
		"-metered=true", "-photo-retention-hours=72", "-max-upload-bytes=31",
		"-max-upload-pixels=32", "-max-concurrent-scans=4", "-max-sse-per-scan=5",
		"-max-sse-total=6",
	}
	f := mustLoad(t, file, env, args)
	flagChecks := []struct {
		name string
		got  any
		want any
	}{
		{"bind", f.Bind, "10.3.3.3"},
		{"port", f.Port, 3001},
		{"tlsPort", f.TLSPort, 3002},
		{"tls", f.TLS, TLSSelfSigned},
		{"tlsCertFile", f.TLSCertFile, "/c3"},
		{"tlsKeyFile", f.TLSKeyFile, "/k3"},
		{"auth", f.Auth, AuthNone},
		{"stateDir", f.StateDir, "/s3"},
		{"modelsDir", f.ModelsDir, "/m3"},
		{"llamaServerPath", f.LlamaServerPath, "/bin/ls3"},
		{"scanCallBudget", f.ScanCallBudget, 5},
		{"contextSize", f.ContextSize, 1024},
		{"batchSize", f.BatchSize, 512},
		{"parallelSlots", f.ParallelSlots, 1},
		{"idleUnloadMinutes", f.IdleUnloadMinutes, 9},
		{"inferQueueTimeout", f.InferQueueTimeout, 3 * time.Minute},
		{"ocr", f.OCR, OCRAuto},
		{"metered", f.Metered, true},
		{"photoRetentionHours", f.PhotoRetentionHours, 72},
		{"maxUploadBytes", f.MaxUploadBytes, int64(31)},
		{"maxUploadPixels", f.MaxUploadPixels, int64(32)},
		{"maxConcurrentScans", f.MaxConcurrentScans, 4},
		{"maxSSEPerScan", f.MaxSSEPerScan, 5},
		{"maxSSETotal", f.MaxSSETotal, 6},
	}
	for _, c := range flagChecks {
		if c.got != c.want {
			t.Errorf("flag %s = %v, want %v (a flag must beat env)", c.name, c.got, c.want)
		}
	}
}

func TestFileLayerErrors(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantSub string
	}{
		{"unknown key", `{"bnid":"127.0.0.1"}`, "bnid"},
		{"malformed json", `{"port":`, "unexpected EOF"},
		{"trailing data", `{"port":9000} and then some`, "trailing data"},
		{"wrong type", `{"port":"9000"}`, "cannot unmarshal"},
		{"bad duration", `{"inferQueueTimeout":"ten minutes"}`, "inferQueueTimeout"},
		{
			// The acknowledgement is flag/env only. A config file copied off a
			// forum post must not be able to assert it.
			"acknowledgement is not a file key",
			`{"iAcceptLanExposure":true}`,
			"iAcceptLanExposure",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := load(t, tc.body, nil, nil)
			if err == nil {
				t.Fatalf("expected an error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestMissingDefaultConfigFileIsNotAnError(t *testing.T) {
	// The built-in path will not exist on a development machine or in a package
	// that ships no config file. That is the normal case, not a failure.
	cfg, _, err := Load(Source{
		ConfigPath: "", // resolve to DefaultConfigPath
		Env:        envFrom(nil),
		Args:       []string{},
		Output:     io.Discard,
	})
	if err != nil {
		t.Fatalf("an absent %s must not fail startup: %v", DefaultConfigPath, err)
	}
	if cfg.ConfigPath != DefaultConfigPath {
		t.Errorf("ConfigPath = %q, want %q", cfg.ConfigPath, DefaultConfigPath)
	}
}

func TestMissingExplicitConfigFileIsAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.json")
	for _, tc := range []struct {
		name string
		src  Source
	}{
		{"flag", Source{Env: envFrom(nil), Args: []string{"-config=" + missing}, Output: io.Discard}},
		{"flag separate value", Source{Env: envFrom(nil), Args: []string{"-config", missing}, Output: io.Discard}},
		{"env", Source{Env: envFrom(map[string]string{"DD_CONFIG": missing}), Args: []string{}, Output: io.Discard}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := Load(tc.src); err == nil {
				t.Fatal("asking for a config file that does not exist must fail loudly")
			}
		})
	}
}

func TestConfigPathFlagBeatsEnv(t *testing.T) {
	fromFlag := writeConfig(t, `{"port":4444}`)
	fromEnv := writeConfig(t, `{"port":5555}`)
	cfg, _, err := Load(Source{
		Env:    envFrom(map[string]string{"DD_CONFIG": fromEnv}),
		Args:   []string{"-config=" + fromFlag},
		Output: io.Discard,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 4444 {
		t.Errorf("port = %d, want 4444 from the -config flag", cfg.Port)
	}
	if cfg.ConfigPath != fromFlag {
		t.Errorf("ConfigPath = %q, want %q", cfg.ConfigPath, fromFlag)
	}
}

func TestEnvLayerErrors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"non-integer int", map[string]string{"DD_PORT": "eighty"}},
		{"non-integer int64", map[string]string{"DD_MAX_UPLOAD_BYTES": "lots"}},
		{"non-boolean", map[string]string{"DD_METERED": "yes please"}},
		{"bad duration", map[string]string{"DD_INFER_QUEUE_TIMEOUT": "forever"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := load(t, "", tc.env, nil)
			if err == nil {
				t.Fatal("a malformed environment value must fail at startup, not silently fall back")
			}
			for k := range tc.env {
				if !strings.Contains(err.Error(), k) {
					t.Errorf("error %q does not name the variable %s", err, k)
				}
			}
		})
	}
}

func TestPositionalArgumentsSurviveFlagParsing(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
		port int
	}{
		{"subcommand first", []string{"token", "show", "-port=7000"}, []string{"token", "show"}, 7000},
		{"flags first", []string{"-port=7000", "token", "show"}, []string{"token", "show"}, 7000},
		{"subcommand only", []string{"token", "show"}, []string{"token", "show"}, 8080},
		{"no arguments", nil, nil, 8080},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, args, err := load(t, "", nil, tc.args)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Port != tc.port {
				t.Errorf("port = %d, want %d", cfg.Port, tc.port)
			}
			if len(args) != len(tc.want) {
				t.Fatalf("positional args = %v, want %v", args, tc.want)
			}
			for i := range args {
				if args[i] != tc.want[i] {
					t.Fatalf("positional args = %v, want %v", args, tc.want)
				}
			}
		})
	}
}

func TestHelpIsARequestNotAFailure(t *testing.T) {
	_, _, err := load(t, "", nil, []string{"-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("err = %v, want flag.ErrHelp so a caller can exit 0", err)
	}
}

func TestUnknownFlagFails(t *testing.T) {
	if _, _, err := load(t, "", nil, []string{"-nonsuch=1"}); err == nil {
		t.Fatal("an unknown flag must fail")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{"valid defaults", func(*Config) {}, ""},
		{"bad tls mode", func(c *Config) { c.TLS = "maybe" }, "tls"},
		{"bad auth mode", func(c *Config) { c.Auth = "password" }, "auth"},
		{"bad ocr mode", func(c *Config) { c.OCR = "sometimes" }, "ocr"},
		{"tls files without cert", func(c *Config) { c.TLS = TLSFiles; c.TLSKeyFile = "/k" }, "tlsCertFile"},
		{"tls files without key", func(c *Config) { c.TLS = TLSFiles; c.TLSCertFile = "/c" }, "tlsKeyFile"},
		{"tls files with both", func(c *Config) { c.TLS = TLSFiles; c.TLSCertFile = "/c"; c.TLSKeyFile = "/k" }, ""},
		{"port too high", func(c *Config) { c.Port = 70000 }, "port"},
		{"port negative", func(c *Config) { c.Port = -1 }, "port"},
		{"tlsPort too high", func(c *Config) { c.TLSPort = 99999 }, "tlsPort"},
		{"same port twice", func(c *Config) { c.TLSPort = c.Port }, "cannot bind the same port twice"},
		{"same port is fine with tls off", func(c *Config) { c.TLS = TLSOff; c.TLSPort = c.Port }, ""},
		{"empty stateDir", func(c *Config) { c.StateDir = "" }, "stateDir"},
		{"zero scanCallBudget", func(c *Config) { c.ScanCallBudget = 0 }, "scanCallBudget"},
		{"zero contextSize", func(c *Config) { c.ContextSize = 0 }, "contextSize"},
		{"zero batchSize", func(c *Config) { c.BatchSize = 0 }, "batchSize"},
		{"zero parallelSlots", func(c *Config) { c.ParallelSlots = 0 }, "parallelSlots"},
		{"zero maxConcurrentScans", func(c *Config) { c.MaxConcurrentScans = 0 }, "maxConcurrentScans"},
		{"zero maxSSEPerScan", func(c *Config) { c.MaxSSEPerScan = 0 }, "maxSSEPerScan"},
		{"zero maxSSETotal", func(c *Config) { c.MaxSSETotal = 0 }, "maxSSETotal"},
		{"negative idleUnloadMinutes", func(c *Config) { c.IdleUnloadMinutes = -1 }, "idleUnloadMinutes"},
		{"zero idleUnloadMinutes disables", func(c *Config) { c.IdleUnloadMinutes = 0 }, ""},
		{"negative photoRetentionHours", func(c *Config) { c.PhotoRetentionHours = -1 }, "photoRetentionHours"},
		{"zero photoRetentionHours sweeps immediately", func(c *Config) { c.PhotoRetentionHours = 0 }, ""},
		{"zero inferQueueTimeout", func(c *Config) { c.InferQueueTimeout = 0 }, "inferQueueTimeout"},
		{"zero maxUploadBytes", func(c *Config) { c.MaxUploadBytes = 0 }, "maxUploadBytes"},
		{"zero maxUploadPixels", func(c *Config) { c.MaxUploadPixels = 0 }, "maxUploadPixels"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Defaults()
			tc.mutate(&c)
			err := c.Validate()
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestLoadRejectsInvalidResult(t *testing.T) {
	if _, _, err := load(t, "", map[string]string{"DD_AUTH": "password"}, nil); err == nil {
		t.Fatal("Load must validate before returning")
	}
}

func TestIsLoopbackBind(t *testing.T) {
	tests := []struct {
		bind string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.4.5.6", true},
		{"::1", true},
		{"[::1]", true},
		{"localhost", true},
		{"LOCALHOST", true},
		{" 127.0.0.1 ", true},
		{"0.0.0.0", false},
		{"::", false},
		{"[::]", false},
		{"192.168.1.10", false},
		{"10.0.0.1", false},
		{"fe80::1%en0", false},
		{"dinnerpi.local", false},
		// The empty bind means every interface. Fail closed: the only caller
		// that matters is the guard refusing an unauthenticated LAN bind.
		{"", false},
		{"   ", false},
		{"not an address", false},
	}
	for _, tc := range tests {
		t.Run("bind="+tc.bind, func(t *testing.T) {
			if got := IsLoopbackBind(tc.bind); got != tc.want {
				t.Errorf("IsLoopbackBind(%q) = %v, want %v", tc.bind, got, tc.want)
			}
		})
	}
}

func TestDerivedPaths(t *testing.T) {
	c := Defaults()
	c.StateDir = "/srv/dd"
	if got := c.TLSDir(); got != "/srv/dd/tls" {
		t.Errorf("TLSDir = %q", got)
	}
	if got := c.AuthDir(); got != "/srv/dd/auth" {
		t.Errorf("AuthDir = %q", got)
	}
	if got := c.PhotosDir(); got != "/srv/dd/photos" {
		t.Errorf("PhotosDir = %q", got)
	}
}

func TestTLSEnabled(t *testing.T) {
	tests := []struct {
		mode TLSMode
		want bool
	}{
		{TLSOff, false},
		{TLSSelfSigned, true},
		{TLSFiles, true},
	}
	for _, tc := range tests {
		c := Defaults()
		c.TLS = tc.mode
		if got := c.TLSEnabled(); got != tc.want {
			t.Errorf("TLSEnabled(%s) = %v, want %v", tc.mode, got, tc.want)
		}
	}
}
