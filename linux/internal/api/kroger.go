// Kroger integration: configuration only, and configuration that this repository
// never contains.
//
// # Why there is no client ID here
//
// On iOS the credentials come from KrogerCredentials.plist, which is gitignored.
// The reason it is gitignored is worth stating plainly, because it is the whole
// argument for this file's shape: Kroger's OAuth2 flow here is a CONFIDENTIAL
// client, the token exchange sends client_secret, and a confidential client's
// secret cannot be shipped to anything the user controls. Gitignoring the plist
// keeps the secret out of the repository; it does not make shipping one safe.
//
// A distributed Linux binary, .deb or container image is STRICTLY WORSE than an
// app bundle in this respect. An app bundle is at least signed, distributed
// through one channel, and mildly inconvenient to open. A binary or an image is
// a file anyone can unpack with `strings`, and an image layer is world-readable
// on a public registry. Anything embedded here is published, not hidden.
//
// So: THERE IS NO FIRST-PARTY CLIENT ID FOR THE LINUX PORT. The integration is
// operator-configured or it is off. There are zero embedded defaults and zero
// fallback constants in this file, and adding one would be a credential leak
// rather than a convenience.
//
// # The redirect URI, and why it needs work from the operator
//
// iOS registers dinnerdecider://kroger-callback, a custom scheme. A web UI
// cannot use a custom scheme, so the Linux redirect has to be a real URL:
//
//	http(s)://<host>:<port>/api/kroger/callback
//
// Two consequences the operator has to plan for, neither of which this code can
// paper over:
//
//  1. Kroger requires the redirect URI to be PRE-REGISTERED on the application,
//     exactly, including scheme, host, port and path. It cannot be discovered at
//     runtime and it cannot vary per client.
//  2. That means a STABLE address. On a home LAN with DHCP the service's address
//     is not stable, and the day the router hands out a different lease every
//     Kroger login breaks with redirect_uri_mismatch. Reserve the address:
//     a DHCP reservation for the box's MAC on the router, or a static address on
//     the box itself. A hostname alone is not enough unless the registered URI
//     uses that hostname and every client resolves it.
//
// FALLBACK when a stable LAN address is not available: register
// http://127.0.0.1:<port>/api/kroger/callback, run the authorization in a
// browser ON the box (or through an SSH tunnel), and let the operator paste the
// authorization code back in by hand. It is worse, and it works from anywhere.
//
// The callback route itself is deliberately NOT mounted: this step configures
// the integration and reports whether it is configured, and the authorization
// flow is not implemented here. When it is added it MUST be served at the path
// above, because that is the path this documentation tells operators to
// register.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// DefaultKrogerConfigPath is where an operator puts the credentials.
//
// Alongside the service's other configuration in /etc, not in the state
// directory: this is something an administrator writes once, not something the
// service manages.
const DefaultKrogerConfigPath = "/etc/dinnerdecider/kroger.json"

// The environment variables, for a container or a systemd drop-in where a file
// is awkward.
const (
	EnvKrogerClientID     = "DD_KROGER_CLIENT_ID"
	EnvKrogerClientSecret = "DD_KROGER_CLIENT_SECRET"
)

// KrogerCallbackPath is the redirect URI's path, and the one place it is
// spelled.
const KrogerCallbackPath = "/api/kroger/callback"

// KrogerScopes and KrogerAPIBase are not secrets; they are the same public
// values the iOS client uses.
const (
	KrogerScopes  = "product.compact cart.basic:write"
	KrogerAPIBase = "https://api.kroger.com/v1"
)

// krogerFileMode is the mode a credentials file must have.
//
// ENFORCED, not suggested. A world-readable file holding a confidential
// client's secret is the leak this whole file exists to prevent, and a service
// that reads one anyway while logging a warning has taught the operator that
// the warning does not matter.
const krogerFileMode os.FileMode = 0o600

// ErrKrogerFilePermissions is returned for a credentials file that anyone but
// its owner can read.
var ErrKrogerFilePermissions = errors.New("api: the Kroger credentials file must be mode 0600")

// KrogerConfig is the loaded credentials, or the reason there are none.
//
// The zero value is the dormant state, which is a supported, ordinary
// configuration rather than an error: most installations will never connect a
// grocery account.
type KrogerConfig struct {
	ClientID     string
	ClientSecret string
	// Source records where the credentials came from, for /api/status and for
	// an operator wondering which of two configurations won.
	Source string
	// Problem is a plain-language reason the integration is dormant when the
	// operator clearly tried to configure it: a file with the wrong mode, or a
	// file that could not be parsed. Empty when nothing was attempted.
	Problem string
}

// Configured reports whether the integration can run at all.
func (c KrogerConfig) Configured() bool {
	return c.ClientID != "" && c.ClientSecret != ""
}

