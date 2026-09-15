package auth

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/config"
)

// Mode is the service's authentication posture. Aliased to the config type so
// there is exactly one set of values in the program rather than two that must
// be kept in step.
type Mode = config.AuthMode

// The two modes.
const (
	ModeToken = config.AuthToken
	ModeNone  = config.AuthNone
)

// Params configures New.
type Params struct {
	// Mode is token or none.
	Mode Mode
	// Tokens is required in token mode, and must already have been Ensured.
	Tokens *TokenStore
	// Sessions is optional; one is created from SessionTTL when nil.
	Sessions   *SessionStore
	SessionTTL time.Duration
	// SecureCookies sets the Secure attribute on the session cookie. It should
	// be true whenever the service terminates TLS.
	SecureCookies bool
	// Now defaults to time.Now.
	Now func() time.Time
	// PairBurst and PairRefillInterval tune the pairing rate limiter.
	PairBurst          int
	PairRefillInterval time.Duration
}

// Authenticator enforces the service's authentication posture.
type Authenticator struct {
	mode          Mode
	tokens        *TokenStore
	sessions      *SessionStore
	limiter       *rateLimiter
	secureCookies bool
}

// New builds an Authenticator.
func New(p Params) (*Authenticator, error) {
	switch p.Mode {
	case ModeToken:
		if p.Tokens == nil {
			return nil, errors.New("auth: token mode needs a TokenStore")
		}
		if !p.Tokens.Loaded() {
			return nil, errors.New("auth: the token store must be Ensured before the authenticator is built")
		}
	case ModeNone:
	default:
		return nil, fmt.Errorf("auth: %q is not a known mode", string(p.Mode))
	}

	sessions := p.Sessions
	if sessions == nil {
		sessions = NewSessionStore(p.SessionTTL, p.Now)
	}
	return &Authenticator{
		mode:          p.Mode,
		tokens:        p.Tokens,
		sessions:      sessions,
		limiter:       newRateLimiter(p.PairBurst, p.PairRefillInterval, p.Now),
		secureCookies: p.SecureCookies,
	}, nil
}

// Mode reports the configured posture.
func (a *Authenticator) Mode() Mode { return a.mode }

// Sessions exposes the session store, for a sign-out handler and for status.
func (a *Authenticator) Sessions() *SessionStore { return a.sessions }

// SecureCookies reports whether issued cookies carry the Secure attribute.
func (a *Authenticator) SecureCookies() bool { return a.secureCookies }

// exemptExact are the paths served without a credential.
//
// Each one is here for a reason, and the list is short on purpose:
//
//	/healthz   a container or systemd health check has no credential, and a
//	           liveness probe that can fail for authentication reasons is worse
//	           than useless.
//	/ca.crt    the certificate authority is public, and requiring a credential
//	           to fetch the thing you need in order to trust the connection is a
//	           bootstrap deadlock.
//	/api/pair  the endpoint whose entire job is to check the token itself.
//	/          the UI shell, so the pairing screen can load. It carries no data.
//
// Nothing that reads or writes the household's inventory, photos, or settings
// is on this list.
var exemptExact = map[string]struct{}{
	"/healthz":              {},
	"/ca.crt":               {},
	"/api/pair":             {},
	"/":                     {},
	"/favicon.ico":          {},
	"/robots.txt":           {},
	"/manifest.webmanifest": {},
}

// exemptPrefixes are the embedded static assets: stylesheets, scripts, fonts.
// They ship inside the binary and contain no user data.
var exemptPrefixes = []string{"/static/", "/assets/"}

