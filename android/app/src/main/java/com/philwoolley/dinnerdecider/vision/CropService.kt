package com.philwoolley.dinnerdecider.vision

import android.graphics.Bitmap
import android.graphics.Rect
import android.util.Log
import com.philwoolley.dinnerdecider.contract.Box
import com.philwoolley.dinnerdecider.contract.CropGeometry
import kotlin.math.ceil
import kotlin.math.floor

/**
 * The crops produced from a source photo, plus the rectangles they came from (in
 * image pixel coordinates, origin top-left) so the UI can show them.
 *
 * Port of `CropService.swift:7-13`.
 */
data class CropResult(
    val images: List<Bitmap>,
    val rects: List<Rect>,
    /**
     * True when crops came from the fallback tile grid rather than targeted object
     * detection, meaning each crop is large and may contain multiple items.
     *
     * **This value selects the prompt downstream** and therefore travels with the
     * result: tiles -> `identify-all-items`, detected boxes -> `identify-item`.
     * See `shared/pipeline/scan-pipeline.md` Stage 2.
     */
    val isTileFallback: Boolean,
) {
    val isEmpty: Boolean get() = images.isEmpty()
}

/**
 * A detector of interesting regions, in **image pixel coordinates with a top-left
 * origin** — the same space [CropResult.rects] uses.
 *
 * Deliberately an interface with no implementation wired in by default. See
 * [MlKitObjectDetector] and the note on [CropService.crops].
 */
fun interface BoxDetector {
    /** Returns raw, unmerged, unpadded boxes. Must never throw; return empty. */
    suspend fun detect(bitmap: Bitmap): List<Box>
}

/**
 * Splits a fridge/pantry photo into per-item crops before identification.
 *
 * Port of `DinnerDecider/Services/Vision/CropService.swift`. The box maths itself
 * lives in the pure, host-JVM-tested [CropGeometry]; this file is the thin bitmap
 * wrapper around it, plus the one thing that is genuinely a **decision**: the
 * order in which the geometry runs.
 *
 * ## Order is merge -> pad -> count, and getting it backwards is silent
 *
 * `CropService.swift:37-42`:
 *
 * ```
 * var boxes = detectBoxes(...)                 // merges internally at IoU 0.3
 * boxes = boxes.map { pad(rect: $0, ...) }     // 0.15 per axis, clamped
 * let isTileFallback = boxes.count < 2         // the < 2 test sees the PADDED set
 * if isTileFallback { boxes = tileGrid(...) }
 * ```
 *
 * Two consequences a reordering would quietly break:
 *
 * - Padded boxes are **not re-merged**. Padding grows each box by 30% per axis,
 *   so two near-neighbours that were comfortably under the 0.3 IoU threshold can
 *   be over it afterwards. Merging then would union boxes iOS keeps separate —
 *   different crops, fewer of them, and possibly a tile fallback where iOS took
 *   the detected boxes. The padded set is used exactly as it comes out.
 * - The `< 2` test therefore runs against **padded** boxes. They are the same
 *   count as the merged set (padding never drops one; a box padded entirely off
 *   the image survives as a degenerate rect and is dropped later, at the 8 px
 *   emit filter), but the rule is stated in terms of the padded set because that
 *   is what the shipped code counts, and any port that swaps the two stages has
 *   already changed the unions above.
 *
 * ## The default path is the tile grid
 *
 * Android has no equivalent of Vision's saliency + rectangle detectors, and
 * `shared/pipeline/scan-pipeline.md` is explicit:
 *
 * > A port with no detector available must go **straight to the tile grid** with
 * > `isTileFallback = true`. That is not a degraded special case — it is a path
 * > iOS already takes routinely [...] Do **not** substitute a different object
 * > detector without re-running the eval harness (`eval/`).
 *
 * So [crops] takes no detector by default: an empty detection list merges to
 * empty, pads to empty, `0 < 2` is true, and the grid runs. [MlKitObjectDetector]
 * exists and is **wired off**; see its KDoc for the gate on turning it on.
 */
object CropService {

    private const val TAG = "CropService"

