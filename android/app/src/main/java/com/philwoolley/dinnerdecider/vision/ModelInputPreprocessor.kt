package com.philwoolley.dinnerdecider.vision

import android.graphics.Bitmap
import android.util.Log
import com.philwoolley.dinnerdecider.contract.ToneCurveLut
import kotlin.math.max
import kotlin.math.roundToInt

/**
 * The last thing that happens to an image before it reaches the model.
 *
 * Port of `GemmaLLMService.downscaled` (`GemmaLLMService.swift:154-190`),
 * described implementation-neutrally in `shared/pipeline/scan-pipeline.md`
 * Stage 4. Two operations, and **the order between them is the whole point**:
 *
 * 1. **Tone curve, at full resolution.** [ToneCurveLut] lifts the shadows and
 *    mid-tones so label text in a dim fridge is legible to the model.
 * 2. **Downscale so the longest side is at most [MAX_IMAGE_SIDE] (896 px).**
 *
 * ## Why the order is a regression risk and not a style choice
 *
 * Curving first and resampling second means the resampler averages **curved**
 * pixels. Resampling first and curving second means the curve is applied to
 * already-averaged pixels, which have different statistics — shadow detail that
 * the curve was there to recover has already been blended away. The output still
 * looks like a plausible 896 px image, the pipeline still runs, no test fails,
 * and recognition quietly gets worse. That class of regression shipped **twice**
 * on iOS (builds 5 and 7 moved this pass to a software renderer and had to be
 * reverted in build 8, "to match the build-4 configuration, the last one proven
 * to recognize items on device").
 *
 * The tempting way to arrive at the wrong order is via the scratch buffer: size
 * it for 896² by mistake, watch [Bitmap.getPixels] overrun, and "fix" it by
 * moving the curve after the downscale so 896² becomes correct. **The buffer is
 * sized for the pre-downscale bitmap** — up to [ImageIntake.CAPTURE_MAX_SIDE]² —
 * for exactly that reason. See [scratchCapacity].
 *
 * ## One buffer for the app's lifetime
 *
 * `GemmaLLMService.swift:152-157` keeps a single `CIContext` alive for the whole
 * app rather than making one per crop, because per-crop contexts "exhaust wired
 * memory on top of the already ~3.5GB GPU-resident model and trigger jetsam".
 * Android has no `CIContext`; the equivalent per-crop allocation here is the
 * pixel array, at up to 16 MB a time. So this class is **stateful on purpose**:
 * hold one instance for the process and reuse its buffer. It is
 * [Synchronized][synchronized]-guarded rather than thread-confined so a shared
 * instance is simply safe.
 */
