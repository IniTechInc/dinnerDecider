package com.philwoolley.dinnerdecider.contract

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Line-for-line port of `DinnerDeciderTests/RecipePromptTests.swift` (8 tests),
 * Swift names preserved.
 *
 * The taste-profile case uses a local test double for [TastePromptSource]
 * because the real `TasteProfile` is being written in a parallel worktree (plan
 * Step 13) and carries its own 9-test suite for `promptFragment()`. The double
 * emits exactly what the real allergies-only profile does, which is all this
 * file is asserting about.
 */
class RecipePromptTest {

    private fun prefs(
        diet: String = RecipePrompt.DIET_NONE,
        allergies: String = "",
        cuisines: String = "",
        household: Int = 2,
    ) = RecipePromptPreferences(
        diet = diet,
        allergies = allergies,
        cuisines = cuisines,
        householdSize = household,
    )

    /**
     * Stands in for `TasteProfile`. `fragment` is the verbatim block the real
     * `promptFragment()` produces for a profile whose only content is one
     * allergy, including the always-emitted spice line.
     */
    private class FakeTaste(
        override val hasContent: Boolean,
        private val fragment: String,
    ) : TastePromptSource {
        override fun promptFragment(): String = fragment
    }

    private fun allergiesOnlyTaste() = FakeTaste(
        hasContent = true,
        fragment = "Allergies: Peanuts. Never include these.\nSpice preference: medium spice.",
    )

    @Test
    fun testPromptListsInventoryAsCommaList() {
        val prompt = RecipePrompt.build(itemNames = listOf("Eggs", "Milk", "Cheese"), prefs = prefs())
        assertTrue(prompt.contains("Inventory: Eggs, Milk, Cheese."))
    }

    @Test
    fun testPromptIncludesPortionScalingForHousehold() {
        val prompt = RecipePrompt.build(itemNames = listOf("Eggs"), prefs = prefs(household = 4))
        assertTrue(prompt.contains("serve 4"))
    }

    @Test
    fun testPromptOmitsDietWhenNone() {
        val prompt = RecipePrompt.build(itemNames = listOf("Eggs"), prefs = prefs())
        assertFalse(prompt.contains("Diet:"))
    }

    @Test
    fun testPromptIncludesDietAllergiesAndCuisines() {
        val prompt = RecipePrompt.build(
            itemNames = listOf("Eggs"),
            prefs = prefs(diet = "vegetarian", allergies = "peanuts", cuisines = "Thai"),
        )
        assertTrue(prompt.contains("Diet: vegetarian."))
        assertTrue(prompt.contains("Avoid (allergies): peanuts."))
        assertTrue(prompt.contains("Preferred cuisines: Thai."))
    }

    @Test
    fun testPromptAlwaysDemandsJSON() {
        val prompt = RecipePrompt.build(itemNames = listOf("Eggs"), prefs = prefs())
        assertTrue(prompt.contains("Respond ONLY with JSON"))
        assertTrue(prompt.contains("makeNow"))
        assertTrue(prompt.contains("almostThere"))
    }

    /**
     * House style. Asserted for **any** combination of preferences: keep
     * semicolons and periods, never introduce a U+2014 em dash into prompt text.
     */
    @Test
    fun testPromptNeverContainsEmDash() {
        val prompt = RecipePrompt.build(
            itemNames = listOf("Eggs"),
            prefs = prefs(diet = "keto", allergies = "shellfish", cuisines = "Italian", household = 3),
        )
        assertFalse(prompt.contains("\u2014"))

        // Every conditional line switched on at once, plus a mood and a taste
        // fragment, so no combination can smuggle one in.
        val everything = RecipePrompt.build(
            itemNames = listOf("Eggs", "Milk"),
            prefs = prefs(diet = "glutenFree", allergies = "peanuts", cuisines = "Thai", household = 1),
            mood = "something warm",
            tasteProfile = allergiesOnlyTaste(),
        )
        assertFalse(everything.contains("\u2014"))
    }

    /**
     * Field feedback: inventory-only generation produced fake dishes ("cheesy
     * Italian dip" from ketchup + cheddar). The prompt must demand real dishes
     * with genuine ingredients and honest hasIt marking against the inventory.
     *
     * The negative assertion is the guard against someone "helpfully" restoring
     * the old inventory-only constraint. Port it too.
     */
    @Test
    fun testPromptDemandsRealDishesWithHonestInventoryMarking() {
        val prompt = RecipePrompt.build(itemNames = listOf("Eggs"), prefs = prefs())
        assertTrue(prompt.contains("real, well-known dishes"))
        assertTrue(prompt.contains("Never invent a dish"))
        assertTrue(prompt.contains("hasIt true only if it is in the inventory"))
        assertFalse(prompt.contains("uses only inventory items"))
    }

