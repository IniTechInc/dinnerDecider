package scan

import (
	"context"
	"errors"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
	"github.com/IniTechInc/dinnerDecider/linux/internal/llm"
	"github.com/IniTechInc/dinnerDecider/linux/internal/vision"
)

// --- Fakes -----------------------------------------------------------------

// recordedCall is one model call as the fake saw it.
type recordedCall struct {
	Kind   llm.RequestKind
	Prompt string
	Bytes  int
}

// fakeModel is a scripted Model. It records every call, every session and every
// recovery, and answers through a function the test supplies.
type fakeModel struct {
	mu         sync.Mutex
	calls      []recordedCall
	sessions   int
	ends       int
	loads      int
	recoveries int

	// respond answers call n (0-based). nil means "an empty success".
	respond func(n int, req llm.Request) (llm.Response, error)
	// recoverFn answers recovery attempt n (0-based). nil means "it worked".
	recoverFn func(n int) error
	loadErr   error
}

func (m *fakeModel) BeginSession() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions++
}

func (m *fakeModel) EndSession() contract.UnloadDecision {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ends++
	return contract.UnloadNothingToDo
}

func (m *fakeModel) EnsureLoaded(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loads++
	return m.loadErr
}

func (m *fakeModel) Infer(_ context.Context, req llm.Request) (llm.Response, error) {
	m.mu.Lock()
	n := len(m.calls)
	m.calls = append(m.calls, recordedCall{Kind: req.Kind, Prompt: req.Prompt, Bytes: len(req.ImageJPEG)})
	respond := m.respond
	m.mu.Unlock()
	if respond == nil {
		return llm.Response{Kind: req.Kind}, nil
	}
	return respond(n, req)
}

func (m *fakeModel) Recover(context.Context, string) error {
	m.mu.Lock()
	n := m.recoveries
	m.recoveries++
	fn := m.recoverFn
	m.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(n)
}

func (m *fakeModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *fakeModel) kinds() []llm.RequestKind {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]llm.RequestKind, len(m.calls))
	for i, c := range m.calls {
		out[i] = c.Kind
	}
	return out
}

// fakeVision hands back a scripted crop set and a trivial model input, so the
// engine's orchestration is tested without the resampler.
type fakeVision struct {
	result   vision.CropResult
	inputErr error

	mu       sync.Mutex
	prepared int
}

func (v *fakeVision) Crops(image.Image) vision.CropResult { return v.result }

func (v *fakeVision) ModelInput(image.Image) ([]byte, error) {
	v.mu.Lock()
	v.prepared++
	v.mu.Unlock()
	if v.inputErr != nil {
		return nil, v.inputErr
	}
	return []byte{0xFF, 0xD8, 0xFF}, nil
}

// recorder captures the streaming updates the API turns into SSE.
type recorder struct {
	mu       sync.Mutex
	progress []Progress
	items    [][]contract.IdentifiedItem
}

func (r *recorder) OnProgress(p Progress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, p)
}

func (r *recorder) OnItems(items []contract.IdentifiedItem) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, items)
}

func (r *recorder) lastProgress() Progress {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.progress) == 0 {
		return Progress{}
	}
	return r.progress[len(r.progress)-1]
}

// --- Helpers ---------------------------------------------------------------

func testImage() image.Image { return image.NewRGBA(image.Rect(0, 0, 64, 48)) }

func photos(n int) []Photo {
	out := make([]Photo, n)
	for i := range out {
		out[i] = Photo{ID: string(rune('a' + i)), Image: testImage()}
	}
	return out
}

// tileCrops is what Linux always produces: the tile grid, IsTileFallback true.
func tileCrops(n int) vision.CropResult {
	res := vision.CropResult{IsTileFallback: true}
	for i := 0; i < n; i++ {
		res.Images = append(res.Images, testImage())
		res.Rects = append(res.Rects, vision.Rect{X: 0, Y: 0, W: 64, H: 48})
	}
	return res
}

// boxCrops is the detected-box path: IsTileFallback false selects identify-item.
// Dead on Linux today (there is no detector), live the moment one is wired.
func boxCrops(n int) vision.CropResult {
	res := tileCrops(n)
	res.IsTileFallback = false
	return res
}

