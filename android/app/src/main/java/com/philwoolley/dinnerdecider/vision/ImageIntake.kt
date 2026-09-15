package com.philwoolley.dinnerdecider.vision

import android.content.ContentResolver
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Canvas
import android.graphics.Matrix
import android.graphics.Paint
import android.net.Uri
import android.util.Log
import androidx.exifinterface.media.ExifInterface
import java.io.InputStream
import kotlin.math.max
import kotlin.math.roundToInt

/**
 * The single door every photo walks through on its way into the app.
 *
 * Port of `DinnerDecider/Services/Vision/ImageDownscaler.swift` plus the
 * orientation/pixel-backing half of `CropService.normalizedCGImage`
 * (`CropService.swift:140-149`). Three obligations, all of them load-bearing:
 *
 * 1. **Downscale to [CAPTURE_MAX_SIDE] immediately.** A 24 MP frame is ~90 MB
 *    decoded and a batch can stage eight of them. Crops are re-scaled to 896 px
 *    for the model anyway (see [ModelInputPreprocessor]), so retaining ~2000 px
 *    originals keeps crop quality while cutting resident pixel memory roughly
 *    10x. `shared/pipeline/scan-pipeline.md` Stage 0.
 *
 * 2. **Bake the EXIF rotation.** Portrait phone photos are stored landscape with
 *    a rotation tag. Without baking it, every detected box lands in the wrong
 *    coordinate frame — a silent wrong-answer bug, not a crash.
 *
 * 3. **Force a software allocation.** This is the Android analogue of the
 *    `cgImage == nil` guard at `CropService.swift:31`, and it is not optional.
 *    `ImageDecoder` hands back [Bitmap.Config.HARDWARE] by default on API 28+,
 *    and [Bitmap.getPixels] on a HARDWARE bitmap **throws**. A hardware-backed
 *    bitmap therefore yields **zero crops from a perfectly good photo**, and it
 *    does so without any error the user or the logs would connect to the cause.
 *    [ensureSoftware] is applied to every bitmap entering the pipeline, whatever
 *    decoded it.
 *
 * ### Why [BitmapFactory] and not [android.graphics.ImageDecoder]
 *
 * `ImageDecoder` would satisfy (3) with `setAllocator(ALLOCATOR_SOFTWARE)`, but
 * it *also* applies EXIF orientation itself, silently and unconditionally.
 * Layering an explicit [ExifInterface] rotation on top of that double-rotates
 * every portrait photo. [BitmapFactory] never touches EXIF, so decoding with it
 * and baking the rotation here is the only formulation where exactly one
 * rotation is applied and it is visible in this file. [ensureSoftware] then
 * covers bitmaps that arrive from anywhere else (CameraX, a caller's own
 * `ImageDecoder`, a test).
 */
object ImageIntake {

    private const val TAG = "ImageIntake"

    /**
     * Longest-side cap for photos retained through the capture/scan pipeline.
     * `ImageDownscaler.captureMaxSide`, verbatim.
     */
    const val CAPTURE_MAX_SIDE: Int = 2016

    /**
     * Guarantee a bitmap whose pixels can actually be read.
     *
     * Returns [bitmap] untouched when it is already a readable software
     * [Bitmap.Config.ARGB_8888]; otherwise copies it. Returns `null` only when
     * the copy itself fails, which is the one case a caller must treat the way
     * iOS treats `normalizedCGImage` returning nil: emit no crops rather than
     * crashing.
     *
     * `copy(ARGB_8888, false)` is the documented way off a HARDWARE bitmap: the
     * copy is CPU-visible, so [Bitmap.getPixels] works on the result.
     */
    fun ensureSoftware(bitmap: Bitmap): Bitmap? {
        if (bitmap.isRecycled) return null
        if (bitmap.config == Bitmap.Config.ARGB_8888 && !isHardware(bitmap)) return bitmap
        return runCatching { bitmap.copy(Bitmap.Config.ARGB_8888, false) }
            .onFailure { Log.w(TAG, "ensureSoftware: copy to ARGB_8888 failed", it) }
            .getOrNull()
    }

    /** True when the bitmap is GPU-resident and [Bitmap.getPixels] would throw. */
    @Suppress("DEPRECATION")
    fun isHardware(bitmap: Bitmap): Boolean =
        bitmap.config == Bitmap.Config.HARDWARE

