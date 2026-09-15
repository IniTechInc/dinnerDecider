package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/model/downloadlogic"
)

// Tests for the downloader.
//
// The pure decision logic is tested in downloadlogic/logic_test.go (20 tests
// ported from ModelDownloadServiceTests.swift). This file carries the remaining
// 5 ported tests - the file definitions and the per-file progress fraction -
// plus the Linux-only behavior that iOS got from URLSession and never had to
// test: byte-range resume across a dropped connection, sha256 rejection, and
// single-writer safety under a duplicate start.

// MARK: - Ported: file definitions match the locator and the required URLs

func TestFileNamesMatchLocator(t *testing.T) {
	specs := DefaultSpecs()
	if specs[0].FileName != PreferredMmprojName {
		t.Errorf("projector filename %q must be the one the locator resolves (%q)", specs[0].FileName, PreferredMmprojName)
	}
	if specs[1].FileName != DefaultModelPreference[0] {
		t.Errorf("weights filename %q must be the locator's first preference (%q)", specs[1].FileName, DefaultModelPreference[0])
	}
}

func TestProjectorDownloadsFirst(t *testing.T) {
	// The fast first success is deliberate UX, not declaration order: 534 MB
	// completes while the 3.46 GB weights would still be at a few percent.
	if got := DefaultSpecs()[0].ID; got != "mmproj" {
		t.Errorf("first download is %q, want the projector", got)
	}
}

func TestRemoteURLsAreTheExpectedPublicURLs(t *testing.T) {
	specs := DefaultSpecs()
	if want := "https://huggingface.co/ggml-org/gemma-4-E4B-it-GGUF/resolve/main/mmproj-gemma-4-E4B-it-Q8_0.gguf"; specs[0].URL != want {
		t.Errorf("projector URL:\n got %q\nwant %q", specs[0].URL, want)
	}
	if want := "https://huggingface.co/unsloth/gemma-4-E4B-it-GGUF/resolve/main/gemma-4-E4B-it-UD-IQ3_XXS.gguf"; specs[1].URL != want {
		t.Errorf("weights URL:\n got %q\nwant %q", specs[1].URL, want)
	}
}

// MARK: - Ported: per-file progress fraction

func TestFileProgressFraction(t *testing.T) {
	if got := (FileProgress{Received: 25, Total: 100}).Fraction(); math.Abs(got-0.25) > 0.0001 {
		t.Errorf("fraction: got %v want 0.25", got)
	}
}

func TestFileProgressFractionZeroTotal(t *testing.T) {
	if got := (FileProgress{Received: 25, Total: 0}).Fraction(); got != 0 {
		t.Errorf("fraction: got %v want 0", got)
	}
}

// MARK: - Go-only: the recorded digests

// The sizes and digests are the ones in docs/PORTING_NOTES.md § Staged model
// weights and shared/runtime/model-files.md. If someone changes a URL without
// changing these, the download will fail verification, which is the intended
// outcome; this test makes it fail at build time instead.
func TestDefaultSpecsCarryTheRecordedDigests(t *testing.T) {
	specs := DefaultSpecs()
	want := map[string]struct {
		bytes  int64
		digest string
	}{
		"mmproj":  {559_874_816, "197f49a93027f9843772bd24a6a9e0be2a32a788de5a3def330e9c585d86edd1"},
		"weights": {3_717_115_008, "dd0b5b2f3ba8b4cbeb35154484fc67729183c8f24f53e43319d806ec9fffc6a7"},
	}
	for _, spec := range specs {
		expected, ok := want[spec.ID]
		if !ok {
			t.Fatalf("unexpected spec %q", spec.ID)
		}
		if spec.ExpectedBytes != expected.bytes {
			t.Errorf("%s size: got %d want %d", spec.ID, spec.ExpectedBytes, expected.bytes)
		}
		if spec.SHA256 != expected.digest {
			t.Errorf("%s digest: got %s want %s", spec.ID, spec.SHA256, expected.digest)
		}
		if len(spec.SHA256) != sha256.Size*2 {
			t.Errorf("%s digest is not a sha256 hex string", spec.ID)
		}
	}
}

