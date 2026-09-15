package com.philwoolley.dinnerdecider.data.repo

import com.philwoolley.dinnerdecider.contract.FoodCategory
import com.philwoolley.dinnerdecider.contract.InventorySort
import com.philwoolley.dinnerdecider.data.entity.InventoryItemEntity
import com.philwoolley.dinnerdecider.data.entity.PlannedMealEntity
import com.philwoolley.dinnerdecider.data.entity.ShoppingListItemEntity
import io.mockk.mockk
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.Instant

/**
 * Host-JVM cover for the repository logic that is **pure** — the grouping,
 * sorting, search and formatting that used to live in `InventoryView`'s computed
 * properties and would otherwise live in a composable.
 *
 * The DAO is mocked and never called: these methods take a list and return a
 * value. Everything that touches the database is in
 * `RepositoryInstrumentedTest`.
 */
class RepositoryDerivationsTest {

    private val inventory = InventoryRepository(mockk(relaxed = true))

    private fun item(
        name: String,
        category: FoodCategory = FoodCategory.OTHER,
        brand: String? = null,
        isStaple: Boolean = false,
        addedMillis: Long = 0L,
    ) = InventoryItemEntity(
        id = name.hashCode().toLong(),
        name = name,
        brand = brand,
        categoryRaw = category.raw,
        isStaple = isStaple,
        dateAdded = Instant.ofEpochMilli(addedMillis),
    )

    // MARK: - Search

    @Test
    fun anEmptyQueryMatchesEverything() {
        val items = listOf(item("Milk"), item("Eggs"))
        assertEquals(items, inventory.filter(items, ""))
        assertEquals(items, inventory.filter(items, "   "))
    }

    @Test
    fun searchMatchesNameAndBrandCaseInsensitively() {
        val items = listOf(
            item("Whole Milk", brand = "Horizon"),
            item("Eggs", brand = "Vital Farms"),
            item("Cheddar", brand = null),
        )
        assertEquals(listOf("Whole Milk"), inventory.filter(items, "MILK").map { it.name })
        assertEquals(listOf("Whole Milk"), inventory.filter(items, "horizon").map { it.name })
        assertEquals(listOf("Eggs"), inventory.filter(items, " vital ").map { it.name })
        assertTrue(inventory.filter(items, "tofu").isEmpty())
    }

    @Test
    fun aNullBrandNeverMatchesAndNeverThrows() {
        val items = listOf(item("Cheddar", brand = null))
        assertTrue(inventory.filter(items, "horizon").isEmpty())
    }

    // MARK: - Grouping

    @Test
    fun categorySortUsesEnumDeclarationOrderNotAlphabeticalOrder() {
        val items = listOf(
            item("Ice Cream", FoodCategory.FROZEN),
            item("Apples", FoodCategory.PRODUCE),
            item("Milk", FoodCategory.DAIRY),
        )
        // produce, dairy, ..., frozen: the order a user sees on iOS.
        assertEquals(
            listOf("Produce", "Dairy", "Frozen"),
            inventory.group(items, InventorySort.CATEGORY).map { it.title },
        )
    }

    @Test
    fun emptyCategoriesAreOmittedEntirely() {
        val groups = inventory.group(listOf(item("Milk", FoodCategory.DAIRY)), InventorySort.CATEGORY)
        assertEquals(1, groups.size)
        assertEquals("dairy", groups.single().id)
    }

    @Test
    fun rowsInsideACategoryAreNameSorted() {
        val items = listOf(
            item("Yogurt", FoodCategory.DAIRY),
            item("butter", FoodCategory.DAIRY),
            item("Milk", FoodCategory.DAIRY),
        )
        assertEquals(
            listOf("butter", "Milk", "Yogurt"),
            inventory.group(items, InventorySort.CATEGORY).single().items.map { it.name },
        )
    }

    @Test
    fun nameSortIsOneSectionCalledAllItems() {
        val groups = inventory.group(
            listOf(item("Zucchini", FoodCategory.PRODUCE), item("apples", FoodCategory.PRODUCE)),
            InventorySort.NAME,
        )
        assertEquals(1, groups.size)
        assertEquals("All items", groups.single().title)
        assertEquals(listOf("apples", "Zucchini"), groups.single().items.map { it.name })
    }