    /**
     * Downscale so the longest side is at most [maxSide], returning the original
     * untouched when it is already small enough.
     *
     * Mirrors `ImageDownscaler.downscaled(_:maxSide:)`: target dimensions are
     * **rounded**, the result is opaque and [Bitmap.Config.ARGB_8888]. Never
     * recycles [bitmap] — the caller still owns it.
     */
    fun downscaled(bitmap: Bitmap, maxSide: Int = CAPTURE_MAX_SIDE): Bitmap {
        val longest = max(bitmap.width, bitmap.height)
        if (longest <= maxSide || longest <= 0) return bitmap

        val scale = maxSide.toDouble() / longest.toDouble()
        val targetWidth = (bitmap.width * scale).roundToInt().coerceAtLeast(1)
        val targetHeight = (bitmap.height * scale).roundToInt().coerceAtLeast(1)
        return redraw(bitmap, targetWidth, targetHeight, Matrix())
    }

    /**
     * Downscale a freshly captured or imported photo to the capture cap, forcing
     * a software allocation on the way.
     *
     * `ImageDownscaler.forCapture(_:)`. Returns `null` when the bitmap cannot be
     * made readable at all.
     */
    fun forCapture(bitmap: Bitmap): Bitmap? {
        val software = ensureSoftware(bitmap) ?: return null
        val scaled = downscaled(software, CAPTURE_MAX_SIDE)
        if (scaled !== software && software !== bitmap) software.recycle()
        return opaque(scaled)
    }

    /**
     * Bake a clockwise rotation (and optional horizontal flip) into the pixels.
     *
     * CameraX supplies `ImageInfo.rotationDegrees` directly, so the capture path
     * calls this instead of going anywhere near EXIF.
     */
    fun withRotationBaked(bitmap: Bitmap, degrees: Int, flipHorizontally: Boolean = false): Bitmap {
        val normalized = ((degrees % 360) + 360) % 360
        if (normalized == 0 && !flipHorizontally) return bitmap
        val matrix = Matrix()
        if (flipHorizontally) matrix.postScale(-1f, 1f)
        if (normalized != 0) matrix.postRotate(normalized.toFloat())
        val swapsAxes = normalized == 90 || normalized == 270
        val width = if (swapsAxes) bitmap.height else bitmap.width
        val height = if (swapsAxes) bitmap.width else bitmap.height
        return redraw(bitmap, width, height, matrix)
    }

    /**
     * Decode a gallery pick: EXIF baked, downscaled to [CAPTURE_MAX_SIDE],
     * opaque, software-allocated [Bitmap.Config.ARGB_8888].
     *
     * Returns `null` for anything undecodable rather than throwing — an
     * unreadable pick drops out of the batch, exactly as `startScan` drops a
     * photo that "carries no usable pixel data".
     */
    fun fromUri(contentResolver: ContentResolver, uri: Uri): Bitmap? = runCatching {
        val orientation = contentResolver.openInputStream(uri)?.use(::readOrientation)
            ?: ExifInterface.ORIENTATION_NORMAL

        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        contentResolver.openInputStream(uri)?.use { BitmapFactory.decodeStream(it, null, bounds) }

        val options = BitmapFactory.Options().apply {
            inPreferredConfig = Bitmap.Config.ARGB_8888
            inMutable = false
            inSampleSize = sampleSizeFor(bounds.outWidth, bounds.outHeight, CAPTURE_MAX_SIDE)
        }
        val decoded = contentResolver.openInputStream(uri)?.use {
            BitmapFactory.decodeStream(it, null, options)
        } ?: return@runCatching null

        fromDecoded(decoded, orientation)
    }.onFailure { Log.w(TAG, "fromUri: could not decode $uri", it) }.getOrNull()

    /**
     * Same contract as [fromUri] for callers that already hold the bytes (a SAF
     * import, a test fixture, a CameraX JPEG).
     */
    fun fromBytes(bytes: ByteArray): Bitmap? = runCatching {
        val orientation = bytes.inputStream().use(::readOrientation)
        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        BitmapFactory.decodeByteArray(bytes, 0, bytes.size, bounds)
        val options = BitmapFactory.Options().apply {
            inPreferredConfig = Bitmap.Config.ARGB_8888
            inMutable = false
            inSampleSize = sampleSizeFor(bounds.outWidth, bounds.outHeight, CAPTURE_MAX_SIDE)
        }
        val decoded = BitmapFactory.decodeByteArray(bytes, 0, bytes.size, options)
            ?: return@runCatching null
        fromDecoded(decoded, orientation)
    }.onFailure { Log.w(TAG, "fromBytes: could not decode ${bytes.size} bytes", it) }.getOrNull()

