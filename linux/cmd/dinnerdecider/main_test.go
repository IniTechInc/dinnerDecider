package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/config"
	"github.com/IniTechInc/dinnerDecider/linux/internal/llm"
	"github.com/IniTechInc/dinnerDecider/linux/internal/model"
)

// testConfig is a service configuration that touches nothing outside t.TempDir:
// no TLS material, no token, no network, no model.
func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.StateDir = t.TempDir()
	cfg.TLS = config.TLSOff
	cfg.Auth = config.AuthNone
	cfg.OCR = config.OCROff
	cfg.Port = 0
	cfg.ConfigPath = ""
	return cfg
}

func quietLogf(*testing.T) func(string, ...any) {
	return func(string, ...any) {}
}

// --- the CLI surface ------------------------------------------------------

func TestHelpExitsZero(t *testing.T) {
	var out, errOut bytes.Buffer
	// -h is a request that was satisfied. Reporting it as a failure makes
	// `dinnerdecider -h` a non-zero exit in every wrapper script.
	if code := run([]string{"-h"}, &out, &errOut); code != 0 {
		t.Errorf("run(-h) = %d, want 0", code)
	}
}

func TestUnknownSubcommandExitsTwo(t *testing.T) {
	var out, errOut bytes.Buffer
	// A bare word, so this exercises subcommand dispatch rather than flag
	// parsing: an unknown FLAG also exits 2, which would make this test pass
	// without ever reaching runSubcommand.
	if code := run([]string{"nonsense"}, &out, &errOut); code != 2 {
		t.Errorf("run(nonsense) = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Errorf("stderr %q does not name the problem", errOut.String())
	}
}

func TestUnknownFlagExitsTwo(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--no-such-flag"}, &out, &errOut); code != 2 {
		t.Errorf("run(--no-such-flag) = %d, want 2", code)
	}
}

func TestVersionSubcommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"version"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("run(version) = %d, want 0 (stderr: %s)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "dinnerdecider") {
		t.Errorf("version output %q does not name the program", out.String())
	}
}

func TestVersionLineDefaultsToDev(t *testing.T) {
	// An unstamped developer build must still produce a sensible line rather
	// than "dinnerdecider  ()".
	if got := versionLine(); got != "dinnerdecider dev" {
		t.Errorf("versionLine() = %q, want %q", got, "dinnerdecider dev")
	}
}

// --- the token verbs ------------------------------------------------------

func TestTokenShowWithNoTokenFails(t *testing.T) {
	cfg := testConfig(t)
	var out, errOut bytes.Buffer
	if code := runToken(cfg, []string{"show"}, &out, &errOut); code != 1 {
		t.Errorf("token show on an empty state dir = %d, want 1", code)
	}
	// The operator needs to be told where a token would come from, or the
	// failure is a dead end on a headless box.
	if !strings.Contains(errOut.String(), "token mode") {
		t.Errorf("stderr %q does not explain how a token is created", errOut.String())
	}
}

func TestTokenRotateThenShowRoundTrips(t *testing.T) {
	cfg := testConfig(t)

	var rotated, errOut bytes.Buffer
	if code := runToken(cfg, []string{"rotate"}, &rotated, &errOut); code != 0 {
		t.Fatalf("token rotate = %d, want 0 (stderr: %s)", code, errOut.String())
	}
	token := strings.TrimSpace(rotated.String())
	if token == "" {
		t.Fatal("token rotate printed nothing")
	}
	// Rotation unpairs every browser. That has to be said out loud.
	if !strings.Contains(errOut.String(), "pair again") {
		t.Errorf("rotate stderr %q does not warn that paired browsers are dropped", errOut.String())
	}

	var shown, showErr bytes.Buffer
	if code := runToken(cfg, []string{"show"}, &shown, &showErr); code != 0 {
		t.Fatalf("token show = %d, want 0 (stderr: %s)", code, showErr.String())
	}
	if got := strings.TrimSpace(shown.String()); got != token {
		t.Errorf("token show = %q, want the rotated token %q", got, token)
	}
}

func TestTokenForgetRemovesIt(t *testing.T) {
	cfg := testConfig(t)
	var out, errOut bytes.Buffer

	if code := runToken(cfg, []string{"rotate"}, &out, &errOut); code != 0 {
		t.Fatalf("token rotate = %d", code)
	}
	out.Reset()
	errOut.Reset()

	if code := runToken(cfg, []string{"forget"}, &out, &errOut); code != 0 {
		t.Fatalf("token forget = %d, want 0 (stderr: %s)", code, errOut.String())
	}
	if code := runToken(cfg, []string{"show"}, &out, &errOut); code != 1 {
		t.Errorf("token show after forget = %d, want 1", code)
	}
}

