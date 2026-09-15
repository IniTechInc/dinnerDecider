package com.philwoolley.dinnerdecider.data

import androidx.room.Room
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import com.philwoolley.dinnerdecider.contract.FoodCategory
import com.philwoolley.dinnerdecider.data.entity.InventoryItemEntity
import com.philwoolley.dinnerdecider.data.entity.PlannedMealEntity
import com.philwoolley.dinnerdecider.data.entity.ShoppingListItemEntity
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
 * Room entities, converters and DAOs against a real SQLite database.
 *
 * **Device status: WRITTEN, NOT RUN.** Room needs an Android runtime and an arm64
 * emulator, which is not available here (plan 0.11 / §2).
 */
@RunWith(AndroidJUnit4::class)
class AppDatabaseInstrumentedTest {

    private lateinit var database: AppDatabase

    @Before
    fun setUp() {
        database = Room.inMemoryDatabaseBuilder(
            ApplicationProvider.getApplicationContext(),
            AppDatabase::class.java,
        ).build()
    }

    @After
    fun tearDown() {
        database.close()
    }

    // MARK: - Inventory

    @Test
    fun theCategoryIsStoredAndReadBackAsItsRawString() = runBlocking {
        val dao = database.inventoryDao()
        dao.insert(
            InventoryItemEntity(
                name = "Whole Milk",
                brand = "Horizon",
                categoryRaw = FoodCategory.DAIRY.raw,
            ),
        )
        val stored = dao.getAll().single()
        assertEquals("dairy", stored.categoryRaw)
        assertEquals(FoodCategory.DAIRY, stored.category)
    }

    @Test
    fun anUnrecognizedCategoryStringFallsBackToOtherWithoutLosingTheStoredValue() = runBlocking {
        val dao = database.inventoryDao()
        dao.insert(InventoryItemEntity(name = "Mystery", categoryRaw = "charcuterie"))
        val stored = dao.getAll().single()
        // The raw string survives: a future release that adds the case recovers it.
        assertEquals("charcuterie", stored.categoryRaw)
        assertEquals(FoodCategory.OTHER, stored.category)
    }

    @Test
    fun isStapleDefaultsToFalse() = runBlocking {
        val dao = database.inventoryDao()
        dao.insert(InventoryItemEntity(name = "Olive Oil"))
        assertFalse(dao.getAll().single().isStaple)
    }

    @Test
    fun theMergeKeyIsComputedNotStored() = runBlocking {
        val dao = database.inventoryDao()
        dao.insertAll(
            listOf(
                InventoryItemEntity(name = " Milk ", brand = " Horizon "),
                InventoryItemEntity(name = "Eggs", brand = null),
            ),
        )
        val keys = dao.getAll().map { it.mergeKey }.toSet()
        assertTrue(keys.contains("milk|horizon"))
        assertTrue(keys.contains("eggs|"))
    }

    @Test
    fun timestampsRoundTripThroughTheEpochMillisColumn() = runBlocking {
        val dao = database.inventoryDao()
        val when1 = Instant.ofEpochMilli(1_757_808_000_123L)
        dao.insert(InventoryItemEntity(name = "Butter", dateAdded = when1))
        assertEquals(when1, dao.getAll().single().dateAdded)
    }

    @Test
    fun theInventoryFlowReEmitsOnEveryWrite() = runBlocking {
        val dao = database.inventoryDao()
        assertTrue(dao.observeAll().first().isEmpty())
        dao.insert(InventoryItemEntity(name = "Carrots"))
        assertEquals(1, dao.observeAll().first().size)
        dao.deleteAll()
        assertTrue(dao.observeAll().first().isEmpty())
    }

    @Test
    fun namesOrderCaseInsensitivelySoTheListReadsTheWayAUserExpects() = runBlocking {
        val dao = database.inventoryDao()
        dao.insertAll(
            listOf("bananas", "Apples", "cherries", "Dates").map { InventoryItemEntity(name = it) },
        )
        assertEquals(
            listOf("Apples", "bananas", "cherries", "Dates"),
            dao.observeAll().first().map { it.name },
        )
    }

    @Test
    fun quantityAndStapleUpdatesTargetOneRow() = runBlocking {
        val dao = database.inventoryDao()
        val id = dao.insert(InventoryItemEntity(name = "Salt", quantity = 1))
        dao.insert(InventoryItemEntity(name = "Pepper", quantity = 1))

        dao.setQuantity(id, 4)
        dao.setStaple(id, true)

        val salt = dao.getById(id)!!
        assertEquals(4, salt.quantity)
        assertTrue(salt.isStaple)
        val pepper = dao.getAll().first { it.name == "Pepper" }
        assertEquals(1, pepper.quantity)
        assertFalse(pepper.isStaple)
    }