    // ---------------------------------------------------------------------

    private fun fromDecoded(decoded: Bitmap, orientation: Int): Bitmap? {
        val upright = applyExifOrientation(decoded, orientation)
        if (upright !== decoded) decoded.recycle()
        return forCapture(upright)
    }

    private fun readOrientation(stream: InputStream): Int =
        runCatching {
            ExifInterface(stream).getAttributeInt(
                ExifInterface.TAG_ORIENTATION,
                ExifInterface.ORIENTATION_NORMAL,
            )
        }.getOrDefault(ExifInterface.ORIENTATION_NORMAL)

    private fun applyExifOrientation(bitmap: Bitmap, orientation: Int): Bitmap = when (orientation) {
        ExifInterface.ORIENTATION_ROTATE_90 -> withRotationBaked(bitmap, 90)
        ExifInterface.ORIENTATION_ROTATE_180 -> withRotationBaked(bitmap, 180)
        ExifInterface.ORIENTATION_ROTATE_270 -> withRotationBaked(bitmap, 270)
        ExifInterface.ORIENTATION_FLIP_HORIZONTAL -> withRotationBaked(bitmap, 0, flipHorizontally = true)
        ExifInterface.ORIENTATION_FLIP_VERTICAL -> withRotationBaked(bitmap, 180, flipHorizontally = true)
        ExifInterface.ORIENTATION_TRANSPOSE -> withRotationBaked(bitmap, 90, flipHorizontally = true)
        ExifInterface.ORIENTATION_TRANSVERSE -> withRotationBaked(bitmap, 270, flipHorizontally = true)
        else -> bitmap
    }

    /**
     * Largest power-of-two sub-sample that still leaves the longest side at or
     * above [maxSide], so the full-resolution frame is never materialised.
     * [downscaled] does the exact final resize afterwards.
     */
    internal fun sampleSizeFor(width: Int, height: Int, maxSide: Int): Int {
        if (width <= 0 || height <= 0 || maxSide <= 0) return 1
        var sample = 1
        while (max(width, height) / (sample * 2) >= maxSide) sample *= 2
        return sample
    }

    /** Flatten to an opaque ARGB_8888 bitmap without changing its size. */
    private fun opaque(bitmap: Bitmap): Bitmap {
        if (bitmap.config == Bitmap.Config.ARGB_8888 && !bitmap.hasAlpha()) return bitmap
        val flattened = redraw(bitmap, bitmap.width, bitmap.height, Matrix())
        if (flattened !== bitmap) return flattened
        return bitmap
    }

    /**
     * The one drawing primitive: allocate an opaque ARGB_8888 target and paint
     * the source into it through [matrix], scaled to fill.
     *
     * Equivalent to iOS's `UIGraphicsImageRenderer` with `scale = 1` and
     * `opaque = true`, which is what bakes orientation and colour space into a
     * plain bitmap. Filtering is on, matching `UIImage.draw(in:)`.
     */
    private fun redraw(source: Bitmap, width: Int, height: Int, matrix: Matrix): Bitmap {
        val target = Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888)
        target.setHasAlpha(false)
        val canvas = Canvas(target)
        canvas.drawColor(android.graphics.Color.BLACK)

        val full = Matrix(matrix)
        // Map the source rect through `matrix`, then scale that onto the target.
        val mapped = android.graphics.RectF(0f, 0f, source.width.toFloat(), source.height.toFloat())
        full.mapRect(mapped)
        val sx = if (mapped.width() > 0f) width / mapped.width() else 1f
        val sy = if (mapped.height() > 0f) height / mapped.height() else 1f
        full.postTranslate(-mapped.left, -mapped.top)
        full.postScale(sx, sy)

        val paint = Paint(Paint.FILTER_BITMAP_FLAG or Paint.ANTI_ALIAS_FLAG)
        canvas.drawBitmap(source, full, paint)
        return target
    }
}
