package com.philwoolley.dinnerdecider.contract

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import kotlin.math.abs
import kotlin.math.roundToInt

/**
 * Contract tests for the pre-inference tone curve.
 *
 * iOS gets this from Core Image and therefore has no test for it; here the curve
 * is a pure 256-entry table, so it is checked with no device at all. What the
 * tests pin is the *shape* — endpoints, monotonicity, and the five control
 * points — rather than a byte-for-byte match with `CIToneCurve`, whose exact
 * interpolant Apple does not document.
 */
class ToneCurveLutTest {

    /** One 8-bit step: the tolerance the control points are checked to. */
    private val oneStep = 1.0

    @Test
    fun tableHasExactly256Entries() {
        assertEquals(256, ToneCurveLut.table.size)
    }

    @Test
    fun endpointsMatchTheControlPoints() {
        // Black is lifted to 0.05: 0.05 * 255 = 12.75 -> 13.
        assertEquals(13, ToneCurveLut.table[0])
        // White stays white.
        assertEquals(255, ToneCurveLut.table[255])
    }

    @Test
    fun tableIsMonotonicNonDecreasingAcrossAll256Entries() {
        for (i in 0 until 255) {
            assertTrue(
                "table[$i] = ${ToneCurveLut.table[i]} > table[${i + 1}] = ${ToneCurveLut.table[i + 1]}",
                ToneCurveLut.table[i] <= ToneCurveLut.table[i + 1],
            )
        }
    }

    @Test
    fun tablePassesThroughTheFiveControlPointsWithinOneStep() {
        for ((x, y) in ToneCurveLut.CONTROL_POINTS) {
            val index = (x * 255.0).roundToInt()
            val target = y * 255.0
            val actual = ToneCurveLut.table[index].toDouble()
            assertTrue(
                "control point ($x, $y): table[$index] = $actual, expected ~$target",
                abs(actual - target) <= oneStep,
            )
        }
    }

    @Test
    fun evaluateIsExactAtTheControlPoints() {
        // Off the 8-bit grid the interpolant must hit the control points exactly.
        for ((x, y) in ToneCurveLut.CONTROL_POINTS) {
            assertEquals(y, ToneCurveLut.evaluate(x), 1e-9)
        }
    }

    @Test
    fun evaluateClampsOutsideTheUnitInterval() {
        assertEquals(0.05, ToneCurveLut.evaluate(-1.0), 1e-9)
        assertEquals(1.0, ToneCurveLut.evaluate(2.0), 1e-9)
    }

    @Test
    fun curveLiftsShadowsAndKeepsMidtonesAboveTheIdentity() {
        // The whole point of the curve: dark pixels get brighter, so label text in
        // a dim fridge survives the downscale.
        assertTrue(ToneCurveLut.table[0] > 0)
        assertTrue(ToneCurveLut.table[32] > 32)
        assertTrue(ToneCurveLut.table[128] > 128)
    }

    @Test
    fun applyClampsOutOfRangeChannelValues() {
        assertEquals(ToneCurveLut.table[0], ToneCurveLut.apply(-5))
        assertEquals(ToneCurveLut.table[255], ToneCurveLut.apply(999))
        assertEquals(ToneCurveLut.table[77], ToneCurveLut.apply(77))
    }
}