func item(name string, confidence float64) contract.IdentifiedItem {
	return contract.IdentifiedItem{Name: name, Category: "other", Confidence: confidence}
}

func itemsResponse(items ...contract.IdentifiedItem) func(int, llm.Request) (llm.Response, error) {
	return func(_ int, req llm.Request) (llm.Response, error) {
		if req.Kind == llm.KindIdentifyItem {
			if len(items) == 0 {
				return llm.Response{Kind: req.Kind}, nil
			}
			return llm.Response{Kind: req.Kind, Item: items[0]}, nil
		}
		return llm.Response{Kind: req.Kind, Items: items}, nil
	}
}

func newEngine(model Model, crops vision.CropResult) (*Engine, *fakeVision) {
	fv := &fakeVision{result: crops}
	return &Engine{
		Model:  model,
		Vision: fv,
		Logf:   func(string, ...any) {},
	}, fv
}

// --- Budget and crop capping ----------------------------------------------

// The budget is scan-wide across every photo, not per photo: a three-photo
// batch still gets four calls, and photos after the budget runs out contribute
// nothing at all.
func TestThreePhotoBatchGetsExactlyFourCalls(t *testing.T) {
	model := &fakeModel{respond: itemsResponse(item("Milk", 0.9))}
	engine, _ := newEngine(model, tileCrops(9))

	result := engine.Run(context.Background(), photos(3), nil)

	if got := model.callCount(); got != 4 {
		t.Fatalf("model calls = %d, want exactly 4 scan-wide across 3 photos", got)
	}
	if result.CallsUsed != 4 {
		t.Fatalf("CallsUsed = %d, want 4", result.CallsUsed)
	}
	if result.Phase != contract.Finished {
		t.Fatalf("phase = %v, want finished", result.Phase)
	}
}

// The whole-image pass spends one call, leaving exactly three for crops.
func TestWholeImagePassLeavesThreeCallsForCrops(t *testing.T) {
	model := &fakeModel{respond: itemsResponse(item("Milk", 0.9))}
	engine, _ := newEngine(model, tileCrops(9))

	engine.Run(context.Background(), photos(1), nil)

	kinds := model.kinds()
	if len(kinds) != 4 {
		t.Fatalf("model calls = %d, want 4 (1 whole image + 3 crops)", len(kinds))
	}
	for i, k := range kinds {
		if k != llm.KindIdentifyAllItems {
			t.Fatalf("call %d kind = %q, want identify_all_items for a tile-fallback scan", i, k)
		}
	}
}

// itemsTotal is the CAPPED crop count, so the progress the user sees is what
// will actually be analysed. A real 900x900 photo tiles into 16; after the
// whole-image pass exactly 3 of them are analysed.
func TestItemsTotalIsTheCappedCropCountForASixteenTilePhoto(t *testing.T) {
	photo := image.NewRGBA(image.Rect(0, 0, 900, 900))
	if tiles := len(vision.Crops(photo).Images); tiles != 16 {
		t.Fatalf("precondition: a 900x900 photo must yield 16 tiles, got %d", tiles)
	}

	model := &fakeModel{respond: itemsResponse(item("Milk", 0.9))}
	engine := &Engine{Model: model, Vision: StdVision{}, Logf: func(string, ...any) {}}
	rec := &recorder{}

	engine.Run(context.Background(), []Photo{{ID: "a", Image: photo}}, rec)

	if got := model.callCount(); got != 4 {
		t.Fatalf("model calls = %d, want 4", got)
	}
	last := rec.lastProgress()
	if last.ItemsTotal != 3 {
		t.Fatalf("itemsTotal = %d, want 3 (the capped crop count, not the raw 16)", last.ItemsTotal)
	}
	if last.Stage != StageIdentifying {
		t.Fatalf("stage = %q, want identifying", last.Stage)
	}
}

// --- Per-crop errors never abort ------------------------------------------

