package com.philwoolley.dinnerdecider.contract

/**
 * Where the scan pipeline currently is, for the Scanning screen.
 *
 * Port of `AppModel.swift:55-68`. Six values, matching iOS case for case.
 *
 * iOS's `.scanning` carries a `ScanProgress` payload; here the phase is a plain
 * enum and progress is surfaced alongside it by the scan orchestrator, so the
 * terminal-phase decision below stays a pure function of two integers.
 */
enum class ScanPhase {
    /** Nothing running. */
    IDLE,

    /** A scan is in flight. */
    SCANNING,

    /** Finished with at least one item found. */
    FINISHED,

    /** Finished, every model call answered, and none of them saw food. */
    EMPTY,

    /**
     * Finished with nothing found because the model calls themselves failed.
     *
     * Distinct from [EMPTY]: the fridge may well be full, we just never got an
     * answer.
     */
    FAILED,

    /** The user cancelled part way through. */
    CANCELLED,
}

/**
 * Decides how a finished scan is reported to the user.
 *
 * Port of `AppModel.swift:70-84`. Shipped iOS behavior, not a port-level choice.
 *
 * TestFlight feedback P2: inference errors were swallowed (a `try?` on the
 * whole-image pass, a bare `catch { print(...) }` in the crop loop) and the
 * terminal phase was chosen purely on "are there items". So a scan where every
 * single model call threw looked exactly like a scan of an empty fridge, and the
 * user was told to add light and move closer when the real problem was that the
 * model never answered.
 *
 * The counting rule that makes this correct lives in the orchestrator, not here:
 * **only a thrown error counts as a failure.** `identifyAllItems` returning an
 * empty list from an unreadable response *answered*; counting that would produce a
 * spurious [ScanPhase.FAILED].
 */
object ScanOutcome {

    /**
     * The terminal phase for a scan that ran to completion.
     *
     * - Anything found wins, **regardless of how many calls failed alongside it**  - 
     *   the user gets what was found rather than an error that throws away real
     *   results.
     * - Nothing found, but something threw: [ScanPhase.FAILED].
     * - Nothing found and nothing threw: [ScanPhase.EMPTY], the only case where
     *   "Nothing spotted" is honest.
     */
    fun terminalPhase(itemsFound: Int, inferenceFailures: Int): ScanPhase {
        // Anything found is worth showing, even if some calls failed alongside it.
        if (itemsFound > 0) return ScanPhase.FINISHED
        return if (inferenceFailures > 0) ScanPhase.FAILED else ScanPhase.EMPTY
    }
}