// MARK: - Test fixtures

// payloadServer serves one payload over HTTP with byte-range support, and can
// drop the connection mid-stream, block mid-stream, or ignore Range entirely.
type payloadServer struct {
	payload []byte

	mu       sync.Mutex
	ranges   []string
	requests int

	// dropFirst aborts the first response halfway through, the way a Wi-Fi
	// handover or a proxy timeout does.
	dropFirst bool
	// gateFirst, when non-nil, holds the first response open halfway through
	// until it is closed.
	gateFirst chan struct{}
	// inFlight receives once the first response has delivered its first half.
	inFlight chan struct{}
	// ignoreRange makes the server answer 200 with the whole file even when a
	// Range was asked for, which some CDNs and every naive proxy will do.
	ignoreRange bool
}

func (s *payloadServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rangeHeader := r.Header.Get("Range")
	s.mu.Lock()
	s.requests++
	requestNumber := s.requests
	s.ranges = append(s.ranges, rangeHeader)
	s.mu.Unlock()

	start := int64(0)
	if rangeHeader != "" && !s.ignoreRange {
		trimmed := strings.TrimSuffix(strings.TrimPrefix(rangeHeader, "bytes="), "-")
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		start = parsed
	}
	if start > int64(len(s.payload)) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}

	body := s.payload[start:]
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if rangeHeader != "" && !s.ignoreRange {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(s.payload)-1, len(s.payload)))
		w.WriteHeader(http.StatusPartialContent)
	}

	interrupt := requestNumber == 1 && (s.dropFirst || s.gateFirst != nil)
	if !interrupt {
		_, _ = w.Write(body)
		return
	}

	half := len(body) / 2
	_, _ = w.Write(body[:half])
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if s.inFlight != nil {
		select {
		case s.inFlight <- struct{}{}:
		default:
		}
	}
	if s.gateFirst != nil {
		select {
		case <-s.gateFirst:
			_, _ = w.Write(body[half:])
		case <-r.Context().Done():
		}
		return
	}
	// Abort without a stack trace: the client sees the connection close before
	// Content-Length bytes arrived, which is io.ErrUnexpectedEOF.
	panic(http.ErrAbortHandler)
}

func testPayload(size int) []byte {
	payload := make([]byte, size)
	source := rand.New(rand.NewSource(1))
	for i := range payload {
		payload[i] = byte(source.Intn(256))
	}
	return payload
}

func digestOf(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// newTestDownloader wires a downloader to a payload server with a generous fake
// disk, so a test only exercises what it means to.
func newTestDownloader(t *testing.T, server *payloadServer, spec Spec, mutate func(*Options)) (*Downloader, *httptest.Server, string) {
	t.Helper()
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	spec.URL = httpServer.URL + "/" + spec.FileName

	dir := t.TempDir()
	opts := Options{
		Dir:       dir,
		Specs:     []Spec{spec},
		Client:    httpServer.Client(),
		FreeBytes: func(string) (int64, error) { return 10_000_000_000, nil },
	}
	if mutate != nil {
		mutate(&opts)
	}
	downloader, err := NewDownloader(opts)
	if err != nil {
		t.Fatal(err)
	}
	return downloader, httpServer, dir
}

func specFor(payload []byte) Spec {
	return Spec{
		ID:            "weights",
		FileName:      "gemma-4-E4B-it-UD-IQ3_XXS.gguf",
		ExpectedBytes: int64(len(payload)),
		SHA256:        digestOf(payload),
		DisplayName:   "Gemma 4 weights",
		ApproxBytes:   int64(len(payload)),
	}
}

func waitForState(t *testing.T, d *Downloader, kind downloadlogic.StateKind) downloadlogic.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if state := d.State(); state.Kind == kind {
			return state
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for state %v, stuck at %v (%q)", kind, d.State(), d.State().Reason)
	return downloadlogic.State{}
}

func (s *payloadServer) rangeHeaders() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ranges...)
}

func (s *payloadServer) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// MARK: - Resume

