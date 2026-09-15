package com.philwoolley.dinnerdecider.contract

/**
 * An axis-aligned rectangle, origin **top-left**, in whatever space the caller is
 * working in (normalized 0..1 for detector output, pixels after [CropGeometry.pixelRect]).
 *
 * The Swift original uses `CGRect`; this is the platform-free equivalent so the
 * geometry can be unit-tested with no `Bitmap`, no device and no `android.*`
 * import. Plan Step 25 wraps it with the actual bitmap I/O.
 */
data class Box(
    val x: Double,
    val y: Double,
    val width: Double,
    val height: Double,
) {
    val minX: Double get() = x
    val minY: Double get() = y
    val maxX: Double get() = x + width
    val maxY: Double get() = y + height
    val area: Double get() = width * height
}

/**
 * Pure crop geometry: the box maths behind `DinnerDecider/Services/Vision/CropService.swift`.
 *
 * ## Pipeline order is merge -> pad -> count
 *
 * Documented here because plan Step 25 consumes it, and because getting it
 * backwards silently changes which photos take the tile fallback.
 * `CropService.swift:37-42` runs:
 *
 * 1. `detectBoxes(...)` — which calls [mergeOverlapping] **internally**, so the
 *    boxes it returns are already merged;
 * 2. `pad(0.15)` over every returned box;
 * 3. `isTileFallback = boxes.count < 2` — the `< 2` test therefore sees the
 *    **padded** set.
 *
 * Padded boxes are deliberately **not** re-merged: padding two near-neighbours
 * into overlap does not collapse them.
 *
 * **Tiles are never padded.** [tileGrid] already overlaps by 20%, and
 * `isTileFallback` changes the prompt the model receives, because each tile is
 * large and may contain several items.
 */
object CropGeometry {

    /** Proportional outset applied to every detected box, per axis. */
    const val PADDING_FRACTION: Double = 0.15

    /** Fractional overlap between adjacent tiles: step = tile * (1 - overlap). */
    const val TILE_OVERLAP: Double = 0.20

    /** Boxes merge when their intersection-over-union is **strictly greater** than this. */
    const val MERGE_IOU_THRESHOLD: Double = 0.3

    /** A tile narrower or shorter than this is dropped rather than emitted. */
    const val MIN_TILE_SIDE: Double = 8.0

    /**
     * Intersection of two boxes, or null when they do not overlap.
     *
     * Mirrors `CGRect.intersection` returning `CGRect.null`: boxes that merely
     * touch produce a zero-area overlap and are treated as disjoint.
     */
    fun intersection(a: Box, b: Box): Box? {
        val minX = maxOf(a.minX, b.minX)
        val minY = maxOf(a.minY, b.minY)
        val maxX = minOf(a.maxX, b.maxX)
        val maxY = minOf(a.maxY, b.maxY)
        if (maxX <= minX || maxY <= minY) return null
        return Box(x = minX, y = minY, width = maxX - minX, height = maxY - minY)
    }

    /** Smallest box containing both. Mirrors `CGRect.union`. */
    fun union(a: Box, b: Box): Box {
        val minX = minOf(a.minX, b.minX)
        val minY = minOf(a.minY, b.minY)
        val maxX = maxOf(a.maxX, b.maxX)
        val maxY = maxOf(a.maxY, b.maxY)
        return Box(x = minX, y = minY, width = maxX - minX, height = maxY - minY)
    }

    /**
     * Intersection over union. **0 when the boxes are disjoint, and 0 when the
     * union area is non-positive** (two zero-area boxes at the same spot), so the
     * threshold comparison never sees a NaN.
     */
    fun iou(a: Box, b: Box): Double {
        val overlap = intersection(a, b) ?: return 0.0
        val intersectionArea = overlap.width * overlap.height
        val unionArea = a.area + b.area - intersectionArea
        return if (unionArea > 0) intersectionArea / unionArea else 0.0
    }

    /**
     * Greedily merge boxes whose IoU exceeds [MERGE_IOU_THRESHOLD].
     *
     * **Greedy and order-dependent — this is not a clustering algorithm and must
     * not be "improved" into one.** Walk the input in order; for each box with a
     * positive area, scan the already-kept boxes in order, union into the
     * **first** one whose IoU exceeds the threshold and **stop**; if none
     * matched, keep the box as a new entry. Zero-area boxes are skipped entirely
     * (`where box.width > 0 && box.height > 0`).
     *
     * Consequences that are load-bearing for parity: a box can chain two kept
     * boxes together only by being merged into whichever comes first, and
     * reordering the input changes the output. The eval harness's 33.5% union
     * recall was measured against exactly this behavior.
     */
    fun mergeOverlapping(boxes: List<Box>): List<Box> {
        val merged = mutableListOf<Box>()
        for (box in boxes) {
            if (box.width <= 0 || box.height <= 0) continue
            var didMerge = false
            for (index in merged.indices) {
                if (iou(merged[index], box) > MERGE_IOU_THRESHOLD) {
                    merged[index] = union(merged[index], box)
                    didMerge = true
                    break
                }
            }
            if (!didMerge) merged.add(box)
        }
        return merged
    }

