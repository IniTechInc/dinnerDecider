package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniTechInc/dinnerDecider/linux/internal/config"
)

func TestIsExempt(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// Exempt, each for a stated reason.
		{"/healthz", true},
		{"/ca.crt", true},
		{"/api/pair", true},
		{"/", true},
		{"/favicon.ico", true},
		{"/robots.txt", true},
		{"/manifest.webmanifest", true},
		{"/static/app.css", true},
		{"/static/fonts/DMSans.woff2", true},
		{"/static/", true},
		{"/assets/logo.svg", true},

		// Everything that touches the household's data is not.
		{"/api/status", false},
		{"/api/scan", false},
		{"/api/inventory", false},
		{"/api/photos", false},
		{"/api/settings", false},
		{"/api/recipes", false},
		{"/api/kroger/status", false},
		{"/api/model/events", false},

		// Near misses that must not be mistaken for the exempt paths.
		{"/healthzz", false},
		{"/healthz/secrets", false},
		{"/ca.crt.bak", false},
		{"/api/pairing", false},
		{"/staticfiles/app.js", false},

		// Walking past a prefix check is the oldest trick there is.
		{"/static/../api/inventory", false},
		{"/static/../../api/inventory", false},
		{"/static/./../api/scan", false},
		{"/assets/../api/settings", false},
		{"//api/inventory", false},

		{"", false},
	}
	for _, tc := range tests {
		t.Run("path="+tc.path, func(t *testing.T) {
			if got := IsExempt(tc.path); got != tc.want {
				t.Errorf("IsExempt(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func get(t *testing.T, h http.Handler, path string, decorate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "192.168.1.5:54321"
	if decorate != nil {
		decorate(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func protectedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("the household's inventory"))
	})
}

func TestMiddlewareRefusesAnUncredentialedRequest(t *testing.T) {
	a, _, _ := testAuth(t, ModeToken)
	h := a.Middleware(protectedHandler())

	rec := get(t, h, "/api/inventory", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "household") {
		t.Fatal("the protected handler ran")
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Errorf("WWW-Authenticate = %q", got)
	}
	if !strings.Contains(rec.Body.String(), "/api/pair") {
		t.Error("the 401 should say how to pair")
	}
}

func TestMiddlewareAcceptsCredentials(t *testing.T) {
	a, tok, _ := testAuth(t, ModeToken)
	h := a.Middleware(protectedHandler())

	id, _, err := a.Sessions().Create()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		decorate func(*http.Request)
		want     int
	}{
		{"session cookie", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: id}) }, http.StatusOK},
		{"bearer token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }, http.StatusOK},
		{"bearer token, lowercase scheme", func(r *http.Request) { r.Header.Set("Authorization", "bearer "+tok) }, http.StatusOK},
		{"token header", func(r *http.Request) { r.Header.Set("X-DinnerDecider-Token", tok) }, http.StatusOK},
		{"wrong cookie", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: "nope"}) }, http.StatusUnauthorized},
		{"wrong bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer NOPE") }, http.StatusUnauthorized},
		{"basic auth", func(r *http.Request) { r.Header.Set("Authorization", "Basic "+tok) }, http.StatusUnauthorized},
		{"revoked cookie", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: CookieName, Value: id})
		}, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := get(t, h, "/api/inventory", tc.decorate).Code; got != tc.want {
				t.Errorf("status = %d, want %d", got, tc.want)
			}
		})
	}

	a.Sessions().Revoke(id)
	rec := get(t, h, "/api/inventory", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: id})
	})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a revoked session must stop working, got %d", rec.Code)
	}
}

func TestMiddlewareLetsExemptPathsThrough(t *testing.T) {
	a, _, _ := testAuth(t, ModeToken)
	h := a.Middleware(protectedHandler())

	for _, p := range []string{"/healthz", "/ca.crt", "/", "/static/app.css", "/api/pair"} {
		t.Run(p, func(t *testing.T) {
			if got := get(t, h, p, nil).Code; got != http.StatusOK {
				t.Errorf("status = %d, want 200 without a credential", got)
			}
		})
	}
}

func TestMiddlewarePassesEverythingWhenAuthIsOff(t *testing.T) {
	a, _, _ := testAuth(t, ModeNone)
	h := a.Middleware(protectedHandler())
	if got := get(t, h, "/api/inventory", nil).Code; got != http.StatusOK {
		t.Errorf("status = %d, want 200 with auth=none", got)
	}
}