// TestResumesAfterMidStreamDrop is the test the iOS service never needed: the
// connection dies halfway through, the .part file survives, and the retry asks
// for exactly the bytes that are missing. The final sha256 matching proves the
// resumed transfer re-hashed the prefix it did not fetch, because a digest over
// the second half alone could not match.
func TestResumesAfterMidStreamDrop(t *testing.T) {
	payload := testPayload(200_000)
	spec := specFor(payload)
	server := &payloadServer{payload: payload, dropFirst: true}
	downloader, _, dir := newTestDownloader(t, server, spec, nil)

	downloader.Start()
	failed := waitForState(t, downloader, downloadlogic.StateFailed)
	if !strings.Contains(failed.Reason, "stopped after") {
		t.Errorf("a dropped connection should read as resumable, got %q", failed.Reason)
	}

	part := filepath.Join(dir, spec.FileName+partSuffix)
	partSize := fileSize(part)
	if partSize <= 0 || partSize >= int64(len(payload)) {
		t.Fatalf("expected a partial file, got %d of %d bytes", partSize, len(payload))
	}
	if _, err := os.Stat(filepath.Join(dir, spec.FileName)); err == nil {
		t.Fatal("an unfinished download must not appear under its final name")
	}

	downloader.Retry()
	waitForState(t, downloader, downloadlogic.StateDone)

	ranges := server.rangeHeaders()
	if len(ranges) != 2 {
		t.Fatalf("expected 2 requests, got %v", ranges)
	}
	if ranges[0] != "" {
		t.Errorf("the first request must not ask for a range, got %q", ranges[0])
	}
	if want := fmt.Sprintf("bytes=%d-", partSize); ranges[1] != want {
		t.Errorf("resume offset: got %q want %q", ranges[1], want)
	}

	assertDownloadedFile(t, filepath.Join(dir, spec.FileName), payload)
	if fileSize(part) != 0 {
		t.Error("the .part file must be gone after the rename")
	}
}

// TestResumeRehashesTheExistingPrefix starts from a .part left behind by an
// earlier process, so the only way the final digest can match is by streaming
// that prefix through the hasher before continuing.
func TestResumeRehashesTheExistingPrefix(t *testing.T) {
	payload := testPayload(120_000)
	spec := specFor(payload)
	server := &payloadServer{payload: payload}
	downloader, _, dir := newTestDownloader(t, server, spec, nil)

	const prefix = 50_000
	writeFile(t, filepath.Join(dir, spec.FileName+partSuffix), string(payload[:prefix]))

	downloader.Start()
	waitForState(t, downloader, downloadlogic.StateDone)

	ranges := server.rangeHeaders()
	if len(ranges) != 1 || ranges[0] != fmt.Sprintf("bytes=%d-", prefix) {
		t.Fatalf("expected one resumed request, got %v", ranges)
	}
	assertDownloadedFile(t, filepath.Join(dir, spec.FileName), payload)
}

// A server that answers a Range request with the whole file must not be
// appended to: the download restarts from zero and still verifies.
func TestServerIgnoringRangeRestartsCleanly(t *testing.T) {
	payload := testPayload(90_000)
	spec := specFor(payload)
	server := &payloadServer{payload: payload, ignoreRange: true}
	downloader, _, dir := newTestDownloader(t, server, spec, nil)

	// A prefix of the WRONG bytes: appending to it, or hashing it, would fail.
	junk := make([]byte, 30_000)
	writeFile(t, filepath.Join(dir, spec.FileName+partSuffix), string(junk))

	downloader.Start()
	waitForState(t, downloader, downloadlogic.StateDone)
	assertDownloadedFile(t, filepath.Join(dir, spec.FileName), payload)
}

// MARK: - Verification

