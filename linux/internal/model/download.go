package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/IniTechInc/dinnerDecider/linux/internal/model/downloadlogic"
)

// Downloading the model pair, headless.
//
// This mirrors DinnerDecider/Services/ModelDownloadService.swift with the UI
// and the background URLSession removed. Three things differ, all deliberate:
//
//   - Resume is explicit. iOS gets it free from URLSession resume data; here
//     the transfer writes <name>.part and asks for "Range: bytes=N-" on the way
//     back in, then fsyncs and renames atomically.
//   - Verification is sha256, not Content-Length. iOS checks only the length,
//     which catches a truncated transfer and nothing else. The digests for both
//     files are recorded in docs/PORTING_NOTES.md § Staged model weights, so
//     checking them is free and strictly stronger. A half-trusted 3.5 GB file
//     is never left on disk: a mismatch deletes the partial file and reports
//     what went wrong.
//   - Every state change goes through downloadlogic.Next, including a duplicate
//     start. See start and dispatch.
//
// Order is projector first, weights second. That is not an accident of
// declaration order: the projector is 534 MB against 3.46 GB, so the user gets
// a fast first success instead of staring at one bar for an hour.

const (
	// partSuffix names the in-progress file. It sits beside the destination so
	// the rename that finishes the download is same-filesystem and therefore
	// atomic, and so it never doubles peak disk usage.
	partSuffix = ".part"

	// progressNotifyInterval throttles observer callbacks. Every 32 KiB copy
	// chunk updates the counters, but a 3.5 GB file is ~110k chunks and nothing
	// downstream wants 110k SSE frames.
	progressNotifyInterval = 250 * time.Millisecond

	// modelDirPerm and modelFilePerm: model weights are not secrets, unlike the
	// photos and the pairing token, so they stay readable by an operator.
	modelDirPerm  os.FileMode = 0o755
	modelFilePerm os.FileMode = 0o644
)

// Spec describes one downloadable model file.
type Spec struct {
	// ID is the stable identifier used in snapshots and logs.
	ID string
	// FileName is the destination name. It MUST be what Locator resolves, or
	// the runtime will not pick the file up.
	FileName string
	// URL is the public, no-auth direct download.
	URL string
	// ExpectedBytes is the exact size, from shared/runtime/model-files.md.
	// Zero means unknown, which SizeIsValid treats as invalid.
	ExpectedBytes int64
	// SHA256 is the lowercase hex digest from docs/PORTING_NOTES.md. Empty
	// disables digest verification, which only tests should ever do.
	SHA256 string
	// DisplayName is the human name used in failure reasons.
	DisplayName string
	// ApproxBytes is a pre-flight progress estimate only, never a verification
	// constant.
	ApproxBytes int64
}

// DefaultSpecs returns the two real files, in download order: the small vision
// projector first so the first success arrives in minutes rather than hours,
// then the weights.
//
// The URLs are verbatim from ModelDownloadFile.remoteURL. The sizes and digests
// are the ones recorded for the staged files, so a download that matches them
// is byte-identical to the pair the shipped configuration was tuned against.
func DefaultSpecs() []Spec {
	return []Spec{
		{
			ID:            "mmproj",
			FileName:      PreferredMmprojName,
			URL:           "https://huggingface.co/ggml-org/gemma-4-E4B-it-GGUF/resolve/main/mmproj-gemma-4-E4B-it-Q8_0.gguf",
			ExpectedBytes: 559_874_816,
			SHA256:        "197f49a93027f9843772bd24a6a9e0be2a32a788de5a3def330e9c585d86edd1",
			DisplayName:   "Vision projector",
			ApproxBytes:   534_000_000,
		},
		{
			ID:            "weights",
			FileName:      DefaultModelPreference[0],
			URL:           "https://huggingface.co/unsloth/gemma-4-E4B-it-GGUF/resolve/main/gemma-4-E4B-it-UD-IQ3_XXS.gguf",
			ExpectedBytes: 3_717_115_008,
			SHA256:        "dd0b5b2f3ba8b4cbeb35154484fc67729183c8f24f53e43319d806ec9fffc6a7",
			DisplayName:   "Gemma 4 weights",
			ApproxBytes:   3_460_000_000,
		},
	}
}