    /**
     * Outset [box] by [fraction] of its own size **per axis**, then clamp to the
     * image bounds.
     *
     * Proportional per axis, not a fixed pixel margin: `dx = width * fraction`,
     * `dy = height * fraction`, applied to **both** sides of each axis (so a box
     * grows by `2 * dx` in width). Mirrors `insetBy(dx: -dx, dy: -dy)` followed by
     * `intersection(bounds)`.
     *
     * Returns a zero-size box at the origin when the padded rect falls entirely
     * outside the image, matching `CGRect.null`'s effect downstream (the caller
     * drops anything under 8 px).
     */
    fun pad(box: Box, width: Int, height: Int, fraction: Double = PADDING_FRACTION): Box {
        val dx = box.width * fraction
        val dy = box.height * fraction
        val padded = Box(
            x = box.x - dx,
            y = box.y - dy,
            width = box.width + 2 * dx,
            height = box.height + 2 * dy,
        )
        val bounds = Box(x = 0.0, y = 0.0, width = width.toDouble(), height = height.toDouble())
        return intersection(padded, bounds) ?: Box(0.0, 0.0, 0.0, 0.0)
    }

    /**
     * Convert a detector's normalized rect (origin **bottom-left**, as Vision and
     * ML Kit's normalized space both report) to pixel coordinates with origin
     * **top-left**, matching the image's own coordinate space.
     *
     * The y flip is the whole point: `y = (1 - normalized.maxY) * height`. Using
     * `minY` here instead is the classic mirrored-crops bug — every box lands in
     * the wrong half of the photo and recognition quietly collapses.
     */
    fun pixelRect(normalized: Box, width: Int, height: Int): Box {
        val w = width.toDouble()
        val h = height.toDouble()
        return Box(
            x = normalized.minX * w,
            y = (1.0 - normalized.maxY) * h,
            width = normalized.width * w,
            height = normalized.height * h,
        )
    }

    /**
     * The overlapping tile grid used when detection is weak (fewer than two
     * padded boxes) or when no detector is available at all.
     *
     * **Per-axis, exactly as shipped.** `tileWidth = width / 3.0` and
     * `tileHeight = height / 3.0` are computed **independently**, and the steps
     * are `tile * (1 - 0.20)` on each axis. A `tile = size / 3` formulation using
     * the image's **minimum** dimension emits **12** tiles for 2016x1512 — the
     * right answer arrived at by the wrong arithmetic. Use the per-axis form.
     *
     * ```
     * tileWidth  = width  / 3.0        stepX = tileWidth  * 0.8
     * tileHeight = height / 3.0        stepY = tileHeight * 0.8
     * y = 0
     * while (y < height) {
     *     x = 0
     *     while (x < width) {
     *         w' = min(tileWidth,  width  - x)
     *         h' = min(tileHeight, height - y)
     *         if (w' > 8 && h' > 8) emit Box(x, y, w', h')
     *         x += stepX
     *     }
     *     y += stepY
     * }
     * ```
     *
     * With `step = tile * 0.8` the origins are `0, s, 2s, 3s` on each axis and
     * `3s = 0.8 * size < size`, so the loop runs **four** times per axis:
     * **16 tiles**, not the nine the Swift comment claims ("A 3x3 grid of
     * overlapping tiles"). The comment is wrong; the code is what shipped and
     * what `eval/` measured.
     *
     * **Do not "fix" this to 9 or 12 without re-running the eval harness.** Note
     * also that the 4-call scan budget means at most **3** of these 16 tiles are
     * ever analyzed (Pass A spends one call before the crop loop) — a documented
     * non-goal, changed only alongside iOS.
     */
    fun tileGrid(width: Int, height: Int): List<Box> {
        val w = width.toDouble()
        val h = height.toDouble()
        val tileWidth = w / 3.0
        val tileHeight = h / 3.0
        val stepX = tileWidth * (1 - TILE_OVERLAP)
        val stepY = tileHeight * (1 - TILE_OVERLAP)

        val rects = mutableListOf<Box>()
        if (stepX <= 0 || stepY <= 0) return rects

        var y = 0.0
        while (y < h) {
            var x = 0.0
            while (x < w) {
                val tw = minOf(tileWidth, w - x)
                val th = minOf(tileHeight, h - y)
                if (tw > MIN_TILE_SIDE && th > MIN_TILE_SIDE) {
                    rects.add(Box(x = x, y = y, width = tw, height = th))
                }
                x += stepX
            }
            y += stepY
        }
        return rects
    }
}