// TestCorruptedByteFailsVerificationAndDeletesThePart is why the Linux port
// checks sha256 where iOS checks only Content-Length: a file of exactly the
// right length can still be wrong, and a wrong 3.5 GB model on disk would be
// found out by llama-server minutes later with a far worse error.
func TestCorruptedByteFailsVerificationAndDeletesThePart(t *testing.T) {
	payload := testPayload(80_000)
	spec := specFor(payload)

	corrupted := append([]byte(nil), payload...)
	corrupted[40_000] ^= 0xFF
	server := &payloadServer{payload: corrupted}

	downloader, _, dir := newTestDownloader(t, server, spec, nil)
	downloader.Start()
	failed := waitForState(t, downloader, downloadlogic.StateFailed)

	if !strings.Contains(failed.Reason, "checksum") {
		t.Errorf("the reason must name the checksum, got %q", failed.Reason)
	}
	if !strings.Contains(failed.Reason, spec.SHA256) {
		t.Errorf("the reason must carry the expected digest, got %q", failed.Reason)
	}
	if _, err := os.Stat(filepath.Join(dir, spec.FileName+partSuffix)); !os.IsNotExist(err) {
		t.Error("a file that failed its checksum must not be left on disk")
	}
	if _, err := os.Stat(filepath.Join(dir, spec.FileName)); !os.IsNotExist(err) {
		t.Error("a file that failed its checksum must never be renamed into place")
	}
	if got := downloader.Snapshot().Files[0].Received; got != 0 {
		t.Errorf("progress must reset after a discard, got %d", got)
	}
}

// A truncated body of a size the server never promised is caught by the size
// check before the digest, and is reported with both numbers.
func TestShortFileFailsTheSizeCheck(t *testing.T) {
	payload := testPayload(60_000)
	spec := specFor(payload)
	spec.ExpectedBytes = int64(len(payload)) + 4096 // the server has fewer bytes than we expect

	server := &payloadServer{payload: payload}
	downloader, _, dir := newTestDownloader(t, server, spec, nil)
	downloader.Start()
	failed := waitForState(t, downloader, downloadlogic.StateFailed)

	if !strings.Contains(failed.Reason, strconv.Itoa(len(payload))) {
		t.Errorf("the reason must name the actual size, got %q", failed.Reason)
	}
	if _, err := os.Stat(filepath.Join(dir, spec.FileName+partSuffix)); !os.IsNotExist(err) {
		t.Error("a short file must be deleted, not kept as a resume point for a size we cannot reach")
	}
}

// MARK: - Single writer

// TestDuplicateStartIsANoOpWithOneWriter is the invariant NB-7 exists for. Two
// transfer goroutines on one .part file would interleave their writes and
// produce a file that fails sha256 after a multi-gigabyte transfer, so the
// no-op is asserted directly: exactly one goroutine, exactly one open .part
// handle, no matter how many starts arrive.
func TestDuplicateStartIsANoOpWithOneWriter(t *testing.T) {
	payload := testPayload(150_000)
	spec := specFor(payload)
	gate := make(chan struct{})
	server := &payloadServer{
		payload:   payload,
		gateFirst: gate,
		inFlight:  make(chan struct{}, 1),
	}
	downloader, _, dir := newTestDownloader(t, server, spec, nil)

	downloader.Start()
	select {
	case <-server.inFlight:
	case <-time.After(10 * time.Second):
		t.Fatal("the first transfer never started")
	}

	for i := 0; i < 5; i++ {
		downloader.Start()
		if state := downloader.State(); state.Kind != downloadlogic.StateDownloading {
			t.Fatalf("a duplicate start must leave the state alone, got %v (%q)", state, state.Reason)
		}
	}
	if got := downloader.workerStarts.Load(); got != 1 {
		t.Errorf("transfer goroutines started: got %d want 1", got)
	}
	if got := downloader.partOpens.Load(); got != 1 {
		t.Errorf("open .part handles: got %d want 1", got)
	}

	close(gate)
	waitForState(t, downloader, downloadlogic.StateDone)

	if got := downloader.workerStarts.Load(); got != 1 {
		t.Errorf("transfer goroutines started, after completion: got %d want 1", got)
	}
	if got := downloader.partOpens.Load(); got != 1 {
		t.Errorf("open .part handles, after completion: got %d want 1", got)
	}
	if got := server.requestCount(); got != 1 {
		t.Errorf("HTTP requests: got %d want 1; a duplicate start must not re-request", got)
	}
	assertDownloadedFile(t, filepath.Join(dir, spec.FileName), payload)
}

