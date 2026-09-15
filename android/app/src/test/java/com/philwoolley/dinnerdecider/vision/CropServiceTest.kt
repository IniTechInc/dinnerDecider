package com.philwoolley.dinnerdecider.vision

import com.philwoolley.dinnerdecider.contract.Box
import com.philwoolley.dinnerdecider.contract.CropGeometry
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * Host-JVM cover for the part of [CropService] that is a decision rather than
 * bitmap I/O: **merge -> pad -> count**, the tile-grid default, and the 8 px emit
 * filter. Actual cropping is exercised in the instrumented suite.
 */
@RunWith(RobolectricTestRunner::class)
class CropServiceTest {

    // MARK: - The Android default: no detector, straight to tiles

    @Test
    fun noDetectorGoesStraightToTheTileGridWithTileFallbackTrue() {
        val plan = CropService.plan(2016, 1512)
        assertTrue(plan.isTileFallback)
        // 0.4: the per-axis loop emits 4 columns x 4 rows for an image of ordinary size.
        assertEquals(16, plan.rects.size)
    }

    @Test
    fun theTileGridEmitsSixteenForEveryShippedFixtureSize() {
        assertEquals(16, CropService.plan(2016, 1512).rects.size)
        assertEquals(16, CropService.plan(1512, 2016).rects.size)
        assertEquals(16, CropService.plan(900, 900).rects.size)
    }

    @Test
    fun tilesAreNotPadded() {
        // A padded tile would be 30% wider than tileWidth. The first tile must be
        // exactly width/3 x height/3, rounded out: tiles already overlap by 20%.
        val plan = CropService.plan(900, 900)
        val first = plan.rects.first()
        assertEquals(0, first.left)
        assertEquals(0, first.top)
        assertEquals(300, first.width())
        assertEquals(300, first.height())
    }

    @Test
    fun tilesCoverTheWholeImageIncludingTheRemainderColumnAndRow() {
        val plan = CropService.plan(900, 900)
        assertEquals(900, plan.rects.maxOf { it.right })
        assertEquals(900, plan.rects.maxOf { it.bottom })
    }

    @Test
    fun aDegenerateImageYieldsNothingAndIsNotFlaggedAsTiles() {
        val plan = CropService.plan(0, 0)
        assertTrue(plan.rects.isEmpty())
        assertFalse(plan.isTileFallback)
    }

    // MARK: - Order: merge, then pad, then count

    @Test
    fun twoOverlappingDetectionsMergeToOneAndThereforeFallBackToTiles() {
        // IoU of these two is well over 0.3, so the greedy merge unions them into a
        // single box. One box is `< 2`, so the tile grid runs.
        val a = Box(100.0, 100.0, 400.0, 400.0)
        val b = Box(140.0, 140.0, 400.0, 400.0)
        assertTrue("fixture must actually exceed the merge threshold", CropGeometry.iou(a, b) > 0.3)

        val plan = CropService.plan(2016, 1512, listOf(a, b))
        assertTrue(plan.isTileFallback)
        assertEquals(16, plan.rects.size)
    }

    @Test
    fun twoDisjointDetectionsSurviveAsPaddedBoxesAndDoNotFallBack() {
        val a = Box(100.0, 100.0, 200.0, 200.0)
        val b = Box(1000.0, 800.0, 200.0, 200.0)
        val plan = CropService.plan(2016, 1512, listOf(a, b))

        assertFalse(plan.isTileFallback)
        assertEquals(2, plan.rects.size)
        // pad(0.15) outsets by 30 px on each side of a 200 px box: 200 -> 260.
        assertEquals(70, plan.rects[0].left)
        assertEquals(70, plan.rects[0].top)
        assertEquals(260, plan.rects[0].width())
        assertEquals(260, plan.rects[0].height())
    }

    /**
     * The order trap, stated as a test.
     *
     * These two boxes are **under** the 0.3 IoU threshold as detected, so the merge
     * keeps them apart and the photo does NOT take the tile fallback. After padding
     * they overlap enough that a pad-then-merge pipeline would union them into one
     * box, count `1 < 2`, and hand the model 16 tiles instead of 2 targeted crops —
     * a different scan with no error anywhere.
     */
    @Test
    fun paddedBoxesAreNotReMergedSoTheFallbackDecisionStaysWithTheDetectedGeometry() {
        val a = Box(100.0, 100.0, 200.0, 200.0)
        val b = Box(230.0, 100.0, 200.0, 200.0)
        assertTrue("fixture must be under the threshold before padding", CropGeometry.iou(a, b) <= 0.3)

        val merged = CropGeometry.mergeOverlapping(listOf(a, b))
        assertEquals(2, merged.size)
        val padded = merged.map { CropGeometry.pad(it, 2016, 1512) }
        assertTrue(
            "fixture must cross the threshold once padded, or it proves nothing",
            CropGeometry.iou(padded[0], padded[1]) > 0.3,
        )

        val plan = CropService.plan(2016, 1512, listOf(a, b))
        assertFalse(plan.isTileFallback)
        assertEquals(2, plan.rects.size)
    }

    @Test
    fun aSingleDetectionIsStillFewerThanTwoAndTakesTheTileGrid() {
        val plan = CropService.plan(2016, 1512, listOf(Box(100.0, 100.0, 400.0, 400.0)))
        assertTrue(plan.isTileFallback)
        assertEquals(16, plan.rects.size)
    }

    @Test
    fun zeroAreaDetectionsAreDroppedByTheMergeAndDoNotPropUpTheCount() {
        // mergeOverlapping skips boxes with non-positive area, so these two
        // "detections" reduce to one real box and the grid runs.
        val plan = CropService.plan(
            2016,
            1512,
            listOf(Box(10.0, 10.0, 0.0, 50.0), Box(100.0, 100.0, 400.0, 400.0)),
        )
        assertTrue(plan.isTileFallback)
    }

    // MARK: - The 8 px emit filter

    @Test
    fun cropsUnderEightPixelsAreNotEmitted() {
        val tiny = Box(10.0, 10.0, 2.0, 2.0) // 2 -> 2.6 after padding, still < 8
        val big = Box(500.0, 500.0, 300.0, 300.0)
        val other = Box(1200.0, 900.0, 300.0, 300.0)
        val plan = CropService.plan(2016, 1512, listOf(tiny, big, other))

        assertFalse(plan.isTileFallback) // three boxes survive the merge
        assertEquals(2, plan.rects.size) // but the 2 px one never becomes a crop
        assertTrue(plan.rects.all { it.width() >= CropService.MIN_CROP_SIDE })
        assertTrue(plan.rects.all { it.height() >= CropService.MIN_CROP_SIDE })
    }

    @Test
    fun integralRoundsOutwardAndClampsToTheImage() {
        val rect = CropService.integral(Box(10.4, 20.6, 30.3, 40.1), 2016, 1512)
        assertEquals(10, rect.left) // floor
        assertEquals(20, rect.top) // floor
        assertEquals(41, rect.right) // ceil(40.7)
        assertEquals(61, rect.bottom) // ceil(60.7)

        val clamped = CropService.integral(Box(-5.0, -5.0, 3000.0, 3000.0), 2016, 1512)
        assertEquals(0, clamped.left)
        assertEquals(0, clamped.top)
        assertEquals(2016, clamped.right)
        assertEquals(1512, clamped.bottom)
    }
}
