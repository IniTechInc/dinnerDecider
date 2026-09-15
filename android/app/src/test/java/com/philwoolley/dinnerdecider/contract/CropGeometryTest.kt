package com.philwoolley.dinnerdecider.contract

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Contract tests for the pure crop geometry.
 *
 * `CropService.swift` has no Swift unit tests (it needs a `CGImage`), so these
 * pin the arithmetic the port must reproduce rather than translating an existing
 * file. Everything here is a pure function of numbers: no bitmap, no device, no
 * `android.*`.
 */
class CropGeometryTest {

    private val eps = 1e-9

    // ------------------------------------------------------------------
    // iou
    // ------------------------------------------------------------------

    @Test
    fun iouIsZeroForDisjointBoxes() {
        val a = Box(0.0, 0.0, 10.0, 10.0)
        val b = Box(20.0, 20.0, 10.0, 10.0)
        assertEquals(0.0, CropGeometry.iou(a, b), eps)
        assertNull(CropGeometry.intersection(a, b))
    }

    @Test
    fun iouIsZeroForBoxesThatOnlyTouch() {
        // Zero-area overlap counts as disjoint, matching CGRect.intersection
        // returning CGRect.null.
        val a = Box(0.0, 0.0, 10.0, 10.0)
        val b = Box(10.0, 0.0, 10.0, 10.0)
        assertEquals(0.0, CropGeometry.iou(a, b), eps)
    }

    @Test
    fun iouIsOneForIdenticalBoxes() {
        val a = Box(3.0, 4.0, 10.0, 20.0)
        assertEquals(1.0, CropGeometry.iou(a, a), eps)
    }

    @Test
    fun iouIsZeroWhenUnionAreaIsNonPositive() {
        // Two degenerate boxes: guards against a NaN reaching the threshold test.
        val a = Box(5.0, 5.0, 0.0, 0.0)
        assertEquals(0.0, CropGeometry.iou(a, a), eps)
    }

    @Test
    fun iouMatchesTheHandComputedRatio() {
        val a = Box(0.0, 0.0, 10.0, 10.0)
        val b = Box(5.0, 0.0, 10.0, 10.0)
        // overlap 5x10 = 50; union 100 + 100 - 50 = 150
        assertEquals(50.0 / 150.0, CropGeometry.iou(a, b), eps)
    }

    // ------------------------------------------------------------------
    // mergeOverlapping
    // ------------------------------------------------------------------

    @Test
    fun mergeSkipsZeroAreaBoxes() {
        val boxes = listOf(Box(0.0, 0.0, 0.0, 10.0), Box(0.0, 0.0, 10.0, 0.0), Box(0.0, 0.0, 10.0, 10.0))
        assertEquals(listOf(Box(0.0, 0.0, 10.0, 10.0)), CropGeometry.mergeOverlapping(boxes))
    }

    @Test
    fun mergeUnionsIntoTheFirstBoxAboveThreshold() {
        val a = Box(0.0, 0.0, 10.0, 10.0)
        val overlapping = Box(1.0, 1.0, 10.0, 10.0) // IoU ~= 0.68
        val merged = CropGeometry.mergeOverlapping(listOf(a, overlapping))
        assertEquals(listOf(Box(0.0, 0.0, 11.0, 11.0)), merged)
    }

    @Test
    fun mergeKeepsBoxesBelowThresholdSeparate() {
        val a = Box(0.0, 0.0, 10.0, 10.0)
        // overlap 2x10 = 20; union 180; IoU = 0.111 -> below 0.3
        val b = Box(8.0, 0.0, 10.0, 10.0)
        assertEquals(listOf(a, b), CropGeometry.mergeOverlapping(listOf(a, b)))
    }

    /**
     * The merge is **greedy and order-dependent**, and that is the shipped
     * behavior, not a defect to be clustered away. The same three boxes in a
     * different order produce a different result: the middle box merges into
     * whichever neighbour it meets first, and the scan stops there.
     */
    @Test
    fun mergeIsGreedyAndOrderDependent() {
        val left = Box(0.0, 0.0, 10.0, 10.0)
        val middle = Box(5.0, 0.0, 10.0, 10.0) // IoU 0.33 with left AND with right
        val right = Box(10.0, 0.0, 10.0, 10.0) // only touches left: IoU 0

        // left first: middle merges into left and the scan stops there. The
        // widened box now overlaps right at only 0.25, so right stays separate.
        val leftFirst = CropGeometry.mergeOverlapping(listOf(left, middle, right))
        assertEquals(2, leftFirst.size)
        assertEquals(Box(0.0, 0.0, 15.0, 10.0), leftFirst[0])
        assertEquals(right, leftFirst[1])

        // right first: now middle merges into right instead, and left is the
        // leftover. Same three boxes, different output.
        val rightFirst = CropGeometry.mergeOverlapping(listOf(right, middle, left))
        assertEquals(2, rightFirst.size)
        assertEquals(Box(5.0, 0.0, 15.0, 10.0), rightFirst[0])
        assertEquals(left, rightFirst[1])
    }

    // ------------------------------------------------------------------
    // pad
    // ------------------------------------------------------------------

    @Test
    fun padOutsetsProportionallyPerAxisOnBothSides() {
        val box = Box(100.0, 200.0, 100.0, 40.0)
        val padded = CropGeometry.pad(box, width = 1000, height = 1000)
        // dx = 15, dy = 6, applied to BOTH sides of each axis.
        assertEquals(85.0, padded.x, eps)
        assertEquals(194.0, padded.y, eps)
        assertEquals(130.0, padded.width, eps)
        assertEquals(52.0, padded.height, eps)
    }