    /**
     * An allergies-only taste profile must reach the model, and the taste
     * fragment must sit before the JSON schema instruction so it reads as
     * context, not an afterthought.
     */
    @Test
    fun testPromptIncludesTasteProfileAllergyLineBeforeSchema() {
        val prompt = RecipePrompt.build(
            itemNames = listOf("Eggs"),
            prefs = prefs(),
            tasteProfile = allergiesOnlyTaste(),
        )
        assertTrue(prompt.contains("Allergies: Peanuts. Never include these."))

        val allergyIndex = prompt.indexOf("Allergies: Peanuts.")
        val schemaIndex = prompt.indexOf("Respond ONLY with JSON")
        assertTrue("Prompt missing allergy or schema lines", allergyIndex >= 0 && schemaIndex >= 0)
        assertTrue(allergyIndex < schemaIndex)
        // The real-dishes and schema guardrails must survive untouched.
        assertTrue(prompt.contains("real, well-known dishes"))
    }

    /**
     * Not in the Swift suite: the exact line ORDER and the `\n` join, asserted as
     * one whole string. `contains` checks cannot catch a line emitted in the
     * wrong place, a join point turned into a newline by a formatter, or a
     * wrapped clause — and all three are silent recognition regressions.
     */
    @Test
    fun testPromptAssemblesExactLinesInOrder() {
        val prompt = RecipePrompt.build(
            itemNames = listOf("Eggs", "Milk"),
            prefs = prefs(diet = "vegan", allergies = "peanuts", cuisines = "Thai", household = 1),
            mood = "  something warm  ",
            tasteProfile = allergiesOnlyTaste(),
        )
        val expected = listOf(
            "You are a helpful home cooking assistant.",
            "Inventory: Eggs, Milk.",
            "The user says: \"something warm\". Tailor your recipe suggestions to match this mood or craving.",
            "Allergies: Peanuts. Never include these.\nSpice preference: medium spice.",
            "Diet: vegan.",
            "Avoid (allergies): peanuts.",
            "Preferred cuisines: Thai.",
            "Cooking for 1 person; use single-serving portions.",
            "Suggest real, well-known dishes only, with their genuine standard ingredients. " +
                "Never invent a dish or force a dish to use only the inventory. " +
                "Assume salt, pepper, water and cooking oil are always on hand.",
            "Respond ONLY with JSON of the form " +
                "{\"makeNow\":[{\"name\":\"\",\"ingredients\":[{\"name\":\"\",\"hasIt\":true}]," +
                "\"steps\":[\"\"],\"timeMinutes\":0,\"missingItems\":[]}],\"almostThere\":[...]}. " +
                "For every ingredient set hasIt true only if it is in the inventory. " +
                "makeNow is for dishes whose ingredients are all in the inventory. " +
                "almostThere is for dishes missing up to 3 ingredients; list each missing one in missingItems.",
        ).joinToString(separator = "\n")
        assertEquals(expected, prompt)
    }

    /** With nothing set, only the four unconditional lines are emitted. */
    @Test
    fun testMinimalPromptIsFourLines() {
        val prompt = RecipePrompt.build(itemNames = emptyList(), prefs = prefs(household = 0))
        assertEquals(
            listOf(
                "You are a helpful home cooking assistant.",
                // An empty inventory legitimately produces "Inventory: ."
                "Inventory: .",
                "Suggest real, well-known dishes only, with their genuine standard ingredients. " +
                    "Never invent a dish or force a dish to use only the inventory. " +
                    "Assume salt, pepper, water and cooking oil are always on hand.",
                "Respond ONLY with JSON of the form " +
                    "{\"makeNow\":[{\"name\":\"\",\"ingredients\":[{\"name\":\"\",\"hasIt\":true}]," +
                    "\"steps\":[\"\"],\"timeMinutes\":0,\"missingItems\":[]}],\"almostThere\":[...]}. " +
                    "For every ingredient set hasIt true only if it is in the inventory. " +
                    "makeNow is for dishes whose ingredients are all in the inventory. " +
                    "almostThere is for dishes missing up to 3 ingredients; list each missing one in missingItems.",
            ).joinToString(separator = "\n"),
            prompt,
        )
    }
}
