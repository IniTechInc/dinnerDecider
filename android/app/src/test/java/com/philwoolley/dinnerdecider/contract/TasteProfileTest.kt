package com.philwoolley.dinnerdecider.contract

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** Port of `DinnerDeciderTests/TasteProfileTests.swift`, Swift names preserved. */
class TasteProfileTest {

    // MARK: - hasContent

    /**
     * The regression that mattered: an allergies-only profile used to report "not
     * complete" and was silently dropped from the prompt, which is a safety issue.
     * It must now count as having content.
     */
    @Test
    fun testHasContentTrueForAllergiesOnly() {
        val profile = TasteProfile(allergies = listOf("Peanuts"))
        assertTrue(profile.hasContent)
    }

    @Test
    fun testHasContentTrueForOtherTextOnly() {
        val profile = TasteProfile(favoriteFoodsOther = "  ramen  ")
        assertTrue(profile.hasContent)
    }

    @Test
    fun testHasContentFalseWhenEmpty() {
        // Default spice level alone is not "content".
        assertFalse(TasteProfile().hasContent)
    }

    @Test
    fun testHasContentFalseForWhitespaceOtherText() {
        val profile = TasteProfile(dislikedFoodsOther = "   ")
        assertFalse(profile.hasContent)
    }

    // MARK: - promptFragment

    @Test
    fun testPromptFragmentJoinsPillsAndOtherText() {
        val profile = TasteProfile(
            favoriteFoods = listOf("Pizza", "Tacos"),
            favoriteFoodsOther = "ramen",
        )
        val fragment = profile.promptFragment()
        assertTrue(fragment.contains("Favorite foods: Pizza, Tacos, ramen."))
    }

    @Test
    fun testPromptFragmentAllergyLinePresentWhenOnlyAllergiesSet() {
        val profile = TasteProfile(allergies = listOf("Peanuts", "Shellfish"))
        val fragment = profile.promptFragment()
        assertTrue(fragment.contains("Allergies: Peanuts, Shellfish. Never include these."))
    }

    @Test
    fun testPromptFragmentOmitsEmptyCategories() {
        val profile = TasteProfile(allergies = listOf("Peanuts"))
        val fragment = profile.promptFragment()
        assertFalse(fragment.contains("Favorite foods:"))
        assertFalse(fragment.contains("Foods the user dislikes:"))
        assertFalse(fragment.contains("Cuisines they enjoy:"))
    }

    @Test
    fun testPromptFragmentAlwaysIncludesSpiceLine() {
        // Even a completely empty profile still states a spice preference.
        assertTrue(TasteProfile().promptFragment().contains("Spice preference:"))
    }

    @Test
    fun testPromptFragmentNeverContainsEmDash() {
        val profile = TasteProfile(
            favoriteFoods = listOf("Pizza"),
            dislikedFoods = listOf("Liver"),
            cuisinesLoved = listOf("Italian"),
            cuisinesAvoided = listOf("Fast food"),
            allergies = listOf("Peanuts"),
            allergiesOther = "kiwi",
            texturesAvoided = listOf("Mushy"),
        )
        assertFalse(profile.promptFragment().contains("—"))
    }
}

/**
 * The persistence traps, and the details the Swift suite leaves implicit. Kept in a
 * separate class so [TasteProfileTest] stays at the nine cases
 * `TasteProfileTests.swift` carries.
 */
class TasteProfilePersistenceTest {

    private val listKeys = listOf(
        "favoriteFoods", "dislikedFoods", "cuisinesLoved",
        "cuisinesAvoided", "allergies", "texturesAvoided",
    )

    private val allKeys = listOf(
        "favoriteFoods", "favoriteFoodsOther",
        "dislikedFoods", "dislikedFoodsOther",
        "cuisinesLoved", "cuisinesLovedOther",
        "cuisinesAvoided", "cuisinesAvoidedOther",
        "allergies", "allergiesOther",
        "texturesAvoided", "texturesAvoidedOther",
        "spiceLevel",
    )

    /**
     * TRAP 1: a partial blob fails Swift's synthesized `Decodable` entirely and the
     * whole profile is lost, allergies included. All thirteen keys, every time.
     */
    @Test
    fun aDefaultProfileWritesAllThirteenKeys() {
        val obj = Json.parseToJsonElement(TasteProfile.encode(TasteProfile())).jsonObject
        assertEquals(13, obj.size)
        allKeys.forEach { key -> assertTrue(key, obj.containsKey(key)) }
    }

    /**
     * TRAP 2, same consequence, different cause (found porting to Go): the six list
     * properties are `"type": "array"` and Swift decodes them into non-optional
     * lists, so a JSON `null` fails the decode exactly as a missing key does. A
     * zero-value struct is the case that gets this wrong, so that is the case tested.
     */
    @Test
    fun aDefaultProfileEmitsEmptyArraysNeverNull() {
        val blob = TasteProfile.encode(TasteProfile())
        val obj = Json.parseToJsonElement(blob).jsonObject
        listKeys.forEach { key ->
            val value = obj[key]
            assertTrue("$key must be a JSON array", value is JsonArray)
            assertEquals("$key must be empty, not null", 0, (value as JsonArray).size)
        }
        assertFalse("no list may serialize as null", blob.contains("null"))
    }

