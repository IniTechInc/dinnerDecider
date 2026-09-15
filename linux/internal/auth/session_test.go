package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var sessionBase = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

// clock is a hand-advanced time source. Every deadline in this package is
// injected precisely so no test has to sleep.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: sessionBase} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// testAuth builds an authenticator over a temp state directory and returns it
// with the pairing token and the clock driving its deadlines.
func testAuth(t *testing.T, mode Mode, tweak ...func(*Params)) (*Authenticator, string, *clock) {
	t.Helper()
	c := newClock()
	store := NewTokenStore(filepath.Join(t.TempDir(), "auth"))
	tok, _, err := store.Ensure()
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	p := Params{
		Mode:   mode,
		Tokens: store,
		Now:    c.now,
	}
	for _, f := range tweak {
		f(&p)
	}
	a, err := New(p)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a, tok, c
}

func TestSessionCreateAndValidate(t *testing.T) {
	c := newClock()
	s := NewSessionStore(time.Hour, c.now)

	id, expires, err := s.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("empty session id")
	}
	if !expires.Equal(sessionBase.Add(time.Hour)) {
		t.Errorf("expires = %s, want base+1h", expires)
	}
	if !s.Valid(id) {
		t.Error("a freshly created session must be valid")
	}
	if s.Valid("") || s.Valid(id+"x") {
		t.Error("an unknown session id must not validate")
	}
	if s.Count() != 1 {
		t.Errorf("Count = %d, want 1", s.Count())
	}

	other, _, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if other == id {
		t.Fatal("two sessions share an identifier")
	}
}

func TestSessionIdentifiersAreNotStored(t *testing.T) {
	// The table is keyed by the hash of the identifier, so a leaked state dump
	// yields nothing a client can present.
	s := NewSessionStore(time.Hour, nil)
	id, _, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, found := s.sessions[id]; found {
		t.Fatal("the session identifier itself is a map key")
	}
	if _, found := s.sessions[sessionKey(id)]; !found {
		t.Fatal("the session is not keyed by its hash")
	}
}

func TestSessionExpiry(t *testing.T) {
	c := newClock()
	s := NewSessionStore(time.Hour, c.now)
	id, _, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}

	c.advance(59 * time.Minute)
	if !s.Valid(id) {
		t.Error("a session must survive up to its expiry")
	}
	c.advance(2 * time.Minute)
	if s.Valid(id) {
		t.Error("an expired session must not validate")
	}
	if s.Count() != 0 {
		t.Errorf("Count = %d, want 0 after expiry", s.Count())
	}
}

func TestSessionRevoke(t *testing.T) {
	s := NewSessionStore(time.Hour, nil)
	a, _, _ := s.Create()
	b, _, _ := s.Create()

	s.Revoke(a)
	if s.Valid(a) {
		t.Error("a revoked session must not validate")
	}
	if !s.Valid(b) {
		t.Error("revoking one session must not touch another")
	}
	s.Revoke("")

	s.RevokeAll()
	if s.Valid(b) || s.Count() != 0 {
		t.Error("RevokeAll must empty the table")
	}
}