    /** A crop narrower or shorter than this, after rounding out, is dropped. */
    const val MIN_CROP_SIDE: Int = 8

    /**
     * The pure half of [crops]: which rectangles this image yields and whether
     * they are tiles. No bitmaps, no detector, no `android.graphics` beyond the
     * plain-data [Rect] — so the order above is covered on the host JVM.
     *
     * @param detectedBoxes raw detector output in pixel coordinates. **Empty is
     *   the normal Android case** and produces the tile grid.
     */
    fun plan(width: Int, height: Int, detectedBoxes: List<Box> = emptyList()): CropPlan {
        if (width <= 0 || height <= 0) return CropPlan(emptyList(), isTileFallback = false)

        // 1. merge (IoU > 0.3, greedy, order-dependent) — inside detectBoxes on iOS.
        val merged = CropGeometry.mergeOverlapping(detectedBoxes)
        // 2. pad (0.15 per axis, clamped to bounds).
        val padded = merged.map { CropGeometry.pad(it, width, height) }
        // 3. count the PADDED set.
        val isTileFallback = padded.size < 2
        // Tiles are NOT padded: tileGrid already overlaps by 20%.
        val boxes = if (isTileFallback) CropGeometry.tileGrid(width, height) else padded

        val rects = boxes
            .map { integral(it, width, height) }
            .filter { it.width() >= MIN_CROP_SIDE && it.height() >= MIN_CROP_SIDE }
        return CropPlan(rects, isTileFallback)
    }

    /** The rectangles [plan] chose, and how the prompt should read them. */
    data class CropPlan(val rects: List<Rect>, val isTileFallback: Boolean)

    /**
     * Crop [photo] into the regions [plan] chose.
     *
     * Returns an empty result with `isTileFallback = false` when the bitmap cannot
     * be made pixel-readable — the exact shape of `CropService.swift:31-33`, where
     * a nil `normalizedCGImage` returns `CropResult(images: [], rects: [],
     * isTileFallback: false)`. Zero crops, no crash, and the scan moves on.
     *
     * @param detector optional and **null by default**; see the class KDoc.
     */
    suspend fun crops(photo: Bitmap, detector: BoxDetector? = null): CropResult {
        val source = ImageIntake.ensureSoftware(photo)
        if (source == null) {
            Log.w(TAG, "crops: bitmap has no readable pixel data; emitting zero crops")
            return CropResult(emptyList(), emptyList(), isTileFallback = false)
        }

        val detected = if (detector == null) {
            emptyList()
        } else {
            runCatching { detector.detect(source) }
                .onFailure { Log.w(TAG, "crops: detector failed; falling back to tiles", it) }
                .getOrDefault(emptyList())
        }

        val cropPlan = plan(source.width, source.height, detected)

        val images = mutableListOf<Bitmap>()
        val rects = mutableListOf<Rect>()
        for (rect in cropPlan.rects) {
            val cropped = runCatching {
                Bitmap.createBitmap(source, rect.left, rect.top, rect.width(), rect.height())
            }.onFailure { Log.w(TAG, "crops: could not crop $rect", it) }.getOrNull() ?: continue
            images.add(cropped)
            rects.add(rect)
        }

        if (source !== photo) source.recycle()
        return CropResult(images, rects, cropPlan.isTileFallback)
    }

    /**
     * Round a box **out** to whole pixels and clamp it to the image, the analogue
     * of `CGRect.integral` followed by `CGImage.cropping`'s own clipping.
     *
     * Rounding out, not to nearest: a 0.4 px sliver of an item is still part of the
     * item. The clamp is what keeps `Bitmap.createBitmap` from throwing on a rect
     * that `ceil` pushed one pixel past the edge.
     */
    internal fun integral(box: Box, width: Int, height: Int): Rect {
        val left = floor(box.minX).toInt().coerceIn(0, width)
        val top = floor(box.minY).toInt().coerceIn(0, height)
        val right = ceil(box.maxX).toInt().coerceIn(left, width)
        val bottom = ceil(box.maxY).toInt().coerceIn(top, height)
        return Rect(left, top, right, bottom)
    }
}
