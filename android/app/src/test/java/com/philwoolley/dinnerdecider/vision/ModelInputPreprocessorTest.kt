package com.philwoolley.dinnerdecider.vision

import android.graphics.Bitmap
import android.graphics.Color
import com.philwoolley.dinnerdecider.contract.ToneCurveLut
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import kotlin.math.max

/**
 * Host-JVM cover for the two things about [ModelInputPreprocessor] that are
 * decisions: **the tone curve runs at full resolution, before the downscale**,
 * and **the scratch buffer is sized for the pre-downscale bitmap**.
 *
 * Robolectric gives real ARGB_8888 pixel storage, so the LUT round trip is
 * genuinely checked. It does not resample, so resampling fidelity is left to the
 * instrumented suite — but the ordering proof does not need resampling: the
 * buffer capacity after a full-size image is itself the evidence that
 * [Bitmap.getPixels] ran on the full-size image.
 */
@RunWith(RobolectricTestRunner::class)
class ModelInputPreprocessorTest {

    private fun filled(width: Int, height: Int, color: Int): Bitmap =
        Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888).apply { eraseColor(color) }

    private fun firstPixel(bitmap: Bitmap): Int {
        val pixels = IntArray(1)
        bitmap.getPixels(pixels, 0, bitmap.width, 0, 0, 1, 1)
        return pixels[0]
    }

    // MARK: - Constants

    @Test
    fun theModelInputCapMatchesTheShippedConstant() {
        // GemmaLLMService.maxImageSide = 896.
        assertEquals(896, ModelInputPreprocessor.MAX_IMAGE_SIDE)
    }

    /**
     * The regression guard, stated as an assertion. Sizing the scratch buffer for
     * 896² is an out-of-bounds write on the very first full-size image, and the
     * tempting "fix" is to move the tone curve after the downscale — which
     * silently changes what the model sees.
     */
    @Test
    fun theScratchBoundIsThePreDownscaleSquareNotEightNinetySixSquared() {
        assertEquals(2016 * 2016, ModelInputPreprocessor.MAX_SCRATCH_PIXELS)
        assertEquals(
            ImageIntake.CAPTURE_MAX_SIDE * ImageIntake.CAPTURE_MAX_SIDE,
            ModelInputPreprocessor.MAX_SCRATCH_PIXELS,
        )
        assertTrue(
            "the scratch bound must exceed 896x896, or the tone curve cannot run at full resolution",
            ModelInputPreprocessor.MAX_SCRATCH_PIXELS > 896 * 896,
        )
    }

    // MARK: - Order: tone curve first, at full resolution

    /**
     * The load-bearing test. A 2016x1512 image is well past the 896 px cap, so a
     * preprocessor that curved *after* downscaling would only ever have read
     * 896x672 = 602,112 pixels. The capacity proves the curve read all 3,048,192.
     */
    @Test
    fun theToneCurveReadsTheFullResolutionImageNotTheDownscaledOne() {
        val preprocessor = ModelInputPreprocessor()
        assertEquals(0, preprocessor.scratchCapacity)

        val result = preprocessor.prepare(filled(2016, 1512, Color.rgb(40, 40, 40)))

        assertEquals(2016 * 1512, preprocessor.scratchCapacity)
        assertTrue(preprocessor.scratchCapacity > 896 * 896)
        assertEquals(896, max(result.width, result.height))
    }

    @Test
    fun aSquareCropOfAFullSizePhotoStillFitsTheBuffer() {
        // A crop can be as large as the capture cap on both axes; the buffer must
        // grow to it without overrunning.
        val preprocessor = ModelInputPreprocessor()
        preprocessor.prepare(filled(2016, 2016, Color.rgb(90, 90, 90)))
        assertEquals(ModelInputPreprocessor.MAX_SCRATCH_PIXELS, preprocessor.scratchCapacity)
    }

    // MARK: - The curve itself

    @Test
    fun everyChannelGoesThroughTheLut() {
        // Small enough that no downscale runs, so the output pixels are comparable.
        val preprocessor = ModelInputPreprocessor()
        val result = preprocessor.prepare(filled(8, 8, Color.rgb(10, 128, 200)))

        val pixel = firstPixel(result)
        assertEquals(ToneCurveLut.apply(10), (pixel shr 16) and 0xFF)
        assertEquals(ToneCurveLut.apply(128), (pixel shr 8) and 0xFF)
        assertEquals(ToneCurveLut.apply(200), pixel and 0xFF)
    }

    @Test
    fun theCurveLiftsTheShadowsWhichIsWhyItIsThere() {
        val preprocessor = ModelInputPreprocessor()
        val result = preprocessor.prepare(filled(8, 8, Color.rgb(0, 0, 0)))
        // Black maps to 0.05 -> 13, the whole point for dim fridge photos.
        assertEquals(13, firstPixel(result) and 0xFF)
    }

    @Test
    fun theWhitePointIsPinnedSoHighlightsDoNotClip() {
        val preprocessor = ModelInputPreprocessor()
        val result = preprocessor.prepare(filled(8, 8, Color.rgb(255, 255, 255)))
        assertEquals(255, firstPixel(result) and 0xFF)
    }

    @Test
    fun alphaSurvivesTheLutUntouchedAndTheOutputIsOpaque() {
        val preprocessor = ModelInputPreprocessor()
        val result = preprocessor.prepare(filled(8, 8, Color.rgb(60, 60, 60)))
        assertEquals(0xFF, (firstPixel(result) shr 24) and 0xFF)
        assertEquals(Bitmap.Config.ARGB_8888, result.config)
    }

    // MARK: - Buffer reuse

    @Test
    fun theBufferIsReusedAcrossCallsRatherThanReallocatedPerCrop() {
        val preprocessor = ModelInputPreprocessor()
        preprocessor.prepare(filled(1000, 1000, Color.rgb(50, 50, 50)))
        val afterFirst = preprocessor.scratchCapacity

        repeat(5) { preprocessor.prepare(filled(1000, 1000, Color.rgb(50, 50, 50))) }
        assertEquals(afterFirst, preprocessor.scratchCapacity)
    }

    @Test
    fun theBufferGrowsButNeverShrinks() {
        val preprocessor = ModelInputPreprocessor()
        preprocessor.prepare(filled(2016, 1512, Color.rgb(50, 50, 50)))
        val big = preprocessor.scratchCapacity

        preprocessor.prepare(filled(64, 64, Color.rgb(50, 50, 50)))
        assertEquals("a shrink would mean reallocating on the next full-size crop", big, preprocessor.scratchCapacity)
    }

    // MARK: - Downscale

    @Test
    fun anImageAlreadyUnderTheCapIsNotResized() {
        val preprocessor = ModelInputPreprocessor()
        val result = preprocessor.prepare(filled(400, 300, Color.rgb(70, 70, 70)))
        assertEquals(400, result.width)
        assertEquals(300, result.height)
    }

    @Test
    fun downscaleCapsTheLongestSideAndRoundsTheOther() {
        val preprocessor = ModelInputPreprocessor()
        val result = preprocessor.prepare(filled(2016, 1512, Color.rgb(70, 70, 70)))
        assertEquals(896, result.width)
        assertEquals(672, result.height)
    }

    @Test
    fun portraitCropsCapTheirHeight() {
        val preprocessor = ModelInputPreprocessor()
        val result = preprocessor.prepare(filled(1512, 2016, Color.rgb(70, 70, 70)))
        assertEquals(896, result.height)
        assertEquals(672, result.width)
    }

    // MARK: - Failure behavior

    @Test
    fun anUnreadableBitmapIsReturnedUnchangedRatherThanThrowing() {
        val preprocessor = ModelInputPreprocessor()
        val source = filled(64, 64, Color.rgb(70, 70, 70))
        source.recycle()
        // ensureSoftware returns null; prepare hands the caller back what it was
        // given. A slightly wrong image beats no inference at all.
        assertSame(source, preprocessor.prepare(source))
    }
}