class ModelInputPreprocessor(
    private val lut: IntArray = ToneCurveLut.table,
) {

    /**
     * The reusable pixel buffer, grown on demand and **never** shrunk.
     *
     * Starts empty: a process that never scans never pays for it.
     */
    private var scratch: IntArray = EMPTY

    /** Current buffer capacity in pixels, for tests and for the memory report. */
    val scratchCapacity: Int
        @Synchronized get() = scratch.size

    /**
     * Tone-curve then downscale, in that order.
     *
     * Returns a new opaque [Bitmap.Config.ARGB_8888] bitmap whose longest side is
     * at most [MAX_IMAGE_SIDE]. Never throws and never returns null: a failure at
     * any stage falls back to the best image available, because a slightly wrong
     * image beats no inference at all.
     *
     * The caller keeps ownership of [source]; it is never recycled here.
     * Intermediates are recycled immediately.
     */
    @Synchronized
    fun prepare(source: Bitmap): Bitmap {
        val readable = ImageIntake.ensureSoftware(source) ?: return source

        // 1. Tone curve, at FULL resolution. Failure falls back to the original
        //    image rather than erroring (GemmaLLMService.swift:180-182).
        val toned = applyToneCurve(readable) ?: readable

        // 2. Downscale, rounded, opaque, ARGB_8888.
        val scaled = downscale(toned)

        if (toned !== scaled && toned !== readable && toned !== source) toned.recycle()
        if (readable !== scaled && readable !== source) readable.recycle()
        return scaled
    }

    /**
     * One [Bitmap.getPixels] / [Bitmap.setPixels] round trip through the 256-entry
     * LUT, applied per channel with alpha untouched.
     *
     * Returns null on any failure, including [OutOfMemoryError] on the destination
     * allocation, so [prepare] can fall back.
     */
    private fun applyToneCurve(source: Bitmap): Bitmap? = try {
        val width = source.width
        val height = source.height
        val pixelCount = width * height
        val buffer = ensureScratch(pixelCount)

        source.getPixels(buffer, 0, width, 0, 0, width, height)
        for (i in 0 until pixelCount) {
            val pixel = buffer[i]
            buffer[i] = (pixel and ALPHA_MASK) or
                (lut[(pixel shr 16) and 0xFF] shl 16) or
                (lut[(pixel shr 8) and 0xFF] shl 8) or
                lut[pixel and 0xFF]
        }

        val target = Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888)
        target.setPixels(buffer, 0, width, 0, 0, width, height)
        target.setHasAlpha(false)
        target
    } catch (error: Throwable) {
        // Includes OutOfMemoryError: a 2016x2016 buffer is 16 MB and the target
        // bitmap another 16 MB. Losing the curve is survivable; crashing is not.
        Log.w(TAG, "applyToneCurve: falling back to the untoned image", error)
        null
    }

    /**
     * Grow [scratch] to hold at least [pixelCount] pixels.
     *
     * **Sized for the pre-downscale bitmap, not for 896².** Every image reaching
     * [prepare] has already passed through [ImageIntake], so it is capped at
     * [ImageIntake.CAPTURE_MAX_SIDE] per side — but a crop of a 2016x2016 photo
     * can itself be 2016x2016, so [MAX_SCRATCH_PIXELS] is that square, ~16 MB.
     * The cap is a sanity bound, not the allocation: the buffer only ever grows to
     * what an actual image needed.
     *
     * Sizing this for 896² (803,816 pixels) would make [Bitmap.getPixels]
     * overrun on the very first full-size image — and the obvious "fix" for that
     * overrun is to move the tone curve after the downscale, which is the silent
     * recognition regression this class exists to prevent.
     */
    private fun ensureScratch(pixelCount: Int): IntArray {
        require(pixelCount > 0) { "pixelCount must be positive, was $pixelCount" }
        if (scratch.size >= pixelCount) return scratch
        scratch = IntArray(pixelCount)
        return scratch
    }

    /**
     * Longest side to at most [MAX_IMAGE_SIDE], rounded, opaque, ARGB_8888.
     * Images already small enough are returned untouched.
     */
    private fun downscale(source: Bitmap): Bitmap {
        val longest = max(source.width, source.height)
        if (longest <= MAX_IMAGE_SIDE || longest <= 0) return source

        val scale = MAX_IMAGE_SIDE.toDouble() / longest.toDouble()
        val targetWidth = (source.width * scale).roundToInt().coerceAtLeast(1)
        val targetHeight = (source.height * scale).roundToInt().coerceAtLeast(1)
        return try {
            Bitmap.createScaledBitmap(source, targetWidth, targetHeight, true)
                .also { it.setHasAlpha(false) }
        } catch (error: Throwable) {
            Log.w(TAG, "downscale: falling back to the full-size image", error)
            source
        }
    }

    companion object {
        private const val TAG = "ModelInputPreprocessor"
        private const val ALPHA_MASK = 0xFF shl 24

        /**
         * Longest-side cap for what the model sees. `GemmaLLMService.maxImageSide`,
         * verbatim. Gemma 4 emits ~256 image tokens for an 896x896 image, which is
         * what the `batch >= 512` floor is sized against.
         */
        const val MAX_IMAGE_SIDE: Int = 896

        /**
         * Upper bound on the scratch buffer: one [ImageIntake.CAPTURE_MAX_SIDE]
         * square, ~16 MB as an `IntArray`. **Not** 896².
         */
        const val MAX_SCRATCH_PIXELS: Int =
            ImageIntake.CAPTURE_MAX_SIDE * ImageIntake.CAPTURE_MAX_SIDE

        private val EMPTY = IntArray(0)
    }
}
