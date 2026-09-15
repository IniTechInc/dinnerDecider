package com.philwoolley.dinnerdecider.contract

import kotlinx.serialization.Serializable

/**
 * One grocery item recognized by the vision model.
 *
 * Wire type only: this is what the model emits and what the scan pipeline
 * consumes; it is never persisted directly. Port of `LLMService.swift:7-17`
 * (`struct IdentifiedItem: Codable, Hashable, Sendable`), schema of record
 * `shared/schema/identified-item.schema.json`.
 *
 * ### Which properties carry a default, and why it is load-bearing
 * [brand] has a default of `null`, mirroring Swift's `decodeIfPresent`: an
 * **absent** key and an **explicit** `null` both produce `null`.
 *
 * [name], [category] and [confidence] deliberately carry **no** default. Swift's
 * synthesized `Decodable` has no defaults either, so a missing key fails the whole
 * decode. In Kotlin that surfaces as `MissingFieldException`, which the response
 * parser catches and turns into `null` - the same observable behavior. Adding a
 * default to any of the three would silently accept a malformed model reply and
 * break three conformance cases.
 *
 * Neither [category] nor [confidence] is validated at decode time. Any string
 * decodes for the category (mapped later via [FoodCategory.fromModelString],
 * falling back to `other`), and the confidence is a plain `Double` with no range
 * check - the pipeline, not the decoder, drops anything below 0.15.
 */
@Serializable
data class IdentifiedItem(
    /**
     * Item name as the model reports it, e.g. "Whole Milk". The literal value
     * "unknown" (compared case-insensitively) is the model's no-item escape hatch
     * and is filtered out by the pipeline.
     */
    val name: String,
    /**
     * Brand read off the packaging, or `null` when there is none or it is
     * unreadable. Absent key and explicit `null` are both valid and both land here
     * as `null`. Part of the dedupe key.
     */
    val brand: String? = null,
    /**
     * Raw category string from the model. Not validated at decode time; see
     * [FoodCategory.fromModelString].
     */
    val category: String,
    /** Model confidence, prompted as 0-1. Not range-validated at decode time. */
    val confidence: Double,
) {
    /**
     * The category string mapped onto the typed enum, falling back to
     * [FoodCategory.OTHER]. Port of `IdentifiedItem.foodCategory`.
     */
    val foodCategory: FoodCategory
        get() = FoodCategory.fromModelString(category)
}

/**
 * One ingredient row in a suggested recipe. Port of `Recipe.swift:5-9`.
 *
 * iOS makes this `Identifiable` by [name] (`id == name`), so names should be
 * unique within a recipe.
 */
@Serializable
data class IngredientLine(
    /**
     * Ingredient name. Matched against inventory by the name-only key (trimmed and
     * lower-cased) when cooking decrements stock.
     */
    val name: String,
    /**
     * True only if the ingredient is in the user's inventory. The have/missing
     * display and the add-to-shopping-list flow both depend on this being honest.
     */
    val hasIt: Boolean,
)

/**
 * One suggested dish. Port of `Recipe.swift:12-20`.
 *
 * Every property is required, exactly as in Swift, so a partial reply fails the
 * whole bundle rather than producing a half-rendered recipe card.
 */
@Serializable
data class RecipeSuggestion(
    /** Dish name. Must be a real, well-known dish (`shared/prompts/recipes.md`). */
    val name: String,
    /** The dish's genuine standard ingredients, each marked have/missing. */
    val ingredients: List<IngredientLine>,
    /** Cooking steps, in order. */
    val steps: List<String>,
    /**
     * Total time in minutes.
     *
     * **`Int`, not a floating-point type, on purpose.** Swift decodes this as `Int`,
     * so a JSON float here fails the whole bundle decode and surfaces to the user as
     * the unreadable-recipes error. A port that widens this to `Double` would accept
     * a reply iOS rejects and diverge on the `recipe-bundle-float-time` conformance
     * case.
     */
    val timeMinutes: Int,
    /**
     * Names the user would need to buy. Empty for make-now dishes. Feeds the
     * shopping-list aggregation (case-insensitive dedupe, first-seen order kept).
     */
    val missingItems: List<String>,
)

/**
 * The whole reply to the recipes prompt: two buckets of suggestions from one model
 * call. Port of `Recipe.swift:23-26`, schema of record
 * `shared/schema/recipe.schema.json`.
 */
@Serializable
data class RecipeBundleResponse(
    /** Dishes whose ingredients are all in the inventory; `missingItems` empty. */
    val makeNow: List<RecipeSuggestion>,
    /** Dishes missing up to 3 ingredients, each listed in `missingItems`. */
    val almostThere: List<RecipeSuggestion>,
)