    @Test
    fun aDefaultProfileRoundTrips() {
        val profile = TasteProfile()
        assertEquals(profile, TasteProfile.decode(TasteProfile.encode(profile)))
    }

    @Test
    fun aFullProfileRoundTrips() {
        val profile = TasteProfile(
            favoriteFoods = listOf("Pizza", "Tacos"),
            favoriteFoodsOther = "ramen",
            dislikedFoods = listOf("Liver"),
            dislikedFoodsOther = "okra",
            cuisinesLoved = listOf("Italian"),
            cuisinesLovedOther = "Levantine",
            cuisinesAvoided = listOf("Fast food"),
            cuisinesAvoidedOther = "buffet",
            allergies = listOf("Peanuts", "Shellfish"),
            allergiesOther = "kiwi",
            texturesAvoided = listOf("Mushy"),
            texturesAvoidedOther = "slimy",
            spiceLevel = 5,
        )
        assertEquals(profile, TasteProfile.decode(TasteProfile.encode(profile)))
    }

    /** Reading is deliberately more forgiving than iOS; writing is not. */
    @Test
    fun decodingToleratesAMissingKey() {
        val decoded = TasteProfile.decode("""{"allergies":["Peanuts"]}""")
        assertEquals(listOf("Peanuts"), decoded?.allergies)
        assertEquals(TasteProfile.DEFAULT_SPICE_LEVEL, decoded?.spiceLevel)
    }

    @Test
    fun decodingGarbageReturnsNullRatherThanThrowing() {
        assertEquals(null, TasteProfile.decode("not json"))
        assertEquals(null, TasteProfile.decode(""))
    }

    @Test
    fun theStorageKeyMatchesIos() {
        assertEquals("tasteProfile", TasteProfile.STORAGE_KEY)
    }
}

/** hasContent must cover all twelve fields; fewer is the logged safety bug. */
class TasteProfileHasContentTest {

    @Test
    fun everyPillListOnItsOwnCountsAsContent() {
        assertTrue(TasteProfile(favoriteFoods = listOf("x")).hasContent)
        assertTrue(TasteProfile(dislikedFoods = listOf("x")).hasContent)
        assertTrue(TasteProfile(cuisinesLoved = listOf("x")).hasContent)
        assertTrue(TasteProfile(cuisinesAvoided = listOf("x")).hasContent)
        assertTrue(TasteProfile(allergies = listOf("x")).hasContent)
        assertTrue(TasteProfile(texturesAvoided = listOf("x")).hasContent)
    }

    @Test
    fun everyOtherFieldOnItsOwnCountsAsContent() {
        assertTrue(TasteProfile(favoriteFoodsOther = "x").hasContent)
        assertTrue(TasteProfile(dislikedFoodsOther = "x").hasContent)
        assertTrue(TasteProfile(cuisinesLovedOther = "x").hasContent)
        assertTrue(TasteProfile(cuisinesAvoidedOther = "x").hasContent)
        assertTrue(TasteProfile(allergiesOther = "x").hasContent)
        assertTrue(TasteProfile(texturesAvoidedOther = "x").hasContent)
    }

    @Test
    fun noSpiceLevelCountsAsContentOnItsOwn() {
        (1..5).forEach { level ->
            assertFalse("spice $level", TasteProfile(spiceLevel = level).hasContent)
        }
    }
}

/** The spice line is always emitted, and out-of-range values degrade, never throw. */
class TasteProfileSpiceTest {

    private fun line(level: Int) = TasteProfile(spiceLevel = level).promptFragment()

    @Test
    fun eachLevelHasItsOwnWording() {
        assertTrue(line(1).contains("Spice preference: very mild, no spice at all."))
        assertTrue(line(2).contains("Spice preference: mild, just a hint of spice."))
        assertTrue(line(3).contains("Spice preference: medium spice."))
        assertTrue(line(4).contains("Spice preference: spicy."))
        assertTrue(line(5).contains("Spice preference: very spicy, the hotter the better."))
    }

    @Test
    fun anythingOutsideOneToFiveFallsBackToMediumSpice() {
        listOf(0, -1, 6, 99, Int.MIN_VALUE, Int.MAX_VALUE).forEach { level ->
            assertTrue("spice $level", line(level).contains("Spice preference: medium spice."))
        }
    }

    @Test
    fun anEmptyProfileIsExactlyTheSpiceLine() {
        assertEquals("Spice preference: medium spice.", TasteProfile().promptFragment())
    }

    /** Lines join with \n, in declaration order, with the spice line last. */
    @Test
    fun linesAreJoinedWithNewlinesInOrder() {
        val fragment = TasteProfile(
            favoriteFoods = listOf("Pizza"),
            dislikedFoods = listOf("Liver"),
            cuisinesLoved = listOf("Italian"),
            cuisinesAvoided = listOf("Fast food"),
            allergies = listOf("Peanuts"),
            texturesAvoided = listOf("Mushy"),
            spiceLevel = 4,
        ).promptFragment()
        assertEquals(
            listOf(
                "Favorite foods: Pizza.",
                "Foods the user dislikes: Liver. Avoid these.",
                "Cuisines they enjoy: Italian. Use these as flavor inspiration.",
                "Cuisines they avoid: Fast food. Avoid those styles.",
                "Allergies: Peanuts. Never include these.",
                "Textures/flavors they avoid: Mushy.",
                "Spice preference: spicy.",
            ),
            fragment.split("\n"),
        )
    }
}
