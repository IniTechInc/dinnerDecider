package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// CookieName is the session cookie. The dd_ prefix keeps it clear which cookie
// belongs to this service when a browser has several self-hosted things open.
const CookieName = "dd_session"

// Session defaults.
const (
	// DefaultSessionTTL is how long a paired browser stays paired. Long enough
	// that a household is not re-pairing constantly, short enough that a
	// borrowed phone does not keep access indefinitely.
	DefaultSessionTTL = 30 * 24 * time.Hour
	// MaxSessions bounds the session table. This is a LAN service for a
	// household; anything near this number is a bug or an attack, and an
	// unbounded map on a memory-ceilinged box is exactly the class of problem
	// the rest of this design spends its effort avoiding.
	MaxSessions = 512
	// sessionIDBytes is the entropy in a session identifier. Same reasoning as
	// the pairing token: random, not chosen, so no KDF.
	sessionIDBytes = 32
)

// Pair-endpoint rate limiting.
const (
	// DefaultPairBurst is how many pairing attempts a client can make back to
	// back. Enough for a fat-fingered token on a phone keyboard.
	DefaultPairBurst = 5
	// DefaultPairRefillInterval is how long one attempt takes to come back,
	// which is the sustained rate: five a minute.
	DefaultPairRefillInterval = 12 * time.Second
	// maxRateLimitKeys bounds the limiter's map for the same reason
	// MaxSessions bounds the session table.
	maxRateLimitKeys = 4096
	// maxPairBodyBytes caps the pairing request body. A token is 52 characters;
	// this is generous and still refuses a body that exists to allocate memory.
	maxPairBodyBytes = 4 << 10
)

// SessionStore issues and validates session identifiers.
//
// Identifiers are never stored. The table is keyed by the SHA-256 of the
// identifier, so a memory dump or a leaked state file yields nothing a client
// can present — the same reasoning that applies to the pairing token, applied
// to the credential the browser actually carries around.
type SessionStore struct {
	ttl time.Duration
	now func() time.Time

	mu       sync.Mutex
	sessions map[string]time.Time
}

// NewSessionStore returns a store with the given lifetime. A ttl of zero uses
// DefaultSessionTTL; a nil clock uses time.Now.
func NewSessionStore(ttl time.Duration, now func() time.Time) *SessionStore {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	if now == nil {
		now = time.Now
	}
	return &SessionStore{ttl: ttl, now: now, sessions: map[string]time.Time{}}
}

// Create issues a session and returns its identifier and expiry.
func (s *SessionStore) Create() (id string, expires time.Time, err error) {
	buf := make([]byte, sessionIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, fmt.Errorf("auth: generate session id: %w", err)
	}
	id = base64.RawURLEncoding.EncodeToString(buf)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.sweepLocked(now)
	if len(s.sessions) >= MaxSessions {
		s.evictSoonestLocked()
	}
	expires = now.Add(s.ttl)
	s.sessions[sessionKey(id)] = expires
	return id, expires, nil
}

// Valid reports whether id names a live session.
func (s *SessionStore) Valid(id string) bool {
	if id == "" {
		return false
	}
	key := sessionKey(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	expires, ok := s.sessions[key]
	if !ok {
		// Do the comparison work anyway so a hit and a miss cost the same. The
		// map lookup itself is not constant time, but there is no point handing
		// out a second, cheaper signal.
		var zero [sha256.Size]byte
		subtle.ConstantTimeCompare(zero[:], zero[:])
		return false
	}
	if !s.now().Before(expires) {
		delete(s.sessions, key)
		return false
	}
	return true
}

// Revoke ends one session.
func (s *SessionStore) Revoke(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionKey(id))
}

// RevokeAll ends every session, which is what a token rotation should be
// followed by when the reason for rotating was a suspected leak.
func (s *SessionStore) RevokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = map[string]time.Time{}
}

// Count reports how many live sessions exist. For /api/status and for tests.
func (s *SessionStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(s.now())
	return len(s.sessions)
}

func (s *SessionStore) sweepLocked(now time.Time) {
	for k, exp := range s.sessions {
		if !now.Before(exp) {
			delete(s.sessions, k)
		}
	}
}

func (s *SessionStore) evictSoonestLocked() {
	type entry struct {
		key string
		exp time.Time
	}
	all := make([]entry, 0, len(s.sessions))
	for k, e := range s.sessions {
		all = append(all, entry{k, e})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].exp.Before(all[j].exp) })
	drop := len(all) - MaxSessions + 1
	for i := 0; i < drop && i < len(all); i++ {
		delete(s.sessions, all[i].key)
	}
}

func sessionKey(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// NewSessionCookie builds the Set-Cookie for a freshly issued session.
//
// HttpOnly so that script cannot read it; SameSite=Lax so that a page on
// another origin cannot drive this service's state-changing endpoints with the
// household's credential attached; Secure whenever TLS is on, because a cookie
// without it will be sent over the plain-HTTP port too.
func NewSessionCookie(id string, expires time.Time, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    id,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	}
}

