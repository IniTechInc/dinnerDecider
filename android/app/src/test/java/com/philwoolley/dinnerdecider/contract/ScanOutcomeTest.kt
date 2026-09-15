package com.philwoolley.dinnerdecider.contract

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Test

/**
 * Port of `DinnerDeciderTests/ScanOutcomeTests.swift`, Swift names preserved.
 *
 * TestFlight feedback P2: a scan whose model calls all FAILED was reported with
 * the same "Nothing spotted" empty state as a scan of a genuinely empty fridge.
 * Users were told to add light and move closer when the real problem was that the
 * model never answered. The two cases must be distinguishable.
 */
class ScanOutcomeTest {

    @Test
    fun testItemsFoundFinishes() {
        assertEquals(
            ScanPhase.FINISHED,
            ScanOutcome.terminalPhase(itemsFound = 3, inferenceFailures = 0),
        )
    }

    /**
     * Partial failure still counts as a success: the user gets what was found
     * rather than an error that throws away real results.
     */
    @Test
    fun testItemsFoundWinsOverFailures() {
        assertEquals(
            ScanPhase.FINISHED,
            ScanOutcome.terminalPhase(itemsFound = 2, inferenceFailures = 5),
        )
    }

    /**
     * The genuinely-empty case: every call answered, none of them saw food. This is
     * the only case where "Nothing spotted" is honest.
     */
    @Test
    fun testNothingFoundAndNoFailuresIsEmpty() {
        assertEquals(
            ScanPhase.EMPTY,
            ScanOutcome.terminalPhase(itemsFound = 0, inferenceFailures = 0),
        )
    }

    /**
     * The regression this type exists to prevent. Before the fix this returned
     * `.empty`, which told the user their fridge looked empty when in fact every
     * single inference had thrown.
     */
    @Test
    fun testNothingFoundWithFailuresIsFailedNotEmpty() {
        val phase = ScanOutcome.terminalPhase(itemsFound = 0, inferenceFailures = 4)
        assertEquals(ScanPhase.FAILED, phase)
        assertNotEquals(
            "A scan where every model call failed must not claim the fridge was empty",
            ScanPhase.EMPTY,
            phase,
        )
    }

    /** One swallowed failure is enough to make "Nothing spotted" a lie. */
    @Test
    fun testSingleFailureIsEnoughToReportFailure() {
        assertEquals(
            ScanPhase.FAILED,
            ScanOutcome.terminalPhase(itemsFound = 0, inferenceFailures = 1),
        )
    }

    /**
     * Mirrors the real budget: four calls, all throwing (the build-4 field report
     * where the model unloaded mid-scan and every identify threw `modelNotLoaded`).
     */
    @Test
    fun testExhaustedBudgetAllThrowingReportsFailure() {
        val budget = ScanCallBudget()
        var failures = 0
        while (budget.consume()) failures += 1

        assertEquals(4, failures)
        assertEquals(
            ScanPhase.FAILED,
            ScanOutcome.terminalPhase(itemsFound = 0, inferenceFailures = failures),
        )
    }
}

/**
 * Kept out of [ScanOutcomeTest] so that suite stays at exactly the six cases
 * `ScanOutcomeTests.swift` pins.
 */
class ScanPhaseTest {

    /** Six phases, matching iOS case for case. FAILED is one of them. */
    @Test
    fun testScanPhaseHasSixValuesIncludingFailed() {
        assertEquals(
            listOf("IDLE", "SCANNING", "FINISHED", "EMPTY", "FAILED", "CANCELLED"),
            ScanPhase.entries.map { it.name },
        )
    }
}
