package com.philwoolley.dinnerdecider.contract

/**
 * Explicit lifecycle state for the on-device model.
 *
 * Port of `DinnerDecider/Services/ModelLifecycle.swift`, specified by
 * `shared/pipeline/model-lifecycle.md` — the most important document in
 * `shared/`. It took three TestFlight builds to get right, and a port that skips
 * it reproduces two specific field bugs:
 *
 * - the **"Nothing spotted" empty scan** — the scan counts N items, analyzes them
 *   all, and reports nothing recognized, and
 * - the **mid-scan use-after-free crash** — the app dies the moment image
 *   analysis starts.
 *
 * Both have the same root cause: the multi-GB inference client was freed while
 * something was still using it.
 *
 * This is a **pure value type with no I/O**. It never touches the runtime: it
 * returns decisions and the caller translates them into real load/unload calls.
 * That separation is what makes every rule here testable without the
 * several-GB model, in milliseconds, on the host JVM with zero `android.*`
 * imports.
 */

/** The states the model can be in. */
enum class ModelState {
    /** No client resident. */
    UNLOADED,

    /** A load is in flight; no client yet. */
    LOADING,

    /** Client resident and idle. */
    READY,

    /** Client resident and serving an in-flight inference. */
    INFERRING,

    /** A client teardown is in progress. */
    UNLOADING,
}

/** System memory-pressure levels we react to. */
enum class MemoryPressureLevel {
    WARNING,
    CRITICAL,
}

/** Outcome of asking the lifecycle to start a load. */
enum class LoadDecision {
    /** Caller should perform the actual load. */
    PROCEED,

    /** A load is already in flight; this request folds into it. */
    COALESCED,

    /** The model is already resident; nothing to do. */
    ALREADY_LOADED,
}

/** Outcome of asking the lifecycle to unload. */
enum class UnloadDecision {
    /** Caller should perform the actual unload now. */
    PROCEED,

    /** Busy (loading/inferring/mid-session); recorded, and runs once idle. */
    DEFERRED,

    /** Nothing resident to unload. */
    NOTHING_TO_DO,
}

/**
 * The pure load / inference / unload state machine.
 *
 * **Every decision in this machine is the function's RETURN VALUE, and is never
 * inferred from the caller's reason for calling.** A caller that maps "the user
 * asked for an unload" straight to "free it now" will tear a client out from
 * under an in-flight encode — that is the build-1 use-after-free.
 */
class ModelLifecycle {

    var state: ModelState = ModelState.UNLOADED
        private set

    /**
     * Set when an unload was requested while it was unsafe to honor it. Honored
     * the instant we are idle **and** no session is active (see
     * [drainPendingUnload]).
     *
     * Set by an explicit unload request while busy, or by a [MemoryPressureLevel.CRITICAL]
     * event. **Never** by a [MemoryPressureLevel.WARNING].
     */
    var pendingUnload: Boolean = false
        private set

    /**
     * How many user-visible model sessions (a scan over N crops across N photos,
     * a whole recipe generation) are in flight.
     *
     * The moments BETWEEN the individual inferences of a session are [ModelState.READY],
     * but freeing the model there guts the rest of the session: after the
     * multi-GB load the system reliably posts a memory warning, the old rules
     * freed the client mid-scan, and every remaining crop failed silently
     * ("9 items analyzed, nothing recognized"). While a session is active a
     * WARNING is ignored and a CRITICAL defers to the end of the whole session.
     *
     * Counted, not boolean: sessions **nest**, because a scan and a recipe run
     * can overlap, and a deferred unload fires only when the last one ends.
     */
    var activeSessionCount: Int = 0
        private set

    /** True while at least one scan or recipe generation is running. */
    val sessionActive: Boolean
        get() = activeSessionCount > 0

    /** Whether an operation the client must survive is currently running. */
    val isBusy: Boolean
        get() = state == ModelState.LOADING ||
            state == ModelState.INFERRING ||
            state == ModelState.UNLOADING

    // ------------------------------------------------------------------
    // Loading
    // ------------------------------------------------------------------

