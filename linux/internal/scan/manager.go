package scan

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
)

// Manager owns the running and recently finished scans.
//
// # Scans are owned by the SERVICE, not by an HTTP connection
//
// This is the LAN analogue of scan-pipeline.md § Stage 1's "a cancellable
// background task owned by the MODEL, not by a view": on iOS, navigating away
// or backgrounding the app must not tear a scan down. Here the equivalents are
// a phone locking its screen, a tab closing, and Wi-Fi dropping, and none of
// them may kill a scan that is four minutes into a four-and-a-half minute run.
//
// Concretely: a scan runs under the MANAGER's context, never under a request
// context. SSE subscribers attach and detach freely and the last one leaving
// does NOT cancel. Cancellation is explicit - DELETE /api/scan/{id} - or
// service shutdown. A reconnecting client gets a full snapshot and then the
// live tail, so it loses nothing by having been away.
type Manager struct {
	engine *Engine
	photos PhotoSweeper
	max    int
	logf   func(format string, args ...any)
	newID  func() (string, error)
	now    func() time.Time

	// base is the scan-owning context. Cancelled only by Shutdown.
	base   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	scans  map[string]*Scan
	order  []string
	active string
	closed bool
}

// PhotoSweeper deletes the photos belonging to a scan.
//
// Satisfied by *store.PhotoStore without importing it: these are photos of the
// inside of someone's home, so retention is implemented rather than promised,
// and the registry cap sweeps them the moment a scan record is evicted.
type PhotoSweeper interface {
	DeleteForScan(ctx context.Context, scanID string) (int, error)
}

// DefaultMaxScans is the registry cap: one running scan plus one
// finished-but-unconfirmed. The third is refused rather than queued.
//
// Two concurrent scans on an 8 GiB box with a 4-5 GiB working set is precisely
// the memory event this design exists to prevent, and even ignoring memory,
// with one llama-server slot the second scan makes no progress while pinning
// its photos. Serialise at the front door and make it visible.
const DefaultMaxScans = 2

// subscriberBuffer is the per-subscriber queue depth.
//
// Overflow COALESCES rather than blocks or disconnects: every event carries a
// full snapshot, so dropping the oldest and keeping the newest loses nothing a
// later event does not already contain. A slow reader must never be able to
// stall the scan goroutine.
const subscriberBuffer = 32

// Errors a caller distinguishes. ErrScanInProgress is the API's 409 and
// ErrTooManyScans its 429.
var (
	// ErrScanInProgress means a scan is already running. The API answers 409
	// with the running scan's id so the client ATTACHES instead of starting a
	// second one.
	ErrScanInProgress = errors.New("a scan is already running")
	// ErrTooManyScans means the registry is full of scans that are all still
	// running, so nothing can be swept.
	ErrTooManyScans = errors.New("too many scans")
	// ErrNoSuchScan means the id is unknown or has been swept.
	ErrNoSuchScan = errors.New("no such scan")
	// ErrManagerClosed means the service is shutting down.
	ErrManagerClosed = errors.New("the scan manager is shut down")
)

// ScanInProgressError carries the running scan's id, which is the whole point
// of the 409: the client attaches to it rather than retrying blindly.
type ScanInProgressError struct {
	ActiveScanID string
}

// Error implements error.
func (e *ScanInProgressError) Error() string {
	return fmt.Sprintf("a scan is already running (%s)", e.ActiveScanID)
}

// Unwrap lets errors.Is(err, ErrScanInProgress) match.
func (e *ScanInProgressError) Unwrap() error { return ErrScanInProgress }

// ManagerOptions configures a Manager. Every field has a working default.
type ManagerOptions struct {
	// Photos is swept when a scan record is evicted or forgotten. Optional.
	Photos PhotoSweeper
	// MaxScans defaults to DefaultMaxScans.
	MaxScans int
	// BaseContext is the scan-owning context. It MUST NOT be a request
	// context: see the Manager doc comment.
	BaseContext context.Context
	// Logf defaults to log.Printf.
	Logf func(format string, args ...any)
	// NewID defaults to a crypto/rand UUID v4.
	NewID func() (string, error)
	// Now defaults to time.Now.
	Now func() time.Time
}

