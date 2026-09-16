package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// maxJSONBodyBytes caps a JSON request body.
//
// Separate from, and far below, MaxRequestBytes: nothing this API accepts as
// JSON is legitimately large. A confirm carries at most a few dozen items and a
// taste profile is thirteen short fields. The photo path is the only one that
// needs megabytes, and it has its own reader.
const maxJSONBodyBytes int64 = 1 << 20

// errorBody is the one error envelope this service speaks.
//
// The shape matches the one internal/auth already writes for 401 and for the
// pairing failures, so a client has a single error handler rather than one per
// subsystem. `error` is a stable machine code; `message` is written for a
// person and may change.
type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	// ActiveScanID is set only on the scan_in_progress 409, where the id IS the
	// point of the response: the client attaches to that scan rather than
	// retrying blindly. See plan section 0.1, layer 2.
	ActiveScanID string `json:"activeScanID,omitempty"`
}

// writeJSON sends a value as JSON with no-store caching.
//
// no-store on every API response, deliberately: these responses carry a
// household's food inventory and their photo ids, and a shared browser or an
// intermediate cache holding them is a privacy leak with no upside. The UI
// re-fetches; it is on the same LAN.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already gone, so there is nothing to report to the
		// client. A truncated body is what the client will see.
		return
	}
}

// writeError sends the error envelope.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: code, Message: message})
}

// writeLimitError sends a boundary refusal, preserving the message that names
// the limit actually hit.
func writeLimitError(w http.ResponseWriter, err error) {
	var limitErr *LimitError
	if errors.As(err, &limitErr) {
		writeError(w, limitErr.Status, limitErr.Code, limitErr.Message)
		return
	}
	writeError(w, http.StatusBadRequest, "bad_request", err.Error())
}

// writeRetryAfter sends a refusal that the client should retry later.
func writeRetryAfter(w http.ResponseWriter, status int, code, message string, after time.Duration) {
	seconds := int(after.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeError(w, status, code, message)
}

// writeInternal logs the real error and tells the client nothing about it.
func writeInternal(logf func(string, ...any), w http.ResponseWriter, what string, err error) {
	if logf != nil {
		logf("[API] %s: %v", what, err)
	}
	writeError(w, http.StatusInternalServerError, "internal", "Something went wrong on the server. Try again.")
}

// decodeJSON reads a JSON request body under a hard cap, refusing unknown
// fields.
//
// Unknown fields are an error on purpose. A client that PUTs
// {"spiceLevl": 5} has a typo, and silently accepting it writes a profile that
// looks saved and is not, which is exactly the class of failure the taste
// profile's own schema comment is about.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.Body == nil {
		return &LimitError{Status: http.StatusBadRequest, Code: "bad_request", Message: "That request had no body."}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return &LimitError{
				Status:  http.StatusRequestEntityTooLarge,
				Code:    CodeRequestTooLarge,
				Message: fmt.Sprintf("That request body is over the %s limit for a JSON request.", megabytes(maxJSONBodyBytes)),
			}
		}
		if errors.Is(err, io.EOF) {
			return &LimitError{Status: http.StatusBadRequest, Code: "bad_request", Message: "That request had no body."}
		}
		return &LimitError{
			Status:  http.StatusBadRequest,
			Code:    "bad_request",
			Message: fmt.Sprintf("That request body could not be read: %v", err),
		}
	}
	// A second value in the stream means the client sent something other than
	// one JSON document, which is never what it meant to do.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &LimitError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Send exactly one JSON object."}
	}
	return nil
}

// notFound is the JSON 404 every unmatched /api/ path gets.
func notFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "There is nothing at that address.")
}
