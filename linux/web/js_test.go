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
