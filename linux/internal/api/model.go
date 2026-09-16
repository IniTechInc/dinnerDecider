package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/IniTechInc/dinnerDecider/linux/internal/model"
	"github.com/IniTechInc/dinnerDecider/linux/internal/store"
)

// ModelDownloader is the API's view of the model file downloader.
//
// An interface so a handler test does not have to move 3.5 GB. *model.Downloader
// satisfies it.
type ModelDownloader interface {
	Snapshot() model.Snapshot
	Start()
	StartConfirmingMetered()
	Pause()
	Resume()
	Retry()
}

// ModelHub fans a downloader's progress out to event-stream subscribers.
//
// It exists because model.Downloader reports progress through a single
// OnUpdate callback set at construction, which is the right shape for the
// downloader and the wrong shape for N browser tabs. The service wiring passes
// Publish as that callback; this type does the fan-out.
type ModelHub struct {
	mu      sync.Mutex
	subs    map[int]chan model.Snapshot
	next    int
	last    model.Snapshot
	hasLast bool
	closed  bool
}

// NewModelHub creates an empty hub.
func NewModelHub() *ModelHub {
	return &ModelHub{subs: make(map[int]chan model.Snapshot)}
}

// Publish delivers a snapshot to every subscriber.
//
// It NEVER blocks the caller, which is the downloader's transfer goroutine: on
// a full queue the oldest snapshot is dropped and the newest kept. Every
// snapshot is a complete state, so the newest is strictly more informative than
// the one it replaces, and a slow browser tab cannot stall a 3.5 GB download.
func (h *ModelHub) Publish(snapshot model.Snapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.last = snapshot
	h.hasLast = true
	if h.closed {
		return
	}
	for _, ch := range h.subs {
		select {
		case ch <- snapshot:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- snapshot:
			default:
			}
		}
	}
}

// Subscribe attaches a listener, returning the last known snapshot and the live
// tail together under one lock so nothing falls into the gap between them.
func (h *ModelHub) Subscribe() (model.Snapshot, bool, <-chan model.Snapshot, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		ch := make(chan model.Snapshot)
		close(ch)
		return h.last, h.hasLast, ch, func() {}
	}
	id := h.next
	h.next++
	ch := make(chan model.Snapshot, 8)
	h.subs[id] = ch
	var once sync.Once
	return h.last, h.hasLast, ch, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if existing, ok := h.subs[id]; ok {
				delete(h.subs, id)
				close(existing)
			}
		})
	}
}

// Subscribers is the live listener count, for the per-stream cap.
func (h *ModelHub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Close ends every stream, for shutdown.
func (h *ModelHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for id, ch := range h.subs {
		delete(h.subs, id)
		close(ch)
	}
}

// modelAPI serves the model file routes.
type modelAPI struct {
	base
	downloads ModelDownloader
	hub       *ModelHub
	locator   model.Locator
	db        *store.DB
}

// modelResponse is what GET /api/model returns.
type modelResponse struct {
	// State is the download state machine's current state.
	State string `json:"state"`
	// FailureReason is the plain-language reason behind a failed state.
	FailureReason string `json:"failureReason,omitempty"`
	// NeedsMeteredConfirmation is set when a start was refused because the link
	// is marked metered, the Linux analogue of the iOS cellular check.
	NeedsMeteredConfirmation bool                 `json:"needsMeteredConfirmation"`
	Received                 int64                `json:"received"`
	Total                    int64                `json:"total"`
	Fraction                 float64              `json:"fraction"`
	Files                    []modelFileResponse  `json:"files"`
	Installed                modelInstalledStatus `json:"installed"`
}

type modelFileResponse struct {
	ID          string  `json:"id"`
	DisplayName string  `json:"displayName"`
	Received    int64   `json:"received"`
	Total       int64   `json:"total"`
	Fraction    float64 `json:"fraction"`
}

// modelInstalledStatus is what the filesystem says, which is a different
// question from what the downloader is doing: a hand-copied pair is installed
// without any download ever having run.
type modelInstalledStatus struct {
	// Present is the single condition for running the real model rather than
	// the mock with its demo badge: BOTH files resolve.
	Present bool `json:"present"`
	// WeightsFile and MmprojFile are the resolved basenames, empty when absent.
	WeightsFile string `json:"weightsFile,omitempty"`
	MmprojFile  string `json:"mmprojFile,omitempty"`
	// Available lists every weights file the user could pin in settings,
	// including a fine-tuned build named gemma-4-E4B-it-finetuned-*.gguf.
	Available []string `json:"available"`
	// Selected is the user's pinned filename, empty when there is no pin.
	Selected string `json:"selected,omitempty"`
	// SearchDirectories is where the service looked, highest priority first.
	// Surfaced because "the file is right there" and "the service looked
	// somewhere else" are indistinguishable without it.
	SearchDirectories []string `json:"searchDirectories"`
}

// handleGet reports the download state and what is actually installed.
func (a *modelAPI) handleGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.snapshotResponse(r))
}

func (a *modelAPI) snapshotResponse(r *http.Request) modelResponse {
	var snapshot model.Snapshot
	if a.downloads != nil {
		snapshot = a.downloads.Snapshot()
	}
	return modelResponse{
		State:                    snapshot.State.String(),
		FailureReason:            snapshot.State.Reason,
		NeedsMeteredConfirmation: snapshot.NeedsMeteredConfirmation,
		Received:                 snapshot.Received,
		Total:                    snapshot.Total,
		Fraction:                 snapshot.Fraction,
		Files:                    modelFiles(snapshot),
		Installed:                a.installed(r),
	}
}

