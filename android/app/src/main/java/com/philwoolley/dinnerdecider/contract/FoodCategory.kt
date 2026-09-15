package com.philwoolley.dinnerdecider.contract

/**
 * The nine food-category buckets used across the app.
 *
 * Port of `DinnerDecider/Models/InventoryItem.swift:7-48`
 * (`enum FoodCategory: String, Codable, CaseIterable, Identifiable, Sendable`).
 * Contract of record: `shared/taxonomy/categories.md`.
 *
 * **The raw string is the wire and storage form.** Never serialize the ordinal or
 * the Kotlin enum-constant name: `categoryRaw` is a `String` column and
 * `IdentifiedItem.category` is a plain `String` on the wire, so a reordered enum
 * must not be able to corrupt persisted data.
 *
 * Declaration order below is the iOS `CaseIterable` order, which is also the order
 * the UI iterates.
 *
 * ### Raw values are singular; three display names are plural
 * `snack` -> "Snacks", `beverage` -> "Beverages", `condiment` -> "Condiments".
 * That disagreement is deliberate and must not be "regularised": the raw value is
 * persisted data, the display name is UI copy, and they are allowed to differ.
 *
 * ### The fallback is silent, so the strings must be copied literally
 * Anything unrecognized maps to [OTHER] without throwing and without dropping the
 * item. A port with a typo in one value (say `beverages`) fails no parsing test  - 
 * it simply files every drink under "Other" forever. The nine strings here are
 * copied verbatim from `shared/taxonomy/categories.md`.
 */
enum class FoodCategory(
    /** The exact wire/storage string. Serialize this, never the ordinal. */
    val raw: String,
    /** UI copy. Deliberately plural for [SNACK], [BEVERAGE] and [CONDIMENT]. */
    val displayName: String,
) {
    PRODUCE("produce", "Produce"),
    DAIRY("dairy", "Dairy"),
    MEAT("meat", "Meat"),
    PANTRY("pantry", "Pantry"),
    SNACK("snack", "Snacks"),
    BEVERAGE("beverage", "Beverages"),
    CONDIMENT("condiment", "Condiments"),
    FROZEN("frozen", "Frozen"),
    OTHER("other", "Other"),
    ;

    companion object {

        /**
         * Map a **model-produced** category string onto the enum.
         *
         * Lower-cases first, then matches exactly - no trimming, no fuzzy matching,
         * no synonym table (`shared/taxonomy/categories.md` rule 2: "Do not add
         * any"). Unrecognized input falls back to [OTHER]; it never throws and never
         * drops the item, because an unexpected category is a display concern, not a
         * validation failure.
         *
         * Port of `LLMService.swift:14-16`:
         * `FoodCategory(rawValue: category.lowercased()) ?? .other`.
         *
         * The lower-casing is here and **only** here because this is the untrusted
         * boundary: the model frequently answers `Dairy` or `PRODUCE`. See
         * [fromStorage] for the other half of the asymmetry.
         */
        fun fromModelString(value: String): FoodCategory {
            val normalized = value.lowercase()
            return entries.firstOrNull { it.raw == normalized } ?: OTHER
        }

        /**
         * Map a **stored** category string onto the enum.
         *
         * Deliberately does **not** lower-case: the value it reads was written by
         * [raw] itself and is already canonical (`shared/taxonomy/categories.md`
         * rule 4 - "Keep the normalization at the boundary where untrusted model
         * text enters, not deeper", and rule 5 - always persist `foodCategory.raw`,
         * never the model's original string).
         *
         * Port of `InventoryItem.swift:65-68`:
         * `FoodCategory(rawValue: categoryRaw) ?? .other`.
         *
         * The asymmetry with [fromModelString] is invisible until a fine-tuned model
         * emits `Dairy` instead of `dairy`, so it is pinned by an explicit test.
         */
        fun fromStorage(value: String): FoodCategory =
            entries.firstOrNull { it.raw == value } ?: OTHER
    }
}
