package scan

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
	"github.com/IniTechInc/dinnerDecider/linux/internal/llm"
)

// fakeSweeper records which scans had their photos deleted.
type fakeSweeper struct {
	mu    sync.Mutex
	swept []string
	err   error
}

func (f *fakeSweeper) DeleteForScan(_ context.Context, scanID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.swept = append(f.swept, scanID)
	return 1, f.err
}

func (f *fakeSweeper) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.swept))
	copy(out, f.swept)
	return out
}

// steppedModel answers one call per token sent on its channel, so a test can
// hold a scan open at a known point.
func steppedModel(steps <-chan struct{}) *fakeModel {
	return &fakeModel{respond: func(_ int, req llm.Request) (llm.Response, error) {
		<-steps
		return llm.Response{Kind: req.Kind, Items: []contract.IdentifiedItem{item("Milk", 0.9)}}, nil
	}}
}

func newTestManager(t *testing.T, model Model) (*Manager, *fakeSweeper) {
	t.Helper()
	engine, _ := newEngine(model, tileCrops(9))
	sweeper := &fakeSweeper{}
	n := 0
	m := NewManager(engine, ManagerOptions{
		Photos: sweeper,
		Logf:   func(string, ...any) {},
		NewID: func() (string, error) {
			n++
			return fmt.Sprintf("scan-%d", n), nil
		},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return m, sweeper
}

func waitDone(t *testing.T, s *Scan) Snapshot {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatalf("scan %s did not finish", s.ID())
	}
	return s.Snapshot()
}

func runScan(t *testing.T, m *Manager) Snapshot {
	t.Helper()
	s, err := m.Start(context.Background(), photos(1))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return waitDone(t, s)
}

// --- Ownership: the service owns the scan, not the connection -------------

// The scan runs under the MANAGER's context. A request context that dies the
// instant the response is written - a closing tab, a dropped Wi-Fi link - must
// not take a four-minute scan with it.
func TestScanIsNotBoundToTheRequestContext(t *testing.T) {
	steps := make(chan struct{}, 4)
	m, _ := newTestManager(t, steppedModel(steps))

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	s, err := m.Start(requestCtx, photos(1))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The client goes away before the first call even returns.
	cancelRequest()
	for i := 0; i < 4; i++ {
		steps <- struct{}{}
	}

	snap := waitDone(t, s)
	if snap.Phase != contract.Finished {
		t.Fatalf("phase = %v, want finished: the scan outlives the request", snap.Phase)
	}
}

// The last subscriber leaving does not cancel. Subscribers attach and detach
// freely; cancellation is explicit.
func TestSubscribersLeavingDoesNotCancelTheScan(t *testing.T) {
	steps := make(chan struct{}, 4)
	m, _ := newTestManager(t, steppedModel(steps))

	s, err := m.Start(context.Background(), photos(1))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, _, unsubA := s.Subscribe()
	_, _, unsubB := s.Subscribe()
	if got := s.Subscribers(); got != 2 {
		t.Fatalf("subscribers = %d, want 2", got)
	}
	unsubA()
	unsubB()
	if got := s.Subscribers(); got != 0 {
		t.Fatalf("subscribers after detaching = %d, want 0", got)
	}

	for i := 0; i < 4; i++ {
		steps <- struct{}{}
	}
	snap := waitDone(t, s)
	if snap.Phase != contract.Finished {
		t.Fatalf("phase = %v, want finished: the last subscriber leaving must not cancel", snap.Phase)
	}
}

// Detaching twice is harmless: an SSE handler unwinding through several defers
// must not panic on a double close.
func TestUnsubscribeIsIdempotent(t *testing.T) {
	steps := make(chan struct{}, 4)
	m, _ := newTestManager(t, steppedModel(steps))
	s, err := m.Start(context.Background(), photos(1))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, _, unsub := s.Subscribe()
	unsub()
	unsub()
	for i := 0; i < 4; i++ {
		steps <- struct{}{}
	}
	waitDone(t, s)
}

// --- Reconnection ----------------------------------------------------------

// A reconnecting client gets a full snapshot and then the live tail, taken
// together under one lock so nothing falls between them.
func TestReconnectingClientGetsSnapshotThenLiveTail(t *testing.T) {
	steps := make(chan struct{})
	m, _ := newTestManager(t, steppedModel(steps))

	s, err := m.Start(context.Background(), photos(1))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Let the whole-image pass land, so there is history to catch up on.
	steps <- struct{}{}
	deadline := time.Now().Add(2 * time.Second)
	for len(s.Snapshot().Items) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	snap, events, unsub := s.Subscribe()
	defer unsub()
	if len(snap.Items) != 1 {
		t.Fatalf("snapshot items = %d, want 1: a reconnecting client must not have to start again", len(snap.Items))
	}
	if snap.Phase != contract.Scanning {
		t.Fatalf("snapshot phase = %v, want scanning", snap.Phase)
	}

	go func() {
		for i := 0; i < 3; i++ {
			steps <- struct{}{}
		}
	}()

	var last Event
	for event := range events {
		last = event
	}
	if last.Kind != EventPhase {
		t.Fatalf("last event kind = %q, want the terminal phase event", last.Kind)
	}
	if last.Snapshot.Phase != contract.Finished {
		t.Fatalf("last event phase = %v, want finished", last.Snapshot.Phase)
	}
	if !last.Snapshot.Terminal {
		t.Fatal("the terminal event must be marked terminal")
	}
	if last.Seq <= snap.Seq {
		t.Fatalf("seq did not advance: snapshot %d, last event %d", snap.Seq, last.Seq)
	}
}

// Subscribing to a scan that already finished returns its snapshot and a closed
// channel, so a late client needs no special case.
func TestSubscribingToATerminalScanReturnsAClosedChannel(t *testing.T) {
	m, _ := newTestManager(t, &fakeModel{respond: itemsResponse(item("Milk", 0.9))})
	snap := runScan(t, m)

	s, ok := m.Get(snap.ID)
	if !ok {
		t.Fatal("the finished scan must stay in the registry until it is confirmed")
	}
	got, events, unsub := s.Subscribe()
	defer unsub()
	if got.Phase != contract.Finished {
		t.Fatalf("phase = %v, want finished", got.Phase)
	}
	select {
	case _, open := <-events:
		if open {
			t.Fatal("a terminal scan must hand back a closed channel")
		}
	case <-time.After(time.Second):
		t.Fatal("the channel of a terminal scan must already be closed")
	}
}

// A subscriber that never reads must not stall the scan goroutine. Events
// coalesce: every one carries a full snapshot, so the newest supersedes what it
// replaces and the terminal event still lands.
//
// The budget is raised well past subscriberBuffer on purpose, so this really
// does overflow rather than merely fitting.
func TestASlowSubscriberDoesNotStallTheScan(t *testing.T) {
	model := &fakeModel{respond: itemsResponse(item("Milk", 0.9))}
	engine, _ := newEngine(model, tileCrops(200))
	engine.CallBudget = 100
	m := NewManager(engine, ManagerOptions{Logf: func(string, ...any) {}})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})

	s, err := m.Start(context.Background(), photos(1))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Attached and never read from until the scan is over.
	_, events, unsub := s.Subscribe()
	defer unsub()

	snap := waitDone(t, s)
	if snap.Phase != contract.Finished {
		t.Fatalf("phase = %v, want finished: a subscriber that never reads must not stall the scan", snap.Phase)
	}
	if model.callCount() != 100 {
		t.Fatalf("model calls = %d, want the full raised budget of 100", model.callCount())
	}
	drained := 0
	var last Event
	for event := range events {
		drained++
		last = event
	}
	if drained > subscriberBuffer {
		t.Fatalf("drained %d events from a queue of %d", drained, subscriberBuffer)
	}
	if last.Snapshot.Phase != contract.Finished {
		t.Fatalf("the terminal event was lost to coalescing: last phase = %v", last.Snapshot.Phase)
	}
	if len(last.Snapshot.Items) == 0 {
		t.Fatal("the surviving event must still carry the full state")
	}
}