// FileProgress is byte progress for a single file.
type FileProgress struct {
	Received int64
	Total    int64
}

// Fraction is the per-file completion, 0 when the total is unknown and clamped
// at 1. A port of FileDownloadProgress.fraction.
func (p FileProgress) Fraction() float64 {
	return downloadlogic.OverallFraction(p.Received, p.Total)
}

// FileSnapshot is one file's progress as handed to an observer.
type FileSnapshot struct {
	ID          string
	DisplayName string
	Received    int64
	Total       int64
	Fraction    float64
}

// Snapshot is the whole download's observable state. It is a value, so an
// observer can hold it without racing the downloader.
type Snapshot struct {
	State    downloadlogic.State
	Files    []FileSnapshot
	Received int64
	Total    int64
	Fraction float64
	// NeedsMeteredConfirmation is set when a start was refused because the link
	// is marked metered. The caller confirms with StartConfirmingMetered.
	NeedsMeteredConfirmation bool
}

// Options configures a Downloader.
type Options struct {
	// Dir is the destination directory, normally $DD_STATE_DIR/models so the
	// Locator finds the result with no further wiring. Required.
	Dir string
	// Specs defaults to DefaultSpecs.
	Specs []Spec
	// Client defaults to a client with NO overall timeout. An
	// http.Client.Timeout covers the body read as well as the handshake, so any
	// value large enough for 3.5 GB on a slow link is not a timeout at all, and
	// any sane-looking value guarantees failure. Staleness is bounded by the
	// transport's dial and response-header timeouts instead.
	Client *http.Client
	// Metered marks the link as metered (--metered / DD_METERED), which is the
	// Linux analogue of the iOS cellular check. Defaults to false.
	Metered bool
	// OnUpdate, when set, is called with a fresh snapshot on every state change
	// and periodically during transfer. It is called from the transfer
	// goroutine and must not block.
	OnUpdate func(Snapshot)
	// FreeBytes defaults to FreeDiskBytes and exists so tests can simulate a
	// full disk.
	FreeBytes func(path string) (int64, error)
}

// Downloader fetches the model pair into a directory, resumably.
//
// It is safe for concurrent use. At most one transfer goroutine exists at a
// time, and at most one writer ever holds a given .part file; see dispatch.
type Downloader struct {
	dir       string
	specs     []Spec
	client    *http.Client
	metered   bool
	onUpdate  func(Snapshot)
	freeBytes func(string) (int64, error)

	mu                sync.Mutex
	state             downloadlogic.State
	progress          map[string]FileProgress
	needsConfirmation bool
	confirmedMetered  bool
	cancel            context.CancelFunc
	workerDone        chan struct{}

	// Observability for tests. Two writers on one .part would only show up as a
	// checksum failure after a multi-gigabyte transfer, so the invariant is
	// asserted directly instead.
	workerStarts atomic.Int64
	partOpens    atomic.Int64
}

// NewDownloader creates a downloader and reflects what is already on disk: if
// both files are present it starts in the done state, exactly as the iOS
// service does at init.
//
// Presence is a size check, not a digest check. Re-hashing 4 GB at every start
// would cost minutes on an SD card for a file this code already verified once.
func NewDownloader(opts Options) (*Downloader, error) {
	if opts.Dir == "" {
		return nil, errors.New("model: download directory is required")
	}
	if err := os.MkdirAll(opts.Dir, modelDirPerm); err != nil {
		return nil, fmt.Errorf("model: create %s: %w", opts.Dir, err)
	}
	specs := opts.Specs
	if specs == nil {
		specs = DefaultSpecs()
	}
	client := opts.Client
	if client == nil {
		client = defaultClient()
	}
	free := opts.FreeBytes
	if free == nil {
		free = FreeDiskBytes
	}

	d := &Downloader{
		dir:       opts.Dir,
		specs:     specs,
		client:    client,
		metered:   opts.Metered,
		onUpdate:  opts.OnUpdate,
		freeBytes: free,
		state:     downloadlogic.NotStarted(),
		progress:  make(map[string]FileProgress, len(specs)),
	}
	for _, spec := range specs {
		total := spec.ExpectedBytes
		if total <= 0 {
			total = spec.ApproxBytes
		}
		received := int64(0)
		if d.isPresent(spec) {
			received = total
		}
		d.progress[spec.ID] = FileProgress{Received: received, Total: total}
	}
	if d.allFilesPresentLocked() {
		d.state = downloadlogic.Done()
	}
	return d, nil
}

func defaultClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			IdleConnTimeout:       90 * time.Second,
		},
	}
}

// Snapshot returns the current observable state.
func (d *Downloader) Snapshot() Snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshotLocked()
}

func (d *Downloader) snapshotLocked() Snapshot {
	snap := Snapshot{
		State:                    d.state,
		Files:                    make([]FileSnapshot, 0, len(d.specs)),
		NeedsMeteredConfirmation: d.needsConfirmation,
	}
	for _, spec := range d.specs {
		p := d.progress[spec.ID]
		snap.Files = append(snap.Files, FileSnapshot{
			ID:          spec.ID,
			DisplayName: spec.DisplayName,
			Received:    p.Received,
			Total:       p.Total,
			Fraction:    p.Fraction(),
		})
		snap.Received += p.Received
		snap.Total += p.Total
	}
	snap.Fraction = downloadlogic.OverallFraction(snap.Received, snap.Total)
	return snap
}

// State returns the current download state.
func (d *Downloader) State() downloadlogic.State {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

// Start begins, or continues, the download.
//
// A second Start while a download is already running is a no-op, and it is a
// no-op because downloadlogic.Next has no (downloading, start) pairing, NOT
// because of a guard here. The transfer goroutine is launched only on an actual
// transition into the downloading state, so there is exactly one writer on a
// .part file no matter how many times this is called. That matters because two
// interleaved writers would produce a file that fails its sha256 after
// transferring 3.5 GB, which is the worst possible moment to find out.
func (d *Downloader) Start() { d.begin(downloadlogic.StartEvent(), false) }

// StartConfirmingMetered is Start with the metered-link confirmation given, the
// analogue of the iOS "download over cellular?" confirmation.
func (d *Downloader) StartConfirmingMetered() { d.begin(downloadlogic.StartEvent(), true) }

// Retry restarts after a failure. It re-runs the same preflight a start runs
// and reuses the metered confirmation the operator already gave, so a retry
// cannot sneak past a gate a start would have stopped at. The partial file is
// kept when it is still usable as a resume point, and was already deleted when
// it was not.
func (d *Downloader) Retry() {
	d.mu.Lock()
	confirmed := d.confirmedMetered
	d.mu.Unlock()
	d.begin(downloadlogic.RetryEvent(), confirmed)
}

// Pause stops the in-flight transfer, keeping the .part file so Resume
// continues from where it stopped rather than from zero.
func (d *Downloader) Pause() { d.dispatch(downloadlogic.PauseEvent()) }

// Resume continues a paused transfer.
func (d *Downloader) Resume() { d.dispatch(downloadlogic.ResumeEvent()) }

// begin runs the preflight gates and then hands the event to the state machine.
func (d *Downloader) begin(event downloadlogic.Event, confirmedMetered bool) {
	d.mu.Lock()
	d.needsConfirmation = false
	d.confirmedMetered = confirmedMetered
	alreadyDownloading := d.state.Kind == downloadlogic.StateDownloading
	allPresent := d.allFilesPresentLocked()
	dir, metered := d.dir, d.metered
	d.mu.Unlock()

	// Everything is already here. Guarded on not-downloading so that a stray
	// duplicate start can never reach past the state machine.
	if allPresent && !alreadyDownloading {
		d.setState(downloadlogic.Done())
		return
	}

	if free, err := d.freeBytes(dir); err == nil && !downloadlogic.HasEnoughDiskSpace(free) {
		needGB := float64(downloadlogic.RequiredFreeBytes) / 1_000_000_000
		d.dispatch(downloadlogic.FailedEvent(fmt.Sprintf(
			"Not enough free space. Free up about %.1f GB and start the download again.", needGB)))
		return
	}

	if !downloadlogic.MayStartDownload(metered, confirmedMetered) {
		d.mu.Lock()
		d.needsConfirmation = true
		d.mu.Unlock()
		d.notify()
		return
	}

	d.dispatch(event)
}

// setState applies a state directly, for the two places the iOS service also
// assigns rather than transitions (an already-complete install at start, and
// construction).
func (d *Downloader) setState(state downloadlogic.State) {
	d.mu.Lock()
	changed := d.state != state
	d.state = state
	d.mu.Unlock()
	if changed {
		d.notify()
	}
}

// dispatch runs one event through the pure state machine and reconciles the
// transfer goroutine with the result.
//
// Only two reconciliations exist, both keyed on the TRANSITION rather than on
// the event that caused it:
//
//   - into downloading from anything else: start one transfer goroutine;
//   - out of downloading into paused or failed: cancel the running one.
//
// Leaving downloading for verifying deliberately does not cancel, because that
// transition is the transfer goroutine itself reporting that it finished and it
// is about to verify.
func (d *Downloader) dispatch(event downloadlogic.Event) {
	d.mu.Lock()
	previous := d.state
	next := downloadlogic.Next(previous, event)
	d.state = next

	startWorker := next.Kind == downloadlogic.StateDownloading &&
		previous.Kind != downloadlogic.StateDownloading
	stopWorker := previous.Kind == downloadlogic.StateDownloading &&
		(next.Kind == downloadlogic.StatePaused || next.Kind == downloadlogic.StateFailed)

	if stopWorker && d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	if startWorker {
		ctx, cancel := context.WithCancel(context.Background())
		d.cancel = cancel
		previousDone := d.workerDone
		done := make(chan struct{})
		d.workerDone = done
		d.workerStarts.Add(1)
		go func() {
			defer close(done)
			// Wait out the previous goroutine before touching any .part file.
			// A pause cancels the transfer but does not synchronously reap the
			// goroutine, so without this a quick pause/resume could briefly
			// have two writers on one path.
			if previousDone != nil {
				<-previousDone
			}
			d.run(ctx)
		}()
	}
	changed := previous != next
	d.mu.Unlock()

	if changed || startWorker {
		d.notify()
	}
}

func (d *Downloader) notify() {
	if d.onUpdate == nil {
		return
	}
	d.onUpdate(d.Snapshot())
}

// run transfers every missing file, then verifies. It is the only writer.
func (d *Downloader) run(ctx context.Context) {
	for _, spec := range d.specs {
		if err := d.ensureFile(ctx, spec); err != nil {
			// A cancelled transfer is a pause or a shutdown, not a failure. The
			// .part file stays exactly where it is and the state was already
			// set by whoever cancelled.
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return
			}
			d.dispatch(downloadlogic.FailedEvent(err.Error()))
			return
		}
	}

	d.dispatch(downloadlogic.AllDownloadedEvent())
	if err := d.verifyAll(); err != nil {
		d.dispatch(downloadlogic.VerificationFailedEvent(err.Error()))
		return
	}
	d.dispatch(downloadlogic.VerifiedOKEvent())
}

