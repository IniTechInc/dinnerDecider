package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// THE CSP TEST. The zero-CDN rule is stated in the plan and would otherwise be
// a rule in a document. This is what makes it enforced by the browser at
// runtime, so the day somebody adds a CDN script tag the page breaks loudly
// instead of quietly reaching out of the house.
func TestSecurityHeadersAreOnEveryResponse(t *testing.T) {
	h := newHarness(t)

	for _, target := range []string{"/healthz", "/api/status", "/api/inventory", "/nope"} {
		w := h.do(http.MethodGet, target, nil)
		got := w.Header().Get("Content-Security-Policy")
		if got != ContentSecurityPolicy {
			t.Errorf("%s: CSP = %q, want %q", target, got, ContentSecurityPolicy)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: nosniff header missing", target)
		}
		if w.Header().Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: frame options = %q", target, w.Header().Get("X-Frame-Options"))
		}
		if w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: referrer policy = %q", target, w.Header().Get("Referrer-Policy"))
		}
	}
}

func TestContentSecurityPolicyIsExactlyTheContractedString(t *testing.T) {
	const want = "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'"
	if ContentSecurityPolicy != want {
		t.Fatalf("CSP = %q, want %q", ContentSecurityPolicy, want)
	}
	// The relaxations that are there are there for a reason, and the one that
	// must never appear is a script relaxation.
	if strings.Contains(ContentSecurityPolicy, "script-src") {
		t.Fatal("the policy must not relax script-src")
	}
}

// The whole route table, asserted as a table. A route that quietly stops being
// registered is otherwise only noticed by whoever was using it.
func TestRouteTableIsComplete(t *testing.T) {
	h := newHarness(t)
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/"},
		{http.MethodGet, "/healthz"},
		{http.MethodGet, "/ca.crt"},
		{http.MethodPost, "/api/pair"},
		{http.MethodGet, "/api/status"},
		{http.MethodPost, "/api/photos"},
		{http.MethodDelete, "/api/photos/abc"},
		{http.MethodPost, "/api/scan"},
		{http.MethodGet, "/api/scan/abc"},
		{http.MethodGet, "/api/scan/abc/events"},
		{http.MethodDelete, "/api/scan/abc"},
		{http.MethodPost, "/api/scan/abc/confirm"},
		{http.MethodGet, "/api/inventory"},
		{http.MethodPost, "/api/inventory"},
		{http.MethodPatch, "/api/inventory/abc"},
		{http.MethodDelete, "/api/inventory/abc"},
		{http.MethodPost, "/api/cook"},
		{http.MethodPost, "/api/recipes"},
		{http.MethodGet, "/api/shopping"},
		{http.MethodPost, "/api/shopping"},
		{http.MethodGet, "/api/shopping/export"},
		{http.MethodPatch, "/api/shopping/abc"},
		{http.MethodDelete, "/api/shopping/abc"},
		{http.MethodGet, "/api/settings"},
		{http.MethodPut, "/api/settings"},
		{http.MethodGet, "/api/taste-profile"},
		{http.MethodPut, "/api/taste-profile"},
		{http.MethodGet, "/api/model"},
		{http.MethodPost, "/api/model/start"},
		{http.MethodGet, "/api/model/events"},
		{http.MethodGet, "/api/kroger/status"},
	}
	for _, route := range routes {
		_, pattern := h.Router.mux.Handler(mustRequest(t, route.method, route.path))
		if pattern == "" || pattern == "/api/" || pattern == "GET /" && route.path != "/" {
			t.Errorf("%s %s is not registered (matched pattern %q)", route.method, route.path, pattern)
		}
	}
}

func mustRequest(t *testing.T, method, path string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(method, "http://example.invalid"+path, nil)
	if err != nil {
		t.Fatalf("building a request: %v", err)
	}
	return r
}

// A mistyped API path must answer JSON, not the HTML shell. A client that gets
// HTML back reports it as a baffling JSON syntax error.
func TestUnknownAPIPathAnswersJSON(t *testing.T) {
	h := newHarness(t)
	w := h.do(http.MethodGet, "/api/nope", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type = %q, want JSON", ct)
	}
	body := decodeBody[errorBody](t, w)
	if body.Error != "not_found" {
		t.Fatalf("error = %q", body.Error)
	}
}