    @Test
    fun padClampsToImageBounds() {
        val box = Box(0.0, 0.0, 100.0, 100.0)
        val padded = CropGeometry.pad(box, width = 100, height = 100)
        assertEquals(Box(0.0, 0.0, 100.0, 100.0), padded)
    }

    @Test
    fun padOfAnEntirelyOutOfBoundsBoxCollapsesToZero() {
        val box = Box(500.0, 500.0, 10.0, 10.0)
        val padded = CropGeometry.pad(box, width = 100, height = 100)
        assertEquals(0.0, padded.width, eps)
        assertEquals(0.0, padded.height, eps)
    }

    // ------------------------------------------------------------------
    // pixelRect
    // ------------------------------------------------------------------

    @Test
    fun pixelRectFlipsBottomLeftNormalizedToTopLeftPixels() {
        // A box in the TOP half of the photo: normalized y from 0.6 to 0.9
        // (origin bottom-left) must land near the TOP in pixels (origin top-left).
        val normalized = Box(x = 0.25, y = 0.6, width = 0.5, height = 0.3)
        val pixels = CropGeometry.pixelRect(normalized, width = 200, height = 100)
        assertEquals(50.0, pixels.x, eps)
        // y = (1 - maxY) * h = (1 - 0.9) * 100
        assertEquals(10.0, pixels.y, eps)
        assertEquals(100.0, pixels.width, eps)
        assertEquals(30.0, pixels.height, eps)
    }

    @Test
    fun pixelRectOfTheFullFrameIsTheFullImage() {
        val pixels = CropGeometry.pixelRect(Box(0.0, 0.0, 1.0, 1.0), width = 640, height = 480)
        assertEquals(Box(0.0, 0.0, 640.0, 480.0), pixels)
    }

    // ------------------------------------------------------------------
    // tileGrid
    // ------------------------------------------------------------------

    /**
     * **16 tiles, not 9 and not 12.**
     *
     * The Swift doc comment says "A 3x3 grid of overlapping tiles" and is wrong:
     * with `step = tile * 0.8` the origins are `0, s, 2s, 3s` on each axis and
     * `3s = 0.8 * size < size`, so the loop runs four times per axis. This is real
     * shipped behavior, reproduced deliberately for parity.
     *
     * A `tile = size / 3` formulation using the image's **minimum** dimension
     * emits 12 for 2016x1512 — the right answer from the wrong arithmetic — so
     * the landscape, portrait and square cases are all asserted.
     *
     * Do not "fix" this without re-running `eval/`.
     */
    @Test
    fun tileGridEmitsSixteenTilesForLandscapePortraitAndSquare() {
        assertEquals(16, CropGeometry.tileGrid(2016, 1512).size)
        assertEquals(16, CropGeometry.tileGrid(1512, 2016).size)
        assertEquals(16, CropGeometry.tileGrid(900, 900).size)
    }

    @Test
    fun tileGridUsesPerAxisTilesAndClampsTheLastColumnAndRow() {
        val tiles = CropGeometry.tileGrid(2016, 1512)
        // 2016/3 = 672 wide, 1512/3 = 504 tall; steps 537.6 and 403.2.
        assertEquals(672.0, tiles[0].width, 1e-6)
        assertEquals(504.0, tiles[0].height, 1e-6)
        assertEquals(0.0, tiles[0].x, 1e-6)
        assertEquals(537.6, tiles[1].x, 1e-6)

        // Last tile starts at 3 * step on each axis and is clipped to the frame.
        val last = tiles.last()
        assertEquals(1612.8, last.x, 1e-6)
        assertEquals(1209.6, last.y, 1e-6)
        assertEquals(2016.0 - 1612.8, last.width, 1e-6)
        assertEquals(1512.0 - 1209.6, last.height, 1e-6)
    }

    @Test
    fun tileGridTilesOverlapByTwentyPercent() {
        val tiles = CropGeometry.tileGrid(1000, 1000)
        // Tiles already overlap, which is exactly why the crop pipeline does NOT
        // pad them the way it pads detected boxes.
        val first = tiles[0]
        val second = tiles[1]
        assertTrue(CropGeometry.iou(first, second) > 0.0)
        assertEquals(first.width * (1 - CropGeometry.TILE_OVERLAP), second.x - first.x, 1e-6)
    }

    @Test
    fun tileGridDropsTilesUnderTheMinimumSide() {
        // 3 px wide: every tile is under 8 px on the x axis, so nothing is emitted.
        assertTrue(CropGeometry.tileGrid(3, 3000).isEmpty())
    }

    @Test
    fun tileGridOfADegenerateImageIsEmptyRatherThanInfinite() {
        assertTrue(CropGeometry.tileGrid(0, 0).isEmpty())
    }

    // ------------------------------------------------------------------
    // Pipeline order: merge -> pad -> count
    // ------------------------------------------------------------------

    /**
     * `isTileFallback = boxes.count < 2` is evaluated on the **padded** set,
     * after the merge that `detectBoxes` performs internally. Stating the order
     * backwards (pad -> count -> merge) produces different unions and a different
     * fallback decision, so plan Step 25 replays exactly this sequence.
     */
    @Test
    fun fallbackDecisionSeesTheMergedThenPaddedSet() {
        val width = 1000
        val height = 1000
        val raw = listOf(
            Box(100.0, 100.0, 200.0, 200.0),
            Box(120.0, 120.0, 200.0, 200.0), // merges with the first
        )

        val merged = CropGeometry.mergeOverlapping(raw)
        assertEquals(1, merged.size)

        val padded = merged.map { CropGeometry.pad(it, width, height) }
        val isTileFallback = padded.size < 2
        assertTrue(isTileFallback)

        val boxes = if (isTileFallback) CropGeometry.tileGrid(width, height) else padded
        assertEquals(16, boxes.size)
    }
}
