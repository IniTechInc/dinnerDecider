package com.philwoolley.dinnerdecider.data.repo

import androidx.room.Room
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import com.philwoolley.dinnerdecider.contract.FoodCategory
import com.philwoolley.dinnerdecider.contract.IngredientLine
import com.philwoolley.dinnerdecider.contract.RecipeSuggestion
import com.philwoolley.dinnerdecider.data.AppDatabase
import com.philwoolley.dinnerdecider.data.entity.InventoryItemEntity
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import java.time.Instant

/**
 * The repository mutation paths against a real database.
 *
 * **Device status: WRITTEN, NOT RUN.** Room needs an arm64 emulator, which is not
 * available here (plan 0.11 / §2). The pure derivations these classes also carry
 * are host-verified in `RepositoryDerivationsTest`.
 */
@RunWith(AndroidJUnit4::class)
class RepositoryInstrumentedTest {

    private lateinit var database: AppDatabase
    private lateinit var inventory: InventoryRepository
    private lateinit var shopping: ShoppingListRepository
    private lateinit var mealPlan: MealPlanRepository

    @Before
    fun setUp() {
        database = Room.inMemoryDatabaseBuilder(
            ApplicationProvider.getApplicationContext(),
            AppDatabase::class.java,
        ).build()
        inventory = InventoryRepository(database.inventoryDao())
        shopping = ShoppingListRepository(database.shoppingListDao())
        mealPlan = MealPlanRepository(database.plannedMealDao())
    }

    @After
    fun tearDown() = database.close()

    // MARK: - Confirm

    @Test
    fun confirmingNewSightingsInsertsThem() = runBlocking {
        inventory.applyConfirmed(
            listOf(
                InventoryRepository.ConfirmedItem("Whole Milk", "Horizon", FoodCategory.DAIRY, 2),
                InventoryRepository.ConfirmedItem("Eggs", null, FoodCategory.DAIRY, 1),
            ),
        )
        val rows = inventory.observeItems().first()
        assertEquals(2, rows.size)
        assertEquals(2, rows.first { it.name == "Whole Milk" }.quantity)
    }

    @Test
    fun confirmingAKeyAlreadyInStockIncrementsRatherThanDuplicating() = runBlocking {
        inventory.add(
            InventoryItemEntity(name = "Milk", brand = "Horizon", quantity = 1, categoryRaw = "dairy"),
        )
        inventory.applyConfirmed(
            listOf(InventoryRepository.ConfirmedItem("milk", "HORIZON", FoodCategory.DAIRY, 3)),
        )
        val rows = inventory.observeItems().first()
        assertEquals(1, rows.size)
        assertEquals(4, rows.single().quantity)
        // Case is part of the key computation, not of the stored row.
        assertEquals("Milk", rows.single().name)
    }

    /**
     * `testConfirmStacksRepeatedKeysAcrossTheBatch`, through the store. Two
     * confirmed entries with the same key yield INSERT(1) then INCREMENT(2): the
     * running tally spans the batch. Collapsing them would end at 1.
     */
    @Test
    fun repeatedKeysInOneBatchStack() = runBlocking {
        inventory.applyConfirmed(
            listOf(
                InventoryRepository.ConfirmedItem("Milk", null, FoodCategory.DAIRY, 1),
                InventoryRepository.ConfirmedItem("Milk", null, FoodCategory.DAIRY, 1),
            ),
        )
        val rows = inventory.observeItems().first()
        assertEquals(1, rows.size)
        assertEquals(2, rows.single().quantity)
    }

    @Test
    fun brandIsPartOfIdentitySoTwoBrandsAreTwoRows() = runBlocking {
        inventory.applyConfirmed(
            listOf(
                InventoryRepository.ConfirmedItem("Milk", "Horizon", FoodCategory.DAIRY, 1),
                InventoryRepository.ConfirmedItem("Milk", "Fairlife", FoodCategory.DAIRY, 1),
            ),
        )
        assertEquals(2, inventory.observeItems().first().size)
    }

    // MARK: - Cook

    @Test
    fun cookingDecrementsOnePerUseAndRemovesRowsThatReachZero() = runBlocking {
        inventory.add(InventoryItemEntity(name = "Eggs", quantity = 3))
        inventory.add(InventoryItemEntity(name = "Milk", quantity = 1))

        val removed = inventory.applyCooked(listOf("eggs", " Milk "))

        val rows = inventory.observeItems().first()
        assertEquals(listOf("Eggs"), rows.map { it.name })
        assertEquals(2, rows.single().quantity)
        assertEquals(listOf("milk"), removed)
    }

    @Test
    fun aRepeatedIngredientDecrementsRepeatedly() = runBlocking {
        inventory.add(InventoryItemEntity(name = "Eggs", quantity = 3))
        inventory.applyCooked(listOf("Eggs", "Eggs"))
        assertEquals(1, inventory.observeItems().first().single().quantity)
    }

    @Test
    fun ingredientsNotInStockAreIgnoredSilently() = runBlocking {
        inventory.add(InventoryItemEntity(name = "Eggs", quantity = 2))
        val removed = inventory.applyCooked(listOf("saffron", "truffle"))
        assertTrue(removed.isEmpty())
        assertEquals(2, inventory.observeItems().first().single().quantity)
    }

