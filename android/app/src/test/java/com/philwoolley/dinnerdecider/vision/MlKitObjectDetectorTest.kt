package com.philwoolley.dinnerdecider.vision

import com.google.mlkit.vision.objects.defaults.ObjectDetectorOptions
import com.philwoolley.dinnerdecider.contract.Box
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * The gate, as a test. ML Kit's own inference needs a device (see
 * `MlKitObjectDetectorInstrumentedTest`); what is provable here is that the
 * detector is **not wired in**, and that a detector's boxes — whoever produces
 * them — go through the same merge -> pad -> count sequence as everything else.
 */
@RunWith(RobolectricTestRunner::class)
class MlKitObjectDetectorTest {

    /**
     * `shared/pipeline/scan-pipeline.md` forbids substituting a different object
     * detector without re-running `eval/`. Flipping this constant is therefore a
     * contract change, not a config change: it needs an `eval/` run beating the
     * 33.5% union recall in `eval/RESULTS.md`, committed with the flip. If this
     * assertion is failing, that run should be in the same diff.
     */
    @Test
    fun theDetectorIsOffByDefault() {
        assertFalse(MlKitObjectDetector.ENABLED_BY_DEFAULT)
    }

    @Test
    fun theDefaultOptionsAskForMultipleObjectsInSingleImageMode() {
        val options = MlKitObjectDetector.DEFAULT_OPTIONS
        assertEquals(ObjectDetectorOptions.SINGLE_IMAGE_MODE, options.detectorMode)
        assertTrue(options.isMultipleObjectsEnabled)
        // Classification stays off: the model decides what an item is, not the detector.
        assertFalse(options.isClassificationEnabled)
    }

    /**
     * ML Kit reports `boundingBox` in **image pixels, top-left origin**, so the
     * detector hands [CropService] pixel boxes and `pixelRect`'s y-flip is not
     * applied. This checks the consequence: boxes arriving in that space land
     * where they were detected, after the same padding every other detector gets.
     */
    @Test
    fun detectorBoxesRunThroughTheSameMergePadCountSequence() {
        val plan = CropService.plan(
            width = 1000,
            height = 1000,
            detectedBoxes = listOf(
                Box(100.0, 100.0, 200.0, 200.0),
                Box(600.0, 600.0, 200.0, 200.0),
            ),
        )
        assertFalse(plan.isTileFallback)
        assertEquals(2, plan.rects.size)
        assertEquals(70, plan.rects[0].left) // padded, not mirrored
        assertEquals(70, plan.rects[0].top)
        assertEquals(570, plan.rects[1].left)
        assertEquals(570, plan.rects[1].top)
    }

    @Test
    fun overlappingDetectorBoxesStillMergeAtIouPointThree() {
        val plan = CropService.plan(
            width = 1000,
            height = 1000,
            detectedBoxes = listOf(
                Box(100.0, 100.0, 400.0, 400.0),
                Box(140.0, 140.0, 400.0, 400.0),
            ),
        )
        // Merged to one, which is `< 2`, so the grid runs — same rule as anywhere else.
        assertTrue(plan.isTileFallback)
    }
}