// NewManager builds a Manager around an engine.
func NewManager(engine *Engine, opts ManagerOptions) *Manager {
	base := opts.BaseContext
	if base == nil {
		base = context.Background()
	}
	base, cancel := context.WithCancel(base)
	m := &Manager{
		engine: engine,
		photos: opts.Photos,
		max:    opts.MaxScans,
		logf:   opts.Logf,
		newID:  opts.NewID,
		now:    opts.Now,
		base:   base,
		cancel: cancel,
		scans:  make(map[string]*Scan),
	}
	if m.max <= 0 {
		m.max = DefaultMaxScans
	}
	if m.logf == nil {
		m.logf = log.Printf
	}
	if m.newID == nil {
		m.newID = newUUID
	}
	if m.now == nil {
		m.now = time.Now
	}
	return m
}

// ActiveScanID reports the running scan, if there is one. It is the API's
// admission check: POST /api/scan answers 409 with this id rather than starting
// a second scan.
func (m *Manager) ActiveScanID() (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active, m.active != ""
}

// Start admits and launches a scan.
//
// ctx bounds the ADMISSION work only - the eviction sweep's database and file
// deletes. It is deliberately NOT the scan's context: a scan outlives the
// request that started it, so the scan runs under the manager's own context.
// Handing a request context to the engine here would make a closing tab kill a
// four-minute scan, which is the exact bug this type exists to prevent.
func (m *Manager) Start(ctx context.Context, photos []Photo) (*Scan, error) {
	// startScan's filter: drop any photo that carries no usable pixel data.
	usable := make([]Photo, 0, len(photos))
	for _, photo := range photos {
		if photo.Image == nil {
			continue
		}
		if b := photo.Image.Bounds(); b.Dx() <= 0 || b.Dy() <= 0 {
			continue
		}
		usable = append(usable, photo)
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrManagerClosed
	}
	if m.active != "" {
		active := m.active
		m.mu.Unlock()
		return nil, &ScanInProgressError{ActiveScanID: active}
	}
	evict, err := m.makeRoomLocked()
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	id, err := m.newID()
	if err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("scan: generating a scan id: %w", err)
	}

	started := m.now()
	scanCtx, cancel := context.WithCancel(m.base)
	s := &Scan{
		id:        id,
		startedAt: started,
		photoIDs:  photoIDs(usable),
		phase:     contract.Scanning,
		progress:  Progress{PhotoCount: len(usable), Stage: StageCropping},
		cancel:    cancel,
		done:      make(chan struct{}),
		subs:      make(map[int]chan Event),
		now:       m.now,
	}
	m.scans[id] = s
	m.order = append(m.order, id)

	// An empty batch is terminal immediately, exactly as startScan sets
	// .empty and returns without opening a model session.
	if len(usable) == 0 {
		m.mu.Unlock()
		m.sweep(ctx, evict)
		s.finish(Result{Phase: contract.Empty})
		return s, nil
	}

	m.active = id
	m.wg.Add(1)
	m.mu.Unlock()
	m.sweep(ctx, evict)

	go func() {
		defer m.wg.Done()
		// The scan's context is scanCtx: the manager's, not the caller's.
		result := m.engine.Run(scanCtx, usable, s)
		m.mu.Lock()
		if m.active == id {
			m.active = ""
		}
		m.mu.Unlock()
		s.finish(result)
		m.logf("[Scan] %s finished: phase=%s items=%d failures=%d reason=%q",
			id, result.Phase, len(result.Items), result.InferenceFailures, result.AbortReason)
	}()
	return s, nil
}

// makeRoomLocked enforces the registry cap by evicting the OLDEST TERMINAL
// scan. A scan that is still running is never evicted; if every slot is running
// the admission fails rather than silently dropping someone's results.
func (m *Manager) makeRoomLocked() (*Scan, error) {
	if len(m.scans) < m.max {
		return nil, nil
	}
	for i, id := range m.order {
		s := m.scans[id]
		if s == nil {
			continue
		}
		if !s.Terminal() {
			continue
		}
		delete(m.scans, id)
		m.order = append(m.order[:i:i], m.order[i+1:]...)
		return s, nil
	}
	return nil, ErrTooManyScans
}

// sweep deletes an evicted scan's photos. Best effort and never fatal: failing
// to delete is worth a log, not a refused scan.
func (m *Manager) sweep(ctx context.Context, s *Scan) {
	if s == nil {
		return
	}
	s.closeSubscribers()
	if m.photos == nil {
		return
	}
	if ctx == nil {
		ctx = m.base
	}
	n, err := m.photos.DeleteForScan(ctx, s.ID())
	if err != nil {
		m.logf("[Scan] Could not sweep the photos for evicted scan %s: %v", s.ID(), err)
		return
	}
	m.logf("[Scan] Evicted scan %s and swept %d photo(s)", s.ID(), n)
}