func (d *Downloader) destPath(spec Spec) string {
	return filepath.Join(d.dir, spec.FileName)
}

// ensureFile is a no-op for a file that is already present and the right size,
// and otherwise downloads it.
func (d *Downloader) ensureFile(ctx context.Context, spec Spec) error {
	dest := d.destPath(spec)
	if size := fileSize(dest); size > 0 {
		if d.isPresent(spec) {
			d.setProgress(spec.ID, size, progressTotal(spec, size))
			d.notify()
			return nil
		}
		// Present but the wrong size. It cannot be resumed (a resume appends,
		// and this file's tail is unknown) and it cannot be trusted, so it goes.
		if err := os.Remove(dest); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("could not remove the damaged %s: %w", spec.DisplayName, err)
		}
	}
	return d.download(ctx, spec, dest)
}

// download transfers one file into <dest>.part, resuming an existing prefix,
// then verifies it and renames it into place.
//
// Failure reasons here are user-facing sentences rather than the usual
// lowercase Go error strings, because they become downloadlogic.State.Reason
// and are the only explanation the operator gets.
func (d *Downloader) download(ctx context.Context, spec Spec, dest string) error {
	part := dest + partSuffix
	hasher := sha256.New()

	offset, err := rehashPrefix(part, hasher)
	if err != nil {
		return fmt.Errorf("Could not read the partial %s: %v", spec.DisplayName, err)
	}
	// A .part longer than the finished file is garbage from an interrupted
	// write against a different remote. Start it over rather than reason about
	// it.
	if spec.ExpectedBytes > 0 && offset > spec.ExpectedBytes {
		if err := os.Remove(part); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("Could not remove the oversized partial %s: %v", spec.DisplayName, err)
		}
		hasher.Reset()
		offset = 0
	}

	file, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, modelFilePerm)
	if err != nil {
		return fmt.Errorf("Could not open the partial %s for writing: %v", spec.DisplayName, err)
	}
	d.partOpens.Add(1)
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("Could not resume %s: %v", spec.DisplayName, err)
	}

	complete := spec.ExpectedBytes > 0 && offset == spec.ExpectedBytes
	if !complete {
		received, err := d.transfer(ctx, spec, file, hasher, offset, part)
		if err != nil {
			// The prefix is the resume point, so make it durable before
			// surfacing the error.
			_ = file.Sync()
			return err
		}
		offset = received
	}

	d.setProgress(spec.ID, offset, progressTotal(spec, offset))

	if spec.ExpectedBytes > 0 && !downloadlogic.SizeIsValid(offset, spec.ExpectedBytes) {
		d.discardPart(spec, part)
		return fmt.Errorf(
			"%s came through as %d bytes instead of %d, so the partial file was deleted. Start the download again.",
			spec.DisplayName, offset, spec.ExpectedBytes)
	}
	if spec.SHA256 != "" {
		sum := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(sum, spec.SHA256) {
			d.discardPart(spec, part)
			return fmt.Errorf(
				"%s failed its checksum (expected %s, got %s), so the partial file was deleted. Start the download again.",
				spec.DisplayName, spec.SHA256, sum)
		}
	}

	if err := file.Sync(); err != nil {
		return fmt.Errorf("Could not flush %s to disk: %v", spec.DisplayName, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("Could not close %s: %v", spec.DisplayName, err)
	}
	closed = true
	if err := os.Rename(part, dest); err != nil {
		return fmt.Errorf("Could not save %s: %v", spec.DisplayName, err)
	}
	// Rename durability is a directory property, not a file one. Without this,
	// a power cut on a Pi can leave a verified file under no name at all.
	syncDir(filepath.Dir(dest))
	return nil
}

