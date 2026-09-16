package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"net/http"
	"strconv"
	"strings"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
	"github.com/IniTechInc/dinnerDecider/linux/internal/scan"
	"github.com/IniTechInc/dinnerDecider/linux/internal/store"
	"github.com/IniTechInc/dinnerDecider/linux/internal/vision"
)

// ScanManager is the API's view of the scan registry.
//
// An interface so a handler test can stand up the admission and error paths
// without an inference backend. *scan.Manager satisfies it.
type ScanManager interface {
	ActiveScanID() (string, bool)
	Start(ctx context.Context, photos []scan.Photo) (*scan.Scan, error)
	Get(id string) (*scan.Scan, bool)
	Snapshot(id string) (scan.Snapshot, bool)
	List() []scan.Snapshot
	Cancel(id string) error
	Forget(ctx context.Context, id string) error
}

// scanAPI serves the photo staging and scan lifecycle routes.
type scanAPI struct {
	base
	scans  ScanManager
	photos *store.PhotoStore
	db     *store.DB
}

// Error codes this group returns.
const (
	// CodeScanInProgress is the 409 that carries the running scan's id.
	CodeScanInProgress = "scan_in_progress"
	// CodeTooManyScans is the registry-full 429.
	CodeTooManyScans = "too_many_scans"
	// CodeTooManySubscribers is the event-stream 503.
	CodeTooManySubscribers = "too_many_subscribers"
)

// handleUploadPhotos stages photos for the next scan.
//
// Every accepted image is normalised HERE, once, at the boundary: EXIF
// orientation baked in, the longest side capped at vision.CaptureMaxSide, and
// re-encoded as JPEG. That is Stage 0 of shared/pipeline/scan-pipeline.md moved
// forward from the scan to the upload, which is a port decision worth stating.
// The reason is memory: a scan that re-read eight original 24 MP frames would
// decode ~90 MB each on a box whose whole point is that it has no headroom.
// Normalising at the boundary bounds what is on disk and what a scan start has
// to hold, and it bakes the rotation once rather than on every read.
func (a *scanAPI) handleUploadPhotos(w http.ResponseWriter, r *http.Request) {
	uploads, err := a.limits.ReadUploads(w, r)
	if err != nil {
		writeLimitError(w, err)
		return
	}

	ctx := r.Context()
	staged := make([]store.Photo, 0, len(uploads))
	for _, upload := range uploads {
		normalized := vision.DownscaleCapture(upload.Image)
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, normalized, &jpeg.Options{Quality: CaptureJPEGQuality}); err != nil {
			writeInternal(a.logf, w, "re-encoding an uploaded photo", err)
			return
		}
		bounds := normalized.Bounds()
		photo, err := a.photos.Stage(ctx, buf.Bytes(), store.PhotoMeta{
			Width:    bounds.Dx(),
			Height:   bounds.Dy(),
			MimeType: "image/jpeg",
		})
		if err != nil {
			writeInternal(a.logf, w, "staging an uploaded photo", err)
			return
		}
		staged = append(staged, photo)
	}

	writeJSON(w, http.StatusCreated, map[string]any{"photos": staged})
}

