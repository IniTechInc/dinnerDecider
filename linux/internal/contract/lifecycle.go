package contract

// ModelState is the explicit lifecycle state of the resident model.
//
// Port of `enum ModelState` in DinnerDecider/Services/ModelLifecycle.swift.
// On iOS the "model" is an in-process llama client holding its weights, mmproj
// and KV cache in WIRED Metal buffers; on Linux it is a supervised
// `llama-server` child on loopback. The mapping is recorded in the plan's Step
// 35 table (UNLOADED = no child, LOADING = spawned but not healthy, READY =
// healthy and idle, INFERRING = >=1 in flight, UNLOADING = SIGTERM sent). The
// machine itself does not care which: it never touches the runtime.
//
// Freeing the client while a load or an inference is in flight is a
// use-after-free; starting a reload before the old teardown drains is a
// multi-GB double allocation, i.e. an OOM. Those two field crashes are why this
// type exists (shared/pipeline/model-lifecycle.md).
//
// An int enum, not a string: the zero value must be a valid, safe state so a
// bare `var lc ModelLifecycle` behaves exactly like Swift's `ModelLifecycle()`,
// which starts at `.unloaded`.
type ModelState int

const (
	// Unloaded: no client resident.
	Unloaded ModelState = iota
	// Loading: a load is in flight (no client yet).
	Loading
	// Ready: client resident and idle.
	Ready
	// Inferring: client resident and serving an in-flight inference.
	Inferring
	// Unloading: a client teardown is in progress.
	Unloading
)

// String returns the lowercase state name, for logs and /api/status.
func (s ModelState) String() string {
	switch s {
	case Unloaded:
		return "unloaded"
	case Loading:
		return "loading"
	case Ready:
		return "ready"
	case Inferring:
		return "inferring"
	case Unloading:
		return "unloading"
	}
	return "unknown"
}

// MemoryPressureLevel is a system memory-pressure tier the machine reacts to.
//
// Port of `enum MemoryPressureLevel`. Only two tiers exist on purpose; a
// platform that reports more must map onto these.
//
// The zero value is PressureWarning deliberately: model-lifecycle.md
// § Adapter obligations requires a port to "default to WARNING when the tier is
// unknown", and WARNING is the tier that never sets pendingUnload.
type MemoryPressureLevel int

const (
	// PressureWarning is the ordinary, expected pressure notification. It is
	// ignored entirely while busy or mid-session.
	PressureWarning MemoryPressureLevel = iota
	// PressureCritical is the urgent tier. While busy or mid-session it is
	// recorded and honored on drain, never acted on immediately.
	PressureCritical
)

// String returns the lowercase level name, for logs.
func (l MemoryPressureLevel) String() string {
	if l == PressureCritical {
		return "critical"
	}
	return "warning"
}

// LoadDecision is the outcome of asking the lifecycle to start a load.
//
// Port of `enum LoadDecision` (.proceed / .coalesced / .alreadyLoaded).
// Go cannot give two enum types in one package the same constant name, so the
// three Swift cases carry a `Load` prefix here; the values and the rules are
// unchanged.
//
// The zero value is LoadCoalesced on purpose: of the three, it is the one that
// starts no multi-GB allocation, so a decision that was never actually returned
// by BeginLoad can never trigger a load.
type LoadDecision int

const (
	// LoadCoalesced: a load is already in flight; this request folds into it.
	LoadCoalesced LoadDecision = iota
	// LoadProceed: the caller should perform the actual load.
	LoadProceed
	// LoadAlreadyLoaded: the model is already resident; nothing to do.
	LoadAlreadyLoaded
)

// String returns the decision name, for logs.
func (d LoadDecision) String() string {
	switch d {
	case LoadProceed:
		return "proceed"
	case LoadCoalesced:
		return "coalesced"
	case LoadAlreadyLoaded:
		return "already_loaded"
	}
	return "unknown"
}

// UnloadDecision is the outcome of asking the lifecycle to unload.
//
// Port of `enum UnloadDecision` (.proceed / .deferred / .nothingToDo), prefixed
// for the same reason as LoadDecision.
//
// THE RULE (model-lifecycle.md § The other bug: mid-operation free): "every
// decision in this machine is the function's RETURN VALUE, and is never
// inferred from the caller's reason for calling". A supervisor that maps "the
// operator asked for a restart" straight to SIGTERM kills a child mid-encode,
// which is the build-1 use-after-free wearing a different hat.
//
// The zero value is UnloadNothingToDo on purpose: it is the decision that frees
// nothing.
type UnloadDecision int

