package api

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
	"github.com/IniTechInc/dinnerDecider/linux/internal/llm"
	"github.com/IniTechInc/dinnerDecider/linux/internal/scan"
	"github.com/IniTechInc/dinnerDecider/linux/internal/store"
	"github.com/IniTechInc/dinnerDecider/linux/internal/vision"
)

// fakeModel is the engine's model, under a test's control.
//
// The blocking hook is what makes a scan observably "running" for as long as a
// test needs, which is what the admission and streaming tests are about.
type fakeModel struct {
	mu       sync.Mutex
	calls    int
	sessions int

	// block, when non-nil, holds every Infer until it is closed.
	block chan struct{}
	// respond builds the answer for call n. Nil means one plausible item.
	respond func(n int, req llm.Request) (llm.Response, error)
}

func (m *fakeModel) BeginSession() {
	m.mu.Lock()
	m.sessions++
	m.mu.Unlock()
}

func (m *fakeModel) EndSession() contract.UnloadDecision {
	m.mu.Lock()
	m.sessions--
	m.mu.Unlock()
	return contract.UnloadNothingToDo
}

func (m *fakeModel) EnsureLoaded(context.Context) error { return nil }

func (m *fakeModel) Recover(context.Context, string) error { return nil }

func (m *fakeModel) Infer(ctx context.Context, req llm.Request) (llm.Response, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	block := m.block
	respond := m.respond
	m.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	}
	if respond != nil {
		return respond(n, req)
	}
	item := contract.IdentifiedItem{Name: "Whole Milk", Category: "dairy", Confidence: 0.9}
	switch req.Kind {
	case llm.KindIdentifyAllItems:
		return llm.Response{Kind: req.Kind, Items: []contract.IdentifiedItem{item}}, nil
	case llm.KindIdentifyItem:
		return llm.Response{Kind: req.Kind, Item: item}, nil
	default:
		return llm.Response{Kind: req.Kind}, nil
	}
}

func (m *fakeModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// fakeVision keeps the image work out of the tests: one crop per photo, and a
// model input that is a byte slice rather than a real encode.
type fakeVision struct{}

func (fakeVision) Crops(img image.Image) vision.CropResult {
	return vision.CropResult{
		Images:         []image.Image{img},
		Rects:          []vision.Rect{{X: 0, Y: 0, W: 4, H: 4}},
		IsTileFallback: true,
	}
}

func (fakeVision) ModelInput(image.Image) ([]byte, error) { return []byte{0xFF, 0xD8, 0xFF}, nil }

// fakeRecipes is a RecipeGenerator under a test's control.
type fakeRecipes struct {
	mu     sync.Mutex
	calls  int
	bundle contract.RecipeBundleResponse
	err    error
	// onCall runs inside the call, for the overlap test.
	onCall func()
}

func (f *fakeRecipes) Recipes(context.Context, string) (contract.RecipeBundleResponse, error) {
	f.mu.Lock()
	f.calls++
	onCall := f.onCall
	bundle, err := f.bundle, f.err
	f.mu.Unlock()
	if onCall != nil {
		onCall()
	}
	return bundle, err
}

// stubScans is a ScanManager whose every answer a test dictates, for the error
// mappings a real manager cannot easily be driven into.
type stubScans struct {
	activeID  string
	running   bool
	startErr  error
	startScan *scan.Scan
}

func (s *stubScans) ActiveScanID() (string, bool) { return s.activeID, s.running }

func (s *stubScans) Start(context.Context, []scan.Photo) (*scan.Scan, error) {
	if s.startErr != nil {
		return nil, s.startErr
	}
	return s.startScan, nil
}

func (s *stubScans) Get(string) (*scan.Scan, bool)         { return nil, false }
func (s *stubScans) Snapshot(string) (scan.Snapshot, bool) { return scan.Snapshot{}, false }
func (s *stubScans) List() []scan.Snapshot                 { return nil }
func (s *stubScans) Cancel(string) error                   { return scan.ErrNoSuchScan }
func (s *stubScans) Forget(context.Context, string) error  { return scan.ErrNoSuchScan }

// harness is a whole service, wired the way the composition root wires it, over
// a temporary state directory.
type harness struct {
	t       *testing.T
	Router  *Router
	DB      *store.DB
	Photos  *store.PhotoStore
	Manager *scan.Manager
	Model   *fakeModel
	Recipes *fakeRecipes
	Hub     *ModelHub
	Limits  Limits
	dir     string
}

type harnessOptions struct {
	limits  Limits
	scans   ScanManager
	recipes RecipeGenerator
	kroger  KrogerConfig
	model   *fakeModel
}

func newHarness(t *testing.T, opts ...func(*harnessOptions)) *harness {
	t.Helper()

	options := harnessOptions{limits: DefaultLimits()}
	for _, apply := range opts {
		apply(&options)
	}

	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "dinnerdecider.db"))
	if err != nil {
		t.Fatalf("opening the test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrating the test database: %v", err)
	}
	photos, err := store.NewPhotoStore(db, dir)
	if err != nil {
		t.Fatalf("creating the test photo store: %v", err)
	}

	m := options.model
	if m == nil {
		m = &fakeModel{}
	}
	engine := &scan.Engine{
		Model:  m,
		Vision: fakeVision{},
		Logf:   func(string, ...any) {},
	}
	manager := scan.NewManager(engine, scan.ManagerOptions{
		Photos:   photos,
		MaxScans: options.limits.withDefaults().MaxConcurrentScans,
		Logf:     func(string, ...any) {},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})

	recipes := options.recipes
	fake, _ := recipes.(*fakeRecipes)
	if recipes == nil {
		fake = &fakeRecipes{}
		recipes = fake
	}

	var scans ScanManager = manager
	if options.scans != nil {
		scans = options.scans
	}

	hub := NewModelHub()
	t.Cleanup(hub.Close)

	rt, err := New(Deps{
		DB:          db,
		Photos:      photos,
		Scans:       scans,
		Recipes:     recipes,
		ModelEvents: hub,
		Kroger:      options.kroger,
		Limits:      options.limits,
		OCRMode:     vision.OCRModeNone,
		Memory:      func() (llm.MemoryStatus, error) { return llm.MemoryStatus{}, errNoMemoryInfo },
		Logf:        func(string, ...any) {},
		Now:         time.Now,
	})
	if err != nil {
		t.Fatalf("building the router: %v", err)
	}

	return &harness{
		t: t, Router: rt, DB: db, Photos: photos, Manager: manager,
		Model: m, Recipes: fake, Hub: hub, Limits: options.limits.withDefaults(), dir: dir,
	}
}

