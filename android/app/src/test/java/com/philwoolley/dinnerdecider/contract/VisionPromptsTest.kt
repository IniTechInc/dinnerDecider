package com.philwoolley.dinnerdecider.contract

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Cheap regression checks on the two prompt templates.
 *
 * The character counts come straight from the two `shared/prompts/` documents and are the only
 * exact figures recorded anywhere (no tokenizer count exists in the repo). They
 * catch the failure this file exists to prevent: a reformatter wrapping a very
 * long string literal, or a "harmless" wording tweak, silently changing what the
 * model sees with nothing else in the suite noticing.
 */
class VisionPromptsTest {

    // MARK: - Rendered length

    @Test
    fun `identify-item renders to exactly 558 characters with none`() {
        assertEquals(558, VisionPrompts.identifyItem("").length)
    }

    @Test
    fun `identify-all-items renders to exactly 491 characters with none`() {
        assertEquals(491, VisionPrompts.identifyAllItems("").length)
    }

    // MARK: - Single line, no embedded newlines

    @Test
    fun `neither template contains a newline`() {
        assertFalse(VisionPrompts.IDENTIFY_ITEM_TEMPLATE.contains('\n'))
        assertFalse(VisionPrompts.IDENTIFY_ALL_ITEMS_TEMPLATE.contains('\n'))
    }

    /**
     * The OCR service joins blocks with a single space and never emits newlines
     * (`shared/pipeline/scan-pipeline.md`), so a rendered prompt stays single-line
     * for every input it can actually receive. Substitution itself is a plain
     * replace: it neither adds nor strips interior characters.
     */
    @Test
    fun `neither rendered prompt contains a newline`() {
        assertFalse(VisionPrompts.identifyItem("Whole Milk 1 Gallon").contains('\n'))
        assertFalse(VisionPrompts.identifyAllItems("Whole Milk 1 Gallon").contains('\n'))
    }

    @Test
    fun `neither prompt ends with a trailing newline`() {
        assertFalse(VisionPrompts.identifyItem("").endsWith("\n"))
        assertFalse(VisionPrompts.identifyAllItems("").endsWith("\n"))
    }

    // MARK: - OCR substitution

    @Test
    fun `an empty OCR result substitutes the literal none`() {
        assertEquals("none", VisionPrompts.renderOcrText(""))
        assertTrue(VisionPrompts.identifyItem("").contains("Text found on the packaging: none."))
        assertTrue(VisionPrompts.identifyAllItems("").contains("Text found in the image: none."))
    }

    @Test
    fun `a whitespace-only OCR result substitutes the literal none`() {
        assertEquals("none", VisionPrompts.renderOcrText("   \n\t  "))
        assertEquals(558, VisionPrompts.identifyItem("   \n  ").length)
        assertEquals(491, VisionPrompts.identifyAllItems("   \n  ").length)
    }

    @Test
    fun `OCR text is trimmed, not otherwise altered`() {
        assertEquals("Horizon Organic", VisionPrompts.renderOcrText("  Horizon Organic  "))
        assertTrue(
            VisionPrompts.identifyItem("  Horizon Organic  ")
                .contains("Text found on the packaging: Horizon Organic."),
        )
    }

    @Test
    fun `the placeholder is fully consumed`() {
        assertFalse(VisionPrompts.identifyItem("Milk").contains(VisionPrompts.OCR_PLACEHOLDER))
        assertFalse(VisionPrompts.identifyAllItems("Milk").contains(VisionPrompts.OCR_PLACEHOLDER))
    }

    // MARK: - Load-bearing wording

