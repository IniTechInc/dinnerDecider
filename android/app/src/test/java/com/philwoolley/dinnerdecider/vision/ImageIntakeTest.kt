package com.philwoolley.dinnerdecider.vision

import android.graphics.Bitmap
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import kotlin.math.max

/**
 * Host-JVM cover for the intake arithmetic and the software-allocation guard.
 *
 * Robolectric gives real [Bitmap] geometry and a real ARGB_8888 pixel buffer; it
 * does not rasterise a [android.graphics.Canvas], so these tests assert
 * **dimensions, config and identity**, never pixel content. Resampling fidelity
 * belongs to the instrumented suite.
 */
@RunWith(RobolectricTestRunner::class)
class ImageIntakeTest {

    private fun bitmap(width: Int, height: Int): Bitmap =
        Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888)

    @Test
    fun captureMaxSideMatchesTheShippedConstant() {
        // ImageDownscaler.captureMaxSide = 2016.
        assertEquals(2016, ImageIntake.CAPTURE_MAX_SIDE)
    }

    @Test
    fun downscaleLeavesASmallImageUntouched() {
        val source = bitmap(1000, 800)
        assertSame(source, ImageIntake.downscaled(source))
    }

    @Test
    fun downscaleCapsTheLongestSideAndRoundsTheOther() {
        // 4032x3024 (a 12 MP phone frame) -> longest side exactly 2016, 2:1.5 kept.
        val scaled = ImageIntake.downscaled(bitmap(4032, 3024))
        assertEquals(2016, scaled.width)
        assertEquals(1512, scaled.height)
    }

    @Test
    fun downscaleHonoursPortraitOrientation() {
        val scaled = ImageIntake.downscaled(bitmap(3024, 4032))
        assertEquals(2016, scaled.height)
        assertEquals(1512, scaled.width)
    }

    @Test
    fun downscaleRoundsRatherThanTruncates() {
        // 5000x3001 -> scale 0.4032; 3001 * 0.4032 = 1210.0 (rounds to 1210).
        val scaled = ImageIntake.downscaled(bitmap(5000, 3001))
        assertEquals(2016, scaled.width)
        assertEquals(1210, scaled.height)
        assertTrue(max(scaled.width, scaled.height) <= ImageIntake.CAPTURE_MAX_SIDE)
    }

    @Test
    fun ensureSoftwareReturnsAReadableArgb8888Untouched() {
        val source = bitmap(4, 4)
        assertSame(source, ImageIntake.ensureSoftware(source))
    }

    @Test
    fun ensureSoftwareCopiesANonArgb8888Bitmap() {
        val source = Bitmap.createBitmap(4, 4, Bitmap.Config.RGB_565)
        val software = ImageIntake.ensureSoftware(source)
        assertNotNull(software)
        assertEquals(Bitmap.Config.ARGB_8888, software!!.config)
    }

    @Test
    fun ensureSoftwareRefusesARecycledBitmap() {
        val source = bitmap(4, 4)
        source.recycle()
        // Null, not an exception: the caller emits zero crops rather than crashing,
        // exactly as iOS does when normalizedCGImage returns nil.
        assertEquals(null, ImageIntake.ensureSoftware(source))
    }

    @Test
    fun forCaptureProducesAnOpaqueSoftwareBitmapAtTheCap() {
        val captured = ImageIntake.forCapture(bitmap(4032, 3024))
        assertNotNull(captured)
        assertEquals(2016, captured!!.width)
        assertEquals(1512, captured.height)
        assertEquals(Bitmap.Config.ARGB_8888, captured.config)
    }

    @Test
    fun rotationBakingSwapsTheAxesForQuarterTurns() {
        val source = bitmap(100, 50)
        assertSame(source, ImageIntake.withRotationBaked(source, 0))
        val rotated = ImageIntake.withRotationBaked(source, 90)
        assertEquals(50, rotated.width)
        assertEquals(100, rotated.height)
        val halfTurn = ImageIntake.withRotationBaked(source, 180)
        assertEquals(100, halfTurn.width)
        assertEquals(50, halfTurn.height)
    }

    @Test
    fun sampleSizeStaysAtOrAboveTheTargetSoTheExactResizeStillHasPixels() {
        assertEquals(1, ImageIntake.sampleSizeFor(2016, 1512, 2016))
        assertEquals(1, ImageIntake.sampleSizeFor(4000, 3000, 2016))
        assertEquals(2, ImageIntake.sampleSizeFor(4032, 3024, 2016))
        assertEquals(4, ImageIntake.sampleSizeFor(8064, 6048, 2016))
        // Degenerate input must never yield 0 (which BitmapFactory treats as 1 anyway,
        // but a 0 here would mean the arithmetic is wrong somewhere upstream).
        assertEquals(1, ImageIntake.sampleSizeFor(0, 0, 2016))
    }
}