// transfer performs the HTTP request and streams the body into the file and the
// hasher at once, returning the file's new total length.
func (d *Downloader) transfer(
	ctx context.Context,
	spec Spec,
	file *os.File,
	hasher io.Writer,
	offset int64,
	part string,
) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.URL, nil)
	if err != nil {
		return 0, fmt.Errorf("Could not request %s: %v", spec.DisplayName, err)
	}
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}

	resp, err := d.client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return 0, context.Canceled
		}
		return 0, fmt.Errorf("The download of %s stopped: %v. Start it again to resume.", spec.DisplayName, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusOK:
		if offset > 0 {
			// The server ignored the Range header and is sending the whole file
			// from byte zero. Appending would corrupt it, so start over.
			if err := file.Truncate(0); err != nil {
				return 0, fmt.Errorf("Could not restart %s: %v", spec.DisplayName, err)
			}
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				return 0, fmt.Errorf("Could not restart %s: %v", spec.DisplayName, err)
			}
			if resetter, ok := hasher.(interface{ Reset() }); ok {
				resetter.Reset()
			}
			offset = 0
		}
	case http.StatusPartialContent:
		// Resuming as asked.
	case http.StatusRequestedRangeNotSatisfiable:
		// The server says our prefix is at or past the end of its file, so the
		// two disagree about what is being downloaded. Throw the prefix away
		// rather than keep resuming into a mismatch.
		d.discardPart(spec, part)
		return 0, fmt.Errorf(
			"The partial %s no longer matches the server, so it was deleted. Start the download again.",
			spec.DisplayName)
	default:
		return 0, fmt.Errorf(
			"The server refused %s with HTTP %d. Start the download again in a moment.",
			spec.DisplayName, resp.StatusCode)
	}

	total := progressTotal(spec, offset)
	if resp.ContentLength > 0 && spec.ExpectedBytes <= 0 {
		total = offset + resp.ContentLength
	}
	d.setProgress(spec.ID, offset, total)

	counter := &progressWriter{downloader: d, id: spec.ID, received: offset, total: total}
	written, err := io.Copy(io.MultiWriter(file, hasher, counter), resp.Body)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return 0, context.Canceled
		}
		// Everything written so far is still a valid prefix, so this is a
		// resumable stop, not a corrupt file.
		return 0, fmt.Errorf(
			"The download of %s stopped after %d bytes: %v. Start it again to resume.",
			spec.DisplayName, offset+written, err)
	}
	return offset + written, nil
}