    @Test
    fun `both prompts spell out the full closed category set inline`() {
        val alternation =
            "\"category\": \"produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other\""
        assertTrue(VisionPrompts.IDENTIFY_ITEM_TEMPLATE.contains(alternation))
        assertTrue(VisionPrompts.IDENTIFY_ALL_ITEMS_TEMPLATE.contains(alternation))
        // If a category is ever added or renamed, both strings must change together.
        FoodCategory.entries.forEach { category ->
            assertTrue(category.raw, VisionPrompts.IDENTIFY_ITEM_TEMPLATE.contains(category.raw))
            assertTrue(
                category.raw,
                VisionPrompts.IDENTIFY_ALL_ITEMS_TEMPLATE.contains(category.raw),
            )
        }
    }

    /** Remove this sentence and the noise filter has nothing to match on. */
    @Test
    fun `identify-item keeps the unknown escape hatch`() {
        assertTrue(
            VisionPrompts.IDENTIFY_ITEM_TEMPLATE.contains(
                "If no grocery item is clearly visible, use name \"unknown\" with confidence 0.",
            ),
        )
    }

    /** The array brackets are what make the model emit a top-level array. */
    @Test
    fun `identify-all-items asks for a top-level JSON array`() {
        assertTrue(
            VisionPrompts.IDENTIFY_ALL_ITEMS_TEMPLATE
                .contains("Respond ONLY with a JSON array, no other words: [{"),
        )
        assertTrue(VisionPrompts.IDENTIFY_ALL_ITEMS_TEMPLATE.endsWith("}]"))
    }

    /**
     * The two passes must agree on naming or the name+brand dedupe between them
     * stops working, so the vocabulary anchor is byte-identical in both.
     */
    @Test
    fun `both prompts carry the identical 25-item vocabulary anchor`() {
        val anchor = "Common items include: milk, eggs, butter, cheese, yogurt, chicken, beef, " +
            "lettuce, tomatoes, onions, carrots, bread, rice, pasta, cereal, juice, soda, " +
            "ketchup, mustard, olive oil, salt, pepper, ice cream, frozen pizza."
        assertTrue(VisionPrompts.IDENTIFY_ITEM_TEMPLATE.contains(anchor))
        assertTrue(VisionPrompts.IDENTIFY_ALL_ITEMS_TEMPLATE.contains(anchor))
    }
}

/** The tuned runtime parameters. Each value has a field incident behind it. */
class InferenceParamsTest {

    /** 256 broke vision entirely in shipped builds and risks a process abort. */
    @Test
    fun `batch is 512`() {
        assertEquals(512, VisionPrompts.InferenceParams.BATCH)
    }

    @Test
    fun `the default context is the 1024-token contract value, not 2048`() {
        assertEquals(1024, VisionPrompts.InferenceParams.CONTEXT_CONTRACT)
        assertEquals(
            VisionPrompts.InferenceParams.CONTEXT_CONTRACT,
            VisionPrompts.InferenceParams.CONTEXT_DEFAULT,
        )
    }

    /**
     * 2048 exists as a named, documented candidate only. Promoting it to the default
     * requires an on-device PSS measurement that is currently blocked for want of an
     * 8 GB+ Android device.
     */
    @Test
    fun `the Android divergence is declared but not adopted`() {
        assertEquals(2048, VisionPrompts.InferenceParams.CONTEXT_ANDROID)
        assertEquals(1024, VisionPrompts.InferenceParams.CONTEXT_DEFAULT)
    }

    @Test
    fun `sampling matches the tuned iOS values`() {
        assertEquals(0.2f, VisionPrompts.InferenceParams.TEMPERATURE, 1e-6f)
        assertEquals(40, VisionPrompts.InferenceParams.TOP_K)
        assertEquals(0.95f, VisionPrompts.InferenceParams.TOP_P, 1e-6f)
    }

    /** A cap iOS did not have: on CPU-only, no cap is an unbounded hang. */
    @Test
    fun `there is a generation cap and it fits the contract context`() {
        val cap = VisionPrompts.InferenceParams.MAX_PREDICT_TOKENS
        assertTrue(cap > 0)
        assertTrue(cap < VisionPrompts.InferenceParams.CONTEXT_CONTRACT)
    }
}