    /**
     * Register a load intent.
     *
     * | From | Returns | To |
     * |---|---|---|
     * | UNLOADED | PROCEED | LOADING |
     * | LOADING | COALESCED | LOADING |
     * | READY | ALREADY_LOADED | READY |
     * | INFERRING | ALREADY_LOADED | INFERRING |
     * | UNLOADING | COALESCED | UNLOADING |
     *
     * Duplicate loads must never start a second multi-GB allocation. A load
     * requested during a teardown folds in and the caller retries once idle.
     */
    fun beginLoad(): LoadDecision = when (state) {
        ModelState.UNLOADED -> {
            state = ModelState.LOADING
            LoadDecision.PROCEED
        }

        ModelState.LOADING -> LoadDecision.COALESCED
        ModelState.READY, ModelState.INFERRING -> LoadDecision.ALREADY_LOADED
        // A teardown is finishing; fold in and let the caller retry once idle.
        ModelState.UNLOADING -> LoadDecision.COALESCED
    }

    /**
     * Complete a load.
     *
     * | From | success | To | Side effect |
     * |---|---|---|---|
     * | LOADING | true | READY | — |
     * | LOADING | false | UNLOADED | `pendingUnload = false` |
     * | anything else | any | unchanged (guard) | — |
     *
     * The `pendingUnload` reset matters: if the load failed there is nothing
     * resident to free, and leaving the flag set would make the next successful
     * load immediately tear itself down.
     */
    fun finishLoad(success: Boolean) {
        if (state != ModelState.LOADING) return
        if (success) {
            state = ModelState.READY
        } else {
            state = ModelState.UNLOADED
            pendingUnload = false
        }
    }

    // ------------------------------------------------------------------
    // Inference
    // ------------------------------------------------------------------

    /**
     * Enter inference. Returns true and moves READY -> INFERRING; returns false
     * from any other state and changes nothing.
     *
     * The caller uses the boolean to decide whether to run the matching
     * [finishInference]. Without it a caller would "close" a bracket it never
     * opened and corrupt the state. Bracket it so the close runs on every exit
     * path (Kotlin `try`/`finally`), including cancellation.
     */
    fun beginInference(): Boolean {
        if (state != ModelState.READY) return false
        state = ModelState.INFERRING
        return true
    }

    /** INFERRING -> READY. Guarded: a no-op from any other state. */
    fun finishInference() {
        if (state != ModelState.INFERRING) return
        state = ModelState.READY
    }

    // ------------------------------------------------------------------
    // Unloading
    // ------------------------------------------------------------------

    /**
     * An explicit (non-pressure) unload request.
     *
     * | From | Returns | To | Side effect |
     * |---|---|---|---|
     * | READY | PROCEED | UNLOADING | — |
     * | LOADING | DEFERRED | LOADING | `pendingUnload = true` |
     * | INFERRING | DEFERRED | INFERRING | `pendingUnload = true` |
     * | UNLOADED | NOTHING_TO_DO | UNLOADED | — |
     * | UNLOADING | NOTHING_TO_DO | UNLOADING | — |
     *
     * Note this proceeds at READY **even during an active session**: an explicit
     * request from the app is trusted, and only *pressure* respects sessions.
     * That is why an Android `ON_STOP` must be routed to
     * [handleMemoryPressure] with CRITICAL rather than to this function — it
     * would otherwise gut a scan the user merely backgrounded.
     */
    fun requestUnload(): UnloadDecision = when (state) {
        ModelState.READY -> {
            state = ModelState.UNLOADING
            UnloadDecision.PROCEED
        }

        ModelState.LOADING, ModelState.INFERRING -> {
            pendingUnload = true
            UnloadDecision.DEFERRED
        }

        ModelState.UNLOADED, ModelState.UNLOADING -> UnloadDecision.NOTHING_TO_DO
    }