// ClearSessionCookie builds the Set-Cookie that ends a session in the browser.
func ClearSessionCookie(secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	}
}

// ---- rate limiting ---------------------------------------------------------

type bucket struct {
	tokens float64
	last   time.Time
}

// rateLimiter is a per-client token bucket. Hand-rolled because go.mod is
// frozen and golang.org/x/time is not in it; it is twenty lines.
type rateLimiter struct {
	burst    float64
	perSec   float64
	maxKeys  int
	now      func() time.Time
	mu       sync.Mutex
	buckets  map[string]*bucket
	disabled bool
}

func newRateLimiter(burst int, refill time.Duration, now func() time.Time) *rateLimiter {
	if now == nil {
		now = time.Now
	}
	if burst <= 0 {
		burst = DefaultPairBurst
	}
	if refill <= 0 {
		refill = DefaultPairRefillInterval
	}
	return &rateLimiter{
		burst:   float64(burst),
		perSec:  1 / refill.Seconds(),
		maxKeys: maxRateLimitKeys,
		now:     now,
		buckets: map[string]*bucket{},
	}
}

// allow spends one attempt for key, reporting whether it was available and, if
// not, how long until one is.
func (r *rateLimiter) allow(key string) (bool, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disabled {
		return true, 0
	}
	now := r.now()
	b, ok := r.buckets[key]
	if !ok {
		if len(r.buckets) >= r.maxKeys {
			r.evictFullLocked(now)
		}
		b = &bucket{tokens: r.burst, last: now}
		r.buckets[key] = b
	}
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens += elapsed.Seconds() * r.perSec
		if b.tokens > r.burst {
			b.tokens = r.burst
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / r.perSec * float64(time.Second))
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}

// evictFullLocked drops buckets that have refilled completely. A full bucket
// carries no state a new one would not have, so this loses nothing.
func (r *rateLimiter) evictFullLocked(now time.Time) {
	for k, b := range r.buckets {
		refilled := b.tokens + now.Sub(b.last).Seconds()*r.perSec
		if refilled >= r.burst {
			delete(r.buckets, k)
		}
	}
	// Still full of active offenders: drop everything rather than grow without
	// bound. The cost is that a few clients get their allowance back early,
	// which is a far better failure than an unbounded map on a Pi.
	if len(r.buckets) >= r.maxKeys {
		r.buckets = map[string]*bucket{}
	}
}

// clientKey identifies the caller for rate-limiting.
//
// It is the transport peer address and nothing else. X-Forwarded-For and
// X-Real-IP are deliberately ignored: this service binds a LAN interface
// directly, nothing in front of it sets those headers, and honouring them would
// let anyone defeat the limiter by putting a fresh value in each request.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- the pairing endpoint --------------------------------------------------

// pairRequest is the POST /api/pair body.
type pairRequest struct {
	Token string `json:"token"`
}

// pairResponse is returned on success. It never echoes the token.
type pairResponse struct {
	OK        bool   `json:"ok"`
	ExpiresAt string `json:"expiresAt"`
}

// PairHandler exchanges the pairing token for a session cookie.
//
// The token may arrive as a JSON body {"token":"..."} or as an
// Authorization: Bearer header, so a browser and a curl one-liner can both
// pair. Every attempt, successful or not, spends one unit of the caller's rate
// allowance: a flood of valid pairings is as unwelcome as a flood of guesses.
func (a *Authenticator) PairHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST a token to pair.")
			return
		}
		if a.mode == ModeNone {
			// Nothing to pair with. Say so rather than issuing a cookie that
			// means nothing, which would make a client believe it is
			// authenticated when authentication is switched off.
			writeJSONError(w, http.StatusConflict, "auth_disabled", "This service is running with authentication disabled; there is nothing to pair.")
			return
		}

		if ok, retry := a.limiter.allow(clientKey(r)); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds()+0.999)))
			writeJSONError(w, http.StatusTooManyRequests, "rate_limited", "Too many pairing attempts. Wait and try again.")
			return
		}

		token, err := extractPairToken(w, r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		if !a.tokens.Verify(token) {
			writeJSONError(w, http.StatusUnauthorized, "invalid_token", "That pairing token is not correct.")
			return
		}

		id, expires, err := a.sessions.Create()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal", "Could not create a session.")
			return
		}
		http.SetCookie(w, NewSessionCookie(id, expires, a.secureCookies))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(pairResponse{OK: true, ExpiresAt: expires.UTC().Format(time.RFC3339)})
	})
}

func extractPairToken(w http.ResponseWriter, r *http.Request) (string, error) {
	if t := bearerToken(r); t != "" {
		return t, nil
	}
	body := http.MaxBytesReader(w, r.Body, maxPairBodyBytes)
	var req pairRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return "", errors.New("the request body is too large to be a pairing token")
		}
		return "", errors.New(`send {"token":"..."} or an Authorization: Bearer header`)
	}
	if req.Token == "" {
		return "", errors.New("no token in the request")
	}
	return req.Token, nil
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}