func modelFiles(snapshot model.Snapshot) []modelFileResponse {
	out := make([]modelFileResponse, 0, len(snapshot.Files))
	for _, f := range snapshot.Files {
		out = append(out, modelFileResponse{
			ID:          f.ID,
			DisplayName: f.DisplayName,
			Received:    f.Received,
			Total:       f.Total,
			Fraction:    f.Fraction,
		})
	}
	return out
}

func (a *modelAPI) installed(r *http.Request) modelInstalledStatus {
	locator := a.locator
	// The pin lives in the settings table, so it is read per request rather
	// than captured at start-up: a user who picks a fine-tuned build in
	// Settings expects the next answer to reflect it.
	if a.db != nil {
		if pinned, err := a.db.SettingOr(r.Context(), store.SettingSelectedModelFileName, ""); err == nil {
			locator.Preferred = strings.TrimSpace(pinned)
		}
	}
	status := modelInstalledStatus{
		Available:         locator.AvailableModelFileNames(),
		Selected:          locator.Preferred,
		SearchDirectories: locator.SearchDirectories(),
	}
	if path, ok := locator.ModelPath(); ok {
		status.WeightsFile = baseName(path)
	}
	if path, ok := locator.MmprojPath(); ok {
		status.MmprojFile = baseName(path)
	}
	status.Present = status.WeightsFile != "" && status.MmprojFile != ""
	if status.Available == nil {
		status.Available = []string{}
	}
	if status.SearchDirectories == nil {
		status.SearchDirectories = []string{}
	}
	return status
}

func baseName(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// The four download actions.
const (
	actionStart  = "start"
	actionPause  = "pause"
	actionResume = "resume"
	actionRetry  = "retry"
)

// handleAction drives the download state machine.
//
// A DUPLICATE start returns 200 with the current state and starts nothing. That
// is not a guard bolted on here: downloadlogic.Next has no (downloading, start)
// pairing at all, so the transfer goroutine is launched only on a real
// transition into the downloading state. There is therefore exactly one writer
// on a .part file no matter how many times this is called, which matters
// because two interleaved writers produce a file that fails its sha256 after
// transferring 3.5 GB, at the worst possible moment to find out.
func (a *modelAPI) handleAction(w http.ResponseWriter, r *http.Request) {
	if a.downloads == nil {
		writeError(w, http.StatusServiceUnavailable, "downloads_disabled",
			"This service was started without model downloading.")
		return
	}
	action := strings.ToLower(strings.TrimSpace(r.PathValue("action")))
	switch action {
	case actionStart:
		// The metered confirmation is the operator saying "yes, on this link":
		// the Linux analogue of the iOS "download over cellular?" prompt.
		if confirmedMetered(r) {
			a.downloads.StartConfirmingMetered()
		} else {
			a.downloads.Start()
		}
	case actionPause:
		a.downloads.Pause()
	case actionResume:
		a.downloads.Resume()
	case actionRetry:
		a.downloads.Retry()
	default:
		writeError(w, http.StatusNotFound, "not_found",
			fmt.Sprintf("There is no model action called %q. Use start, pause, resume or retry.", action))
		return
	}
	writeJSON(w, http.StatusOK, a.snapshotResponse(r))
}

func confirmedMetered(r *http.Request) bool {
	value := strings.TrimSpace(r.URL.Query().Get("metered"))
	if value == "" {
		return false
	}
	if strings.EqualFold(value, "confirm") || strings.EqualFold(value, "confirmed") {
		return true
	}
	ok, err := strconv.ParseBool(value)
	return err == nil && ok
}

// handleEvents streams download progress.
func (a *modelAPI) handleEvents(w http.ResponseWriter, r *http.Request) {
	if a.hub == nil {
		writeError(w, http.StatusServiceUnavailable, "downloads_disabled",
			"This service was started without model downloading.")
		return
	}
	// The same two caps a scan stream is under, and for the same reason: this
	// route also runs with no write deadline, so nothing in net/http will ever
	// reap an idle one.
	if a.hub.Subscribers() >= a.limits.MaxSSEPerScan {
		writeRetryAfter(w, http.StatusServiceUnavailable, CodeTooManySubscribers,
			fmt.Sprintf("The model stream already has the maximum of %d live listeners.", a.limits.MaxSSEPerScan),
			SSERetryAfter)
		return
	}
	if !a.sse.acquire() {
		writeRetryAfter(w, http.StatusServiceUnavailable, CodeTooManySubscribers,
			fmt.Sprintf("This service is streaming to the maximum of %d listeners.", a.limits.MaxSSETotal),
			SSERetryAfter)
		return
	}
	defer a.sse.release()

	last, hasLast, updates, detach := a.hub.Subscribe()
	defer detach()

	stream, err := newSSEStream(w, SSEClientRetry)
	if err != nil {
		a.logf("[API] Could not open the model event stream: %v", err)
		return
	}
	if hasLast {
		if err := stream.send(modelFrame(last)); err != nil {
			return
		}
	}
	_ = pumpSSE(r.Context(), stream, updates, modelFrame, a.limits.SSEIdleDeadline, a.limits.SSEKeepalive)
}

func modelFrame(snapshot model.Snapshot) sseFrame {
	return sseFrame{
		Event: "model",
		Data: modelResponse{
			State:                    snapshot.State.String(),
			FailureReason:            snapshot.State.Reason,
			NeedsMeteredConfirmation: snapshot.NeedsMeteredConfirmation,
			Received:                 snapshot.Received,
			Total:                    snapshot.Total,
			Fraction:                 snapshot.Fraction,
			Files:                    modelFiles(snapshot),
		},
	}
}