// A crop whose identify call throws is logged and counted, and the scan carries
// on. Aborting here is how one bad crop used to lose an entire scan.
func TestThrowingIdentifyItemDoesNotAbortTheScan(t *testing.T) {
	boom := errors.New("transport exploded")
	model := &fakeModel{respond: func(n int, req llm.Request) (llm.Response, error) {
		if n == 1 {
			return llm.Response{}, boom
		}
		if req.Kind == llm.KindIdentifyItem {
			return llm.Response{Kind: req.Kind, Item: item("Eggs", 0.8)}, nil
		}
		return llm.Response{Kind: req.Kind, Items: []contract.IdentifiedItem{item("Milk", 0.9)}}, nil
	}}
	// IsTileFallback false, so the crops take the identify-item branch.
	engine, _ := newEngine(model, boxCrops(9))

	var logged []string
	engine.Logf = func(format string, args ...any) { logged = append(logged, format) }

	result := engine.Run(context.Background(), photos(1), nil)

	if got := model.callCount(); got != 4 {
		t.Fatalf("model calls = %d, want 4: a thrown crop must not abort the scan", got)
	}
	if result.InferenceFailures != 1 {
		t.Fatalf("inferenceFailures = %d, want 1", result.InferenceFailures)
	}
	if result.Phase != contract.Finished {
		t.Fatalf("phase = %v, want finished (items were found alongside the failure)", result.Phase)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items = %d, want 2 (Milk from the whole image, Eggs from the crops)", len(result.Items))
	}
	var sawCropLog bool
	for _, format := range logged {
		if strings.Contains(format, "Crop %d/%d failed") {
			sawCropLog = true
		}
	}
	if !sawCropLog {
		t.Fatal("a swallowed crop failure must still be logged; silence is what made a systemic failure look like an empty fridge")
	}
	if kinds := model.kinds(); kinds[1] != llm.KindIdentifyItem {
		t.Fatalf("crop call kind = %q, want identify_item when IsTileFallback is false", kinds[1])
	}
}

// --- The noise filter ------------------------------------------------------

// Strictly less-than: exactly 0.15 is KEPT, 0.149 is dropped.
func TestConfidenceCutoffIsStrictlyLessThan(t *testing.T) {
	model := &fakeModel{respond: itemsResponse(
		item("Kept At The Boundary", 0.15),
		item("Just Below", 0.149),
	)}
	engine, _ := newEngine(model, tileCrops(0))

	result := engine.Run(context.Background(), photos(1), nil)

	if len(result.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(result.Items))
	}
	if result.Items[0].Name != "Kept At The Boundary" {
		t.Fatalf("kept %q, want the item at exactly 0.15", result.Items[0].Name)
	}
}

// The "unknown" escape hatch is compared case-insensitively.
func TestUnknownIsDroppedCaseInsensitively(t *testing.T) {
	for _, name := range []string{"unknown", "Unknown", "UNKNOWN", "UnKnOwN"} {
		t.Run(name, func(t *testing.T) {
			model := &fakeModel{respond: itemsResponse(item(name, 0.99))}
			engine, _ := newEngine(model, tileCrops(0))

			result := engine.Run(context.Background(), photos(1), nil)

			if len(result.Items) != 0 {
				t.Fatalf("items = %d, want 0: %q is the model's no-item escape hatch", len(result.Items), name)
			}
			if result.Phase != contract.Empty {
				t.Fatalf("phase = %v, want empty", result.Phase)
			}
		})
	}
}

// --- Cancellation ----------------------------------------------------------

// Closing the session is what runs a deferred unload. Skipping it on the
// cancellation path leaves a multi-GB model resident forever.
func TestCancellationStillEndsTheSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	model := &fakeModel{}
	model.respond = func(n int, req llm.Request) (llm.Response, error) {
		if n == 0 {
			cancel()
		}
		return llm.Response{Kind: req.Kind, Items: []contract.IdentifiedItem{item("Milk", 0.9)}}, nil
	}
	engine, _ := newEngine(model, tileCrops(9))

	result := engine.Run(ctx, photos(1), nil)

	if result.Phase != contract.Cancelled {
		t.Fatalf("phase = %v, want cancelled", result.Phase)
	}
	model.mu.Lock()
	sessions, ends := model.sessions, model.ends
	model.mu.Unlock()
	if sessions != 1 || ends != 1 {
		t.Fatalf("sessions begun/ended = %d/%d, want 1/1 on the cancellation path", sessions, ends)
	}
	if got := model.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want 1: cancellation must stop the scan", got)
	}
}