func TestSessionTableIsBounded(t *testing.T) {
	// An unbounded map on a memory-ceilinged box is the class of problem the
	// rest of this design spends its effort avoiding.
	s := NewSessionStore(time.Hour, nil)
	for i := 0; i < MaxSessions+50; i++ {
		if _, _, err := s.Create(); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Count(); got > MaxSessions {
		t.Errorf("Count = %d, want at most %d", got, MaxSessions)
	}
}

func TestSessionTTLDefaults(t *testing.T) {
	c := newClock()
	s := NewSessionStore(0, c.now)
	_, expires, err := s.Create()
	if err != nil {
		t.Fatal(err)
	}
	if !expires.Equal(sessionBase.Add(DefaultSessionTTL)) {
		t.Errorf("expires = %s, want the default TTL applied", expires)
	}
}

func TestCookieAttributes(t *testing.T) {
	tests := []struct {
		name   string
		secure bool
	}{
		{"plain HTTP", false},
		{"TLS on", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewSessionCookie("abc", sessionBase.Add(time.Hour), tc.secure)
			if c.Name != CookieName {
				t.Errorf("name = %q", c.Name)
			}
			if !c.HttpOnly {
				t.Error("the session cookie must be HttpOnly")
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Errorf("SameSite = %v, want Lax", c.SameSite)
			}
			if c.Secure != tc.secure {
				t.Errorf("Secure = %v, want %v", c.Secure, tc.secure)
			}
			if c.Path != "/" {
				t.Errorf("Path = %q", c.Path)
			}

			cleared := ClearSessionCookie(tc.secure)
			if cleared.Value != "" || cleared.MaxAge >= 0 {
				t.Error("the clearing cookie must empty the value and expire immediately")
			}
			if !cleared.HttpOnly || cleared.SameSite != http.SameSiteLaxMode || cleared.Secure != tc.secure {
				t.Error("the clearing cookie must carry the same attributes, or a browser will keep the original")
			}
		})
	}
}

func TestRateLimiter(t *testing.T) {
	c := newClock()
	r := newRateLimiter(3, 10*time.Second, c.now)

	for i := 0; i < 3; i++ {
		if ok, _ := r.allow("10.0.0.1"); !ok {
			t.Fatalf("attempt %d of the burst was refused", i+1)
		}
	}
	ok, retry := r.allow("10.0.0.1")
	if ok {
		t.Fatal("the fourth attempt must be refused")
	}
	if retry <= 0 {
		t.Errorf("retry-after = %s, want a positive wait", retry)
	}

	// A different client has its own allowance.
	if ok, _ := r.allow("10.0.0.2"); !ok {
		t.Error("one client's flood must not lock out another")
	}

	// One attempt comes back per refill interval.
	c.advance(10 * time.Second)
	if ok, _ := r.allow("10.0.0.1"); !ok {
		t.Error("an attempt must come back after the refill interval")
	}
	if ok, _ := r.allow("10.0.0.1"); ok {
		t.Error("only one attempt comes back per interval")
	}

	// And the bucket never fills past the burst.
	c.advance(time.Hour)
	for i := 0; i < 3; i++ {
		if ok, _ := r.allow("10.0.0.1"); !ok {
			t.Fatalf("attempt %d after a long idle was refused", i+1)
		}
	}
	if ok, _ := r.allow("10.0.0.1"); ok {
		t.Error("a long idle must not bank more than the burst")
	}
}

func TestRateLimiterKeyTableIsBounded(t *testing.T) {
	c := newClock()
	r := newRateLimiter(1, time.Second, c.now)
	for i := 0; i < maxRateLimitKeys+100; i++ {
		r.allow("10.0." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256))
	}
	r.mu.Lock()
	n := len(r.buckets)
	r.mu.Unlock()
	if n > maxRateLimitKeys {
		t.Errorf("the limiter is tracking %d keys, want at most %d", n, maxRateLimitKeys)
	}
}

func TestClientKeyIgnoresForwardedHeaders(t *testing.T) {
	// Honouring X-Forwarded-For would let anyone defeat the limiter by putting
	// a fresh value in every request.
	r := httptest.NewRequest(http.MethodPost, "/api/pair", nil)
	r.RemoteAddr = "192.168.1.5:54321"
	r.Header.Set("X-Forwarded-For", "10.9.9.9")
	r.Header.Set("X-Real-IP", "10.9.9.8")
	if got := clientKey(r); got != "192.168.1.5" {
		t.Errorf("clientKey = %q, want the transport peer address", got)
	}
}

// pair posts a pairing request and returns the recorder.
func pair(t *testing.T, a *Authenticator, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pair", strings.NewReader(body))
	req.RemoteAddr = "192.168.1.5:54321"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	a.PairHandler().ServeHTTP(rec, req)
	return rec
}