// LoadKrogerConfig resolves credentials at RUNTIME, from a file or the
// environment, with no compiled-in fallback of any kind.
//
// Precedence: the file first, then the environment. The file is the
// administrator's deliberate act and lives on the box; the environment is the
// convenience path. Neither present means dormant.
func LoadKrogerConfig(path string) KrogerConfig {
	if path == "" {
		path = DefaultKrogerConfigPath
	}

	cfg, err := loadKrogerFile(path)
	switch {
	case err == nil && cfg.Configured():
		return cfg
	case errors.Is(err, os.ErrNotExist):
		// Ordinary. Fall through to the environment.
	case err != nil:
		// A file that exists and cannot be used is worth reporting, and worth
		// NOT silently falling back from: an operator who wrote a credentials
		// file and got a dormant integration needs to be told why.
		return KrogerConfig{Source: path, Problem: err.Error()}
	}

	id := strings.TrimSpace(os.Getenv(EnvKrogerClientID))
	secret := strings.TrimSpace(os.Getenv(EnvKrogerClientSecret))
	if id != "" && secret != "" {
		return KrogerConfig{ClientID: id, ClientSecret: secret, Source: "environment"}
	}
	if id != "" || secret != "" {
		return KrogerConfig{
			Source:  "environment",
			Problem: fmt.Sprintf("set both %s and %s, not just one", EnvKrogerClientID, EnvKrogerClientSecret),
		}
	}
	return KrogerConfig{}
}

// krogerFile is the on-disk shape, matching the plist's two keys so an operator
// moving from iOS copies two values rather than learning a new format.
type krogerFile struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

func loadKrogerFile(path string) (KrogerConfig, error) {
	info, err := os.Stat(path)
	if err != nil {
		return KrogerConfig{}, err
	}
	if info.IsDir() {
		return KrogerConfig{}, fmt.Errorf("api: %s is a directory, not a credentials file", path)
	}
	// The mode check comes BEFORE the read. Refusing to read is the point:
	// a secret that has already been loaded into a process that then complains
	// about permissions has still been read from a world-readable file, and the
	// operator has still published it to every account on the box.
	if perm := info.Mode().Perm(); perm&^krogerFileMode != 0 {
		return KrogerConfig{}, fmt.Errorf("%w: %s is mode %#o, run: chmod 600 %s",
			ErrKrogerFilePermissions, path, perm, path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return KrogerConfig{}, fmt.Errorf("api: reading %s: %w", path, err)
	}
	var file krogerFile
	if err := json.Unmarshal(data, &file); err != nil {
		return KrogerConfig{}, fmt.Errorf("api: %s is not valid JSON: %w", path, err)
	}
	id := strings.TrimSpace(file.ClientID)
	secret := strings.TrimSpace(file.ClientSecret)
	if id == "" || secret == "" {
		return KrogerConfig{}, fmt.Errorf("api: %s needs both clientId and clientSecret", path)
	}
	return KrogerConfig{ClientID: id, ClientSecret: secret, Source: path}, nil
}

// krogerAPI serves GET /api/kroger/status.
type krogerAPI struct {
	base
	config KrogerConfig
	// redirectURI is computed from the service's own configuration so the
	// status response can tell the operator the exact string to register.
	redirectURI string
}

// krogerStatus is the whole public answer. It carries NO credential, not even
// the client id: the id is not as sensitive as the secret, but it is still an
// account identifier and nothing in the UI needs it.
type krogerStatus struct {
	// Configured is false on a stock installation, and that is the normal
	// state. The UI shows the integration unconfigured rather than hiding it,
	// so an operator can find out it exists.
	Configured bool `json:"configured"`
	// Source is where the configuration came from, or empty when dormant.
	Source string `json:"source,omitempty"`
	// Problem explains a configuration that was attempted and rejected.
	Problem string `json:"problem,omitempty"`
	// RedirectURI is the exact string to pre-register with Kroger.
	RedirectURI string `json:"redirectUri"`
	// Scopes and ApiBase are public values, included so the operator setting up
	// the application does not have to go looking for them.
	Scopes  string `json:"scopes"`
	APIBase string `json:"apiBase"`
	// SetupHint is the one-line instruction for the dormant case.
	SetupHint string `json:"setupHint,omitempty"`
}

func (a *krogerAPI) status() krogerStatus {
	status := krogerStatus{
		Configured:  a.config.Configured(),
		Source:      a.config.Source,
		Problem:     a.config.Problem,
		RedirectURI: a.redirectURI,
		Scopes:      KrogerScopes,
		APIBase:     KrogerAPIBase,
	}
	if !status.Configured {
		status.SetupHint = fmt.Sprintf(
			"Kroger is not configured. There is no built-in application for this service: register your own, then write %s as {\"clientId\":\"...\",\"clientSecret\":\"...\"} with mode 0600, or set %s and %s. Register %s as the redirect URI, and give this machine a fixed address so it keeps working.",
			DefaultKrogerConfigPath, EnvKrogerClientID, EnvKrogerClientSecret, a.redirectURI)
	}
	return status
}

// handleStatus reports whether the integration is live.
func (a *krogerAPI) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.status())
}

// krogerRedirectURI builds the exact string an operator must pre-register.
func krogerRedirectURI(scheme, host string, port int) string {
	if scheme == "" {
		scheme = "http"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		// Bound to everything means there is no single address to name, and
		// guessing one would be worse than saying so: the operator has to pick
		// the address their phones will actually use, and register that.
		host = "<this-machine>"
	}
	return fmt.Sprintf("%s://%s:%d%s", scheme, host, port, KrogerCallbackPath)
}