// The session opens BEFORE the load, so pressure arriving during a multi-minute
// cold load is already covered.
func TestSessionOpensBeforeTheLoad(t *testing.T) {
	model := &fakeModel{}
	var order []string
	model.loadErr = nil
	engine, _ := newEngine(model, tileCrops(0))
	engine.Model = &orderedModel{fakeModel: model, record: func(s string) { order = append(order, s) }}

	engine.Run(context.Background(), photos(1), nil)

	if len(order) < 2 || order[0] != "begin_session" || order[1] != "ensure_loaded" {
		t.Fatalf("order = %v, want begin_session before ensure_loaded", order)
	}
	if order[len(order)-1] != "end_session" {
		t.Fatalf("order = %v, want end_session last", order)
	}
}

// orderedModel records the order of the lifecycle calls.
type orderedModel struct {
	*fakeModel
	record func(string)
}

func (m *orderedModel) BeginSession() {
	m.record("begin_session")
	m.fakeModel.BeginSession()
}

func (m *orderedModel) EndSession() contract.UnloadDecision {
	m.record("end_session")
	return m.fakeModel.EndSession()
}

func (m *orderedModel) EnsureLoaded(ctx context.Context) error {
	m.record("ensure_loaded")
	return m.fakeModel.EnsureLoaded(ctx)
}

// --- Failure counting ------------------------------------------------------

// Every call threw: nothing found BECAUSE the model never answered. Failed, not
// Empty, and every failure is counted.
func TestAllCallsThrowingReportsFailedWithFourFailures(t *testing.T) {
	model := &fakeModel{respond: func(int, llm.Request) (llm.Response, error) {
		return llm.Response{}, errors.New("the child died")
	}}
	engine, _ := newEngine(model, tileCrops(9))

	result := engine.Run(context.Background(), photos(1), nil)

	if result.InferenceFailures != 4 {
		t.Fatalf("inferenceFailures = %d, want 4", result.InferenceFailures)
	}
	if result.Phase != contract.Failed {
		t.Fatalf("phase = %v, want failed", result.Phase)
	}
	if result.Phase == contract.Empty {
		t.Fatal("a scan where every call threw must never render as Nothing spotted")
	}
}

// A partial success shows what was found rather than discarding it.
func TestOneItemFoundWithThreeFailuresReportsFinished(t *testing.T) {
	model := &fakeModel{respond: func(n int, req llm.Request) (llm.Response, error) {
		if n == 0 {
			return llm.Response{Kind: req.Kind, Items: []contract.IdentifiedItem{item("Milk", 0.9)}}, nil
		}
		return llm.Response{}, errors.New("boom")
	}}
	engine, _ := newEngine(model, tileCrops(9))

	result := engine.Run(context.Background(), photos(1), nil)

	if result.InferenceFailures != 3 {
		t.Fatalf("inferenceFailures = %d, want 3", result.InferenceFailures)
	}
	if result.Phase != contract.Finished {
		t.Fatalf("phase = %v, want finished: anything found is worth showing", result.Phase)
	}
}

// Every call answered and nothing survived the filters: a genuinely empty
// fridge, and the photo advice is honest.
func TestEverythingFilteredOutReportsEmptyWithNoFailures(t *testing.T) {
	model := &fakeModel{respond: itemsResponse(item("unknown", 0.1), item("Shadow", 0.01))}
	engine, _ := newEngine(model, tileCrops(9))

	result := engine.Run(context.Background(), photos(1), nil)

	if result.InferenceFailures != 0 {
		t.Fatalf("inferenceFailures = %d, want 0: every call answered", result.InferenceFailures)
	}
	if result.Phase != contract.Empty {
		t.Fatalf("phase = %v, want empty", result.Phase)
	}
}

// IdentifyAllItems returns [] with NO error when the response was unreadable.
// An unreadable-but-answered call is not an inference failure; counting it
// manufactures a spurious Failed.
func TestParseFailureEmptyListIsNotAnInferenceFailure(t *testing.T) {
	model := &fakeModel{respond: func(_ int, req llm.Request) (llm.Response, error) {
		// Exactly the asymmetry in the client: an empty slice, no error.
		return llm.Response{Kind: req.Kind, Items: nil}, nil
	}}
	engine, _ := newEngine(model, tileCrops(9))

	result := engine.Run(context.Background(), photos(1), nil)

	if result.InferenceFailures != 0 {
		t.Fatalf("inferenceFailures = %d, want 0 for a parse failure", result.InferenceFailures)
	}
	if result.Phase != contract.Empty {
		t.Fatalf("phase = %v, want empty, NOT failed", result.Phase)
	}
}

