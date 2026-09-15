package com.philwoolley.dinnerdecider.data.entity

import androidx.room.Entity
import androidx.room.Index
import androidx.room.PrimaryKey
import com.philwoolley.dinnerdecider.contract.RecipeSuggestion
import java.time.Instant
import java.util.UUID

/**
 * A [RecipeSuggestion] saved to the meal plan and synced to the calendar.
 *
 * Mirrors `shared/schema/recipe.schema.json` `$defs/PlannedMeal` field for field,
 * which describes `DinnerDecider/Models/PlannedMeal.swift`.
 *
 * The flattening is **deliberate**, not lossy by accident: ingredients collapse
 * to names only and `hasIt` / `missingItems` are dropped, so the deep link can
 * render a planned meal without the model regenerating it. Whether the user has
 * an ingredient is a fact about *now*, not about the plan.
 *
 * `ingredientNames` and `steps` are stored as **JSON arrays** through
 * [com.philwoolley.dinnerdecider.data.Converters]. A delimiter-joined string
 * would corrupt the first step containing the delimiter, and cooking steps are
 * free text.
 */
@Entity(
    tableName = "planned_meals",
    indices = [Index(value = ["plannedDate"])],
)
data class PlannedMealEntity(
    /** Stable identity, generated at creation. Stored as the UUID's string form. */
    @PrimaryKey
    val id: String = UUID.randomUUID().toString(),

    /** Copied from [RecipeSuggestion.name]. */
    val recipeName: String,

    /**
     * [RecipeSuggestion.ingredients] mapped to their names. `hasIt` is
     * intentionally not carried over.
     */
    val ingredientNames: List<String> = emptyList(),

    /** Cooking steps in order. */
    val steps: List<String> = emptyList(),

    /** Total time in minutes. */
    val timeMinutes: Int = 0,

    /** When the meal is planned for. Epoch millis; see [InventoryItemEntity]. */
    val plannedDate: Instant,

    /**
     * Platform calendar event identifier, used to update or delete the event.
     * Null until the event is created, and platform-specific by nature — on iOS
     * this is `EKEvent.eventIdentifier`; on Android it is the `CalendarContract`
     * event id, when the user completes the `ACTION_INSERT` flow.
     */
    val calendarEventId: String? = null,
) {
    companion object {
        /**
         * Flatten a model-produced suggestion into the persisted projection.
         * Port of `PlannedMeal.init(from:date:)`.
         */
        fun from(recipe: RecipeSuggestion, date: Instant): PlannedMealEntity =
            PlannedMealEntity(
                recipeName = recipe.name,
                ingredientNames = recipe.ingredients.map { it.name },
                steps = recipe.steps,
                timeMinutes = recipe.timeMinutes,
                plannedDate = date,
            )
    }
}