const (
	// UnloadNothingToDo: nothing resident to unload.
	UnloadNothingToDo UnloadDecision = iota
	// UnloadProceed: the caller should perform the actual unload now.
	UnloadProceed
	// UnloadDeferred: busy or mid-session; the unload was recorded and runs
	// once the work drains.
	UnloadDeferred
)

// String returns the decision name, for logs.
func (d UnloadDecision) String() string {
	switch d {
	case UnloadProceed:
		return "proceed"
	case UnloadDeferred:
		return "deferred"
	case UnloadNothingToDo:
		return "nothing_to_do"
	}
	return "unknown"
}

// UnloadReason names why a deferred-capable unload was requested, for the
// caller's log line only. See RequestDeferredUnload.
type UnloadReason string

const (
	// UnloadReasonHealthFailure: the runtime stopped answering health checks.
	UnloadReasonHealthFailure UnloadReason = "health_failure"
	// UnloadReasonConfigChange: the model file or runtime args changed.
	UnloadReasonConfigChange UnloadReason = "config_change"
	// UnloadReasonIdleTimeout: nothing has used the model for long enough that
	// keeping several GB resident is no longer worth it.
	UnloadReasonIdleTimeout UnloadReason = "idle_timeout"
)

// ModelLifecycle is a pure value-type state machine for the model's
// load / inference / unload lifecycle.
//
// Port of `struct ModelLifecycle`. It performs NO I/O and knows nothing about
// processes, HTTP or files: callers translate its decisions into real load and
// unload calls, so every rule here is unit-testable without the several-GB
// model. Keep it that way.
//
// # It is a SINGLE-state machine, deliberately
//
// BeginInference returns true only from Ready, and returns false from Inferring
// while changing nothing. On iOS one user and one UI serialize this implicitly;
// on a LAN service two phones, two tabs, or a recipe generation overlapping a
// scan can reach BeginInference concurrently. Do NOT "fix" that here by adding
// mutexes, slots or multi-state support - it is contract-pinned. Serialization
// lives OUTSIDE this type, in the supervisor's capacity-1 inference gate which
// is acquired BEFORE BeginInference is ever called, so the machine is only ever
// asked when it can say yes (plan §0.1).
//
// The zero value is a usable machine in state Unloaded with no pending unload
// and no active sessions, exactly like Swift's `ModelLifecycle()`.
type ModelLifecycle struct {
	// state is the current lifecycle state.
	state ModelState

	// pendingUnload is set when an unload was requested while it was unsafe to
	// honor it. Honored the instant we are idle AND no session is active (see
	// DrainPendingUnload). A PressureWarning while busy NEVER sets this; only
	// an explicit unload request or a PressureCritical does.
	pendingUnload bool

	// activeSessionCount is how many user-visible model sessions (a scan over N
	// crops across N photos, a recipe generation) are in flight.
	//
	// This counter is the fix for the "Nothing spotted" field bug. The moments
	// BETWEEN the individual inferences of a session are Ready, but freeing the
	// model there guts the rest of the session: after the multi-GB load the
	// system reliably posts a memory warning, the old rules freed the client
	// mid-scan, and every remaining crop failed silently ("9 items analyzed,
	// nothing recognized"). While a session is active, PressureWarning is
	// ignored and PressureCritical defers to the end of the WHOLE session.
	activeSessionCount int
}

// State returns the current state. Read-only; the state is only ever changed by
// the transition methods below.
func (m ModelLifecycle) State() ModelState { return m.state }

// PendingUnload reports whether an unload is recorded and waiting to drain.
func (m ModelLifecycle) PendingUnload() bool { return m.pendingUnload }

// ActiveSessionCount returns how many sessions are currently open.
func (m ModelLifecycle) ActiveSessionCount() int { return m.activeSessionCount }

// SessionActive is true while at least one scan or recipe generation is running.
func (m ModelLifecycle) SessionActive() bool { return m.activeSessionCount > 0 }

// IsBusy reports whether an operation the client must survive is running.
func (m ModelLifecycle) IsBusy() bool {
	return m.state == Loading || m.state == Inferring || m.state == Unloading
}

// --- Loading ---------------------------------------------------------------

