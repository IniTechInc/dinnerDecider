package com.philwoolley.dinnerdecider.vision

import com.philwoolley.dinnerdecider.contract.VisionPrompts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Test

/**
 * Host-JVM cover for the OCR **join contract**.
 *
 * Recognition itself needs ML Kit on a device, and so does the blocks -> lines
 * traversal: ML Kit's `Text` has no public constructor and its final classes
 * cannot be instrumented off-device (mockk fails with "class redefinition failed"
 * against the stubbed jar). `OcrServiceInstrumentedTest` covers that mapping. What
 * is covered here is the part that is a **decision** and that a port gets wrong:
 * how recognized lines become one prompt string.
 */
class OcrServiceTest {

    @Test
    fun linesJoinWithASingleSpace() {
        assertEquals(
            "HORIZON Organic Whole Milk Half Gallon",
            OcrService.joinLines(listOf("HORIZON", "Organic", "Whole Milk", "Half Gallon")),
        )
    }

    @Test
    fun theResultCarriesNoNewlines() {
        val flattened = OcrService.joinLines(listOf("Line one", "Line two"))
        assertFalse(flattened.contains('\n'))
        assertFalse(flattened.contains('\r'))
    }

    @Test
    fun lineOrderIsPreservedExactlyAsReported() {
        // No reordering, no sorting by position, no confidence ranking: the
        // detector's own order is the order.
        assertEquals("c a b", OcrService.joinLines(listOf("c", "a", "b")))
    }

    @Test
    fun emptyLinesNeverProduceADoubleSpace() {
        assertEquals("A B", OcrService.joinLines(listOf("A", "", "B")))
        assertEquals("A", OcrService.joinLines(listOf("", "A", "")))
    }

    @Test
    fun noTextAtAllIsTheEmptyStringNotNullAndNotAnError() {
        assertEquals("", OcrService.joinLines(emptyList()))
        assertEquals("", OcrService.joinLines(listOf("", "")))
    }

    @Test
    fun lowConfidenceLookingTextIsKeptBecauseThereIsNoFiltering() {
        // Smudged label text is exactly what OCR was added for; a confidence or
        // length threshold here would drop it. Single characters survive.
        assertEquals("2% MLK 1 GAL", OcrService.joinLines(listOf("2%", "MLK", "1", "GAL")))
    }

    /**
     * The empty string is not a dead end: at the prompt boundary it becomes the
     * literal `none`, so "OCR failed" and "there was no text" reach the model as
     * the same thing. This is the reason [OcrService] may swallow every failure.
     */
    @Test
    fun anEmptyResultBecomesTheLiteralNoneAtThePromptBoundary() {
        assertEquals(
            VisionPrompts.NO_OCR_TEXT,
            VisionPrompts.renderOcrText(OcrService.joinLines(emptyList())),
        )
        assertEquals("none", VisionPrompts.NO_OCR_TEXT)
    }
}