    // MARK: - Shopping list

    @Test
    fun theShoppingListReadsOldestFirst() = runBlocking {
        val dao = database.shoppingListDao()
        dao.insert(ShoppingListItemEntity(name = "Third", dateAdded = Instant.ofEpochMilli(3_000)))
        dao.insert(ShoppingListItemEntity(name = "First", dateAdded = Instant.ofEpochMilli(1_000)))
        dao.insert(ShoppingListItemEntity(name = "Second", dateAdded = Instant.ofEpochMilli(2_000)))

        assertEquals(listOf("First", "Second", "Third"), dao.observeAll().first().map { it.name })
    }

    @Test
    fun checkingALineDoesNotMoveIt() = runBlocking {
        val dao = database.shoppingListDao()
        val first = dao.insert(ShoppingListItemEntity(name = "Milk", dateAdded = Instant.ofEpochMilli(1_000)))
        dao.insert(ShoppingListItemEntity(name = "Bread", dateAdded = Instant.ofEpochMilli(2_000)))

        dao.setChecked(first, true)
        assertEquals(listOf("Milk", "Bread"), dao.observeAll().first().map { it.name })
        assertEquals(1, dao.observeCheckedCount().first())
    }

    @Test
    fun clearCheckedRemovesOnlyTheCheckedLines() = runBlocking {
        val dao = database.shoppingListDao()
        dao.insert(ShoppingListItemEntity(name = "Milk", isChecked = true))
        dao.insert(ShoppingListItemEntity(name = "Bread", isChecked = false))
        dao.deleteChecked()
        assertEquals(listOf("Bread"), dao.getAll().map { it.name })
    }

    @Test
    fun caseVariantsCanBothBeStoredWhichIsWhyDedupeIsNotAUniqueIndex() = runBlocking {
        // The database does not enforce it; ShoppingListLogic.newItems does, and it
        // is case-INsensitive in a way no SQL UNIQUE constraint expresses.
        val dao = database.shoppingListDao()
        dao.insert(ShoppingListItemEntity(name = "Milk"))
        dao.insert(ShoppingListItemEntity(name = "milk"))
        assertEquals(2, dao.getAll().size)
    }

    // MARK: - Planned meals

    @Test
    fun listColumnsSurviveDelimitersAndQuotes() = runBlocking {
        val dao = database.plannedMealDao()
        val steps = listOf("Add salt, pepper", """Stir the "sauce"""", "Rest 5-7 min")
        val meal = PlannedMealEntity(
            recipeName = "Test Dish",
            ingredientNames = listOf("Milk", "Eggs"),
            steps = steps,
            timeMinutes = 25,
            plannedDate = Instant.ofEpochMilli(1_757_808_000_000L),
        )
        dao.upsert(meal)

        val stored = dao.getById(meal.id)!!
        assertEquals(steps, stored.steps)
        assertEquals(listOf("Milk", "Eggs"), stored.ingredientNames)
        assertEquals(25, stored.timeMinutes)
    }

    @Test
    fun mealsReadForwardsInTimeAndFilterFromABoundary() = runBlocking {
        val dao = database.plannedMealDao()
        listOf(3_000L, 1_000L, 2_000L).forEach { millis ->
            dao.upsert(
                PlannedMealEntity(
                    recipeName = "Meal $millis",
                    plannedDate = Instant.ofEpochMilli(millis),
                ),
            )
        }
        assertEquals(
            listOf("Meal 1000", "Meal 2000", "Meal 3000"),
            dao.observeAll().first().map { it.recipeName },
        )
        assertEquals(2, dao.observeFrom(2_000L).first().size)
    }

    @Test
    fun theCalendarEventIdStartsNullAndCanBeSetThenCleared() = runBlocking {
        val dao = database.plannedMealDao()
        val meal = PlannedMealEntity(recipeName = "Chili", plannedDate = Instant.ofEpochMilli(1_000))
        dao.upsert(meal)
        assertNull(dao.getById(meal.id)!!.calendarEventId)

        dao.setCalendarEventId(meal.id, "event-42")
        assertEquals("event-42", dao.getById(meal.id)!!.calendarEventId)

        dao.setCalendarEventId(meal.id, null)
        assertNull(dao.getById(meal.id)!!.calendarEventId)
    }

    @Test
    fun aMealResolvesByItsDeepLinkIdentity() = runBlocking {
        val dao = database.plannedMealDao()
        val meal = PlannedMealEntity(recipeName = "Ramen", plannedDate = Instant.ofEpochMilli(1_000))
        dao.upsert(meal)
        assertNotNull(dao.getById(meal.id))
        assertNull(dao.getById("not-a-real-id"))
    }
}