    @Test
    fun dateAddedSortIsNewestFirst() {
        val groups = inventory.group(
            listOf(
                item("Old", addedMillis = 1_000),
                item("New", addedMillis = 3_000),
                item("Middle", addedMillis = 2_000),
            ),
            InventorySort.DATE_ADDED,
        )
        assertEquals("Newest first", groups.single().title)
        assertEquals(listOf("New", "Middle", "Old"), groups.single().items.map { it.name })
    }

    // MARK: - The staples split

    @Test
    fun staplesAreSplitOutOfTheGroupsAndNameSorted() {
        val listing = inventory.listing(
            listOf(
                item("Salt", FoodCategory.PANTRY, isStaple = true),
                item("Olive Oil", FoodCategory.PANTRY, isStaple = true),
                item("Milk", FoodCategory.DAIRY),
            ),
            search = "",
            sort = InventorySort.CATEGORY,
        )
        assertEquals(listOf("Olive Oil", "Salt"), listing.staples.map { it.name })
        assertEquals(listOf("Milk"), listing.groups.flatMap { it.items }.map { it.name })
    }

    @Test
    fun aStapleThatDoesNotMatchTheSearchIsNotInTheStaplesSectionEither() {
        // Search filters first, THEN staples split out of the matching set.
        val listing = inventory.listing(
            listOf(
                item("Salt", FoodCategory.PANTRY, isStaple = true),
                item("Milk", FoodCategory.DAIRY),
            ),
            search = "milk",
            sort = InventorySort.CATEGORY,
        )
        assertTrue(listing.staples.isEmpty())
        assertEquals(listOf("Milk"), listing.groups.flatMap { it.items }.map { it.name })
    }

    // MARK: - Empty vs no-matches

    @Test
    fun anEmptyStoreAndAnUnmatchedSearchAreDistinguishable() {
        val empty = inventory.listing(emptyList(), "", InventorySort.CATEGORY)
        assertTrue(empty.isEmpty)
        assertFalse(empty.hasNoMatches)

        val unmatched = inventory.listing(listOf(item("Milk")), "tofu", InventorySort.CATEGORY)
        assertFalse(unmatched.isEmpty)
        assertTrue(unmatched.hasNoMatches)

        val matched = inventory.listing(listOf(item("Milk")), "mil", InventorySort.CATEGORY)
        assertFalse(matched.isEmpty)
        assertFalse(matched.hasNoMatches)
    }

    // MARK: - Shopping list export

    @Test
    fun theExportFormatIsContractual() {
        val text = ShoppingListRepository.exportTextFor(
            listOf(
                ShoppingListItemEntity(id = 1, name = "Milk", isChecked = true),
                ShoppingListItemEntity(id = 2, name = "Bread", isChecked = false),
            ),
        )
        assertEquals("Shopping List\n\n[x] Milk\n[ ] Bread", text)
    }

    @Test
    fun anEmptyListExportsExactlyTheEmptyString() {
        assertEquals("Shopping List\n\n(empty)", ShoppingListRepository.exportTextFor(emptyList()))
    }

    // MARK: - Calendar notes

    @Test
    fun calendarNotesMatchTheShippedLayout() {
        val meal = PlannedMealEntity(
            id = "fixed",
            recipeName = "Chili",
            ingredientNames = listOf("Beans", "Tomatoes"),
            steps = listOf("Chop", "Simmer"),
            timeMinutes = 40,
            plannedDate = Instant.ofEpochMilli(0),
        )
        assertEquals(
            "⏱ 40 min\n\nIngredients:\n• Beans\n• Tomatoes\n\nSteps:\n1. Chop\n2. Simmer",
            MealPlanRepository.calendarNotes(meal),
        )
    }

    @Test
    fun calendarNotesStillRenderWithNoIngredientsOrSteps() {
        val meal = PlannedMealEntity(
            id = "fixed",
            recipeName = "Toast",
            timeMinutes = 2,
            plannedDate = Instant.ofEpochMilli(0),
        )
        assertEquals("⏱ 2 min\n\nIngredients:\n\nSteps:", MealPlanRepository.calendarNotes(meal))
    }
}