// handleDeletePhoto removes a staged photo.
//
// Removal is by UUID and a stale id is a no-op rather than a trap: the store's
// Remove swallows ErrNotFound for exactly that reason, so a client that deleted
// the same photo twice, or that is working from a list the scan already
// consumed, gets 204 rather than an error it cannot act on.
func (a *scanAPI) handleDeletePhoto(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Name the photo to delete.")
		return
	}
	if err := a.photos.Remove(r.Context(), id); err != nil {
		writeInternal(a.logf, w, "deleting a staged photo", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// startScanRequest names the staged photos to scan. An absent or empty list
// means every staged photo, which is what the web UI sends.
type startScanRequest struct {
	PhotoIDs []string `json:"photoIds"`
}

// handleStartScan is the admission point.
//
// ADMISSION, plan section 0.1 layer 2. If a scan is already running this
// answers 409 with the running scan's id, so the client ATTACHES to it rather
// than starting a second one.
//
// Two reasons, either of which is sufficient. Memory: two concurrent scans on
// an 8 GiB box with a 4-5 GiB working set is precisely the event the whole
// design exists to prevent. Progress: with one llama-server slot and a
// capacity-1 inference gate, the second scan would make no progress at all
// while pinning its photos in the registry, so the user would wait minutes
// behind an opaque spinner for a scan that had not started. Serialising at the
// front door and making it VISIBLE is the better failure.
//
// Recipe generation is deliberately NOT admitted through here. Nesting is
// contractual (sessions nest, activeSessionCount can be 2), it is one short
// text-only call, and it queues on the layer-1 gate. That is the case the gate
// exists for.
func (a *scanAPI) handleStartScan(w http.ResponseWriter, r *http.Request) {
	if activeID, running := a.scans.ActiveScanID(); running {
		a.writeScanInProgress(w, activeID)
		return
	}

	var req startScanRequest
	if err := decodeOptionalJSON(w, r, &req); err != nil {
		writeLimitError(w, err)
		return
	}

	ctx := r.Context()
	rows, err := a.resolvePhotos(ctx, req.PhotoIDs)
	if err != nil {
		writeLimitError(w, err)
		return
	}
	if len(rows) == 0 {
		writeError(w, http.StatusBadRequest, CodeNoFiles, "Add at least one photo before starting a scan.")
		return
	}
	if len(rows) > a.limits.MaxFilesPerRequest {
		writeError(w, http.StatusRequestEntityTooLarge, CodeTooManyFiles,
			fmt.Sprintf("A scan takes at most %d photos.", a.limits.MaxFilesPerRequest))
		return
	}

	photos := make([]scan.Photo, 0, len(rows))
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		data, err := a.photos.ReadBytes(ctx, row.ID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found",
					"One of those photos is no longer here. Take it again.")
				return
			}
			writeInternal(a.logf, w, "reading a staged photo", err)
			return
		}
		img, err := vision.DecodeNormalized(data)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeBadImage,
				"One of those photos could not be decoded. Take it again.")
			return
		}
		photos = append(photos, scan.Photo{ID: row.ID, Image: vision.DownscaleCapture(img)})
		ids = append(ids, row.ID)
	}

	s, err := a.scans.Start(ctx, photos)
	if err != nil {
		a.writeStartError(w, err)
		return
	}

	// Attached AFTER Start, because Start is what mints the id. The window
	// between the two is microseconds against a scan measured in minutes, and
	// the cost of losing it is that the photos stay staged until the retention
	// sweep rather than being swept with the scan. Losing a photo would be
	// worse, so the attach never fails the request.
	if err := a.photos.AttachToScan(ctx, s.ID(), ids); err != nil {
		a.logf("[API] Could not attach photos to scan %s: %v", s.ID(), err)
	}

	writeJSON(w, http.StatusAccepted, s.Snapshot())
}

