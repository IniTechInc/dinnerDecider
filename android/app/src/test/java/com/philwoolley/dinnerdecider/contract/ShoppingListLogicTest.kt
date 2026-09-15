package com.philwoolley.dinnerdecider.contract

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** Port of `DinnerDeciderTests/ShoppingListLogicTests.swift`, Swift names preserved. */
class ShoppingListLogicTest {

    @Test
    fun testAggregateDedupesCaseInsensitivelyPreservingOrder() {
        val result = ShoppingListLogic.aggregate(
            listOf("Onion", "onion", "Chili Powder", "  ", "ONION"),
        )
        assertEquals(listOf("Onion", "Chili Powder"), result)
    }

    @Test
    fun testAggregateTrimsWhitespaceAndDropsBlanks() {
        val result = ShoppingListLogic.aggregate(listOf("  Milk  ", "", "   "))
        assertEquals(listOf("Milk"), result)
    }

    @Test
    fun testNewItemsExcludesThingsAlreadyOnList() {
        val result = ShoppingListLogic.newItems(
            adding = listOf("Onion", "Garlic", "onion"),
            existing = listOf("ONION", "Salt"),
        )
        assertEquals(listOf("Garlic"), result)
    }

    @Test
    fun testExportTextMarksCheckedItems() {
        val lines = listOf(
            ShoppingListLogic.Line(name = "Onion", isChecked = false),
            ShoppingListLogic.Line(name = "Milk", isChecked = true),
        )
        val text = ShoppingListLogic.exportText(lines)
        assertTrue(text.contains("Shopping List"))
        assertTrue(text.contains("[ ] Onion"))
        assertTrue(text.contains("[x] Milk"))
    }

    @Test
    fun testExportTextHandlesEmptyList() {
        assertEquals("Shopping List\n\n(empty)", ShoppingListLogic.exportText(emptyList()))
    }
}

/**
 * Supplementary pins for the details the Swift suite leaves implicit. Kept in a
 * separate class so [ShoppingListLogicTest] stays at the five cases
 * `ShoppingListLogicTests.swift` carries.
 */
class ShoppingListLogicDetailTest {

    /** First-seen casing wins, not last-seen and not a normalized form. */
    @Test
    fun aggregateKeepsFirstSeenCasing() {
        assertEquals(listOf("ONION"), ShoppingListLogic.aggregate(listOf("ONION", "Onion", "onion")))
        assertEquals(listOf("onion"), ShoppingListLogic.aggregate(listOf("onion", "ONION")))
    }

    /** The trimmed original is kept, not the raw input. */
    @Test
    fun aggregateKeepsTheTrimmedOriginal() {
        assertEquals(listOf("Milk"), ShoppingListLogic.aggregate(listOf("  Milk  ", "milk")))
    }

    /** Existing entries are trimmed before they become keys. */
    @Test
    fun newItemsTrimsExistingEntriesBeforeComparing() {
        assertEquals(
            emptyList<String>(),
            ShoppingListLogic.newItems(adding = listOf("Onion"), existing = listOf("  onion  ")),
        )
    }

    @Test
    fun newItemsReturnsEverythingWhenTheListIsEmpty() {
        assertEquals(
            listOf("Onion", "Garlic"),
            ShoppingListLogic.newItems(adding = listOf("Onion", "Garlic"), existing = emptyList()),
        )
    }

    /** The whole rendered string, not just `contains` probes. */
    @Test
    fun exportTextRendersTheExactString() {
        val text = ShoppingListLogic.exportText(
            listOf(
                ShoppingListLogic.Line("Onion", false),
                ShoppingListLogic.Line("Milk", true),
            ),
        )
        assertEquals("Shopping List\n\n[ ] Onion\n[x] Milk", text)
    }

    /** The last row's newline is trimmed away; the header blank line is not. */
    @Test
    fun exportTextHasNoTrailingNewline() {
        val text = ShoppingListLogic.exportText(listOf(ShoppingListLogic.Line("Onion", false)))
        assertEquals("Shopping List\n\n[ ] Onion", text)
    }
}