// IsExempt reports whether a request path is served without a credential.
//
// The path is cleaned first. Without that, /static/../api/inventory matches the
// /static/ prefix while naming a data endpoint — the oldest way there is to
// walk past a path-prefix check.
func IsExempt(p string) bool {
	if p == "" {
		return false
	}
	clean := path.Clean(p)
	if !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	// path.Clean strips a trailing slash, which would turn "/" into "/" but
	// "/static/" into "/static"; match the prefixes against the cleaned path
	// plus a separator so a directory request still matches.
	if _, ok := exemptExact[clean]; ok {
		return true
	}
	for _, prefix := range exemptPrefixes {
		if strings.HasPrefix(clean+"/", prefix) || strings.HasPrefix(clean, prefix) {
			return true
		}
	}
	return false
}

// Authenticate reports whether a request carries a valid credential: a live
// session cookie, or the pairing token as a bearer credential for clients that
// do not keep cookies (curl, a script, the benchmark harness).
func (a *Authenticator) Authenticate(r *http.Request) bool {
	if a.mode == ModeNone {
		return true
	}
	if c, err := r.Cookie(CookieName); err == nil && a.sessions.Valid(c.Value) {
		return true
	}
	if t := bearerToken(r); t != "" && a.tokens.Verify(t) {
		return true
	}
	return false
}

// Middleware enforces authentication on everything that is not exempt.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.mode == ModeNone || IsExempt(r.URL.Path) || a.Authenticate(r) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="dinnerdecider"`)
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "Pair this device first: POST the pairing token to /api/pair.")
	})
}

// bearerToken reads the pairing token from the request headers. Both the
// standard Authorization: Bearer form and a plain X-DinnerDecider-Token header
// are accepted, because the second is much easier to type into a shell.
func bearerToken(r *http.Request) string {
	if v := r.Header.Get("Authorization"); v != "" {
		const prefix = "bearer "
		if len(v) > len(prefix) && strings.EqualFold(v[:len(prefix)], prefix) {
			return strings.TrimSpace(v[len(prefix):])
		}
	}
	return strings.TrimSpace(r.Header.Get("X-DinnerDecider-Token"))
}

// ---- the startup guard -----------------------------------------------------

// ErrLANExposureRefused is returned when the service is asked to serve an
// unauthenticated endpoint to something other than loopback without the
// operator saying so explicitly.
var ErrLANExposureRefused = errors.New("auth: refusing to bind a non-loopback address with authentication disabled")

// LANExposureFlagName is the acknowledgement an operator must pass.
const LANExposureFlagName = "-i-accept-lan-exposure"

// GuardStartupExposure refuses a configuration that would put an
// unauthenticated service on the network.
//
// The thing behind this port is a model-inference endpoint and a complete
// inventory of what is in someone's kitchen. Unauthenticated on a LAN, that is
// an open compute resource for anyone on the Wi-Fi and a detailed record of a
// household's habits, and it is reachable from every guest phone, every smart
// television, and anything that has ever joined the network.
//
// That posture is a legitimate choice — a trusted lab segment, a box behind a
// reverse proxy that does its own authentication — but it must be a choice.
// Nobody should arrive at it by copying a command line that happened to include
// -bind=0.0.0.0. So both switches are required: -auth=none says what you want,
// and -i-accept-lan-exposure says you meant it.
//
// Loopback with auth=none is permitted without ceremony: the only clients are
// processes already on the machine, which can read the state directory anyway.
func GuardStartupExposure(cfg config.Config) error {
	if cfg.Auth != config.AuthNone {
		return nil
	}
	if cfg.BindIsLoopback() {
		return nil
	}
	if cfg.AcceptLANExposure {
		return nil
	}
	bind := cfg.Bind
	if bind == "" {
		bind = "every interface"
	}
	return fmt.Errorf(
		"%w: bind is %s, which is not loopback.\n"+
			"  This would serve an inference endpoint and your entire food inventory to anything on the network,\n"+
			"  with no credential of any kind. Pick one:\n"+
			"    - drop -auth=none (token authentication is the default), or\n"+
			"    - bind loopback (-bind=127.0.0.1) and reach it through a tunnel or a reverse proxy, or\n"+
			"    - if you really mean it, pass %s as well",
		ErrLANExposureRefused, bind, LANExposureFlagName)
}
