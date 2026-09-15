package com.philwoolley.dinnerdecider.data.repo

import com.philwoolley.dinnerdecider.contract.RecipeSuggestion
import com.philwoolley.dinnerdecider.data.dao.PlannedMealDao
import com.philwoolley.dinnerdecider.data.entity.PlannedMealEntity
import kotlinx.coroutines.flow.Flow
import java.time.Instant

/**
 * The meal-plan aggregate.
 *
 * Saving a suggestion **flattens** it (`PlannedMeal.init(from:date:)`):
 * ingredients collapse to names and `hasIt` / `missingItems` are dropped. That is
 * deliberate — whether the user has an ingredient is a fact about *now*, not
 * about the plan, and the deep link must be able to render a saved meal without
 * the model regenerating it.
 *
 * The calendar event id is stored but never created here. On Android the meal is
 * added through an `ACTION_INSERT` intent so the app needs no `WRITE_CALENDAR`
 * permission, which means the id only exists if the user completed that flow;
 * [setCalendarEventId] is how it gets back, and null clears it.
 */
class MealPlanRepository(private val dao: PlannedMealDao) {

    fun observeMeals(): Flow<List<PlannedMealEntity>> = dao.observeAll()

    /** Meals at or after [from], soonest first. Defaults to upcoming only. */
    fun observeUpcoming(from: Instant = Instant.now()): Flow<List<PlannedMealEntity>> =
        dao.observeFrom(from.toEpochMilli())

    /** Resolves `dinnerdecider://meal/<id>`. */
    fun observeMeal(id: String): Flow<PlannedMealEntity?> = dao.observeById(id)

    suspend fun getMeal(id: String): PlannedMealEntity? = dao.getById(id)

    /**
     * Save a suggestion to the plan, returning the stored row so the caller has
     * its identity for the calendar intent and the deep link.
     */
    suspend fun plan(recipe: RecipeSuggestion, date: Instant): PlannedMealEntity {
        val meal = PlannedMealEntity.from(recipe, date)
        dao.upsert(meal)
        return meal
    }

    suspend fun save(meal: PlannedMealEntity) = dao.upsert(meal)

    suspend fun reschedule(id: String, date: Instant) {
        val meal = dao.getById(id) ?: return
        dao.update(meal.copy(plannedDate = date))
    }

    /** Null clears the link, which is what deleting the calendar event means. */
    suspend fun setCalendarEventId(id: String, calendarEventId: String?) =
        dao.setCalendarEventId(id, calendarEventId)

    suspend fun delete(id: String) = dao.deleteById(id)

    suspend fun delete(meal: PlannedMealEntity) = dao.delete(meal)

    suspend fun deleteAll() = dao.deleteAll()

    companion object {
        /**
         * Format a planned meal for calendar event notes. Port of
         * `PlannedMeal.calendarNotes` (`PlannedMeal.swift:33-47`), including the
         * hourglass, the blank lines and the 1-based step numbering.
         */
        fun calendarNotes(meal: PlannedMealEntity): String {
            val lines = mutableListOf<String>()
            lines.add("⏱ ${meal.timeMinutes} min")
            lines.add("")
            lines.add("Ingredients:")
            meal.ingredientNames.forEach { lines.add("• $it") }
            lines.add("")
            lines.add("Steps:")
            meal.steps.forEachIndexed { index, step -> lines.add("${index + 1}. $step") }
            return lines.joinToString("\n")
        }
    }
}