// The typed errors, each classified by llm.CountsAsInferenceFailure rather than
// re-derived here.
func TestFailureCountingByErrorType(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		failures int
		phase    contract.ScanPhase
	}{
		{"queue timeout counts", llm.ErrInferenceQueueTimeout, 4, contract.Failed},
		{"epoch changed counts", llm.ErrEpochChanged, 4, contract.Failed},
		{"bad response counts", llm.ErrBadResponse, 4, contract.Failed},
		{"cancelled does not count", llm.ErrCancelled, 0, contract.Empty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := &fakeModel{respond: func(int, llm.Request) (llm.Response, error) {
				return llm.Response{}, tc.err
			}}
			engine, _ := newEngine(model, tileCrops(9))

			result := engine.Run(context.Background(), photos(1), nil)

			if result.InferenceFailures != tc.failures {
				t.Fatalf("inferenceFailures = %d, want %d", result.InferenceFailures, tc.failures)
			}
			if result.Phase != tc.phase {
				t.Fatalf("phase = %v, want %v", result.Phase, tc.phase)
			}
		})
	}
}

// --- ErrModelNotReady: recover, retry, and only then abort -----------------

// It never reached the model, so it is NOT counted. It is not skipped either:
// skip-and-continue ends at TerminalPhase(0, 0) = Empty, the "Nothing spotted"
// lie.
func TestModelNotReadyRunsRecoveryAndRetriesWithoutCounting(t *testing.T) {
	model := &fakeModel{respond: func(n int, req llm.Request) (llm.Response, error) {
		if n == 0 {
			return llm.Response{}, llm.ErrModelNotReady
		}
		return llm.Response{Kind: req.Kind, Items: []contract.IdentifiedItem{item("Milk", 0.9)}}, nil
	}}
	engine, _ := newEngine(model, tileCrops(9))

	result := engine.Run(context.Background(), photos(1), nil)

	model.mu.Lock()
	recoveries := model.recoveries
	model.mu.Unlock()
	if recoveries != 1 {
		t.Fatalf("recoveries = %d, want exactly 1", recoveries)
	}
	if got := model.callCount(); got != 5 {
		t.Fatalf("model calls = %d, want 5: 4 budgeted calls plus one retry of the same call", got)
	}
	if result.InferenceFailures != 0 {
		t.Fatalf("inferenceFailures = %d, want 0: ErrModelNotReady never reached the model", result.InferenceFailures)
	}
	if result.Phase != contract.Finished {
		t.Fatalf("phase = %v, want finished", result.Phase)
	}
	if result.AbortReason != "" {
		t.Fatalf("abortReason = %q, want empty: the retry succeeded", result.AbortReason)
	}
}

// Recovery spent: abort with Failed set DIRECTLY, without consulting
// TerminalPhase, with the reason model_not_ready and the failure count still 0.
func TestModelNotReadyWithRecoverySpentAbortsWithoutConsultingTerminalPhase(t *testing.T) {
	model := &fakeModel{
		respond: func(int, llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrModelNotReady
		},
		recoverFn: func(int) error { return llm.ErrRecoveryExhausted },
	}
	engine, _ := newEngine(model, tileCrops(9))

	consulted := false
	engine.TerminalPhase = func(int, int) contract.ScanPhase {
		consulted = true
		return contract.Empty
	}

	result := engine.Run(context.Background(), photos(1), nil)

	if consulted {
		t.Fatal("an aborted scan must set Failed directly; consulting TerminalPhase(0, 0) yields Empty, the Nothing spotted lie")
	}
	if result.Phase != contract.Failed {
		t.Fatalf("phase = %v, want failed", result.Phase)
	}
	if result.AbortReason != AbortReasonModelNotReady {
		t.Fatalf("abortReason = %q, want %q", result.AbortReason, AbortReasonModelNotReady)
	}
	if result.InferenceFailures != 0 {
		t.Fatalf("inferenceFailures = %d, want 0: an abort never reached the model", result.InferenceFailures)
	}
	if !errors.Is(result.Err, llm.ErrModelNotReady) {
		t.Fatalf("err = %v, want it to wrap llm.ErrModelNotReady", result.Err)
	}
	if got := model.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want 1: the scan aborts rather than burning the budget", got)
	}
	model.mu.Lock()
	ends := model.ends
	model.mu.Unlock()
	if ends != 1 {
		t.Fatalf("EndSession ran %d times on the abort path, want 1", ends)
	}
}