    @Test
    fun rowsNotUsedAreUntouched() = runBlocking {
        inventory.add(InventoryItemEntity(name = "Eggs", quantity = 2))
        inventory.add(InventoryItemEntity(name = "Flour", quantity = 5))
        inventory.applyCooked(listOf("Eggs"))
        val flour = inventory.observeItems().first().first { it.name == "Flour" }
        assertEquals(5, flour.quantity)
    }

    // MARK: - Shopping list

    @Test
    fun addingNamesSkipsDuplicatesCaseInsensitivelyAndKeepsFirstSeenCasing() = runBlocking {
        shopping.addNames(listOf("Milk", "  Bread  ", "milk", "", "   "))
        assertEquals(listOf("Milk", "Bread"), shopping.observeItems().first().map { it.name })

        val added = shopping.addNames(listOf("MILK", "Butter"))
        assertEquals(listOf("Butter"), added)
        assertEquals(listOf("Milk", "Bread", "Butter"), shopping.observeItems().first().map { it.name })
    }

    @Test
    fun aBlankManualEntryIsNeverStored() = runBlocking {
        assertFalse(shopping.addManual("   "))
        assertTrue(shopping.observeItems().first().isEmpty())
    }

    @Test
    fun aBatchKeepsItsOrderOnDisk() = runBlocking {
        shopping.addNames(listOf("First", "Second", "Third"), now = Instant.ofEpochMilli(1_000))
        assertEquals(
            listOf("First", "Second", "Third"),
            shopping.observeItems().first().map { it.name },
        )
    }

    @Test
    fun clearCheckedRemovesOnlyCheckedLinesAndTheExportTracksIt() = runBlocking {
        shopping.addNames(listOf("Milk", "Bread"))
        val milk = shopping.observeItems().first().first { it.name == "Milk" }
        shopping.setChecked(milk.id, true)

        assertEquals("Shopping List\n\n[x] Milk\n[ ] Bread", shopping.exportText())
        shopping.clearChecked()
        assertEquals("Shopping List\n\n[ ] Bread", shopping.exportText())
    }

    @Test
    fun anEmptyListExportsTheContractualString() = runBlocking {
        assertEquals("Shopping List\n\n(empty)", shopping.exportText())
    }

    // MARK: - Meal plan

    @Test
    fun planningFlattensTheSuggestionAndDropsHasIt() = runBlocking {
        val recipe = RecipeSuggestion(
            name = "Shakshuka",
            ingredients = listOf(
                IngredientLine("Eggs", hasIt = true),
                IngredientLine("Harissa", hasIt = false),
            ),
            steps = listOf("Simmer the sauce", "Poach the eggs"),
            timeMinutes = 30,
            missingItems = listOf("Harissa"),
        )
        val meal = mealPlan.plan(recipe, Instant.ofEpochMilli(2_000))

        val stored = mealPlan.getMeal(meal.id)!!
        assertEquals(listOf("Eggs", "Harissa"), stored.ingredientNames)
        assertEquals(listOf("Simmer the sauce", "Poach the eggs"), stored.steps)
        assertEquals(30, stored.timeMinutes)
        assertNull(stored.calendarEventId)
    }

    @Test
    fun upcomingFiltersOutPastMealsButObserveMealsDoesNot() = runBlocking {
        val recipe = RecipeSuggestion("Soup", emptyList(), emptyList(), 20, emptyList())
        mealPlan.plan(recipe, Instant.ofEpochMilli(1_000))
        mealPlan.plan(recipe, Instant.ofEpochMilli(5_000))

        assertEquals(2, mealPlan.observeMeals().first().size)
        assertEquals(1, mealPlan.observeUpcoming(Instant.ofEpochMilli(3_000)).first().size)
    }

    @Test
    fun theCalendarEventIdIsSetAndClearedIndependentlyOfTheMeal() = runBlocking {
        val meal = mealPlan.plan(
            RecipeSuggestion("Curry", emptyList(), emptyList(), 45, emptyList()),
            Instant.ofEpochMilli(1_000),
        )
        mealPlan.setCalendarEventId(meal.id, "event-7")
        assertEquals("event-7", mealPlan.getMeal(meal.id)!!.calendarEventId)

        mealPlan.setCalendarEventId(meal.id, null)
        assertNull(mealPlan.getMeal(meal.id)!!.calendarEventId)
        assertNotNull(mealPlan.getMeal(meal.id))
    }

    @Test
    fun reschedulingMovesTheMealWithoutLosingTheRecipe() = runBlocking {
        val meal = mealPlan.plan(
            RecipeSuggestion("Tacos", emptyList(), listOf("Warm the tortillas"), 15, emptyList()),
            Instant.ofEpochMilli(1_000),
        )
        mealPlan.reschedule(meal.id, Instant.ofEpochMilli(9_000))

        val moved = mealPlan.getMeal(meal.id)!!
        assertEquals(Instant.ofEpochMilli(9_000), moved.plannedDate)
        assertEquals(listOf("Warm the tortillas"), moved.steps)
    }
}
