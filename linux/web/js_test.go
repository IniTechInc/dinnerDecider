package web

import (
	"strings"
	"testing"
)

// TestApiHandles409ByAttaching pins the admission behaviour from the plan's
// section 0.1 Layer 2: a scan already running is not an error to show the user,
// it is an invitation to join the scan that is running.
func TestApiHandles409ByAttaching(t *testing.T) {
	src := readAsset(t, "js/api.js")

	if !strings.Contains(src, "err.status === 409") {
		t.Error("api.js does not special-case 409 on scan start")
	}
	if !strings.Contains(src, "activeScanID") {
		t.Error("api.js never reads activeScanID, so a 409 cannot attach to the running scan")
	}
	if !strings.Contains(src, "attached: true") {
		t.Error("api.js does not report that it attached rather than started")
	}
}

// TestApiHumanisesBackpressure keeps 429 and 503 from reaching a person as a
// bare status code. Both are reachable on a LAN: a second phone starting a
// scan, or a seventeenth tab subscribing to one.
func TestApiHumanisesBackpressure(t *testing.T) {
	src := readAsset(t, "js/api.js")

	for _, status := range []string{"case 429:", "case 503:"} {
		if !strings.Contains(src, status) {
			t.Errorf("api.js has no human message for %s", strings.Trim(status, "case :"))
		}
	}
	if !strings.Contains(src, "Retry-After") {
		t.Error("api.js ignores Retry-After, so the message cannot say how long to wait")
	}
}

// TestApiSSEResumes pins the two halves of resume: the browser's own header on
// an automatic retry, and our query parameter on a reconnect we make ourselves,
// which cannot set a header.
func TestApiSSEResumes(t *testing.T) {
	src := readAsset(t, "js/api.js")

	for _, want := range []string{"lastEventId", "EventSource", "BACKOFF_MS"} {
		if !strings.Contains(src, want) {
			t.Errorf("api.js is missing %q; the SSE helper cannot reconnect and resume without it", want)
		}
	}
	if !strings.Contains(src, "POLL_AFTER_MS") {
		t.Error("api.js has no polling fallback; a buffering home router would leave the scan looking dead")
	}
}

// functionBody returns the source of `func` by brace counting from its opening
// brace. Good enough for this file, which deliberately keeps braces out of the
// string literals in the function under test.
func functionBody(t *testing.T, src, signature string) string {
	t.Helper()
	start := strings.Index(src, signature)
	if start < 0 {
		t.Fatalf("cannot find %q in the source", signature)
	}
	open := strings.Index(src[start:], "{")
	if open < 0 {
		t.Fatalf("%q has no body", signature)
	}
	open += start
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open : i+1]
			}
		}
	}
	t.Fatalf("unbalanced braces after %q", signature)
	return ""
}

// TestFileInputIsNotGatedOnSecureContext is the whole point of step 68.
//
// getUserMedia needs a secure context and a LAN address over plain HTTP is not
// one. A file input with capture="environment" opens the phone's own camera app
// and is not gated at all. If someone ever wraps it in an isSecureContext check
// "for consistency", the app stops being able to take a photo on exactly the
// hardware it was built for, and nothing else in the suite would notice.
func TestFileInputIsNotGatedOnSecureContext(t *testing.T) {
	src := readAsset(t, "js/capture.js")

	body := functionBody(t, src, "export function createCameraInput(")
	for _, want := range []string{"'file'", "'image/*'", `'capture', 'environment'`} {
		if !strings.Contains(body, want) {
			t.Errorf("createCameraInput does not set %s", want)
		}
	}
	if strings.Contains(body, "isSecureContext") {
		t.Error("the file-input path is gated on isSecureContext; it must work over plain HTTP on a LAN address")
	}

	// Positional proof that the whole Path 1 section is above the guard.
	path2 := strings.Index(src, "PATH 2")
	first := strings.Index(src, "isSecureContext")
	if path2 < 0 {
		t.Fatal("capture.js has no PATH 2 marker, so the split between the two paths cannot be checked")
	}
	if first >= 0 && first < path2 {
		t.Errorf("isSecureContext appears at byte %d, above the PATH 2 marker at %d; the primary path must not touch it", first, path2)
	}
	if strings.Index(src, "createCameraInput") > path2 {
		t.Error("the file input is defined below the PATH 2 marker; it is the primary path and belongs above it")
	}
}