// A retry that comes back not-ready again aborts too.
func TestRetryStillNotReadyAborts(t *testing.T) {
	model := &fakeModel{respond: func(int, llm.Request) (llm.Response, error) {
		return llm.Response{}, llm.ErrModelNotReady
	}}
	engine, _ := newEngine(model, tileCrops(9))

	result := engine.Run(context.Background(), photos(1), nil)

	if result.Phase != contract.Failed || result.AbortReason != AbortReasonModelNotReady {
		t.Fatalf("phase/reason = %v/%q, want failed/model_not_ready", result.Phase, result.AbortReason)
	}
	if got := model.callCount(); got != 2 {
		t.Fatalf("model calls = %d, want 2: the original call plus exactly one retry", got)
	}
}

// Recovery blocked by the MemAvailable preflight is its own abort reason: the
// model stays unloaded so the box can recover, and the UI explains why.
func TestRecoveryBlockedByThePreflightAbortsWithInsufficientMemory(t *testing.T) {
	model := &fakeModel{
		respond: func(int, llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrModelNotReady
		},
		recoverFn: func(int) error { return llm.ErrInsufficientMemory },
	}
	engine, _ := newEngine(model, tileCrops(9))

	result := engine.Run(context.Background(), photos(1), nil)

	if result.Phase != contract.Failed {
		t.Fatalf("phase = %v, want failed", result.Phase)
	}
	if result.AbortReason != AbortReasonInsufficientMemory {
		t.Fatalf("abortReason = %q, want %q", result.AbortReason, AbortReasonInsufficientMemory)
	}
	if result.InferenceFailures != 0 {
		t.Fatalf("inferenceFailures = %d, want 0", result.InferenceFailures)
	}
}

// --- The gated model: the real machine, the real gate ---------------------

// gatedModel is a miniature supervisor built on the REAL pure lifecycle machine
// plus the real capacity-1 gate. It exists so two properties can be tested for
// real rather than mimed: that a session-wide CRITICAL only DEFERS, and that
// concurrent callers never overlap at the model.
type gatedModel struct {
	mu   sync.Mutex
	lc   contract.ModelLifecycle
	gate chan struct{}

	calls      []recordedCall
	active     int
	maxActive  int
	unloads    int
	deferrals  int
	notReady   int
	hold       time.Duration
	insideCall func(n int)
}

func newGatedModel() *gatedModel {
	return &gatedModel{gate: make(chan struct{}, 1)}
}

func (m *gatedModel) BeginSession() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lc.BeginSession()
}

func (m *gatedModel) EndSession() contract.UnloadDecision {
	m.mu.Lock()
	d := m.lc.EndSession()
	m.mu.Unlock()
	m.apply(d)
	return d
}

func (m *gatedModel) EnsureLoaded(context.Context) error {
	m.mu.Lock()
	if m.lc.BeginLoad() == contract.LoadProceed {
		m.lc.FinishLoad(true)
	}
	m.mu.Unlock()
	m.drain()
	return nil
}

func (m *gatedModel) Recover(context.Context, string) error { return nil }

func (m *gatedModel) HandleMemoryPressure(level contract.MemoryPressureLevel) contract.UnloadDecision {
	m.mu.Lock()
	d := m.lc.HandleMemoryPressure(level)
	m.mu.Unlock()
	m.apply(d)
	return d
}

// apply is the one place a decision becomes an action, exactly as the
// supervisor does it: act on the RETURN VALUE, never on the reason.
func (m *gatedModel) apply(d contract.UnloadDecision) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch d {
	case contract.UnloadProceed:
		m.unloads++
		m.lc.FinishUnload()
	case contract.UnloadDeferred:
		m.deferrals++
	}
}

func (m *gatedModel) drain() {
	m.mu.Lock()
	d := m.lc.DrainPendingUnload()
	m.mu.Unlock()
	m.apply(d)
}