// resolvePhotos turns the request's id list into photo rows, or returns every
// staged photo when the list is absent.
func (a *scanAPI) resolvePhotos(ctx context.Context, ids []string) ([]store.Photo, error) {
	if len(ids) == 0 {
		rows, err := a.photos.ListStaged(ctx)
		if err != nil {
			return nil, &LimitError{Status: http.StatusInternalServerError, Code: "internal", Message: "Could not list the staged photos."}
		}
		return rows, nil
	}
	rows := make([]store.Photo, 0, len(ids))
	for _, id := range ids {
		row, err := a.photos.Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil, &LimitError{
				Status:  http.StatusNotFound,
				Code:    "not_found",
				Message: "One of those photos is no longer here. Take it again.",
			}
		}
		if err != nil {
			return nil, &LimitError{Status: http.StatusInternalServerError, Code: "internal", Message: "Could not read the staged photos."}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (a *scanAPI) writeScanInProgress(w http.ResponseWriter, activeID string) {
	writeJSON(w, http.StatusConflict, errorBody{
		Error:        CodeScanInProgress,
		Message:      "A scan is already running. Attach to that one rather than starting a second.",
		ActiveScanID: activeID,
	})
}

// writeStartError maps the manager's typed refusals onto status codes.
func (a *scanAPI) writeStartError(w http.ResponseWriter, err error) {
	var inProgress *scan.ScanInProgressError
	if errors.As(err, &inProgress) {
		// The manager lost the same race the handler's own check covers, and
		// it knows the winning id.
		a.writeScanInProgress(w, inProgress.ActiveScanID)
		return
	}
	if errors.Is(err, scan.ErrScanInProgress) {
		activeID, _ := a.scans.ActiveScanID()
		a.writeScanInProgress(w, activeID)
		return
	}
	if errors.Is(err, scan.ErrTooManyScans) {
		writeRetryAfter(w, http.StatusTooManyRequests, CodeTooManyScans,
			fmt.Sprintf("This service keeps at most %d scans at a time. Confirm or discard one and try again.",
				a.limits.MaxConcurrentScans), ScanRetryAfter)
		return
	}
	if errors.Is(err, scan.ErrManagerClosed) {
		writeRetryAfter(w, http.StatusServiceUnavailable, "shutting_down",
			"The service is shutting down.", ScanRetryAfter)
		return
	}
	writeInternal(a.logf, w, "starting a scan", err)
}

// handleScanSnapshot returns one scan's whole observable state: phase (one of
// the six), progress, items, inferenceFailures and abortReason.
func (a *scanAPI) handleScanSnapshot(w http.ResponseWriter, r *http.Request) {
	snapshot, ok := a.scans.Snapshot(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "There is no scan with that id. It may have been swept.")
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

// handleScanEvents streams one scan.
//
// A subscriber attaching or detaching NEVER affects the scan. A phone locking
// its screen, a tab closing and Wi-Fi dropping are all just a subscriber
// leaving, and none of them may kill a scan four minutes into a four and a half
// minute run. Cancellation is explicit and has its own route.
func (a *scanAPI) handleScanEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := a.scans.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "There is no scan with that id. It may have been swept.")
		return
	}

	// Per-stream cap first: it is a read, so a refusal costs nothing.
	if s.Subscribers() >= a.limits.MaxSSEPerScan {
		writeRetryAfter(w, http.StatusServiceUnavailable, CodeTooManySubscribers,
			fmt.Sprintf("That scan already has the maximum of %d live listeners.", a.limits.MaxSSEPerScan),
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

	// Subscribe takes the snapshot and the live tail under one lock, so a
	// reconnecting client cannot fall into the gap between them. Last-Event-ID
	// needs no replay buffer here for the same reason every frame carries a
	// FULL snapshot: the newest frame is strictly more informative than any
	// frame it missed.
	snapshot, events, detach := s.Subscribe()
	defer detach()

	stream, err := newSSEStream(w, SSEClientRetry)
	if err != nil {
		a.logf("[API] Could not open the event stream for scan %s: %v", id, err)
		return
	}
	if resumeFrom := strings.TrimSpace(r.Header.Get("Last-Event-ID")); resumeFrom != "" {
		a.logf("[API] Scan %s stream resumed from event %s; sending a full snapshot", id, resumeFrom)
	}
	if err := stream.send(scanFrame(scan.Event{Seq: snapshot.Seq, Kind: "snapshot", Snapshot: snapshot})); err != nil {
		return
	}
	if snapshot.Terminal {
		// Nothing more will ever arrive. Ending the response is kinder than
		// holding a slot open for ten minutes to say nothing.
		return
	}

	_ = pumpSSE(r.Context(), stream, events, scanFrame, a.limits.SSEIdleDeadline, a.limits.SSEKeepalive)
}

func scanFrame(event scan.Event) sseFrame {
	return sseFrame{ID: strconv.FormatUint(event.Seq, 10), Event: string(event.Kind), Data: event}
}

// handleDeleteScan cancels a running scan, or discards a finished one.
//
// One route, two meanings, chosen by the scan's own state rather than by a
// query parameter: from the client's side both are "I am done with this scan",
// and the difference is only whether there is still something to stop. A
// discard sweeps the photos, which is the retention promise being kept rather
// than described.
func (a *scanAPI) handleDeleteScan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := a.scans.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "There is no scan with that id.")
		return
	}
	if !s.Terminal() {
		if err := a.scans.Cancel(id); err != nil {
			if errors.Is(err, scan.ErrNoSuchScan) {
				writeError(w, http.StatusNotFound, "not_found", "There is no scan with that id.")
				return
			}
			writeInternal(a.logf, w, "cancelling a scan", err)
			return
		}
		writeJSON(w, http.StatusAccepted, s.Snapshot())
		return
	}
	if err := a.scans.Forget(r.Context(), id); err != nil && !errors.Is(err, scan.ErrNoSuchScan) {
		writeInternal(a.logf, w, "discarding a scan", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// confirmRequest is the review screen's output: the items the user kept, with
// whatever edits they made.
type confirmRequest struct {
	Items []confirmItem `json:"items"`
}

type confirmItem struct {
	Name          string  `json:"name"`
	Brand         *string `json:"brand"`
	Category      string  `json:"category"`
	Quantity      int     `json:"quantity"`
	IsStaple      bool    `json:"isStaple"`
	SourcePhotoID *string `json:"sourcePhotoID"`
}

// handleConfirmScan writes the reviewed items into inventory and retires the
// scan.
//
// The plan is DERIVED by contract.ConfirmPlan and APPLIED by the store. This
// handler does neither job: it maps the request onto the planner's inputs and
// hands the result over. Re-deriving "insert or increment" here from whatever
// happens to be in the table would be a second implementation of a rule that
// already has one, and the second implementation is the one that drifts.
func (a *scanAPI) handleConfirmScan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := a.scans.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "There is no scan with that id.")
		return
	}
	if !s.Terminal() {
		writeError(w, http.StatusConflict, CodeScanInProgress, "That scan is still running. Wait for it to finish or cancel it.")
		return
	}

	var req confirmRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeLimitError(w, err)
		return
	}

	ctx := r.Context()
	entries := make([]contract.ConfirmEntry, 0, len(req.Items))
	rows := make([]store.ConfirmItem, 0, len(req.Items))
	for _, item := range req.Items {
		quantity := item.Quantity
		if quantity <= 0 {
			// The review screen confirms one of a thing unless the user says
			// otherwise; a zero here is an omitted field, not a request to
			// store nothing.
			quantity = 1
		}
		entries = append(entries, contract.ConfirmEntry{
			Key:      contract.MergeKey(item.Name, item.Brand),
			Quantity: quantity,
		})
		rows = append(rows, store.ConfirmItem{
			Name:          item.Name,
			Brand:         item.Brand,
			Category:      contract.FromModelString(item.Category),
			SourcePhotoID: item.SourcePhotoID,
			IsStaple:      item.IsStaple,
		})
	}

	if len(entries) > 0 {
		stock, err := a.db.MergeKeyStock(ctx)
		if err != nil {
			writeInternal(a.logf, w, "reading the inventory before a confirm", err)
			return
		}
		if err := a.db.ApplyConfirmPlan(ctx, contract.ConfirmPlan(entries, stock), rows); err != nil {
			writeInternal(a.logf, w, "applying a confirm plan", err)
			return
		}
	}

	// Retiring the scan sweeps its photos. These are photos of the inside of
	// someone's home, so the moment they have served their purpose they go.
	if err := a.scans.Forget(ctx, id); err != nil && !errors.Is(err, scan.ErrNoSuchScan) {
		a.logf("[API] Could not retire scan %s after a confirm: %v", id, err)
	}

	inventory, err := a.db.ListInventoryItems(ctx, a.inventoryOrder(ctx))
	if err != nil {
		writeInternal(a.logf, w, "reading the inventory after a confirm", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"confirmed": len(entries),
		"inventory": inventory,
	})
}

// inventoryOrder reads the user's saved sort, falling back to the store's
// default when it has never been set or holds something unrecognised.
func (a *scanAPI) inventoryOrder(ctx context.Context) store.InventoryOrder {
	value, err := a.db.SettingOr(ctx, store.SettingInventorySortOrder, string(store.OrderByCategory))
	if err != nil {
		return store.OrderByCategory
	}
	return inventoryOrderFrom(value)
}

// decodeOptionalJSON decodes a body that the client may legitimately omit.
//
// POST /api/scan with no body means "scan everything staged", which is the
// common case from the web UI, so an absent body is not an error there.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.ContentLength == 0 {
		return nil
	}
	err := decodeJSON(w, r, dst)
	var limitErr *LimitError
	if errors.As(err, &limitErr) && strings.Contains(limitErr.Message, "no body") {
		return nil
	}
	return err
}
