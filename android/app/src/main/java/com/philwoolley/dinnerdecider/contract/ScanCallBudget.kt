package com.philwoolley.dinnerdecider.contract

/**
 * The scan-wide cap on heavy model calls.
 *
 * Port of `AppModel.swift:39-52`. Field fix: an 8 GB phone jetsammed on the
 * **fifth** heavy call of a scan, so a scan gets at most four — one whole-image
 * pass plus at most three crops.
 *
 * The budget is **scan-wide across all photos**, not per photo, and it is spent by
 * the whole-image pass before the crop loop ever starts. That is why at most 3 of
 * the 16 emitted tiles are ever analyzed (`shared/pipeline/scan-pipeline.md`).
 *
 * Swift models this as a `struct` with a `mutating func`; the Kotlin equivalent is
 * a small mutable class. Callers must therefore pass the same instance around
 * rather than copying it, and must read [remaining] **before** the loop that
 * consumes it (`prefix(budget.remaining)`), not during.
 *
 * Not thread-safe by design: the scan orchestrator owns exactly one of these and
 * drives it from a single coroutine.
 */
class ScanCallBudget(limit: Int = DEFAULT_LIMIT) {

    /**
     * Calls still available. Readable before consuming, which is how the crop list
     * is capped.
     */
    var remaining: Int = limit
        private set

    /**
     * Spend one call. Returns false when the budget is exhausted — and once
     * exhausted it stays exhausted for the life of the scan; there is no refill.
     */
    fun consume(): Boolean {
        if (remaining <= 0) return false
        remaining -= 1
        return true
    }

    companion object {
        /**
         * Four heavy calls per scan. A named, documented parameter: change it only
         * alongside iOS, since it is very likely the cap on recall.
         */
        const val DEFAULT_LIMIT: Int = 4
    }
}