// Pause keeps the prefix and Resume continues from it, with one writer
// throughout.
func TestPauseKeepsThePartAndResumeContinues(t *testing.T) {
	payload := testPayload(150_000)
	spec := specFor(payload)
	gate := make(chan struct{})
	server := &payloadServer{
		payload:   payload,
		gateFirst: gate,
		inFlight:  make(chan struct{}, 1),
	}
	downloader, _, dir := newTestDownloader(t, server, spec, nil)
	defer close(gate)

	downloader.Start()
	select {
	case <-server.inFlight:
	case <-time.After(10 * time.Second):
		t.Fatal("the first transfer never started")
	}

	// Wait until bytes have actually landed, so the test is about what a pause
	// keeps rather than about how fast it arrives.
	part := filepath.Join(dir, spec.FileName+partSuffix)
	waitForBytes(t, part)

	downloader.Pause()
	waitForState(t, downloader, downloadlogic.StatePaused)
	downloader.waitForWorker()

	partSize := fileSize(part)
	if partSize <= 0 {
		t.Fatal("a pause must keep the bytes already written")
	}

	downloader.Resume()
	waitForState(t, downloader, downloadlogic.StateDone)

	ranges := server.rangeHeaders()
	if len(ranges) != 2 {
		t.Fatalf("expected 2 requests, got %v", ranges)
	}
	if want := fmt.Sprintf("bytes=%d-", partSize); ranges[1] != want {
		t.Errorf("resume offset: got %q want %q", ranges[1], want)
	}
	if got := downloader.partOpens.Load(); got != 2 {
		t.Errorf("open .part handles: got %d want 2 (one per transfer, never two at once)", got)
	}
	assertDownloadedFile(t, filepath.Join(dir, spec.FileName), payload)
}

// MARK: - Gates and presence

func TestInsufficientDiskSpaceFailsBeforeAnyRequest(t *testing.T) {
	payload := testPayload(10_000)
	spec := specFor(payload)
	server := &payloadServer{payload: payload}
	downloader, _, _ := newTestDownloader(t, server, spec, func(o *Options) {
		o.FreeBytes = func(string) (int64, error) {
			return downloadlogic.RequiredFreeBytes - 1, nil
		}
	})

	downloader.Start()
	failed := waitForState(t, downloader, downloadlogic.StateFailed)
	if !strings.Contains(failed.Reason, "free space") {
		t.Errorf("the reason must name the space problem, got %q", failed.Reason)
	}
	if got := server.requestCount(); got != 0 {
		t.Errorf("a failed preflight must not hit the network, got %d requests", got)
	}
}

// The metered gate is the cellular gate under its Linux name: --metered marks
// the link, and the start waits for an explicit confirmation.
func TestMeteredLinkNeedsConfirmationFirst(t *testing.T) {
	payload := testPayload(10_000)
	spec := specFor(payload)
	server := &payloadServer{payload: payload}
	downloader, _, dir := newTestDownloader(t, server, spec, func(o *Options) { o.Metered = true })

	downloader.Start()
	if state := downloader.State(); state.Kind != downloadlogic.StateNotStarted {
		t.Fatalf("a metered start must not begin, got %v", state)
	}
	if !downloader.Snapshot().NeedsMeteredConfirmation {
		t.Error("the snapshot must tell the caller a confirmation is needed")
	}
	if got := server.requestCount(); got != 0 {
		t.Errorf("a blocked start must not hit the network, got %d requests", got)
	}

	downloader.StartConfirmingMetered()
	waitForState(t, downloader, downloadlogic.StateDone)
	if downloader.Snapshot().NeedsMeteredConfirmation {
		t.Error("the confirmation flag must clear once confirmed")
	}
	assertDownloadedFile(t, filepath.Join(dir, spec.FileName), payload)
}