func TestNewValidatesItsParameters(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), "auth"))

	if _, err := New(Params{Mode: "password"}); err == nil {
		t.Error("an unknown mode must be rejected")
	}
	if _, err := New(Params{Mode: ModeToken}); err == nil {
		t.Error("token mode without a store must be rejected")
	}
	if _, err := New(Params{Mode: ModeToken, Tokens: store}); err == nil {
		t.Error("token mode with an un-Ensured store must be rejected: it would verify nothing and refuse everyone")
	}
	if _, err := New(Params{Mode: ModeNone}); err != nil {
		t.Errorf("auth=none needs no token store: %v", err)
	}

	if _, _, err := store.Ensure(); err != nil {
		t.Fatal(err)
	}
	a, err := New(Params{Mode: ModeToken, Tokens: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.Mode() != ModeToken {
		t.Errorf("Mode = %q", a.Mode())
	}
	if a.SecureCookies() {
		t.Error("SecureCookies must default to false")
	}
}

// TestGuardStartupExposure is the refusal this step exists for.
//
// An open inference endpoint plus a household's complete food inventory, on an
// unauthenticated LAN port, is not a posture anyone should reach by accident.
func TestGuardStartupExposure(t *testing.T) {
	tests := []struct {
		name       string
		auth       config.AuthMode
		bind       string
		accepted   bool
		wantRefuse bool
	}{
		// The refusal itself, across every way of naming a non-loopback bind.
		{"auth=none on 0.0.0.0", config.AuthNone, "0.0.0.0", false, true},
		{"auth=none on a LAN address", config.AuthNone, "192.168.1.42", false, true},
		{"auth=none on a private range", config.AuthNone, "10.8.0.3", false, true},
		{"auth=none on ::", config.AuthNone, "::", false, true},
		{"auth=none on a global IPv6", config.AuthNone, "2001:db8::5", false, true},
		{"auth=none on every interface", config.AuthNone, "", false, true},
		{"auth=none on a hostname", config.AuthNone, "dinnerpi.local", false, true},

		// Loopback with auth=none is permitted, no ceremony: the only clients
		// are processes already on the machine.
		{"auth=none on 127.0.0.1", config.AuthNone, "127.0.0.1", false, false},
		{"auth=none on another loopback address", config.AuthNone, "127.0.1.1", false, false},
		{"auth=none on ::1", config.AuthNone, "::1", false, false},
		{"auth=none on localhost", config.AuthNone, "localhost", false, false},

		// The acknowledgement unlocks it, and only together with auth=none.
		{"auth=none on a LAN address, acknowledged", config.AuthNone, "192.168.1.42", true, false},
		{"auth=none on 0.0.0.0, acknowledged", config.AuthNone, "0.0.0.0", true, false},

		// Token auth is never refused; that is the default posture.
		{"auth=token on a LAN address", config.AuthToken, "192.168.1.42", false, false},
		{"auth=token on 0.0.0.0", config.AuthToken, "0.0.0.0", false, false},
		{"auth=token on loopback", config.AuthToken, "127.0.0.1", false, false},
		// The acknowledgement on its own changes nothing.
		{"auth=token, acknowledged", config.AuthToken, "0.0.0.0", true, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Auth = tc.auth
			cfg.Bind = tc.bind
			cfg.AcceptLANExposure = tc.accepted

			err := GuardStartupExposure(cfg)
			if !tc.wantRefuse {
				if err != nil {
					t.Fatalf("startup must be permitted, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("startup must be REFUSED: this would serve an unauthenticated inference endpoint and the household's inventory to the network")
			}
			if !errors.Is(err, ErrLANExposureRefused) {
				t.Errorf("err = %v, want ErrLANExposureRefused", err)
			}
			// The message has to tell the operator what to do about it.
			msg := err.Error()
			for _, want := range []string{LANExposureFlagName, "-bind=127.0.0.1", "-auth=none"} {
				if !strings.Contains(msg, want) {
					t.Errorf("the refusal does not mention %q; got:\n%s", want, msg)
				}
			}
		})
	}
}

func TestGuardStartupExposureAcceptsTheRealDefaults(t *testing.T) {
	// The shipped defaults must start without an acknowledgement, or every
	// first run begins with an error.
	if err := GuardStartupExposure(config.Defaults()); err != nil {
		t.Fatalf("the built-in defaults must start: %v", err)
	}
}
