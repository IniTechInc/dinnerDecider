package com.philwoolley.dinnerdecider.vision

import android.graphics.Bitmap
import android.graphics.Color
import androidx.test.ext.junit.runners.AndroidJUnit4
import com.philwoolley.dinnerdecider.contract.ToneCurveLut
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import kotlin.math.abs
import kotlin.math.max

/**
 * The half of [ModelInputPreprocessor] that needs a real rasteriser: that the
 * downscale genuinely resamples, and that it resamples **already-curved** pixels.
 *
 * **Device status: WRITTEN, NOT RUN.** Needs an arm64 emulator (plan 0.11 / §2).
 */
@RunWith(AndroidJUnit4::class)
class ModelInputPreprocessorInstrumentedTest {

    private fun checkerboard(width: Int, height: Int, dark: Int, light: Int): Bitmap {
        val bitmap = Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888)
        val pixels = IntArray(width * height) { index ->
            val x = index % width
            val y = index / width
            if (((x / 8) + (y / 8)) % 2 == 0) dark else light
        }
        bitmap.setPixels(pixels, 0, width, 0, 0, width, height)
        return bitmap
    }

    private fun meanChannel(bitmap: Bitmap): Double {
        val pixels = IntArray(bitmap.width * bitmap.height)
        bitmap.getPixels(pixels, 0, bitmap.width, 0, 0, bitmap.width, bitmap.height)
        return pixels.sumOf { (it and 0xFF).toDouble() } / pixels.size
    }

    /**
     * The order proof that only a real resampler can give.
     *
     * A checkerboard of 20 and 200 downscaled to 896 px averages its cells. Curve
     * first and the mean of the result is near `mean(lut[20], lut[200])`; downscale
     * first and it is near `lut[mean(20, 200)] = lut[110]`. Those are materially
     * different numbers, because the curve is non-linear in exactly the shadow
     * range this pass exists to lift.
     */
    @Test
    fun theCurveIsAppliedBeforeResamplingNotAfter() {
        val source = checkerboard(2016, 2016, Color.rgb(20, 20, 20), Color.rgb(200, 200, 200))
        val result = ModelInputPreprocessor().prepare(source)

        assertEquals(896, max(result.width, result.height))

        val curveThenScale = (ToneCurveLut.apply(20) + ToneCurveLut.apply(200)) / 2.0
        val scaleThenCurve = ToneCurveLut.apply(110).toDouble()
        assertTrue(
            "fixture is only meaningful if the two orders differ measurably",
            abs(curveThenScale - scaleThenCurve) > 4.0,
        )

        val actual = meanChannel(result)
        assertTrue(
            "mean $actual should track curve-then-scale ($curveThenScale), not scale-then-curve ($scaleThenCurve)",
            abs(actual - curveThenScale) < abs(actual - scaleThenCurve),
        )
    }

    @Test
    fun theDownscaleGenuinelyResamplesRatherThanCropping() {
        val source = checkerboard(1792, 1792, Color.rgb(0, 0, 0), Color.rgb(255, 255, 255))
        val result = ModelInputPreprocessor().prepare(source)

        assertEquals(896, result.width)
        assertEquals(896, result.height)
        // A crop of one corner would be uniformly one shade; a resample keeps both.
        val mean = meanChannel(result)
        assertTrue("mean $mean suggests a crop, not a resample", mean in 60.0..200.0)
    }

    @Test
    fun oneInstanceSurvivesManyCropsWithoutReallocatingPerCrop() {
        val preprocessor = ModelInputPreprocessor()
        preprocessor.prepare(checkerboard(2016, 1512, Color.BLACK, Color.WHITE))
        val capacity = preprocessor.scratchCapacity

        repeat(16) {
            preprocessor.prepare(checkerboard(504, 504, Color.BLACK, Color.WHITE)).recycle()
        }
        assertEquals(capacity, preprocessor.scratchCapacity)
    }

    @Test
    fun theOutputIsAlwaysOpaqueArgb8888ReadyForThePackedRgbConversion() {
        // The JNI shim memcpys nx*ny*3 tightly-packed RGB with no length parameter,
        // so the config reaching it must be predictable.
        val result = ModelInputPreprocessor().prepare(checkerboard(1200, 900, Color.BLACK, Color.WHITE))
        assertEquals(Bitmap.Config.ARGB_8888, result.config)
        val pixels = IntArray(result.width * result.height)
        result.getPixels(pixels, 0, result.width, 0, 0, result.width, result.height)
        assertTrue(pixels.all { ((it shr 24) and 0xFF) == 0xFF })
    }
}