// BeginLoad registers a load intent.
//
//	From       | Returns          | To
//	-----------+------------------+-----------
//	Unloaded   | LoadProceed      | Loading
//	Loading    | LoadCoalesced    | Loading
//	Ready      | LoadAlreadyLoaded| Ready
//	Inferring  | LoadAlreadyLoaded| Inferring
//	Unloading  | LoadCoalesced    | Unloading
//
// Duplicate loads must never start a second multi-GB allocation. A load
// requested during a teardown folds in and the caller retries once idle.
func (m *ModelLifecycle) BeginLoad() LoadDecision {
	switch m.state {
	case Unloaded:
		m.state = Loading
		return LoadProceed
	case Loading:
		return LoadCoalesced
	case Ready, Inferring:
		return LoadAlreadyLoaded
	case Unloading:
		// A teardown is finishing; fold in and let the caller retry once idle.
		return LoadCoalesced
	}
	return LoadCoalesced
}

// FinishLoad completes a load.
//
//	From      | success | To        | Side effect
//	----------+---------+-----------+---------------------
//	Loading   | true    | Ready     | -
//	Loading   | false   | Unloaded  | pendingUnload = false
//	anything  | any     | unchanged | - (guard)
//
// The pendingUnload reset matters: if the load failed there is nothing resident
// to free, and leaving the flag set would make the next successful load
// immediately tear itself down. Pinned by TestFailedLoadDropsDeferredUnload.
func (m *ModelLifecycle) FinishLoad(success bool) {
	if m.state != Loading {
		return
	}
	if success {
		m.state = Ready
		return
	}
	m.state = Unloaded
	m.pendingUnload = false
}

// --- Inference -------------------------------------------------------------

// BeginInference enters inference, moving Ready -> Inferring and returning
// true. From ANY other state it returns false and changes nothing.
//
// The caller uses the bool to decide whether to run the matching
// FinishInference; without it a caller would "close" a bracket it never opened
// and corrupt the state. In Go the bracket is a defer, so it is panic- and
// cancellation-safe.
//
// A false return is NOT an inference failure for scan-outcome counting: it
// never reached the model, and counting it would manufacture a spurious Failed
// (plan §0.1). It must not be silently skipped either - that ends at
// TerminalPhase(0, 0) = Empty, the "Nothing spotted" lie.
func (m *ModelLifecycle) BeginInference() bool {
	if m.state != Ready {
		return false
	}
	m.state = Inferring
	return true
}

// FinishInference moves Inferring -> Ready. Guarded: a no-op from any other
// state.
func (m *ModelLifecycle) FinishInference() {
	if m.state != Inferring {
		return
	}
	m.state = Ready
}

// --- Unloading -------------------------------------------------------------

// RequestUnload is an explicit, non-pressure unload request.
//
//	From       | Returns           | To        | Side effect
//	-----------+-------------------+-----------+---------------------
//	Ready      | UnloadProceed     | Unloading | -
//	Loading    | UnloadDeferred    | Loading   | pendingUnload = true
//	Inferring  | UnloadDeferred    | Inferring | pendingUnload = true
//	Unloaded   | UnloadNothingToDo | Unloaded  | -
//	Unloading  | UnloadNothingToDo | Unloading | -
//
// Note that at Ready this proceeds EVEN DURING AN ACTIVE SESSION: an explicit
// request from the app or operator is trusted, and only *pressure* respects
// sessions. So an operator restart during a scan is not necessarily immediate  -
// a scan alternates Ready between calls and Inferring during a call; at Ready it
// proceeds, at Inferring it defers and fires on drain.
func (m *ModelLifecycle) RequestUnload() UnloadDecision {
	switch m.state {
	case Ready:
		m.state = Unloading
		return UnloadProceed
	case Loading, Inferring:
		m.pendingUnload = true
		return UnloadDeferred
	case Unloaded, Unloading:
		return UnloadNothingToDo
	}
	return UnloadNothingToDo
}

