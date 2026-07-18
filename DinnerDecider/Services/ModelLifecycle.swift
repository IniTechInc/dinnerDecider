import Foundation

/// Explicit lifecycle state for the on-device model.
///
/// The multi-GB llama client holds its weights, mmproj and KV cache in WIRED
/// Metal buffers. Freeing that client while a load or an inference is still in
/// flight is a use-after-free (and, if a reload starts before the old operation
/// drains, a double-allocation OOM). This machine exists so those rules live in
/// one pure, unit-testable place instead of being spread across ad-hoc booleans.
enum ModelState: Equatable {
    /// No client resident.
    case unloaded
    /// A load is in flight (no client yet).
    case loading
    /// Client resident and idle.
    case ready
    /// Client resident and serving an in-flight inference.
    case inferring
    /// A client teardown is in progress.
    case unloading
}

/// System memory-pressure levels we react to.
enum MemoryPressureLevel: Equatable {
    case warning
    case critical
}

/// Outcome of asking the lifecycle to start a load.
enum LoadDecision: Equatable {
    /// Caller should perform the actual load.
    case proceed
    /// A load is already in flight; this request folds into it.
    case coalesced
    /// The model is already resident; nothing to do.
    case alreadyLoaded
}

/// Outcome of asking the lifecycle to unload.
enum UnloadDecision: Equatable {
    /// Caller should perform the actual unload now.
    case proceed
    /// Busy (loading/inferring); the unload was recorded and runs once idle.
    case deferred
    /// Nothing resident to unload.
    case nothingToDo
}

/// A pure value-type state machine for the model's load/inference/unload
/// lifecycle. It never touches the runtime; callers translate its decisions into
/// real `loadModel()` / `unloadModel()` calls, so every rule here is testable
/// without the several-GB model.
struct ModelLifecycle: Equatable {

    private(set) var state: ModelState = .unloaded

    /// Set when an unload was requested while busy. Honored the instant we go
    /// idle (see `drainPendingUnload`). A `.warning` while busy never sets this;
    /// only an explicit unload request or a `.critical` event does.
    private(set) var pendingUnload = false

    /// How many user-visible model sessions (a scan over N crops, a recipe
    /// generation) are in flight. The moments BETWEEN the individual inferences
    /// of a session are `.ready`, but freeing the model there guts the rest of
    /// the session: after the multi-GB load the system reliably posts a memory
    /// warning, the old rules freed the client mid-scan, and every remaining
    /// crop failed silently ("9 items analyzed, nothing recognized"). While a
    /// session is active, `.warning` is ignored and `.critical` defers to the
    /// end of the whole session.
    private(set) var activeSessionCount = 0

    /// True while at least one scan or recipe generation is running.
    var sessionActive: Bool { activeSessionCount > 0 }

    /// Whether an operation the client must survive is currently running.
    var isBusy: Bool { state == .loading || state == .inferring || state == .unloading }

    // MARK: - Loading

    /// Register a load intent. Duplicate loads while one is in flight coalesce,
    /// and a load while already resident is a no-op.
    mutating func beginLoad() -> LoadDecision {
        switch state {
        case .unloaded:
            state = .loading
            return .proceed
        case .loading:
            return .coalesced
        case .ready, .inferring:
            return .alreadyLoaded
        case .unloading:
            // A teardown is finishing; fold in and let the caller retry once idle.
            return .coalesced
        }
    }

    /// Complete a load. On failure we fall back to `.unloaded` and drop any
    /// deferred unload (there is nothing left to free).
    mutating func finishLoad(success: Bool) {
        guard state == .loading else { return }
        if success {
            state = .ready
        } else {
            state = .unloaded
            pendingUnload = false
        }
    }

    // MARK: - Inference

    /// Enter inference. Returns false if the model is not ready, so the caller
    /// does not falsely bracket an operation that never touched the client.
    mutating func beginInference() -> Bool {
        guard state == .ready else { return false }
        state = .inferring
        return true
    }

    mutating func finishInference() {
        guard state == .inferring else { return }
        state = .ready
    }

    // MARK: - Unloading

    /// An explicit (non-pressure) unload request. Deferred while busy so an
    /// in-flight load or inference is never torn out from under itself.
    mutating func requestUnload() -> UnloadDecision {
        switch state {
        case .ready:
            state = .unloading
            return .proceed
        case .loading, .inferring:
            pendingUnload = true
            return .deferred
        case .unloaded, .unloading:
            return .nothingToDo
        }
    }

    /// A memory-pressure event.
    ///
    /// - `.warning` is IGNORED entirely while loading/inferring: those phases
    ///   legitimately spike memory, so a warning is expected, not actionable. It
    ///   only frees the model when idle-and-resident.
    /// - `.critical` while busy is RECORDED (`pendingUnload`) so the model is
    ///   freed the moment the operation drains, but never mid-operation.
    mutating func handleMemoryPressure(_ level: MemoryPressureLevel) -> UnloadDecision {
        switch state {
        case .unloaded, .unloading:
            return .nothingToDo
        case .ready:
            guard sessionActive else {
                state = .unloading
                return .proceed
            }
            // Resident but mid-session (between crops / before the next call):
            // same rules as any busy phase, the session must keep its client.
            switch level {
            case .warning:
                return .nothingToDo
            case .critical:
                pendingUnload = true
                return .deferred
            }
        case .loading, .inferring:
            switch level {
            case .warning:
                return .nothingToDo
            case .critical:
                pendingUnload = true
                return .deferred
            }
        }
    }

    mutating func finishUnload() {
        state = .unloaded
        pendingUnload = false
    }

    /// Honor a deferred unload once we are back to idle-and-resident AND no
    /// session is running (a mid-session drain would gut the remaining crops).
    /// Returns `.proceed` when the caller should now perform the real unload.
    mutating func drainPendingUnload() -> UnloadDecision {
        guard pendingUnload, state == .ready, !sessionActive else { return .nothingToDo }
        pendingUnload = false
        state = .unloading
        return .proceed
    }

    // MARK: - Sessions

    /// Mark the start of a user-visible model session (scan / recipe run).
    mutating func beginSession() {
        activeSessionCount += 1
    }

    /// Mark the end of a session. When the last session ends, any unload that
    /// was deferred during it (a `.critical` event) finally runs.
    mutating func endSession() -> UnloadDecision {
        activeSessionCount = max(0, activeSessionCount - 1)
        guard !sessionActive else { return .nothingToDo }
        return drainPendingUnload()
    }
}