// --- Admission -------------------------------------------------------------

// ActiveScanID is the API's 409 check, and it clears when the scan ends.
func TestActiveScanIDTracksTheRunningScan(t *testing.T) {
	steps := make(chan struct{}, 4)
	m, _ := newTestManager(t, steppedModel(steps))

	if _, running := m.ActiveScanID(); running {
		t.Fatal("no scan should be active before one starts")
	}
	s, err := m.Start(context.Background(), photos(1))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	id, running := m.ActiveScanID()
	if !running || id != s.ID() {
		t.Fatalf("ActiveScanID = %q/%v, want %q/true", id, running, s.ID())
	}

	for i := 0; i < 4; i++ {
		steps <- struct{}{}
	}
	waitDone(t, s)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, running := m.ActiveScanID(); !running {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("ActiveScanID must clear once the scan finishes")
}

// A second scan is refused, carrying the running id so the client ATTACHES
// rather than starting a second one behind an opaque spinner.
func TestSecondScanIsRefusedWithTheRunningID(t *testing.T) {
	steps := make(chan struct{}, 4)
	m, _ := newTestManager(t, steppedModel(steps))

	first, err := m.Start(context.Background(), photos(1))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, err = m.Start(context.Background(), photos(1))
	if !errors.Is(err, ErrScanInProgress) {
		t.Fatalf("second Start returned %v, want ErrScanInProgress", err)
	}
	var busy *ScanInProgressError
	if !errors.As(err, &busy) {
		t.Fatalf("error %v does not carry the active scan id", err)
	}
	if busy.ActiveScanID != first.ID() {
		t.Fatalf("ActiveScanID = %q, want %q", busy.ActiveScanID, first.ID())
	}

	for i := 0; i < 4; i++ {
		steps <- struct{}{}
	}
	waitDone(t, first)
}

// The registry cap is one running plus one finished-but-unconfirmed. Starting a
// third evicts the OLDEST TERMINAL scan and sweeps its photos: these are photos
// of the inside of someone's home.
func TestRegistryCapSweepsTheOldestTerminalScan(t *testing.T) {
	m, sweeper := newTestManager(t, &fakeModel{respond: itemsResponse(item("Milk", 0.9))})

	first := runScan(t, m)
	second := runScan(t, m)
	if len(m.List()) != 2 {
		t.Fatalf("registry holds %d scans, want 2", len(m.List()))
	}

	third := runScan(t, m)

	if _, ok := m.Get(first.ID); ok {
		t.Fatalf("scan %s should have been evicted", first.ID)
	}
	if _, ok := m.Get(second.ID); !ok {
		t.Fatalf("scan %s should have survived", second.ID)
	}
	if _, ok := m.Get(third.ID); !ok {
		t.Fatalf("scan %s should be registered", third.ID)
	}
	if swept := sweeper.list(); len(swept) != 1 || swept[0] != first.ID {
		t.Fatalf("swept = %v, want exactly the oldest terminal scan %q", swept, first.ID)
	}
}

// --- Cancellation and cleanup ---------------------------------------------

// Cancellation is explicit, and the cancelled scan reports Cancelled.
func TestCancelIsExplicit(t *testing.T) {
	steps := make(chan struct{}, 4)
	m, _ := newTestManager(t, steppedModel(steps))

	s, err := m.Start(context.Background(), photos(1))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	steps <- struct{}{}
	if err := m.Cancel(s.ID()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	// Unblock anything already waiting so the goroutine can unwind.
	for i := 0; i < 3; i++ {
		select {
		case steps <- struct{}{}:
		default:
		}
	}

	snap := waitDone(t, s)
	if snap.Phase != contract.Cancelled {
		t.Fatalf("phase = %v, want cancelled", snap.Phase)
	}
	if err := m.Cancel("nope"); !errors.Is(err, ErrNoSuchScan) {
		t.Fatalf("Cancel of an unknown id returned %v, want ErrNoSuchScan", err)
	}
}

// Forget is confirm and discard: it drops the record and sweeps the photos. A
// running scan is refused.
func TestForgetSweepsPhotosAndRefusesARunningScan(t *testing.T) {
	steps := make(chan struct{}, 8)
	m, sweeper := newTestManager(t, steppedModel(steps))

	s, err := m.Start(context.Background(), photos(1))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Forget(context.Background(), s.ID()); !errors.Is(err, ErrScanInProgress) {
		t.Fatalf("Forget of a running scan returned %v, want ErrScanInProgress", err)
	}
	for i := 0; i < 4; i++ {
		steps <- struct{}{}
	}
	waitDone(t, s)

	if err := m.Forget(context.Background(), s.ID()); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, ok := m.Get(s.ID()); ok {
		t.Fatal("a forgotten scan must leave the registry")
	}
	if swept := sweeper.list(); len(swept) != 1 || swept[0] != s.ID() {
		t.Fatalf("swept = %v, want %q", swept, s.ID())
	}
	if err := m.Forget(context.Background(), s.ID()); !errors.Is(err, ErrNoSuchScan) {
		t.Fatalf("Forget twice returned %v, want ErrNoSuchScan", err)
	}
}

// An empty batch is terminal immediately and never opens a model session,
// exactly as startScan sets .empty and returns.
func TestEmptyBatchIsEmptyWithoutTouchingTheModel(t *testing.T) {
	model := &fakeModel{}
	m, _ := newTestManager(t, model)

	s, err := m.Start(context.Background(), []Photo{{ID: "a"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	snap := waitDone(t, s)
	if snap.Phase != contract.Empty {
		t.Fatalf("phase = %v, want empty", snap.Phase)
	}
	model.mu.Lock()
	sessions, calls := model.sessions, len(model.calls)
	model.mu.Unlock()
	if sessions != 0 || calls != 0 {
		t.Fatalf("sessions/calls = %d/%d, want 0/0 for a batch with no usable pixels", sessions, calls)
	}
	if _, running := m.ActiveScanID(); running {
		t.Fatal("an immediately terminal scan must not hold the active slot")
	}
}

// blockingModel holds one call in flight until the context is cancelled, so
// shutdown is tested against a genuinely in-flight inference rather than a race.
type blockingModel struct {
	*fakeModel
	entered chan struct{}
	once    sync.Once
}

func (m *blockingModel) Infer(ctx context.Context, req llm.Request) (llm.Response, error) {
	m.mu.Lock()
	m.calls = append(m.calls, recordedCall{Kind: req.Kind, Prompt: req.Prompt, Bytes: len(req.ImageJPEG)})
	m.mu.Unlock()
	m.once.Do(func() { close(m.entered) })
	<-ctx.Done()
	return llm.Response{}, llm.ErrCancelled
}

// Shutdown cancels running scans and waits for them to unwind, so their
// deferred EndSession runs and the model is released.
func TestShutdownCancelsRunningScans(t *testing.T) {
	model := &blockingModel{fakeModel: &fakeModel{}, entered: make(chan struct{})}
	engine, _ := newEngine(model, tileCrops(9))
	m := NewManager(engine, ManagerOptions{Logf: func(string, ...any) {}})

	s, err := m.Start(context.Background(), photos(1))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-model.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the scan never reached the model")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	snap := s.Snapshot()
	if snap.Phase != contract.Cancelled {
		t.Fatalf("phase = %v, want cancelled", snap.Phase)
	}
	if snap.InferenceFailures != 0 {
		t.Fatalf("inferenceFailures = %d, want 0: cancellation is not an inference failure", snap.InferenceFailures)
	}
	model.mu.Lock()
	sessions, ends := model.sessions, model.ends
	model.mu.Unlock()
	if sessions != 1 || ends != 1 {
		t.Fatalf("sessions begun/ended = %d/%d, want 1/1: a cancelled scan must still close its session", sessions, ends)
	}
	if _, err := m.Start(context.Background(), photos(1)); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("Start after shutdown returned %v, want ErrManagerClosed", err)
	}
}

// --- Snapshots -------------------------------------------------------------

// The snapshot carries what the three terminal treatments need to stay
// distinguishable: the phase, the failure count and the abort reason.
func TestSnapshotCarriesTheFailureCountAndAbortReason(t *testing.T) {
	model := &fakeModel{
		respond: func(int, llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrModelNotReady
		},
		recoverFn: func(int) error { return llm.ErrInsufficientMemory },
	}
	m, _ := newTestManager(t, model)

	snap := runScan(t, m)
	if snap.Phase != contract.Failed {
		t.Fatalf("phase = %v, want failed", snap.Phase)
	}
	if snap.AbortReason != AbortReasonInsufficientMemory {
		t.Fatalf("abortReason = %q, want %q", snap.AbortReason, AbortReasonInsufficientMemory)
	}
	if snap.InferenceFailures != 0 {
		t.Fatalf("inferenceFailures = %d, want 0", snap.InferenceFailures)
	}
	if len(snap.PhotoIDs) != 1 {
		t.Fatalf("photoIds = %v, want the one photo that was scanned", snap.PhotoIDs)
	}
	if snap.FinishedAt.IsZero() {
		t.Fatal("a terminal snapshot must carry finishedAt")
	}
}

// Scan ids are UUIDs: a scan addresses a LAN-reachable resource carrying photos
// of someone's home, so they must not be guessable or sequential.
func TestGeneratedScanIDsAreVersion4UUIDs(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id, err := newUUID()
		if err != nil {
			t.Fatalf("newUUID: %v", err)
		}
		if len(id) != 36 {
			t.Fatalf("uuid %q is not 36 characters", id)
		}
		if id[14] != '4' {
			t.Fatalf("uuid %q is not version 4", id)
		}
		switch id[19] {
		case '8', '9', 'a', 'b':
		default:
			t.Fatalf("uuid %q does not carry the RFC 4122 variant", id)
		}
		if seen[id] {
			t.Fatalf("uuid %q repeated", id)
		}
		seen[id] = true
	}
}
