// Package downloadlogic is the side-effect-free half of the model download: the
// preflight gates and the state machine, with no HTTP, no filesystem and no
// clock.
//
// A verbatim port of `enum ModelDownloadLogic` in
// DinnerDecider/Services/ModelDownloadService.swift. It is a separate package
// rather than a few unexported functions so that the decisions stay testable
// without a server, and so that nothing in here can grow an I/O dependency by
// accident.
package downloadlogic

// RequiredFreeBytes is the free space insisted on before starting. Weights plus
// projector are ~4.0 GB; 4.5 GB leaves headroom for the .part file during the
// atomic rename.
//
// The headroom is not doubled because a rename within one filesystem does not
// copy: the .part and the final name are the same inode, so peak usage is one
// copy of the file, not two.
const RequiredFreeBytes int64 = 4_500_000_000

// HasEnoughDiskSpace reports whether there is enough free disk for the default
// requirement.
func HasEnoughDiskSpace(freeBytes int64) bool {
	return HasEnoughDiskSpaceFor(freeBytes, RequiredFreeBytes)
}

// HasEnoughDiskSpaceFor reports whether freeBytes meets an explicit
// requirement. Go has no default arguments; this pair is the Swift
// `required: Int64 = requiredFreeBytes` parameter split in two.
func HasEnoughDiskSpaceFor(freeBytes, required int64) bool {
	return freeBytes >= required
}

// SizeIsValid reports whether a finished file's real size matches the expected
// size.
//
// A zero (unknown) expected size is INVALID, deliberately. Accepting it would
// mean vouching for a multi-gigabyte file on no evidence at all, and the whole
// point of this check is to refuse exactly that.
func SizeIsValid(actual, expected int64) bool {
	return expected > 0 && actual == expected
}

// MayStartDownload reports whether a download may begin given the network.
//
// On iOS this is the cellular gate: a metered connection requires an explicit
// user confirmation before spending several gigabytes of someone's data plan.
// Linux has no cellular notion, so the service wires isCellular from the
// --metered flag (DD_METERED), which defaults to false. The function is kept
// rather than inlined so the semantics survive the port: an operator who marks
// a link metered gets the same "ask first" behavior an iPhone user gets.
func MayStartDownload(isCellular, userConfirmedCellular bool) bool {
	if isCellular && !userConfirmedCellular {
		return false
	}
	return true
}

// OverallFraction is the completion fraction from summed bytes, clamped to 1
// and defined as 0 when nothing is known yet.
func OverallFraction(received, total int64) float64 {
	if total <= 0 {
		return 0
	}
	fraction := float64(received) / float64(total)
	if fraction > 1 {
		return 1
	}
	return fraction
}

// StateKind enumerates the download lifecycle states.
type StateKind int

// The lifecycle states, matching Swift's ModelDownloadState cases.
const (
	StateNotStarted StateKind = iota
	StateDownloading
	StatePaused
	StateVerifying
	StateDone
	StateFailed
)

// State is the download state. Failed carries a plain-language reason for the
// UI, which is the Swift associated value. The struct is comparable, so tests
// and callers can use == exactly as the Swift Equatable conformance allowed.
type State struct {
	Kind StateKind
	// Reason is set only when Kind is StateFailed.
	Reason string
}

// NotStarted returns the initial state.
func NotStarted() State { return State{Kind: StateNotStarted} }

// Downloading returns the downloading state.
func Downloading() State { return State{Kind: StateDownloading} }

// Paused returns the paused state.
func Paused() State { return State{Kind: StatePaused} }

// Verifying returns the verifying state.
func Verifying() State { return State{Kind: StateVerifying} }

// Done returns the terminal success state.
func Done() State { return State{Kind: StateDone} }

// Failed returns the failure state carrying a plain-language reason.
func Failed(reason string) State { return State{Kind: StateFailed, Reason: reason} }

// String renders the state for logs and the /api/model payload.
func (s State) String() string {
	switch s.Kind {
	case StateNotStarted:
		return "notStarted"
	case StateDownloading:
		return "downloading"
	case StatePaused:
		return "paused"
	case StateVerifying:
		return "verifying"
	case StateDone:
		return "done"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// EventKind enumerates the events that drive the state machine.
type EventKind int

// The events, matching Swift's ModelDownloadLogic.Event cases.
const (
	EventStart EventKind = iota
	EventPause
	EventResume
	EventAllDownloaded
	EventVerifiedOK
	EventVerificationFailed
	EventFailed
	EventRetry
)

// Event drives a transition. Reason is set only for EventFailed and
// EventVerificationFailed.
type Event struct {
	Kind   EventKind
	Reason string
}

// StartEvent asks to begin or restart the download.
func StartEvent() Event { return Event{Kind: EventStart} }

// PauseEvent asks to pause the in-flight transfer.
func PauseEvent() Event { return Event{Kind: EventPause} }

// ResumeEvent asks to continue a paused transfer.
func ResumeEvent() Event { return Event{Kind: EventResume} }

// AllDownloadedEvent reports that every file has been transferred.
func AllDownloadedEvent() Event { return Event{Kind: EventAllDownloaded} }

// VerifiedOKEvent reports that verification passed.
func VerifiedOKEvent() Event { return Event{Kind: EventVerifiedOK} }

// VerificationFailedEvent reports that verification rejected a file.
func VerificationFailedEvent(reason string) Event {
	return Event{Kind: EventVerificationFailed, Reason: reason}
}

// FailedEvent reports a transfer or I/O failure.
func FailedEvent(reason string) Event { return Event{Kind: EventFailed, Reason: reason} }

// RetryEvent asks to start again after a failure.
func RetryEvent() Event { return Event{Kind: EventRetry} }

// Next is the pure state transition.
//
// Any unhandled pairing is a NO-OP that returns the current state. That is the
// load-bearing property, not a default clause someone forgot to fill in: a
// stray callback arriving late from a transfer that has already been abandoned
// cannot corrupt what the user is looking at.
//
// The pairing that matters most on Linux is (downloading, start). A second
// start while a download is already running returns downloading unchanged, and
// the service starts its transfer goroutine only on an actual transition INTO
// downloading. Two writers interleaving on one .part file would produce a file
// that fails its sha256 after 3.5 GB of transfer, which is the worst possible
// moment to discover it.
func Next(state State, event Event) State {
	// Checked first, matching Swift's `case (_, .failed(let reason))`: a
	// failure from any state, including a terminal one, is a failure.
	if event.Kind == EventFailed {
		return Failed(event.Reason)
	}

	switch {
	case state.Kind == StateNotStarted && event.Kind == EventStart,
		state.Kind == StateFailed && event.Kind == EventRetry,
		state.Kind == StateFailed && event.Kind == EventStart,
		state.Kind == StateDone && event.Kind == EventStart:
		return Downloading()
	case state.Kind == StateDownloading && event.Kind == EventPause:
		return Paused()
	case state.Kind == StatePaused && event.Kind == EventResume,
		state.Kind == StatePaused && event.Kind == EventStart:
		return Downloading()
	case state.Kind == StateDownloading && event.Kind == EventAllDownloaded:
		return Verifying()
	case state.Kind == StateVerifying && event.Kind == EventVerifiedOK:
		return Done()
	case state.Kind == StateVerifying && event.Kind == EventVerificationFailed:
		return Failed(event.Reason)
	default:
		return state
	}
}