// discardPart deletes an untrustworthy partial file and resets its progress.
// Never leave a half-trusted multi-gigabyte file on disk: the next start would
// happily resume into it.
func (d *Downloader) discardPart(spec Spec, part string) {
	_ = os.Remove(part)
	d.setProgress(spec.ID, 0, progressTotal(spec, 0))
	d.notify()
}

// verifyAll re-checks what landed on disk.
//
// The digest was already verified while streaming, so this is a presence and
// size check. Re-hashing 4 GB here would add minutes to every completion on an
// SD card and prove nothing the stream did not already prove.
func (d *Downloader) verifyAll() error {
	for _, spec := range d.specs {
		size := fileSize(d.destPath(spec))
		if spec.ExpectedBytes > 0 {
			if !downloadlogic.SizeIsValid(size, spec.ExpectedBytes) {
				_ = os.Remove(d.destPath(spec))
				d.setProgress(spec.ID, 0, progressTotal(spec, 0))
				return fmt.Errorf(
					"%s looked incomplete, so it was removed. Start the download again to finish.",
					spec.DisplayName)
			}
			continue
		}
		if size <= 0 {
			return fmt.Errorf("%s came through empty. Start the download again.", spec.DisplayName)
		}
	}
	return nil
}

func (d *Downloader) setProgress(id string, received, total int64) {
	d.mu.Lock()
	d.progress[id] = FileProgress{Received: received, Total: total}
	d.mu.Unlock()
}

// isPresent reports whether a file is already installed and can be skipped.
//
// iOS can only ask "is it non-empty", because the expected size comes from a
// HEAD request it may not have made yet. Here the exact size is a constant, so
// a truncated leftover from an interrupted transfer is caught at start rather
// than reported as done and handed to llama-server minutes later.
func (d *Downloader) isPresent(spec Spec) bool {
	size := fileSize(d.destPath(spec))
	if size <= 0 {
		return false
	}
	if spec.ExpectedBytes <= 0 {
		return true
	}
	return downloadlogic.SizeIsValid(size, spec.ExpectedBytes)
}

func (d *Downloader) allFilesPresentLocked() bool {
	for _, spec := range d.specs {
		if !d.isPresent(spec) {
			return false
		}
	}
	return true
}

// progressWriter keeps the snapshot counters moving during a transfer without
// flooding observers.
type progressWriter struct {
	downloader *Downloader
	id         string
	received   int64
	total      int64
	lastNotify time.Time
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.received += int64(len(p))
	if w.total > 0 && w.received > w.total {
		w.total = w.received
	}
	w.downloader.setProgress(w.id, w.received, w.total)
	if now := time.Now(); now.Sub(w.lastNotify) >= progressNotifyInterval {
		w.lastNotify = now
		w.downloader.notify()
	}
	return len(p), nil
}

// rehashPrefix feeds an existing .part file through the hasher and returns its
// length, so a resumed download ends with the digest of the whole file and not
// just of the bytes this process happened to fetch. It streams, so a 3 GB
// prefix costs one pass and no memory.
func rehashPrefix(part string, hasher io.Writer) (int64, error) {
	file, err := os.Open(part)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer func() { _ = file.Close() }()
	return io.Copy(hasher, file)
}

func progressTotal(spec Spec, atLeast int64) int64 {
	total := spec.ExpectedBytes
	if total <= 0 {
		total = spec.ApproxBytes
	}
	if total < atLeast {
		total = atLeast
	}
	return total
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0
	}
	return info.Size()
}

func syncDir(dir string) {
	handle, err := os.Open(dir)
	if err != nil {
		return
	}
	defer func() { _ = handle.Close() }()
	_ = handle.Sync()
}

// FreeDiskBytes reports the space available to an unprivileged writer on the
// filesystem holding path. Bavail rather than Bfree: the root reserve is not
// ours to spend.
func FreeDiskBytes(path string) (int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("model: statfs %s: %w", path, err)
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}
