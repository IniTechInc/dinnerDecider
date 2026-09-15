package com.philwoolley.dinnerdecider.contract

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Line-for-line port of `DinnerDeciderTests/InventoryLogicTests.swift` (15 tests),
 * Swift names preserved.
 *
 * Pure and synchronous, no `android.*`: these run on the host JVM in
 * milliseconds.
 */
class InventoryLogicTest {

    private fun item(
        name: String,
        brand: String? = null,
        confidence: Double = 0.9,
    ) = IdentifiedItem(name = name, brand = brand, category = "other", confidence = confidence)

    // ------------------------------------------------------------------
    // Keys
    // ------------------------------------------------------------------

    @Test
    fun testMergeKeyIsCaseAndWhitespaceInsensitive() {
        assertEquals(
            InventoryLogic.mergeKey(name = "  Whole Milk ", brand = " Horizon "),
            InventoryLogic.mergeKey(name = "whole milk", brand = "horizon"),
        )
    }

    @Test
    fun testMergeKeyDistinguishesBrand() {
        assertNotEquals(
            InventoryLogic.mergeKey(name = "Milk", brand = "Horizon"),
            InventoryLogic.mergeKey(name = "Milk", brand = "Fairlife"),
        )
    }

    @Test
    fun testNameKeyIgnoresBrand() {
        assertEquals("olive oil", InventoryLogic.nameKey("  Olive Oil "))
    }

    /**
     * Not in the Swift suite: the exact key SHAPE. The pipe separator and the
     * empty-brand tail are only implied there, through the `"milk|horizon"` and
     * `"eggs|"` literals the confirm-plan tests pass in. Pinning them directly
     * means a port that used a different separator fails here rather than
     * mysteriously three files away.
     */
    @Test
    fun testMergeKeyShapeMatchesTheLiteralsUsedByConfirmPlan() {
        assertEquals("milk|horizon", InventoryLogic.mergeKey(name = "Milk", brand = "Horizon"))
        assertEquals("eggs|", InventoryLogic.mergeKey(name = "Eggs", brand = null))
        assertEquals("eggs|", InventoryLogic.mergeKey(name = "Eggs", brand = "  "))
    }

    // ------------------------------------------------------------------
    // Dedupe
    // ------------------------------------------------------------------

    @Test
    fun testDedupeCollapsesDuplicatesPreservingOrder() {
        val input = listOf(item("Milk", brand = "Horizon"), item("Eggs"), item("milk", brand = "horizon"))
        val result = InventoryLogic.dedupe(input)
        assertEquals(listOf("Milk", "Eggs"), result.map { it.name })
    }

    @Test
    fun testDedupeWithCountsTalliesDuplicates() {
        val input = listOf(item("Milk"), item("Milk"), item("Eggs"))
        val grouped = InventoryLogic.dedupeWithCounts(input)
        assertEquals(2, grouped.size)
        assertEquals(2, grouped[0].count)
        assertEquals(1, grouped[1].count)
    }

    @Test
    fun testDedupeKeepsHighestConfidence() {
        val input = listOf(item("Milk", confidence = 0.4), item("Milk", confidence = 0.95))
        val result = InventoryLogic.dedupe(input)
        assertEquals(1, result.size)
        assertEquals(0.95, result[0].confidence, 0.0001)
    }

    // ------------------------------------------------------------------
    // Confirm / merge quantities
    // ------------------------------------------------------------------

    @Test
    fun testConfirmInsertsWhenNoExistingMatch() {
        val plan = InventoryLogic.confirmPlan(
            confirming = listOf(InventoryLogic.ConfirmEntry(key = "milk|", quantity = 1)),
            existing = emptyList(),
        )
        assertEquals(listOf(InventoryLogic.ConfirmAction.Insert(key = "milk|", quantity = 1)), plan)
    }

    @Test
    fun testConfirmIncrementsExistingQuantity() {
        val plan = InventoryLogic.confirmPlan(
            confirming = listOf(InventoryLogic.ConfirmEntry(key = "milk|horizon", quantity = 1)),
            existing = listOf(InventoryLogic.StockRef(key = "milk|horizon", quantity = 2)),
        )
        assertEquals(
            listOf(InventoryLogic.ConfirmAction.Increment(key = "milk|horizon", newQuantity = 3)),
            plan,
        )
    }