func (m *gatedModel) Infer(ctx context.Context, req llm.Request) (llm.Response, error) {
	select {
	case m.gate <- struct{}{}:
	case <-ctx.Done():
		return llm.Response{}, llm.ErrCancelled
	}
	defer func() { <-m.gate }()

	m.mu.Lock()
	if !m.lc.BeginInference() {
		m.notReady++
		m.mu.Unlock()
		return llm.Response{}, llm.ErrModelNotReady
	}
	n := len(m.calls)
	m.calls = append(m.calls, recordedCall{Kind: req.Kind, Prompt: req.Prompt, Bytes: len(req.ImageJPEG)})
	m.active++
	if m.active > m.maxActive {
		m.maxActive = m.active
	}
	hold, inside := m.hold, m.insideCall
	m.mu.Unlock()

	if inside != nil {
		inside(n)
	}
	if hold > 0 {
		time.Sleep(hold)
	}

	m.mu.Lock()
	m.active--
	m.lc.FinishInference()
	m.mu.Unlock()
	m.drain()

	return llm.Response{
		Kind:  req.Kind,
		Item:  item("Eggs", 0.8),
		Items: []contract.IdentifiedItem{item("Milk", 0.9)},
	}, nil
}

// This is the "Nothing spotted" field bug, as a test. A CRITICAL arriving
// mid-scan may only DEFER: freeing the model between two crop inferences is
// what produced "9 items analyzed, nothing recognized".
func TestSessionWideCriticalDefersTheUnloadAndEveryCropStillGetsRealInference(t *testing.T) {
	model := newGatedModel()
	var pressureDecision contract.UnloadDecision
	var unloadsDuringScan int
	model.insideCall = func(n int) {
		if n != 1 {
			return
		}
		// Arrives while the first crop is being identified, mid-session.
		pressureDecision = model.HandleMemoryPressure(contract.PressureCritical)
		model.mu.Lock()
		unloadsDuringScan = model.unloads
		model.mu.Unlock()
	}
	engine, _ := newEngine(model, tileCrops(9))

	result := engine.Run(context.Background(), photos(1), nil)

	if pressureDecision != contract.UnloadDeferred {
		t.Fatalf("CRITICAL mid-scan decided %v, want DEFERRED", pressureDecision)
	}
	if unloadsDuringScan != 0 {
		t.Fatalf("unloads during the scan = %d, want 0", unloadsDuringScan)
	}
	model.mu.Lock()
	calls, unloads, notReady := len(model.calls), model.unloads, model.notReady
	model.mu.Unlock()
	if calls != 4 {
		t.Fatalf("model calls = %d, want 4: every remaining crop must still get real inference", calls)
	}
	if notReady != 0 {
		t.Fatalf("BeginInference returned false %d times, want 0: the model must not be freed mid-scan", notReady)
	}
	if unloads != 1 {
		t.Fatalf("unloads after the scan = %d, want exactly 1 - the deferred unload fires at session end", unloads)
	}
	if result.Phase != contract.Finished {
		t.Fatalf("phase = %v, want finished", result.Phase)
	}
}

// A scan and a recipe generation started concurrently both complete, their model
// calls never overlap, and neither observes ErrModelNotReady. Sessions nest, so
// the recipe call is deliberately not refused while a scan runs: it queues on
// the gate, which is the case the gate exists for.
func TestScanAndRecipeGenerationDoNotOverlap(t *testing.T) {
	model := newGatedModel()
	model.hold = 2 * time.Millisecond
	engine, _ := newEngine(model, tileCrops(9))

	var (
		wg         sync.WaitGroup
		scanResult Result
		recipeErr  error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanResult = engine.Run(context.Background(), photos(1), nil)
	}()
	go func() {
		defer wg.Done()
		_, recipeErr = engine.Recipes(context.Background(), "recipes please")
	}()
	wg.Wait()

	if recipeErr != nil {
		t.Fatalf("recipe generation failed: %v", recipeErr)
	}
	if scanResult.Phase != contract.Finished {
		t.Fatalf("scan phase = %v, want finished", scanResult.Phase)
	}
	model.mu.Lock()
	maxActive, notReady, calls := model.maxActive, model.notReady, len(model.calls)
	model.mu.Unlock()
	if maxActive != 1 {
		t.Fatalf("peak concurrent model calls = %d, want 1: llama-server has one slot by design", maxActive)
	}
	if notReady != 0 {
		t.Fatalf("BeginInference returned false %d times, want 0 under normal concurrent load", notReady)
	}
	if calls != 5 {
		t.Fatalf("model calls = %d, want 5 (4 scan calls plus 1 recipe call)", calls)
	}
	model.mu.Lock()
	sessionsOpen := model.lc.ActiveSessionCount()
	model.mu.Unlock()
	if sessionsOpen != 0 {
		t.Fatalf("active sessions after both finished = %d, want 0", sessionsOpen)
	}
}

