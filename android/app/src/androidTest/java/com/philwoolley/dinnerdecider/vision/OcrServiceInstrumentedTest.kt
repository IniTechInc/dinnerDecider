package com.philwoolley.dinnerdecider.vision

import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * ML Kit text recognition end to end, including the blocks -> lines traversal the
 * host-JVM suite cannot reach.
 *
 * **Device status: WRITTEN, NOT RUN.** Needs an arm64 emulator (plan 0.11 / §2).
 *
 * The first test is the one that matters for the **bundled vs Play-Services**
 * decision: it must pass on a freshly installed app with no network. If the
 * dependency were ever swapped to `play-services-mlkit-text-recognition`, this
 * would return "" on first run and the scan would silently lose every packaged
 * good's label.
 */
@RunWith(AndroidJUnit4::class)
class OcrServiceInstrumentedTest {

    private fun labelBitmap(vararg lines: String): Bitmap =
        Bitmap.createBitmap(600, 400, Bitmap.Config.ARGB_8888).apply {
            val canvas = Canvas(this)
            canvas.drawColor(Color.WHITE)
            val paint = Paint().apply {
                color = Color.BLACK
                textSize = 56f
                isAntiAlias = true
            }
            lines.forEachIndexed { index, line ->
                canvas.drawText(line, 40f, 90f + index * 80f, paint)
            }
        }

    @Test
    fun theBundledModelRecognizesTextWithoutAnyNetworkWarmUp() = runBlocking {
        val service = OcrService()
        try {
            val recognized = service.recognizeText(labelBitmap("HORIZON", "WHOLE MILK"))
            assertTrue("expected some text, got '$recognized'", recognized.isNotEmpty())
            assertTrue(recognized.uppercase().contains("MILK"))
        } finally {
            service.close()
        }
    }

    @Test
    fun recognizedLinesArriveSpaceJoinedWithNoNewlines() = runBlocking {
        val service = OcrService()
        try {
            val recognized = service.recognizeText(labelBitmap("ORGANIC", "OAT MILK"))
            assertFalse(recognized.contains('\n'))
            assertFalse(recognized.contains("  "))
        } finally {
            service.close()
        }
    }

    @Test
    fun aBlankImageYieldsTheEmptyStringRatherThanAnError() = runBlocking {
        val service = OcrService()
        try {
            val blank = Bitmap.createBitmap(200, 200, Bitmap.Config.ARGB_8888)
                .apply { eraseColor(Color.WHITE) }
            assertEquals("", service.recognizeText(blank))
        } finally {
            service.close()
        }
    }

    @Test
    fun aRecycledBitmapYieldsTheEmptyStringAndTheScanContinues() = runBlocking {
        val service = OcrService()
        try {
            val bitmap = labelBitmap("MILK")
            bitmap.recycle()
            assertEquals("", service.recognizeText(bitmap))
        } finally {
            service.close()
        }
    }

    @Test
    fun aClosedRecognizerFailsToTheEmptyStringNotAnException() = runBlocking {
        val service = OcrService()
        service.close()
        assertEquals("", service.recognizeText(labelBitmap("MILK")))
    }
}