    @Test
    fun testConfirmStacksRepeatedKeysAcrossTheBatch() {
        // Two confirmed items with the same key: first inserts, second bumps.
        // Both actions are emitted; collapsing them loses either the row or the
        // quantity when the caller replays them.
        val plan = InventoryLogic.confirmPlan(
            confirming = listOf(
                InventoryLogic.ConfirmEntry(key = "eggs|", quantity = 1),
                InventoryLogic.ConfirmEntry(key = "eggs|", quantity = 1),
            ),
            existing = emptyList(),
        )
        assertEquals(
            listOf(
                InventoryLogic.ConfirmAction.Insert(key = "eggs|", quantity = 1),
                InventoryLogic.ConfirmAction.Increment(key = "eggs|", newQuantity = 2),
            ),
            plan,
        )
    }

    // ------------------------------------------------------------------
    // Cooking decrement
    // ------------------------------------------------------------------

    @Test
    fun testCookPlanDecrementsUsedItems() {
        val changes = InventoryLogic.cookPlan(
            usedKeys = listOf("eggs", "cheese"),
            stock = listOf(
                InventoryLogic.StockRef(key = "eggs", quantity = 6),
                InventoryLogic.StockRef(key = "cheese", quantity = 2),
                InventoryLogic.StockRef(key = "milk", quantity = 1),
            ),
        )
        assertEquals(
            listOf(
                InventoryLogic.CookChange(key = "eggs", newQuantity = 5, removed = false),
                InventoryLogic.CookChange(key = "cheese", newQuantity = 1, removed = false),
            ),
            changes,
        )
    }

    @Test
    fun testCookPlanFlagsItemsThatReachZeroForRemoval() {
        val changes = InventoryLogic.cookPlan(
            usedKeys = listOf("milk"),
            stock = listOf(InventoryLogic.StockRef(key = "milk", quantity = 1)),
        )
        assertEquals(
            listOf(InventoryLogic.CookChange(key = "milk", newQuantity = 0, removed = true)),
            changes,
        )
    }

    @Test
    fun testCookPlanIgnoresIngredientsNotInStock() {
        val changes = InventoryLogic.cookPlan(
            usedKeys = listOf("saffron"),
            stock = listOf(InventoryLogic.StockRef(key = "milk", quantity = 1)),
        )
        assertTrue(changes.isEmpty())
    }

    /**
     * Not in the Swift suite: a repeated used key decrements repeatedly, output
     * follows **stock** order rather than `usedKeys` order, and the quantity
     * clamps at 0. All three are in the Swift implementation but only implied by
     * its tests, and all three are what a naive `Set(usedKeys)` port gets wrong.
     */
    @Test
    fun testCookPlanRepeatsDecrementsAndClampsAtZeroInStockOrder() {
        val changes = InventoryLogic.cookPlan(
            usedKeys = listOf("eggs", "eggs", "eggs", "milk"),
            stock = listOf(
                InventoryLogic.StockRef(key = "milk", quantity = 4),
                InventoryLogic.StockRef(key = "eggs", quantity = 2),
            ),
        )
        assertEquals(
            listOf(
                InventoryLogic.CookChange(key = "milk", newQuantity = 3, removed = false),
                // 2 - 3 = -1, clamped to 0 and flagged for removal.
                InventoryLogic.CookChange(key = "eggs", newQuantity = 0, removed = true),
            ),
            changes,
        )
    }

    // ------------------------------------------------------------------
    // Portion scaling
    // ------------------------------------------------------------------

    @Test
    fun testPortionLineSingleServing() {
        assertEquals(
            "Cooking for 1 person; use single-serving portions.",
            InventoryLogic.portionLine(householdSize = 1),
        )
    }

    @Test
    fun testPortionLineMultiplePeople() {
        assertEquals(
            "Cooking for 4 people; scale ingredient amounts to serve 4.",
            InventoryLogic.portionLine(householdSize = 4),
        )
    }

    @Test
    fun testPortionLineNilForZeroOrNegative() {
        assertNull(InventoryLogic.portionLine(householdSize = 0))
        assertNull(InventoryLogic.portionLine(householdSize = -3))
    }
}