// --- Streaming -------------------------------------------------------------

// Results stream: the deduped list is published every time it grows, so a
// four-minute scan is not four minutes of nothing.
func TestResultsStreamAsTheyAreFound(t *testing.T) {
	model := &fakeModel{respond: func(n int, req llm.Request) (llm.Response, error) {
		return llm.Response{Kind: req.Kind, Items: []contract.IdentifiedItem{
			item("Item "+string(rune('A'+n)), 0.9),
		}}, nil
	}}
	engine, _ := newEngine(model, tileCrops(9))
	rec := &recorder{}

	result := engine.Run(context.Background(), photos(1), rec)

	rec.mu.Lock()
	publications := len(rec.items)
	rec.mu.Unlock()
	if publications != 4 {
		t.Fatalf("item publications = %d, want 4, one per call that found something", publications)
	}
	if len(result.Items) != 4 {
		t.Fatalf("items = %d, want 4", len(result.Items))
	}
	rec.mu.Lock()
	first := rec.items[0]
	rec.mu.Unlock()
	if len(first) != 1 {
		t.Fatalf("the first publication had %d items, want 1: results must stream, not arrive at the end", len(first))
	}
}

// A model-input preparation failure is a failure of the CALL, not a silent
// skip: on iOS the tone curve and the downscale live inside the LLM service,
// inside the try.
func TestModelInputFailureIsCountedLikeAnyOtherCallFailure(t *testing.T) {
	model := &fakeModel{}
	engine, fv := newEngine(model, tileCrops(9))
	fv.inputErr = errors.New("encoder exploded")

	result := engine.Run(context.Background(), photos(1), nil)

	if got := model.callCount(); got != 0 {
		t.Fatalf("model calls = %d, want 0: nothing could be prepared", got)
	}
	if result.InferenceFailures != 4 {
		t.Fatalf("inferenceFailures = %d, want 4", result.InferenceFailures)
	}
	if result.Phase != contract.Failed {
		t.Fatalf("phase = %v, want failed, never empty", result.Phase)
	}
}

// A photo with no usable pixel data is skipped without being counted, which is
// what the Swift's normalisation guard does. The manager drops these before the
// scan starts.
func TestPhotoWithoutPixelsIsSkippedNotCounted(t *testing.T) {
	model := &fakeModel{}
	engine, _ := newEngine(model, vision.CropResult{IsTileFallback: true})

	result := engine.Run(context.Background(), []Photo{{ID: "a"}}, nil)

	if result.InferenceFailures != 0 {
		t.Fatalf("inferenceFailures = %d, want 0", result.InferenceFailures)
	}
	if result.Phase != contract.Empty {
		t.Fatalf("phase = %v, want empty", result.Phase)
	}
}

// The budget stays exhausted: a second photo after it ran out contributes
// nothing, and the progress reflects that.
func TestExhaustedBudgetStaysExhausted(t *testing.T) {
	model := &fakeModel{respond: itemsResponse(item("Milk", 0.9))}
	engine, _ := newEngine(model, tileCrops(9))
	rec := &recorder{}

	engine.Run(context.Background(), photos(2), rec)

	if got := model.callCount(); got != 4 {
		t.Fatalf("model calls = %d, want 4", got)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var sawSecondPhoto bool
	for _, p := range rec.progress {
		if p.PhotoIndex == 1 && p.Stage == StageIdentifying {
			sawSecondPhoto = true
		}
		if p.PhotoIndex == 1 && p.Stage == StageCropping && p.ItemsTotal != 0 {
			t.Fatalf("second photo reported itemsTotal %d with an exhausted budget", p.ItemsTotal)
		}
	}
	if sawSecondPhoto {
		t.Fatal("the second photo must not enter the identify loop with an exhausted budget")
	}
}
