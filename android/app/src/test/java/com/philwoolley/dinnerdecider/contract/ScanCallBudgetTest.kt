package com.philwoolley.dinnerdecider.contract

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Port of `DinnerDeciderTests/ScanCallBudgetTests.swift`.
 *
 * The scan-wide cap on heavy model calls (field fix: an 8 GB phone jetsammed on
 * the fifth heavy call of a scan, so a scan gets at most four).
 */
class ScanCallBudgetTest {

    @Test
    fun testDefaultBudgetAllowsExactlyFourCalls() {
        val budget = ScanCallBudget()
        assertTrue(budget.consume()) // whole-image pass
        assertTrue(budget.consume()) // crop 1
        assertTrue(budget.consume()) // crop 2
        assertTrue(budget.consume()) // crop 3
        assertFalse(budget.consume()) // fifth call: the one that crashed
        assertEquals(0, budget.remaining)
    }

    @Test
    fun testRemainingDrivesCropCapping() {
        val budget = ScanCallBudget()
        budget.consume() // whole-image pass spends one
        assertEquals(3, budget.remaining)

        val nineCrops = List(9) { "crop" }
        val capped = nineCrops.take(budget.remaining)
        assertEquals(3, capped.size)
    }

    @Test
    fun testExhaustedBudgetStaysExhausted() {
        val budget = ScanCallBudget(limit = 1)
        assertTrue(budget.consume())
        assertFalse(budget.consume())
        assertFalse(budget.consume())
        assertEquals(0, budget.remaining)
    }
}