var errNoMemoryInfo = errNoMemory{}

type errNoMemory struct{}

func (errNoMemory) Error() string { return "no memory information on this platform" }

// do runs one request through the whole handler chain, headers and all.
func (h *harness) do(method, target string, body []byte, headers ...[2]string) *httptest.ResponseRecorder {
	h.t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	r := httptest.NewRequest(method, target, reader)
	if body == nil {
		r.ContentLength = 0
	}
	for _, header := range headers {
		r.Header.Set(header[0], header[1])
	}
	w := httptest.NewRecorder()
	h.Router.Handler().ServeHTTP(w, r)
	return w
}

// doJSON runs a request with a JSON body.
func (h *harness) doJSON(method, target string, payload any) *httptest.ResponseRecorder {
	h.t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		h.t.Fatalf("encoding the request body: %v", err)
	}
	return h.do(method, target, body, [2]string{"Content-Type", "application/json"})
}

// decode reads a JSON response body.
func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding the response %q: %v", w.Body.String(), err)
	}
	return out
}

// uploadPhoto stages one real photo through the real upload route.
func (h *harness) uploadPhoto() string {
	h.t.Helper()
	contentType, body := multipartBody(h.t, map[string][]byte{"a.jpg": testJPEG(h.t)})
	w := h.do(http.MethodPost, "/api/photos", body, [2]string{"Content-Type", contentType})
	if w.Code != http.StatusCreated {
		h.t.Fatalf("staging a photo: status %d, body %s", w.Code, w.Body.String())
	}
	resp := decodeBody[struct {
		Photos []store.Photo `json:"photos"`
	}](h.t, w)
	if len(resp.Photos) != 1 {
		h.t.Fatalf("expected one staged photo, got %d", len(resp.Photos))
	}
	return resp.Photos[0].ID
}

// startScan stages a photo and starts a scan, returning the snapshot.
func (h *harness) startScan() scan.Snapshot {
	h.t.Helper()
	h.uploadPhoto()
	w := h.do(http.MethodPost, "/api/scan", nil)
	if w.Code != http.StatusAccepted {
		h.t.Fatalf("starting a scan: status %d, body %s", w.Code, w.Body.String())
	}
	return decodeBody[scan.Snapshot](h.t, w)
}

func testJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := range 8 {
		for y := range 8 {
			img.Set(x, y, color.RGBA{R: uint8(x * 30), G: uint8(y * 30), B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encoding the test jpeg: %v", err)
	}
	return buf.Bytes()
}

// waitFor polls until cond is true or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