// TestCaptureTranscodePipeline pins the four steps that make an iPhone HEIC
// work with no server-side dependency and cut the upload roughly tenfold.
func TestCaptureTranscodePipeline(t *testing.T) {
	src := readAsset(t, "js/capture.js")

	for _, want := range []string{
		"createImageBitmap",
		"imageOrientation: 'from-image'",
		"CAPTURE_LONG_EDGE = 2016",
		"JPEG_QUALITY = 0.9",
		"'image/jpeg'",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("capture.js is missing %q from the shared transcode", want)
		}
	}
}

// TestCaptureChecksLimitsLocally keeps a 413 from being the first thing the
// user learns after waiting out an upload on a slow LAN.
func TestCaptureChecksLimitsLocally(t *testing.T) {
	src := readAsset(t, "js/capture.js")

	for _, want := range []string{
		"MAX_FILES = 8",
		"MAX_FILE_BYTES = 12 * 1024 * 1024",
		"MAX_TOTAL_BYTES = 48 * 1024 * 1024",
		"MAX_PIXELS = 40 * 1000 * 1000",
		"MAX_DIMENSION = 20000",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("capture.js does not mirror the server limit %q", want)
		}
	}
}

// The three terminal states, verbatim from the shipped ScanningView
// (emptyResults / failedResults / cancelledState).
const (
	emptyTitle  = `Nothing spotted`
	emptyBody   = `We could not spot anything. Try getting closer, adding more light, or filling more of the frame.`
	emptyAction = `Try another photo`

	failedStatus = `The scan could not finish.`
	failedTitle  = `Scan could not finish`
	failedBody   = `Something went wrong while identifying your food, so we could not read this photo. This usually clears up after closing other apps and trying again.`
	failedAction = `Try again`

	cancelledStatus = `Scan cancelled.`
	cancelledTitle  = `Scan cancelled`
	cancelledBody   = `No items were saved. You can start again whenever you like.`
)

// TestTerminalStatesStayDistinct is the load-bearing test of step 69.
//
// `empty` means the model answered every call and genuinely saw no food, so
// advice about light and framing is honest. `failed` means the calls themselves
// failed and we do not know what was in the photo, so the same advice blames
// the user's photo for our failure. Collapsing the two back together is the
// exact bug the ScanOutcome split exists to fix, and it would look perfectly
// reasonable in a diff.
func TestTerminalStatesStayDistinct(t *testing.T) {
	src := readAsset(t, "js/scan.js")

	if emptyBody == failedBody {
		t.Fatal("the test constants themselves are equal; the fixture is wrong")
	}
	if !strings.Contains(src, emptyBody) {
		t.Errorf("scan.js is missing the empty-phase body:\n\t%s", emptyBody)
	}
	if !strings.Contains(src, failedBody) {
		t.Errorf("scan.js is missing the failed-phase body:\n\t%s", failedBody)
	}
	if strings.Count(src, emptyBody) > 1 || strings.Count(src, failedBody) > 1 {
		t.Error("a terminal body appears more than once; there must be exactly one copy of each")
	}

	// Each phase must carry its own body, not a shared one.
	empty := blockFor(t, src, "  empty: {")
	failed := blockFor(t, src, "  failed: {")
	cancelled := blockFor(t, src, "  cancelled: {")

	if strings.Contains(failed, emptyBody) {
		t.Error("the failed phase reuses the empty copy, which blames the user's photo for our failure")
	}
	if strings.Contains(empty, failedBody) {
		t.Error("the empty phase reuses the failed copy")
	}
	if strings.Contains(cancelled, emptyBody) || strings.Contains(cancelled, failedBody) {
		t.Error("the cancelled phase reuses another phase's copy")
	}
}

