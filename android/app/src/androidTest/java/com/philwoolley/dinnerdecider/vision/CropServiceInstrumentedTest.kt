package com.philwoolley.dinnerdecider.vision

import android.graphics.Bitmap
import android.graphics.Color
import androidx.test.ext.junit.runners.AndroidJUnit4
import com.philwoolley.dinnerdecider.contract.Box
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Bitmap half of [CropService] — the part the host JVM cannot prove, because
 * Robolectric does not rasterise a `Canvas` and its `Bitmap.createBitmap(src, ...)`
 * does not genuinely resample.
 *
 * **Device status: WRITTEN, NOT RUN.** Requires an arm64 emulator, which is not
 * available in this environment (see the plan's 0.11 / §2). Nothing here depends
 * on the model or on native libraries, so it should pass as soon as an emulator
 * exists.
 */
@RunWith(AndroidJUnit4::class)
class CropServiceInstrumentedTest {

    private fun photo(width: Int, height: Int): Bitmap =
        Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888).apply {
            eraseColor(Color.rgb(120, 90, 60))
        }

    @Test
    fun cropsFromARealBitmapMatchThePlannedRects() = runBlocking {
        val source = photo(900, 900)
        val result = CropService.crops(source)

        assertTrue(result.isTileFallback)
        assertEquals(16, result.images.size)
        assertEquals(result.images.size, result.rects.size)
        result.images.forEachIndexed { index, bitmap ->
            assertEquals(result.rects[index].width(), bitmap.width)
            assertEquals(result.rects[index].height(), bitmap.height)
        }
    }

    @Test
    fun everyEmittedCropIsPixelReadable() = runBlocking {
        val result = CropService.crops(photo(900, 900))
        // getPixels() on a HARDWARE bitmap throws; this is the assertion that the
        // whole ensureSoftware guard exists for.
        result.images.forEach { crop ->
            val pixels = IntArray(crop.width * crop.height)
            crop.getPixels(pixels, 0, crop.width, 0, 0, crop.width, crop.height)
            assertEquals(Color.rgb(120, 90, 60), pixels.first())
        }
    }

    @Test
    fun aDetectorsBoxesBecomeTargetedCropsRatherThanTiles() = runBlocking {
        val detector = BoxDetector {
            listOf(
                Box(100.0, 100.0, 200.0, 200.0),
                Box(600.0, 600.0, 200.0, 200.0),
            )
        }
        val result = CropService.crops(photo(1000, 1000), detector)

        assertFalse(result.isTileFallback)
        assertEquals(2, result.images.size)
        assertEquals(260, result.images[0].width) // padded 200 -> 260
    }

    @Test
    fun aThrowingDetectorFallsBackToTilesRatherThanPropagating() = runBlocking {
        val detector = BoxDetector { error("detector exploded") }
        val result = CropService.crops(photo(900, 900), detector)

        assertTrue(result.isTileFallback)
        assertEquals(16, result.images.size)
    }

    @Test
    fun aRecycledBitmapYieldsZeroCropsAndNotACrash() = runBlocking {
        val source = photo(900, 900)
        source.recycle()
        val result = CropService.crops(source)

        // The exact shape of CropService.swift:31-33 when normalizedCGImage is nil.
        assertTrue(result.images.isEmpty())
        assertTrue(result.rects.isEmpty())
        assertFalse(result.isTileFallback)
    }

    @Test
    fun aHardwareBitmapIsCopiedToSoftwareRatherThanThrowing() = runBlocking {
        val software = photo(600, 600)
        val hardware = software.copy(Bitmap.Config.HARDWARE, false)
        // Some emulators refuse a HARDWARE allocation; skip rather than fail there.
        if (hardware == null) return@runBlocking

        assertTrue(ImageIntake.isHardware(hardware))
        val recovered = ImageIntake.ensureSoftware(hardware)
        assertNotNull(recovered)
        assertEquals(Bitmap.Config.ARGB_8888, recovered!!.config)

        val result = CropService.crops(hardware)
        assertEquals(16, result.images.size)
    }
}