// Get returns a scan by id.
func (m *Manager) Get(id string) (*Scan, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.scans[id]
	return s, ok
}

// Snapshot is Get plus a snapshot, for GET /api/scan/{id}.
func (m *Manager) Snapshot(id string) (Snapshot, bool) {
	s, ok := m.Get(id)
	if !ok {
		return Snapshot{}, false
	}
	return s.Snapshot(), true
}

// List returns a snapshot of every registered scan, oldest first.
func (m *Manager) List() []Snapshot {
	m.mu.Lock()
	scans := make([]*Scan, 0, len(m.order))
	for _, id := range m.order {
		if s := m.scans[id]; s != nil {
			scans = append(scans, s)
		}
	}
	m.mu.Unlock()
	out := make([]Snapshot, 0, len(scans))
	for _, s := range scans {
		out = append(out, s.Snapshot())
	}
	return out
}

// Cancel cancels a running scan. Cancellation is EXPLICIT: nothing else in this
// package cancels a scan, and in particular no subscriber leaving does.
func (m *Manager) Cancel(id string) error {
	s, ok := m.Get(id)
	if !ok {
		return ErrNoSuchScan
	}
	s.cancel()
	return nil
}

// Forget removes a terminal scan from the registry and sweeps its photos. The
// API calls it on confirm and on discard; a running scan is refused.
func (m *Manager) Forget(ctx context.Context, id string) error {
	m.mu.Lock()
	s, ok := m.scans[id]
	if !ok {
		m.mu.Unlock()
		return ErrNoSuchScan
	}
	if !s.Terminal() {
		m.mu.Unlock()
		return ErrScanInProgress
	}
	delete(m.scans, id)
	for i, existing := range m.order {
		if existing == id {
			m.order = append(m.order[:i:i], m.order[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
	m.sweep(ctx, s)
	return nil
}

// Shutdown cancels every running scan and waits for them to unwind, so their
// deferred EndSession runs and the model is released. Bounded by ctx.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()

	m.cancel()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// --- One scan --------------------------------------------------------------

// Scan is one scan's live state and its subscriber fan-out. It implements
// Observer, so the engine streams straight into it.
type Scan struct {
	id        string
	startedAt time.Time
	photoIDs  []string
	cancel    context.CancelFunc
	done      chan struct{}
	now       func() time.Time

	mu          sync.Mutex
	phase       contract.ScanPhase
	progress    Progress
	items       []contract.IdentifiedItem
	failures    int
	abortReason string
	finishedAt  time.Time
	seq         uint64
	subs        map[int]chan Event
	nextSub     int
	subsClosed  bool
}

// EventKind names what changed. Every event still carries a FULL snapshot, so a
// client that missed one loses nothing.
type EventKind string

const (
	// EventPhase is a phase change, including the terminal one.
	EventPhase EventKind = "phase"
	// EventProgress is a new progress line.
	EventProgress EventKind = "progress"
	// EventItems is a grown item list.
	EventItems EventKind = "items"
)

// Event is one SSE frame.
type Event struct {
	Seq      uint64    `json:"seq"`
	Kind     EventKind `json:"kind"`
	Snapshot Snapshot  `json:"scan"`
}

// Snapshot is a scan's whole observable state: what GET /api/scan/{id} returns
// and what a reconnecting SSE client is handed before the live tail.
type Snapshot struct {
	ID       string                    `json:"id"`
	Phase    contract.ScanPhase        `json:"phase"`
	Progress Progress                  `json:"progress"`
	Items    []contract.IdentifiedItem `json:"items"`
	// InferenceFailures is what makes Failed distinguishable from Empty on the
	// wire, so the UI can stop blaming the user's photo for our failure.
	InferenceFailures int       `json:"inferenceFailures"`
	AbortReason       string    `json:"abortReason,omitempty"`
	PhotoIDs          []string  `json:"photoIds"`
	StartedAt         time.Time `json:"startedAt"`
	FinishedAt        time.Time `json:"finishedAt,omitempty"`
	Terminal          bool      `json:"terminal"`
	Seq               uint64    `json:"seq"`
}

// ID is the scan's UUID.
func (s *Scan) ID() string { return s.id }

// Done closes when the scan reaches a terminal state.
func (s *Scan) Done() <-chan struct{} { return s.done }

// Terminal reports whether the scan has finished, in any of the four ways.
func (s *Scan) Terminal() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return isTerminal(s.phase)
}

func isTerminal(p contract.ScanPhase) bool {
	switch p {
	case contract.Finished, contract.Empty, contract.Failed, contract.Cancelled:
		return true
	default:
		return false
	}
}

// Snapshot returns the scan's whole observable state.
func (s *Scan) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *Scan) snapshotLocked() Snapshot {
	items := make([]contract.IdentifiedItem, len(s.items))
	copy(items, s.items)
	photoIDs := make([]string, len(s.photoIDs))
	copy(photoIDs, s.photoIDs)
	return Snapshot{
		ID:                s.id,
		Phase:             s.phase,
		Progress:          s.progress,
		Items:             items,
		InferenceFailures: s.failures,
		AbortReason:       s.abortReason,
		PhotoIDs:          photoIDs,
		StartedAt:         s.startedAt,
		FinishedAt:        s.finishedAt,
		Terminal:          isTerminal(s.phase),
		Seq:               s.seq,
	}
}

// Subscribe attaches a listener and returns the current snapshot plus the live
// tail, taken together under one lock so nothing is missed between them. That
// is what lets a phone that locked its screen mid-scan reconnect and catch up
// rather than starting again.
//
// Detaching is just calling the returned function. It does NOT cancel the scan,
// no matter how many subscribers are left: cancellation is explicit.
//
// A scan that is already terminal returns its snapshot and a closed channel,
// so a late subscriber needs no special case.
func (s *Scan) Subscribe() (Snapshot, <-chan Event, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.snapshotLocked()
	if s.subsClosed || isTerminal(s.phase) {
		ch := make(chan Event)
		close(ch)
		return snap, ch, func() {}
	}
	id := s.nextSub
	s.nextSub++
	ch := make(chan Event, subscriberBuffer)
	s.subs[id] = ch
	var once sync.Once
	return snap, ch, func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if existing, ok := s.subs[id]; ok {
				delete(s.subs, id)
				close(existing)
			}
		})
	}
}

