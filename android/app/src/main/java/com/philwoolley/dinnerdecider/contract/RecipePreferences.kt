package com.philwoolley.dinnerdecider.contract

/**
 * Diet options offered on the Settings screen.
 *
 * Port of `DinnerDecider/Services/Preferences.swift:4-24`.
 *
 * The **raw string is the contract**, exactly as for [FoodCategory]: it is what is
 * persisted and what the prompt builder reads. Note that [GLUTEN_FREE] is
 * `glutenFree` — **camelCase, unlike every `FoodCategory` value**, which are all
 * lower-case single words. That inconsistency is shipped data; do not regularise
 * it to `gluten_free` or `glutenfree`.
 *
 * [NONE] is a sentinel, not merely a default: it **omits** the `Diet:` line from
 * the recipe prompt entirely rather than emitting "Diet: none".
 */
enum class DietPreference(val raw: String, val displayName: String) {
    NONE("none", "No preference"),
    VEGETARIAN("vegetarian", "Vegetarian"),
    VEGAN("vegan", "Vegan"),
    PESCATARIAN("pescatarian", "Pescatarian"),
    KETO("keto", "Keto"),
    GLUTEN_FREE("glutenFree", "Gluten free"),
    ;

    companion object {
        /** Stored values are canonical, so this matches exactly (no lower-casing). */
        fun fromStorage(value: String): DietPreference =
            entries.firstOrNull { it.raw == value } ?: NONE
    }
}

/**
 * How the inventory list is ordered. Port of `Preferences.swift:27-47`.
 *
 * Raw values `category`, `dateAdded`, `name` — `dateAdded` is camelCase for the
 * same reason [DietPreference.GLUTEN_FREE] is.
 */
enum class InventorySort(val raw: String, val displayName: String) {
    CATEGORY("category", "Category"),
    DATE_ADDED("dateAdded", "Date added"),
    NAME("name", "Name"),
    ;

    companion object {
        fun fromStorage(value: String): InventorySort =
            entries.firstOrNull { it.raw == value } ?: CATEGORY
    }
}

/**
 * Preference keys shared between Settings, onboarding and the prompt builder.
 *
 * Port of `Preferences.swift:50-70`. The strings are the on-disk key names and are
 * kept identical to iOS's `UserDefaults` keys so the two ports describe the same
 * user state; the storage mechanism behind them (DataStore here) is a platform
 * detail.
 */
object PrefKey {
    const val DIET: String = "dietPreference"
    const val ALLERGIES: String = "allergies"
    const val CUISINE_LIKES: String = "cuisineLikes"
    const val HOUSEHOLD_SIZE: String = "householdSize"
    const val HAS_SEEN_MODEL_SETUP: String = "hasSeenModelSetup"
    const val HAS_SEEN_ONBOARDING: String = "hasSeenOnboarding"
    const val INVENTORY_SORT: String = "inventorySortOrder"

    /**
     * Hidden debug switch that forces a malformed recipe response so the error UI
     * can be demonstrated. Off in normal use.
     */
    const val DEBUG_SIMULATE_FAILURE: String = "debugSimulateRecipeFailure"

    /** True once the user has completed (or skipped) the taste profile wizard. */
    const val HAS_COMPLETED_TASTE_PROFILE: String = "hasCompletedTasteProfile"

    // Model selection uses ModelFileSelection.SELECTED_MODEL_KEY.

    /**
     * Default number of people to cook for when the user has not set a size.
     *
     * Zero is the "unset" marker on read, not a real household of nobody, so a
     * stored 0 maps to this rather than being used literally.
     */
    const val DEFAULT_HOUSEHOLD_SIZE: Int = 2
}

/**
 * A snapshot of the preferences that shape a recipe prompt.
 *
 * Port of `Preferences.swift:74-90`. A pure value type so the prompt builder is
 * fully testable without touching any store — the reading of those keys belongs to
 * the preferences store, not here.
 */
data class RecipePreferences(
    /** A [DietPreference.raw] value; `none` omits the Diet line entirely. */
    val diet: String = DietPreference.NONE.raw,
    /** Free text, comma-separated by convention. */
    val allergies: String = "",
    /** Free text, comma-separated by convention. */
    val cuisines: String = "",
    val householdSize: Int = PrefKey.DEFAULT_HOUSEHOLD_SIZE,
) {
    companion object {
        /**
         * Build from raw stored values, applying the same fallbacks as
         * `RecipePreferences.current(_:)`: absent strings become empty, and a
         * household size of 0 (the "never set" value) becomes
         * [PrefKey.DEFAULT_HOUSEHOLD_SIZE].
         */
        fun fromStored(
            diet: String? = null,
            allergies: String? = null,
            cuisines: String? = null,
            householdSize: Int = 0,
        ): RecipePreferences = RecipePreferences(
            diet = diet ?: DietPreference.NONE.raw,
            allergies = allergies ?: "",
            cuisines = cuisines ?: "",
            householdSize = if (householdSize == 0) PrefKey.DEFAULT_HOUSEHOLD_SIZE else householdSize,
        )
    }
}
