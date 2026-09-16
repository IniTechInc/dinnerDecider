package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// sseStream is one Server-Sent Events response.
//
// # Why the write deadline is cleared here
//
// The server sets WriteTimeout for every route (see Timeouts), because a route
// that can be held open by a slow reader is a route that can be held open by a
// thousand of them. A stream is the one shape where that deadline is simply
// wrong: the response never ends, so any finite write deadline kills a healthy
// connection at exactly the moment the user is waiting on a four-minute scan.
//
// So the deadline is cleared, per connection, with http.ResponseController.
// The consequence is stated plainly because it is the reason the caps exist:
// once cleared, NOTHING in net/http will ever reap this connection. The idle
// deadline in pump and the subscriber caps in limits.go are the whole of the
// replacement.
type sseStream struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

// sseFrame is one event to write.
type sseFrame struct {
	// ID becomes the SSE id field, which the browser sends back as
	// Last-Event-ID when it reconnects.
	ID string
	// Event is the event name. Empty means the default "message".
	Event string
	// Data is marshalled as JSON.
	Data any
}

// newSSEStream writes the SSE headers and takes the connection out of the
// server's write-deadline regime.
func newSSEStream(w http.ResponseWriter, clientRetry time.Duration) (*sseStream, error) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	// Without this an nginx or similar in front of the service buffers the
	// stream and the user sees nothing until the scan ends, which defeats the
	// entire point of streaming a four-minute operation.
	h.Set("X-Accel-Buffering", "no")

	s := &sseStream{w: w, rc: http.NewResponseController(w)}

	// A zero time means no deadline. ErrNotSupported comes back from a
	// ResponseRecorder in tests and from some wrapped writers; the stream still
	// works, it just inherits whatever deadline the wrapper has.
	if err := s.rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return nil, fmt.Errorf("api: clearing the stream write deadline: %w", err)
	}

	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprintf(s.w, "retry: %d\n\n", int(clientRetry.Milliseconds())); err != nil {
		return nil, err
	}
	return s, s.flush()
}

func (s *sseStream) flush() error {
	if err := s.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

// send writes one frame.
func (s *sseStream) send(frame sseFrame) error {
	payload, err := json.Marshal(frame.Data)
	if err != nil {
		return fmt.Errorf("api: encoding a stream event: %w", err)
	}
	var b strings.Builder
	if frame.ID != "" {
		b.WriteString("id: ")
		b.WriteString(frame.ID)
		b.WriteByte('\n')
	}
	if frame.Event != "" {
		b.WriteString("event: ")
		b.WriteString(frame.Event)
		b.WriteByte('\n')
	}
	// The payloads here are single-line JSON, so one data: line is correct.
	// Guarding anyway, because a multi-line data field silently truncates at
	// the first newline and that is a miserable bug to find.
	b.WriteString("data: ")
	b.Write(sanitizeSSEData(payload))
	b.WriteString("\n\n")

	if _, err := s.w.Write([]byte(b.String())); err != nil {
		return err
	}
	return s.flush()
}

// comment writes a keepalive comment frame, which a client ignores.
func (s *sseStream) comment(text string) error {
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	return s.flush()
}

// sanitizeSSEData replaces any newline inside a payload, which would otherwise
// end the data field early.
func sanitizeSSEData(payload []byte) []byte {
	if !containsNewline(payload) {
		return payload
	}
	out := make([]byte, 0, len(payload))
	for _, b := range payload {
		if b == '\n' || b == '\r' {
			out = append(out, ' ')
			continue
		}
		out = append(out, b)
	}
	return out
}

func containsNewline(payload []byte) bool {
	for _, b := range payload {
		if b == '\n' || b == '\r' {
			return true
		}
	}
	return false
}

// pumpSSE writes frames until the source closes, the client goes away, or the
// idle deadline expires.
//
// The keepalive DELIBERATELY does not reset the idle deadline. If it did, the
// deadline would only ever fire on a connection that is already dead, which is
// the one case the operating system handles without help. Here the deadline
// means what it says: a stream that has carried no real event for this long is
// a stream nobody is using, and it is holding a slot that is capped.
func pumpSSE[T any](ctx context.Context, s *sseStream, src <-chan T, conv func(T) sseFrame, idle, keepalive time.Duration) error {
	if idle <= 0 {
		idle = SSEIdleDeadline
	}
	if keepalive <= 0 {
		keepalive = SSEKeepalive
	}
	idleTimer := time.NewTimer(idle)
	defer idleTimer.Stop()
	ticker := time.NewTicker(keepalive)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-idleTimer.C:
			// Say why, so a client that reconnects knows it was not an error.
			_ = s.comment("idle deadline reached")
			return nil
		case <-ticker.C:
			if err := s.comment("keepalive"); err != nil {
				return nil
			}
		case value, ok := <-src:
			if !ok {
				return nil
			}
			if err := s.send(conv(value)); err != nil {
				return nil
			}
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(idle)
		}
	}
}