// Subscribers is the live subscriber count, for the API's per-scan SSE cap.
func (s *Scan) Subscribers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// OnProgress implements Observer.
func (s *Scan) OnProgress(p Progress) {
	s.mu.Lock()
	s.progress = p
	event := s.buildLocked(EventProgress)
	s.mu.Unlock()
	s.broadcast(event)
}

// OnItems implements Observer.
func (s *Scan) OnItems(items []contract.IdentifiedItem) {
	s.mu.Lock()
	s.items = items
	event := s.buildLocked(EventItems)
	s.mu.Unlock()
	s.broadcast(event)
}

// finish records the terminal state and closes the subscriber channels, so an
// SSE handler sees a clean end of stream.
func (s *Scan) finish(result Result) {
	s.mu.Lock()
	if isTerminal(s.phase) {
		s.mu.Unlock()
		return
	}
	s.phase = result.Phase
	if result.Items != nil {
		s.items = result.Items
	}
	s.failures = result.InferenceFailures
	s.abortReason = result.AbortReason
	s.finishedAt = s.now()
	event := s.buildLocked(EventPhase)
	s.mu.Unlock()

	s.broadcast(event)
	s.closeSubscribers()
	// Release the scan's context whatever the exit path, so nothing leaks.
	s.cancelIfSet()
	close(s.done)
}

func (s *Scan) cancelIfSet() {
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *Scan) buildLocked(kind EventKind) Event {
	s.seq++
	snap := s.snapshotLocked()
	return Event{Seq: s.seq, Kind: kind, Snapshot: snap}
}

// broadcast delivers to every subscriber without ever blocking the scan
// goroutine. On a full queue the OLDEST event is dropped and the newest kept:
// every event carries a full snapshot, so the newest is strictly more
// informative than the one it replaces, and the terminal event always lands.
func (s *Scan) broadcast(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- event:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- event:
			default:
			}
		}
	}
}

func (s *Scan) closeSubscribers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subsClosed {
		return
	}
	s.subsClosed = true
	for id, ch := range s.subs {
		delete(s.subs, id)
		close(ch)
	}
}

func photoIDs(photos []Photo) []string {
	out := make([]string, 0, len(photos))
	for _, photo := range photos {
		out = append(out, photo.ID)
	}
	return out
}

// newUUID returns a random RFC 4122 version 4 UUID.
//
// Hand-rolled on purpose: github.com/google/uuid appears in go.sum as an
// indirect of modernc.org/sqlite, and finding it there is not a licence to
// import it. go.mod is frozen, and this is fifteen lines of crypto/rand.
//
// crypto/rand, never math/rand: a scan id addresses a LAN-reachable resource
// that carries photos of the inside of someone's home, so guessable ids are an
// enumeration vector rather than a style preference.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("scan: generating uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