func TestPairSuccess(t *testing.T) {
	a, tok, _ := testAuth(t, ModeToken)
	rec := pair(t, a, `{"token":"`+tok+`"}`, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}

	var got pairResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if !got.OK || got.ExpiresAt == "" {
		t.Errorf("response = %+v", got)
	}
	if strings.Contains(rec.Body.String(), tok) {
		t.Error("the response echoed the pairing token")
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != CookieName {
		t.Errorf("cookie name = %q", c.Name)
	}
	if !c.HttpOnly {
		t.Error("cookie must be HttpOnly")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie SameSite = %v, want Lax", c.SameSite)
	}
	if !a.Sessions().Valid(c.Value) {
		t.Error("the issued cookie does not name a live session")
	}
}

func TestPairSecureCookieFollowsTLS(t *testing.T) {
	tests := []struct {
		name   string
		secure bool
	}{
		{"TLS off", false},
		{"TLS on", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, tok, _ := testAuth(t, ModeToken, func(p *Params) { p.SecureCookies = tc.secure })
			rec := pair(t, a, `{"token":"`+tok+`"}`, nil)
			cookies := rec.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("got %d cookies", len(cookies))
			}
			if cookies[0].Secure != tc.secure {
				t.Errorf("Secure = %v, want %v", cookies[0].Secure, tc.secure)
			}
		})
	}
}

func TestPairWithBearerHeader(t *testing.T) {
	a, tok, _ := testAuth(t, ModeToken)
	rec := pair(t, a, "", map[string]string{"Authorization": "Bearer " + tok})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
}

func TestPairRejections(t *testing.T) {
	tests := []struct {
		name    string
		body    func(tok string) string
		headers map[string]string
		method  string
		want    int
	}{
		{"wrong token", func(string) string { return `{"token":"WRONGWRONGWRONG"}` }, nil, http.MethodPost, http.StatusUnauthorized},
		{"empty token", func(string) string { return `{"token":""}` }, nil, http.MethodPost, http.StatusBadRequest},
		{"no token field", func(string) string { return `{}` }, nil, http.MethodPost, http.StatusBadRequest},
		{"malformed json", func(string) string { return `{"token":` }, nil, http.MethodPost, http.StatusBadRequest},
		{"oversize body", func(string) string { return `{"token":"` + strings.Repeat("A", maxPairBodyBytes*2) + `"}` }, nil, http.MethodPost, http.StatusBadRequest},
		{"GET", func(tok string) string { return "" }, nil, http.MethodGet, http.StatusMethodNotAllowed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, tok, _ := testAuth(t, ModeToken)
			req := httptest.NewRequest(tc.method, "/api/pair", strings.NewReader(tc.body(tok)))
			req.RemoteAddr = "192.168.1.5:54321"
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			a.PairHandler().ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.want, rec.Body)
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Error("a failed pairing must not set a session cookie")
			}
		})
	}
}

func TestPairIsRateLimited(t *testing.T) {
	a, tok, c := testAuth(t, ModeToken, func(p *Params) {
		p.PairBurst = 3
		p.PairRefillInterval = 10 * time.Second
	})

	for i := 0; i < 3; i++ {
		if rec := pair(t, a, `{"token":"WRONG"}`, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, rec.Code)
		}
	}

	rec := pair(t, a, `{"token":"WRONG"}`, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 once the burst is spent", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a 429 must say when to come back")
	}

	// Even the correct token is refused while the client is limited: an
	// attacker who finally guesses right should not get a free pass, and the
	// honest user is one refill interval away.
	if rec := pair(t, a, `{"token":"`+tok+`"}`, nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 while limited", rec.Code)
	}

	c.advance(10 * time.Second)
	if rec := pair(t, a, `{"token":"`+tok+`"}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after the refill; body %s", rec.Code, rec.Body)
	}
}

func TestPairIsAConflictWhenAuthIsOff(t *testing.T) {
	// Issuing a cookie that means nothing would make a client believe it is
	// authenticated when authentication is switched off.
	a, _, _ := testAuth(t, ModeNone)
	rec := pair(t, a, `{"token":"anything"}`, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("no cookie must be issued when authentication is disabled")
	}
}