func TestUnknownTokenVerbExitsTwo(t *testing.T) {
	cfg := testConfig(t)
	var out, errOut bytes.Buffer
	if code := runToken(cfg, []string{"melt"}, &out, &errOut); code != 2 {
		t.Errorf("token melt = %d, want 2", code)
	}
}

// --- the composition root -------------------------------------------------

// TestBuildServiceServesTheWholeSurface is the test this package exists for:
// every package the port produced, assembled, answering real HTTP.
func TestBuildServiceServesTheWholeSurface(t *testing.T) {
	cfg := testConfig(t)
	svc, err := buildService(context.Background(), cfg, quietLogf(t))
	if err != nil {
		t.Fatalf("buildService: %v", err)
	}
	t.Cleanup(svc.closeAll)

	srv := httptest.NewServer(svc.router.Handler())
	t.Cleanup(srv.Close)

	t.Run("healthz", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/healthz")
		if err != nil {
			t.Fatalf("GET /healthz: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET /healthz = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("ui is served", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/")
		if err != nil {
			t.Fatalf("GET /: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET / = %d, want 200", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET / Content-Type = %q, want text/html", ct)
		}
		// The embedded UI must actually be wired in. A nil Deps.UI serves a
		// placeholder, which would still be 200 text/html.
		if resp.ContentLength >= 0 && resp.ContentLength < 1000 {
			t.Errorf("GET / returned %d bytes, which looks like the placeholder rather than the UI",
				resp.ContentLength)
		}
	})

	t.Run("status reports demo mode honestly", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/api/status")
		if err != nil {
			t.Fatalf("GET /api/status: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/status = %d, want 200", resp.StatusCode)
		}

		var status struct {
			Service string `json:"service"`
			Model   struct {
				DemoMode   bool   `json:"demoMode"`
				DemoReason string `json:"demoReason"`
			} `json:"model"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		if status.Service != "dinnerdecider" {
			t.Errorf("service = %q", status.Service)
		}
		// No weights were installed, so the only correct answer is demo mode
		// WITH a reason. A service quietly serving sample data while reporting
		// a real backend is the failure this assertion exists to prevent.
		if !status.Model.DemoMode {
			t.Error("a service with no weights must report demoMode")
		}
		if status.Model.DemoReason == "" {
			t.Error("demoMode must come with a reason the user can act on")
		}
	})
}

// TestBuildServiceWithoutWeightsChoosesDemo pins the backend decision itself,
// independently of how the status route renders it.
func TestBuildServiceWithoutWeightsChoosesDemo(t *testing.T) {
	cfg := testConfig(t)
	svc, err := buildService(context.Background(), cfg, quietLogf(t))
	if err != nil {
		t.Fatalf("buildService: %v", err)
	}
	t.Cleanup(svc.closeAll)

	if svc.sup != nil {
		t.Error("a service with no weights must not construct a real supervisor")
	}
}

// TestChooseBackendExplainsEachRefusal walks the reasons a real backend is
// declined. Each one has its own operator-facing message, and a silent demote
// is the bug.
func TestChooseBackendExplainsEachRefusal(t *testing.T) {
	cases := []struct {
		name   string
		target llm.Target
		want   string
	}{
		{
			name:   "no official build for this architecture",
			target: llm.Target{GOARCH: "mips64"},
			want:   "no official llama-server build",
		},
		{
			name:   "no weights installed",
			target: llm.Target{GOARCH: "arm64", AssetVariant: "ubuntu-arm64"},
			want:   "no model weights",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logged strings.Builder
			s := &service{
				cfg:  testConfig(t),
				logf: func(f string, a ...any) { logged.WriteString(fmt.Sprintf(f, a...) + "\n") },
			}
			locator := model.Locator{StateDir: s.cfg.StateDir}

			backend := s.chooseBackend(locator, tc.target)
			if backend == nil {
				t.Fatal("chooseBackend returned nil; the scan engine requires a model")
			}
			if s.sup != nil {
				t.Error("no real supervisor should have been built")
			}
			if !strings.Contains(logged.String(), tc.want) {
				t.Errorf("log %q does not contain %q", logged.String(), tc.want)
			}
			if !strings.Contains(logged.String(), "DEMO MODE") {
				t.Errorf("log %q does not announce demo mode", logged.String())
			}
		})
	}
}

// TestListenersTopology pins the two-listener decision, which is load-bearing:
// HTTPS alone is unreachable until the browser has the CA, and the CA is served
// over the plain listener.
func TestListenersTopology(t *testing.T) {
	t.Run("tls off serves one plain listener", func(t *testing.T) {
		cfg := testConfig(t)
		svc, err := buildService(context.Background(), cfg, quietLogf(t))
		if err != nil {
			t.Fatalf("buildService: %v", err)
		}
		t.Cleanup(svc.closeAll)

		ls, err := svc.listeners()
		if err != nil {
			t.Fatalf("listeners: %v", err)
		}
		if len(ls) != 1 {
			t.Fatalf("len(listeners) = %d, want 1", len(ls))
		}
		if ls[0].scheme != "http" {
			t.Errorf("scheme = %q, want http", ls[0].scheme)
		}
	})

	t.Run("self-signed tls serves https and plain", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.TLS = config.TLSSelfSigned
		cfg.TLSPort = 18443
		cfg.Port = 18080

		svc, err := buildService(context.Background(), cfg, quietLogf(t))
		if err != nil {
			t.Fatalf("buildService: %v", err)
		}
		t.Cleanup(svc.closeAll)

		ls, err := svc.listeners()
		if err != nil {
			t.Fatalf("listeners: %v", err)
		}
		if len(ls) != 2 {
			t.Fatalf("len(listeners) = %d, want 2 (https for the camera, http to bootstrap the CA)", len(ls))
		}
		if ls[0].scheme != "https" || ls[1].scheme != "http" {
			t.Errorf("schemes = %q,%q; want https,http", ls[0].scheme, ls[1].scheme)
		}
		if ls[0].tls == nil {
			t.Error("the https listener has no TLS configuration")
		}
		if svc.tlsMat == nil || len(svc.tlsMat.CACertPEM) == 0 {
			t.Error("self-signed mode produced no CA to serve at /ca.crt")
		}
	})
}

// TestSelfSignedServiceServesItsOwnCA closes the bootstrap loop: the CA a
// browser needs must be reachable from the service that generated it.
func TestSelfSignedServiceServesItsOwnCA(t *testing.T) {
	cfg := testConfig(t)
	cfg.TLS = config.TLSSelfSigned

	svc, err := buildService(context.Background(), cfg, quietLogf(t))
	if err != nil {
		t.Fatalf("buildService: %v", err)
	}
	t.Cleanup(svc.closeAll)

	srv := httptest.NewServer(svc.router.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/ca.crt")
	if err != nil {
		t.Fatalf("GET /ca.crt: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ca.crt = %d, want 200", resp.StatusCode)
	}
}

// TestBuildServiceCreatesItsStateDir covers a first boot onto a box where
// nothing exists yet, which is the normal case for a freshly imaged Pi.
func TestBuildServiceCreatesItsStateDir(t *testing.T) {
	cfg := testConfig(t)
	cfg.StateDir = filepath.Join(cfg.StateDir, "not", "created", "yet")

	svc, err := buildService(context.Background(), cfg, quietLogf(t))
	if err != nil {
		t.Fatalf("buildService on a missing state dir: %v", err)
	}
	t.Cleanup(svc.closeAll)

	if svc.db == nil {
		t.Error("no database was opened")
	}
}

// TestRunReturnsOnContextCancellation proves SIGTERM handling: run must return
// rather than hang, and it must take the listeners down with it.
func TestRunReturnsOnContextCancellation(t *testing.T) {
	cfg := testConfig(t)
	cfg.Port = 18099

	svc, err := buildService(context.Background(), cfg, quietLogf(t))
	if err != nil {
		t.Fatalf("buildService: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.run(ctx) }()

	// Let the listener bind before asking it to stop.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run returned %v, want nil on a clean shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return within 10s of cancellation")
	}
}

// TestCloseAllIsSafeOnAPartialService covers the error paths in buildService,
// each of which calls closeAll on a half-built service.
func TestCloseAllIsSafeOnAPartialService(t *testing.T) {
	s := &service{logf: quietLogf(t)}
	s.closeAll()
	s.closeAll()
}