// TestTerminalCopyIsVerbatim pins every string in the table, not only the two
// bodies, so a well-meaning reword of a title or a button label also fails.
func TestTerminalCopyIsVerbatim(t *testing.T) {
	src := readAsset(t, "js/scan.js")

	empty := blockFor(t, src, "  empty: {")
	failed := blockFor(t, src, "  failed: {")
	cancelled := blockFor(t, src, "  cancelled: {")

	for _, c := range []struct {
		phase string
		block string
		want  []string
	}{
		{"empty", empty, []string{emptyTitle, emptyBody, emptyAction}},
		{"failed", failed, []string{failedStatus, failedTitle, failedBody, failedAction}},
		{"cancelled", cancelled, []string{cancelledStatus, cancelledTitle, cancelledBody}},
	} {
		for _, want := range c.want {
			if !strings.Contains(c.block, want) {
				t.Errorf("the %s phase is missing the verbatim string %q", c.phase, want)
			}
		}
	}

	// The empty phase has no status line at all; the table's dash is literal.
	if strings.Contains(empty, "status: 'We could not spot anything to add.'") {
		t.Error("the empty phase carries a status line; the contract table leaves it blank")
	}

	// insufficient_memory appends the service's own detail, because on a box at
	// the memory floor the "close other apps" advice is literally correct.
	if !strings.Contains(src, `state.abortReason === 'insufficient_memory'`) {
		t.Error("scan.js does not append the service detail when the abort reason is insufficient_memory")
	}
	if !strings.Contains(src, "state.abortDetail") {
		t.Error("scan.js never reads the abort detail string")
	}
}

// TestScanShowsNoFabricatedProgress pins the honesty rules: no percentage bar,
// and no ETA unless something measured one on hardware like this.
func TestScanShowsNoFabricatedProgress(t *testing.T) {
	src := readAsset(t, "js/scan.js")

	if !strings.Contains(src, "state.estimateMeasured && state.estimateSeconds > 0") {
		t.Error("scan.js shows an ETA without requiring a measurement for this hardware class")
	}
	if !strings.Contains(src, "scanEstimateMeasured === true") {
		t.Error("scan.js does not require the service to claim the estimate was measured")
	}

	// The running screen must not drive a percentage fill. The indeterminate
	// pulse in app.css is a motion cue and is driven entirely by CSS.
	//
	// Checked against code with the comments removed, so the doc comment that
	// explains WHY there is no percentage bar does not trip its own rule.
	code := codeOnly(src)
	for _, forbidden := range []string{"progress__fill", "style.width", "percent"} {
		if strings.Contains(code, forbidden) {
			t.Errorf("scan.js references %q; the running screen must not render a percentage of a scan whose calls are not equal in length", forbidden)
		}
	}

	for _, want := range []string{"Call ", "Elapsed ", "this call "} {
		if !strings.Contains(src, want) {
			t.Errorf("scan.js does not show %q, which is one of the three facts it has to offer instead of a fake percentage", want)
		}
	}
}

// TestScanReattachesOnLoad pins the property that makes a four-minute scan
// survive a locked phone, a closed tab, or a Wi-Fi handover.
func TestScanReattachesOnLoad(t *testing.T) {
	src := readAsset(t, "js/scan.js")

	for _, want := range []string{"reattachOnLoad", "activeScanID", "EventStream", "LAST_SCAN_KEY"} {
		if !strings.Contains(src, want) {
			t.Errorf("scan.js is missing %q, so a page reload cannot rejoin a scan in flight", want)
		}
	}
	if !strings.Contains(src, "api.cancelScan") {
		t.Error("scan.js offers no way to cancel a running scan")
	}
}

// blockFor returns the object literal that starts at `opening`, by brace
// counting. The terminal table deliberately keeps braces out of its strings.
func blockFor(t *testing.T, src, opening string) string {
	t.Helper()
	start := strings.Index(src, opening)
	if start < 0 {
		t.Fatalf("cannot find %q in scan.js", strings.TrimSpace(opening))
	}
	open := strings.Index(src[start:], "{") + start
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open : i+1]
			}
		}
	}
	t.Fatalf("unbalanced braces after %q", strings.TrimSpace(opening))
	return ""
}

// codeOnly strips JavaScript comments so a rule can be asserted against what
// the file DOES rather than what it says about itself.
func codeOnly(src string) string {
	var out strings.Builder
	for i := 0; i < len(src); {
		if strings.HasPrefix(src[i:], "/*") {
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				break
			}
			i += 2 + end + 2
			continue
		}
		if strings.HasPrefix(src[i:], "//") {
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				break
			}
			i += end
			continue
		}
		out.WriteByte(src[i])
		i++
	}
	return out.String()
}