    /**
     * A memory-pressure event.
     *
     * | From | Level | Returns | To | Side effect |
     * |---|---|---|---|---|
     * | UNLOADED | either | NOTHING_TO_DO | UNLOADED | — |
     * | UNLOADING | either | NOTHING_TO_DO | UNLOADING | — |
     * | READY, **no** session | either | PROCEED | UNLOADING | — |
     * | READY, session active | WARNING | NOTHING_TO_DO | READY | — |
     * | READY, session active | CRITICAL | DEFERRED | READY | `pendingUnload = true` |
     * | LOADING | WARNING | NOTHING_TO_DO | LOADING | — |
     * | LOADING | CRITICAL | DEFERRED | LOADING | `pendingUnload = true` |
     * | INFERRING | WARNING | NOTHING_TO_DO | INFERRING | — |
     * | INFERRING | CRITICAL | DEFERRED | INFERRING | `pendingUnload = true` |
     *
     * Two rules, stated as rules:
     * - **WARNING is ignored entirely while busy or mid-session.** Loading and
     *   inferring legitimately spike memory, so a warning during them is
     *   expected, not actionable. It is **not** ignorable otherwise: at READY and
     *   idle a plain WARNING really does unload. A port that treats WARNING as
     *   always-ignorable keeps a multi-GB model resident through exactly the
     *   pressure it exists to relieve.
     * - **CRITICAL while busy or mid-session is recorded, never acted on.** The
     *   model is freed the moment the work drains, and never mid-operation.
     */
    fun handleMemoryPressure(level: MemoryPressureLevel): UnloadDecision = when (state) {
        ModelState.UNLOADED, ModelState.UNLOADING -> UnloadDecision.NOTHING_TO_DO

        ModelState.READY -> {
            if (!sessionActive) {
                state = ModelState.UNLOADING
                UnloadDecision.PROCEED
            } else {
                // Resident but mid-session (between crops / before the next
                // call): same rules as any busy phase, the session keeps its
                // client.
                deferOrIgnore(level)
            }
        }

        ModelState.LOADING, ModelState.INFERRING -> deferOrIgnore(level)
    }

    /** WARNING is ignored; CRITICAL is recorded for the next drain. */
    private fun deferOrIgnore(level: MemoryPressureLevel): UnloadDecision = when (level) {
        MemoryPressureLevel.WARNING -> UnloadDecision.NOTHING_TO_DO
        MemoryPressureLevel.CRITICAL -> {
            pendingUnload = true
            UnloadDecision.DEFERRED
        }
    }

    /** Unconditional: -> UNLOADED, `pendingUnload = false`. */
    fun finishUnload() {
        state = ModelState.UNLOADED
        pendingUnload = false
    }

    /**
     * Honor a deferred unload once we are back to idle-and-resident **and** no
     * session is running.
     *
     * All three conditions are load-bearing. The caller runs this **after every
     * load and after every inference**, recipe generation included. The
     * `!sessionActive` clause is what stops a CRITICAL that arrived during a scan
     * from freeing the model between crops: a mid-session drain would gut the
     * remaining crops.
     */
    fun drainPendingUnload(): UnloadDecision {
        if (!pendingUnload || state != ModelState.READY || sessionActive) {
            return UnloadDecision.NOTHING_TO_DO
        }
        pendingUnload = false
        state = ModelState.UNLOADING
        return UnloadDecision.PROCEED
    }

    // ------------------------------------------------------------------
    // Sessions
    // ------------------------------------------------------------------

    /**
     * Mark the start of a user-visible model session (scan / recipe run).
     *
     * Open the session **before** the load, so pressure arriving during the load
     * is already covered.
     */
    fun beginSession() {
        activeSessionCount += 1
    }

    /**
     * Mark the end of a session. When the last session ends, any unload that was
     * deferred during it (a CRITICAL event) finally runs.
     *
     * The clamp at 0 makes an unbalanced `endSession` harmless. Close the session
     * on **every** exit path including cancellation and thrown errors (Kotlin
     * `try`/`finally`) — closing it is what runs a deferred unload, so skipping
     * it on the cancellation path leaves the model resident forever.
     */
    fun endSession(): UnloadDecision {
        activeSessionCount = maxOf(0, activeSessionCount - 1)
        if (sessionActive) return UnloadDecision.NOTHING_TO_DO
        return drainPendingUnload()
    }

    // ------------------------------------------------------------------
    // Value semantics (Swift's ModelLifecycle is Equatable)
    // ------------------------------------------------------------------

    override fun equals(other: Any?): Boolean {
        if (this === other) return true
        if (other !is ModelLifecycle) return false
        return state == other.state &&
            pendingUnload == other.pendingUnload &&
            activeSessionCount == other.activeSessionCount
    }

    override fun hashCode(): Int {
        var result = state.hashCode()
        result = 31 * result + pendingUnload.hashCode()
        result = 31 * result + activeSessionCount
        return result
    }

    override fun toString(): String =
        "ModelLifecycle(state=$state, pendingUnload=$pendingUnload, activeSessionCount=$activeSessionCount)"
}
