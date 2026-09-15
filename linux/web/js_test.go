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
