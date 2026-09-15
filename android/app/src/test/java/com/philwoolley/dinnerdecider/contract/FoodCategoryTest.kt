package com.philwoolley.dinnerdecider.contract

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Test

/**
 * Pins `shared/taxonomy/categories.md`.
 *
 * The fallback to `other` is silent by design, so a typo in one of the nine raw
 * strings fails no parsing test — it just files a whole category under "Other"
 * forever. These tests are the only thing that catches that.
 */
class FoodCategoryTest {

    @Test
    fun `nine categories in CaseIterable order with the exact raw strings`() {
        assertEquals(
            listOf(
                "produce", "dairy", "meat", "pantry", "snack",
                "beverage", "condiment", "frozen", "other",
            ),
            FoodCategory.entries.map { it.raw },
        )
    }

    /** The three plural display names must survive; do not regularise them. */
    @Test
    fun `display names keep the three plurals and nothing else`() {
        assertEquals(
            listOf(
                "Produce", "Dairy", "Meat", "Pantry", "Snacks",
                "Beverages", "Condiments", "Frozen", "Other",
            ),
            FoodCategory.entries.map { it.displayName },
        )
        assertEquals("Snacks", FoodCategory.SNACK.displayName)
        assertEquals("Beverages", FoodCategory.BEVERAGE.displayName)
        assertEquals("Condiments", FoodCategory.CONDIMENT.displayName)
        // The other six stay singular.
        assertEquals("Produce", FoodCategory.PRODUCE.displayName)
        assertEquals("Frozen", FoodCategory.FROZEN.displayName)
    }

    @Test
    fun `fromModelString matches the canonical lowercase values`() {
        assertEquals(FoodCategory.PRODUCE, FoodCategory.fromModelString("produce"))
        assertEquals(FoodCategory.CONDIMENT, FoodCategory.fromModelString("condiment"))
        assertEquals(FoodCategory.OTHER, FoodCategory.fromModelString("other"))
    }

    /**
     * The untrusted boundary: the model frequently answers `Dairy` or `PRODUCE`.
     */
    @Test
    fun `fromModelString lowercases model output`() {
        assertEquals(FoodCategory.DAIRY, FoodCategory.fromModelString("Dairy"))
        assertEquals(FoodCategory.PRODUCE, FoodCategory.fromModelString("PRODUCE"))
        assertEquals(FoodCategory.BEVERAGE, FoodCategory.fromModelString("BeVeRaGe"))
    }

    @Test
    fun `fromModelString falls back to other rather than throwing`() {
        assertEquals(FoodCategory.OTHER, FoodCategory.fromModelString("beverages"))
        assertEquals(FoodCategory.OTHER, FoodCategory.fromModelString("groceries"))
        assertEquals(FoodCategory.OTHER, FoodCategory.fromModelString(""))
    }

    /** No trimming, no fuzzy matching, no synonym table. Matching is exact. */
    @Test
    fun `fromModelString does not trim`() {
        assertEquals(FoodCategory.OTHER, FoodCategory.fromModelString(" dairy"))
        assertEquals(FoodCategory.OTHER, FoodCategory.fromModelString("dairy "))
    }

    @Test
    fun `fromStorage matches canonical values`() {
        assertEquals(FoodCategory.MEAT, FoodCategory.fromStorage("meat"))
        assertEquals(FoodCategory.OTHER, FoodCategory.fromStorage("nonsense"))
    }

    /**
     * THE ASYMMETRY. `fromStorage` does **not** lower-case, because the value it
     * reads was written by the enum's own raw value and is already canonical
     * (`categories.md` rule 4). Keeping the normalization at the untrusted boundary
     * only is invisible until a fine-tune produces `Dairy` instead of `dairy` — at
     * which point one of these two calls must still answer OTHER.
     */
    @Test
    fun `fromStorage does not lowercase but fromModelString does`() {
        assertEquals(FoodCategory.DAIRY, FoodCategory.fromModelString("Dairy"))
        assertEquals(FoodCategory.OTHER, FoodCategory.fromStorage("Dairy"))
        assertNotEquals(FoodCategory.fromModelString("Dairy"), FoodCategory.fromStorage("Dairy"))

        assertEquals(FoodCategory.PRODUCE, FoodCategory.fromModelString("PRODUCE"))
        assertEquals(FoodCategory.OTHER, FoodCategory.fromStorage("PRODUCE"))
    }

    /**
     * Rule 5: persisting `foodCategory.raw` (never the model's original string) is
     * what keeps the non-lower-casing storage read safe.
     */
    @Test
    fun `canonical raw round-trips through storage`() {
        FoodCategory.entries.forEach { category ->
            assertEquals(category, FoodCategory.fromStorage(category.raw))
        }
    }
}