// HandleMemoryPressure processes a memory-pressure event.
//
//	From                | Level    | Returns           | To        | Side effect
//	--------------------+----------+-------------------+-----------+---------------------
//	Unloaded            | either   | UnloadNothingToDo | Unloaded  | -
//	Unloading           | either   | UnloadNothingToDo | Unloading | -
//	Ready, no session   | either   | UnloadProceed     | Unloading | -
//	Ready, in session   | warning  | UnloadNothingToDo | Ready     | -
//	Ready, in session   | critical | UnloadDeferred    | Ready     | pendingUnload = true
//	Loading             | warning  | UnloadNothingToDo | Loading   | -
//	Loading             | critical | UnloadDeferred    | Loading   | pendingUnload = true
//	Inferring           | warning  | UnloadNothingToDo | Inferring | -
//	Inferring           | critical | UnloadDeferred    | Inferring | pendingUnload = true
//
// Two rules, stated as rules:
//
//   - PressureWarning is ignored entirely while BUSY OR MID-SESSION. Loading and
//     inferring legitimately spike memory, so a warning during them is expected,
//     not actionable. It is NOT always-ignorable: at Ready and idle a plain
//     warning really does unload (TestWarningPressureWhenReadyIdleUnloads). A
//     port that treats warning as always-ignorable keeps a multi-GB model
//     resident through exactly the pressure it exists to relieve.
//   - PressureCritical while busy or mid-session is RECORDED, never acted on.
//     The model is freed the moment the work drains, and never mid-operation.
func (m *ModelLifecycle) HandleMemoryPressure(level MemoryPressureLevel) UnloadDecision {
	switch m.state {
	case Unloaded, Unloading:
		return UnloadNothingToDo
	case Ready:
		if !m.SessionActive() {
			m.state = Unloading
			return UnloadProceed
		}
		// Resident but mid-session (between crops / before the next call):
		// same rules as any busy phase, the session must keep its client.
		if level == PressureCritical {
			m.pendingUnload = true
			return UnloadDeferred
		}
		return UnloadNothingToDo
	case Loading, Inferring:
		if level == PressureCritical {
			m.pendingUnload = true
			return UnloadDeferred
		}
		return UnloadNothingToDo
	}
	return UnloadNothingToDo
}

// RequestDeferredUnload is a Linux-port addition that CHANGES NO BEHAVIOR.
//
// It is a thin alias for the PressureCritical branch of HandleMemoryPressure  -
// one implementation, not two - so that non-memory causes (a health-check
// failure, a config change, an idle timeout) stop having to lie about being
// memory events just to get "unload as soon as it is safe" semantics. The
// Swift has no such caller because iOS has no supervised child process to lose.
//
// Identical semantics and identical return values to
// HandleMemoryPressure(PressureCritical) in every state; only the name and the
// caller's log line differ. Pinned by
// TestRequestDeferredUnloadMatchesCriticalPressureInAllStates.
//
// The reason is carried for the caller's log line and is DELIBERATELY not
// consulted: every decision in this machine is the return value, never inferred
// from the caller's reason for calling.
func (m *ModelLifecycle) RequestDeferredUnload(reason UnloadReason) UnloadDecision {
	_ = reason
	return m.HandleMemoryPressure(PressureCritical)
}

// FinishUnload records that a teardown completed. Unconditional: -> Unloaded,
// pendingUnload = false.
func (m *ModelLifecycle) FinishUnload() {
	m.state = Unloaded
	m.pendingUnload = false
}

// DrainPendingUnload honors a deferred unload once we are back to
// idle-and-resident AND no session is running.
//
//	if pendingUnload AND state == Ready AND NOT sessionActive:
//	    pendingUnload = false; state = Unloading; return UnloadProceed
//	return UnloadNothingToDo
//
// All three conditions are load-bearing. The caller runs this after EVERY load
// and after EVERY inference, and at EndSession. The !sessionActive clause is
// what stops a critical event that arrived during a scan from freeing the model
// between crops - a mid-session drain would gut the remaining crops.
func (m *ModelLifecycle) DrainPendingUnload() UnloadDecision {
	if !m.pendingUnload || m.state != Ready || m.SessionActive() {
		return UnloadNothingToDo
	}
	m.pendingUnload = false
	m.state = Unloading
	return UnloadProceed
}

// --- Sessions --------------------------------------------------------------

// BeginSession marks the start of a user-visible model session (a whole scan, a
// whole recipe generation).
//
// Open it BEFORE the load, so pressure arriving during the load is already
// covered, and close it on EVERY exit path including cancellation and errors
// (in Go: defer EndSession()). Closing the session is what runs a deferred
// unload, so skipping it on the cancellation path leaves the model wired
// forever.
func (m *ModelLifecycle) BeginSession() {
	m.activeSessionCount++
}

// EndSession marks the end of a session and, when the last one ends, honors any
// unload that was deferred during it.
//
//	activeSessionCount = max(0, activeSessionCount - 1)
//	if sessionActive { return UnloadNothingToDo }   // others still running
//	return drainPendingUnload()                     // last one out honors it
//
// Sessions NEST - counted, not boolean - because a scan and a recipe run can
// overlap; the deferred unload fires only when the last one ends. The clamp at
// 0 makes an unbalanced EndSession harmless.
func (m *ModelLifecycle) EndSession() UnloadDecision {
	m.activeSessionCount--
	if m.activeSessionCount < 0 {
		m.activeSessionCount = 0
	}
	if m.SessionActive() {
		return UnloadNothingToDo
	}
	return m.DrainPendingUnload()
}
