package com.philwoolley.dinnerdecider.vision

import android.graphics.Bitmap
import android.util.Log
import com.google.mlkit.vision.common.InputImage
import com.google.mlkit.vision.objects.ObjectDetection
import com.google.mlkit.vision.objects.ObjectDetector
import com.google.mlkit.vision.objects.defaults.ObjectDetectorOptions
import com.philwoolley.dinnerdecider.contract.Box
import kotlinx.coroutines.suspendCancellableCoroutine
import java.io.Closeable
import kotlin.coroutines.resume

/**
 * ML Kit on-device object detection, mapped onto [BoxDetector].
 *
 * ## This is WIRED OFF, and turning it on has a gate
 *
 * [ENABLED_BY_DEFAULT] is `false` and [CropService.crops] takes `detector = null`
 * by default, so nothing in the app constructs this class. That is not caution
 * about ML Kit's quality — it is the shared contract.
 * `shared/pipeline/scan-pipeline.md`, "Ports without a saliency detector":
 *
 * > A port with no detector available must go **straight to the tile grid** with
 * > `isTileFallback = true`. [...] Do **not** substitute a different object
 * > detector without re-running the eval harness (`eval/`): crop geometry changes
 * > recall, and the confidence/dedupe constants were tuned against these crops.
 *
 * Two things change the moment a detector produces two or more padded boxes:
 * the crops themselves, and — because `isTileFallback` flips to false — the
 * **prompt**, from `identify-all-items` to `identify-item`. Neither shows up as
 * an error. Both move recall.
 *
 * **Flipping [ENABLED_BY_DEFAULT] requires an `eval/` run whose union recall
 * beats the 33.5% recorded in `eval/RESULTS.md`, committed alongside the flip.**
 * Absent that number, leave it false.
 *
 * ## Coordinate space
 *
 * ML Kit's `DetectedObject.boundingBox` is an [android.graphics.Rect] **in the
 * input image's own pixel coordinates with a top-left origin** — not a normalized
 * bottom-left rect the way Vision reports. So [CropGeometry.pixelRect][com.philwoolley.dinnerdecider.contract.CropGeometry.pixelRect]
 * is deliberately **not** applied here; applying it would mirror every box into
 * the wrong half of the photo. The output of [detect] goes through exactly the
 * same **merge -> pad -> count** sequence as any other detector, because that
 * sequence lives in [CropService.plan] and not in the detector.
 */
class MlKitObjectDetector(
    private val detector: ObjectDetector = ObjectDetection.getClient(DEFAULT_OPTIONS),
) : BoxDetector, Closeable {

    /**
     * Detected boxes in image pixel coordinates, raw: unmerged and unpadded.
     * [CropService.plan] owns the merge, the padding and the count.
     *
     * Never throws. A detector failure returns an empty list, which lands the scan
     * on the tile grid — the same place it starts from with no detector at all.
     */
    override suspend fun detect(bitmap: Bitmap): List<Box> {
        val readable = ImageIntake.ensureSoftware(bitmap) ?: return emptyList()
        return runCatching {
            val image = InputImage.fromBitmap(readable, 0)
            val objects = suspendCancellableCoroutine { continuation ->
                detector.process(image)
                    .addOnSuccessListener { continuation.resume(it) }
                    .addOnFailureListener {
                        Log.w(TAG, "detect: ML Kit failed; scan will use tiles", it)
                        continuation.resume(emptyList())
                    }
                    .addOnCanceledListener { continuation.resume(emptyList()) }
            }
            objects.map { detected ->
                val rect = detected.boundingBox
                Box(
                    x = rect.left.toDouble(),
                    y = rect.top.toDouble(),
                    width = rect.width().toDouble(),
                    height = rect.height().toDouble(),
                )
            }
        }.onFailure { Log.w(TAG, "detect: unexpected failure", it) }.getOrDefault(emptyList())
    }

    override fun close() {
        runCatching { detector.close() }
    }

    companion object {
        private const val TAG = "MlKitObjectDetector"

        /**
         * **false, and it stays false** until an `eval/` run beats 33.5% union
         * recall. See the class KDoc. Read this flag rather than constructing the
         * detector directly, so the gate is visible at every call site.
         */
        const val ENABLED_BY_DEFAULT: Boolean = false

        /**
         * `MULTIPLE_OBJECTS` because a fridge shelf is many items, and
         * `SINGLE_IMAGE_MODE` (`enableMultipleObjects` + no stream mode) because
         * each photo is an independent still. Stream mode carries tracking state
         * between frames, which is meaningless here and would make the crop set
         * depend on which photo was scanned before it.
         *
         * Classification stays off: the categories ML Kit offers are not the
         * app's [com.philwoolley.dinnerdecider.contract.FoodCategory] set, and the
         * model — not the detector — decides what an item is.
         */
        val DEFAULT_OPTIONS: ObjectDetectorOptions = ObjectDetectorOptions.Builder()
            .setDetectorMode(ObjectDetectorOptions.SINGLE_IMAGE_MODE)
            .enableMultipleObjects()
            .build()
    }
}
