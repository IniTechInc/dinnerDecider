package com.philwoolley.dinnerdecider.vision

import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * ML Kit's bundled object detector, exercised for real.
 *
 * **Device status: WRITTEN, NOT RUN.** ML Kit needs Android runtime services and
 * an arm64 emulator, which is not available here (plan 0.11 / §2).
 *
 * Note what these deliberately do **not** assert: how many objects ML Kit finds,
 * or where. That number is exactly what would have to be measured by an `eval/`
 * run before [MlKitObjectDetector.ENABLED_BY_DEFAULT] could be flipped, and a
 * test asserting it here would be inventing the measurement. What is asserted is
 * the contract: pixel-space boxes inside the image, and never an exception.
 */
@RunWith(AndroidJUnit4::class)
class MlKitObjectDetectorInstrumentedTest {

    private fun sceneBitmap(): Bitmap =
        Bitmap.createBitmap(640, 480, Bitmap.Config.ARGB_8888).apply {
            val canvas = Canvas(this)
            canvas.drawColor(Color.WHITE)
            val paint = Paint().apply { color = Color.rgb(200, 60, 40) }
            canvas.drawRect(80f, 100f, 260f, 340f, paint)
            paint.color = Color.rgb(40, 120, 200)
            canvas.drawRect(360f, 120f, 560f, 360f, paint)
        }

    @Test
    fun detectReturnsPixelSpaceBoxesInsideTheImage() = runBlocking {
        val detector = MlKitObjectDetector()
        try {
            val bitmap = sceneBitmap()
            val boxes = detector.detect(bitmap)
            boxes.forEach { box ->
                assertTrue("x within image", box.minX >= 0.0 && box.maxX <= bitmap.width.toDouble())
                assertTrue("y within image", box.minY >= 0.0 && box.maxY <= bitmap.height.toDouble())
                assertTrue("positive area", box.width > 0.0 && box.height > 0.0)
            }
        } finally {
            detector.close()
        }
    }

    @Test
    fun detectNeverThrowsOnARecycledBitmap() = runBlocking {
        val detector = MlKitObjectDetector()
        try {
            val bitmap = sceneBitmap()
            bitmap.recycle()
            assertTrue(detector.detect(bitmap).isEmpty())
        } finally {
            detector.close()
        }
    }

    @Test
    fun aClosedDetectorDegradesToTilesRatherThanCrashingTheScan() = runBlocking {
        val detector = MlKitObjectDetector()
        detector.close()
        val result = CropService.crops(sceneBitmap(), detector)
        // Zero boxes -> 0 < 2 -> tile grid, the Android default path.
        assertTrue(result.isTileFallback)
        assertTrue(result.images.isNotEmpty())
    }
}
