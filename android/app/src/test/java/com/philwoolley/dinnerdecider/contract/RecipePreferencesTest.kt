package com.philwoolley.dinnerdecider.contract

import org.junit.Assert.assertEquals
import org.junit.Test

/** The closed sets from `Preferences.swift`, pinned by their raw values. */
class RecipePreferencesTest {

    /**
     * `glutenFree` is camelCase, unlike every `FoodCategory` value. That is shipped
     * data, not a typo to regularise.
     */
    @Test
    fun dietPreferenceRawValuesMatchIos() {
        assertEquals(
            listOf("none", "vegetarian", "vegan", "pescatarian", "keto", "glutenFree"),
            DietPreference.entries.map { it.raw },
        )
        assertEquals("glutenFree", DietPreference.GLUTEN_FREE.raw)
    }

    @Test
    fun dietPreferenceDisplayNamesMatchIos() {
        assertEquals(
            listOf("No preference", "Vegetarian", "Vegan", "Pescatarian", "Keto", "Gluten free"),
            DietPreference.entries.map { it.displayName },
        )
    }

    @Test
    fun dietPreferenceFallsBackToNone() {
        assertEquals(DietPreference.KETO, DietPreference.fromStorage("keto"))
        assertEquals(DietPreference.NONE, DietPreference.fromStorage("paleo"))
        // Canonical values only: storage is not an untrusted boundary.
        assertEquals(DietPreference.NONE, DietPreference.fromStorage("GlutenFree"))
    }

    @Test
    fun inventorySortRawValuesMatchIos() {
        assertEquals(
            listOf("category", "dateAdded", "name"),
            InventorySort.entries.map { it.raw },
        )
        assertEquals(
            listOf("Category", "Date added", "Name"),
            InventorySort.entries.map { it.displayName },
        )
    }

    @Test
    fun inventorySortFallsBackToCategory() {
        assertEquals(InventorySort.NAME, InventorySort.fromStorage("name"))
        assertEquals(InventorySort.CATEGORY, InventorySort.fromStorage("whatever"))
    }

    @Test
    fun defaultHouseholdSizeIsTwo() {
        assertEquals(2, PrefKey.DEFAULT_HOUSEHOLD_SIZE)
    }

    @Test
    fun prefKeysMatchTheIosDefaultsKeys() {
        assertEquals("dietPreference", PrefKey.DIET)
        assertEquals("allergies", PrefKey.ALLERGIES)
        assertEquals("cuisineLikes", PrefKey.CUISINE_LIKES)
        assertEquals("householdSize", PrefKey.HOUSEHOLD_SIZE)
        assertEquals("hasSeenModelSetup", PrefKey.HAS_SEEN_MODEL_SETUP)
        assertEquals("hasSeenOnboarding", PrefKey.HAS_SEEN_ONBOARDING)
        assertEquals("inventorySortOrder", PrefKey.INVENTORY_SORT)
        assertEquals("debugSimulateRecipeFailure", PrefKey.DEBUG_SIMULATE_FAILURE)
        assertEquals("hasCompletedTasteProfile", PrefKey.HAS_COMPLETED_TASTE_PROFILE)
    }

    @Test
    fun unsetPreferencesFallBackTheSameWayAsIos() {
        val prefs = RecipePreferences.fromStored()
        assertEquals("none", prefs.diet)
        assertEquals("", prefs.allergies)
        assertEquals("", prefs.cuisines)
        assertEquals(2, prefs.householdSize)
    }

    /** A stored 0 is the "never set" marker, not a household of nobody. */
    @Test
    fun aZeroHouseholdSizeBecomesTheDefault() {
        assertEquals(2, RecipePreferences.fromStored(householdSize = 0).householdSize)
        assertEquals(1, RecipePreferences.fromStored(householdSize = 1).householdSize)
        assertEquals(6, RecipePreferences.fromStored(householdSize = 6).householdSize)
    }

    @Test
    fun storedValuesAreCarriedThroughUnchanged() {
        val prefs = RecipePreferences.fromStored(
            diet = "vegan",
            allergies = "Peanuts, Shellfish",
            cuisines = "Italian",
            householdSize = 4,
        )
        assertEquals(RecipePreferences("vegan", "Peanuts, Shellfish", "Italian", 4), prefs)
    }
}