// A retry is not a shortcut past the gates: the disk can fill between the first
// attempt and the second, and a retry that skipped the check would start a
// 4 GB transfer that cannot finish.
func TestRetryReRunsThePreflight(t *testing.T) {
	payload := testPayload(50_000)
	spec := specFor(payload)
	corrupted := append([]byte(nil), payload...)
	corrupted[100] ^= 0xFF
	server := &payloadServer{payload: corrupted}

	var free atomic.Int64
	free.Store(10_000_000_000)
	downloader, _, _ := newTestDownloader(t, server, spec, func(o *Options) {
		o.FreeBytes = func(string) (int64, error) { return free.Load(), nil }
	})

	downloader.Start()
	failed := waitForState(t, downloader, downloadlogic.StateFailed)
	if !strings.Contains(failed.Reason, "checksum") {
		t.Fatalf("expected a checksum failure first, got %q", failed.Reason)
	}

	free.Store(downloadlogic.RequiredFreeBytes - 1)
	downloader.Retry()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if state := downloader.State(); strings.Contains(state.Reason, "free space") {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("the retry ignored the disk gate, state is %v (%q)", downloader.State(), downloader.State().Reason)
}

func TestAlreadyPresentFilesStartDone(t *testing.T) {
	payload := testPayload(10_000)
	spec := specFor(payload)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, spec.FileName), string(payload))

	downloader, err := NewDownloader(Options{Dir: dir, Specs: []Spec{spec}})
	if err != nil {
		t.Fatal(err)
	}
	if state := downloader.State(); state.Kind != downloadlogic.StateDone {
		t.Errorf("a complete install must start done, got %v", state)
	}
	if got := downloader.Snapshot().Fraction; math.Abs(got-1) > 0.0001 {
		t.Errorf("progress: got %v want 1", got)
	}
}

// A file already on disk is not re-fetched, and one that is the wrong size is.
func TestPresentFileIsSkippedAndWrongSizedFileIsReplaced(t *testing.T) {
	payload := testPayload(70_000)
	spec := specFor(payload)
	server := &payloadServer{payload: payload}
	downloader, _, dir := newTestDownloader(t, server, spec, nil)

	writeFile(t, filepath.Join(dir, spec.FileName), string(payload[:1000]))
	downloader.Start()
	waitForState(t, downloader, downloadlogic.StateDone)

	if got := server.requestCount(); got != 1 {
		t.Errorf("a wrong-sized file must be re-fetched, got %d requests", got)
	}
	assertDownloadedFile(t, filepath.Join(dir, spec.FileName), payload)
}

func TestObserverSeesTerminalSnapshot(t *testing.T) {
	payload := testPayload(40_000)
	spec := specFor(payload)
	server := &payloadServer{payload: payload}

	var mu sync.Mutex
	var seen []downloadlogic.StateKind
	downloader, _, _ := newTestDownloader(t, server, spec, func(o *Options) {
		o.OnUpdate = func(snapshot Snapshot) {
			mu.Lock()
			defer mu.Unlock()
			if len(seen) == 0 || seen[len(seen)-1] != snapshot.State.Kind {
				seen = append(seen, snapshot.State.Kind)
			}
		}
	})

	downloader.Start()
	waitForState(t, downloader, downloadlogic.StateDone)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 || seen[0] != downloadlogic.StateDownloading || seen[len(seen)-1] != downloadlogic.StateDone {
		t.Errorf("observer saw %v, want downloading through done", seen)
	}
}

func TestNewDownloaderRequiresADirectory(t *testing.T) {
	if _, err := NewDownloader(Options{}); err == nil {
		t.Error("an empty destination directory must be rejected")
	}
}

func TestFreeDiskBytesReportsSomething(t *testing.T) {
	free, err := FreeDiskBytes(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if free <= 0 {
		t.Errorf("free space: got %d, want a positive number", free)
	}
}

// waitForWorker blocks until the current transfer goroutine has exited, so an
// assertion about the .part file cannot race the goroutine still finishing its
// last write.
func (d *Downloader) waitForWorker() {
	d.mu.Lock()
	done := d.workerDone
	d.mu.Unlock()
	if done != nil {
		<-done
	}
}

func waitForBytes(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if fileSize(path) > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no bytes ever landed in %s", path)
}

func assertDownloadedFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the finished file: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("finished file is %d bytes, want %d", len(got), len(want))
	}
	if digestOf(got) != digestOf(want) {
		t.Fatal("finished file does not match the payload digest")
	}
}