// The mux answers 405 with Allow for a known path reached the wrong way, which
// is the Go 1.22 pattern syntax earning its keep.
func TestWrongMethodIsMethodNotAllowed(t *testing.T) {
	h := newHarness(t)
	w := h.do(http.MethodDelete, "/api/settings", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
	if allow := w.Header().Get("Allow"); !strings.Contains(allow, http.MethodPut) {
		t.Fatalf("Allow = %q, want it to include PUT", allow)
	}
}

func TestHealthzIsCheapAndDoesNotDependOnTheModel(t *testing.T) {
	h := newHarness(t)
	w := h.do(http.MethodGet, "/healthz", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := decodeBody[map[string]any](t, w)
	if body["ok"] != true {
		t.Fatalf("body = %v", body)
	}
}

func TestCACertIsHonestWhenThereIsNoTLS(t *testing.T) {
	h := newHarness(t)
	w := h.do(http.MethodGet, "/ca.crt", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// Status must say demo mode when the model files are absent, which on a test
// box they always are. Never present canned data as real recognition.
func TestStatusReportsDemoModeWhenTheModelIsNotInstalled(t *testing.T) {
	h := newHarness(t)
	w := h.do(http.MethodGet, "/api/status", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := decodeBody[statusResponse](t, w)
	if !body.Model.DemoMode {
		t.Fatal("demoMode should be true with no model files installed")
	}
	if body.Model.DemoReason == "" {
		t.Fatal("demo mode must carry a reason")
	}
	if body.Model.Installed.Present {
		t.Fatal("no model pair should resolve on a test box")
	}
	if body.OCRMode == "" {
		t.Fatal("the OCR mode must be reported")
	}
	if body.Memory.Known {
		t.Fatal("memory should be reported as unknown when it cannot be read")
	}
	if len(body.Phases) != 6 {
		t.Fatalf("scanPhases = %v, want the six terminal and running phases", body.Phases)
	}
	var sawFailed bool
	for _, phase := range body.Phases {
		if phase == "failed" {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Fatal("failed must be one of the published phases: it is the whole point of the split")
	}
	if body.Limits.MaxRequestBytes != MaxRequestBytes {
		t.Fatalf("limits are not published: %+v", body.Limits)
	}
}

func TestStatusCarriesTheActiveScanID(t *testing.T) {
	model := &fakeModel{block: make(chan struct{})}
	h := newHarness(t, func(o *harnessOptions) { o.model = model })
	defer close(model.block)

	snapshot := h.startScan()
	waitFor(t, "the scan to be admitted", func() bool {
		id, running := h.Manager.ActiveScanID()
		return running && id == snapshot.ID
	})

	body := decodeBody[statusResponse](t, h.do(http.MethodGet, "/api/status", nil))
	if !body.Scan.Running || body.Scan.ActiveScanID != snapshot.ID {
		t.Fatalf("status scan = %+v, want the running scan %s", body.Scan, snapshot.ID)
	}
}

// The server's deadlines are set, not left at Go's unlimited defaults. Asserted
// on the real http.Server the service runs.
func TestServerDeadlinesAreSet(t *testing.T) {
	h := newHarness(t)
	srv := h.Router.Server("127.0.0.1:0")
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 120*time.Second {
		t.Errorf("ReadTimeout = %v, want 120s", srv.ReadTimeout)
	}
	if srv.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout = %v, want 120s", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 1<<20 {
		t.Errorf("MaxHeaderBytes = %d, want 1 MiB", srv.MaxHeaderBytes)
	}
	if srv.WriteTimeout == 0 {
		t.Error("WriteTimeout must be set; the two stream routes clear it per connection instead")
	}
}

// A slow-body client is cut off by ReadTimeout rather than holding a goroutine
// and a descriptor forever. This is the slow-loris case, run against the real
// server with the real timeout plumbing and a test-sized deadline.
func TestSlowBodyClientHitsTheReadTimeout(t *testing.T) {
	h := newHarness(t)
	h.Router.timeouts = Timeouts{
		ReadHeader:     200 * time.Millisecond,
		Read:           300 * time.Millisecond,
		Write:          2 * time.Second,
		Idle:           time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	srv := h.Router.Server("")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Full headers announcing a body, then a body sent one byte at a time and
	// never finished.
	_, err = fmt.Fprintf(conn, "POST /api/settings HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 4096\r\n\r\n")
	if err != nil {
		t.Fatalf("writing headers: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 4096 {
			if _, err := conn.Write([]byte("a")); err != nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	reader := bufio.NewReader(conn)
	// The server either answers (408 or 4xx) or closes; both mean the slow
	// client did not get to hold the connection open indefinitely.
	line, err := reader.ReadString('\n')
	if err == nil && strings.Contains(line, "200") {
		t.Fatalf("a never-finished body was served: %q", line)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the slow writer was never stopped, so the read deadline did not bind")
	}
}

func TestNewRefusesAnIncompleteWiring(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Fatal("a router with no database should be refused")
	}
}

// The error envelope is one shape across the whole service, including the one
// internal/auth writes, so a client has one error handler.
func TestErrorEnvelopeShape(t *testing.T) {
	h := newHarness(t)
	w := h.do(http.MethodGet, "/api/nope", nil)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if _, ok := raw["error"]; !ok {
		t.Fatal("every error carries a machine-readable error code")
	}
	if _, ok := raw["message"]; !ok {
		t.Fatal("every error carries a human-readable message")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("API responses must not be cached: they carry a household's inventory")
	}
}
