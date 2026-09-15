package com.philwoolley.dinnerdecider.contract

/**
 * Pure, unit-testable rules for the inventory.
 *
 * Port of `DinnerDecider/Services/InventoryLogic.swift`, pinned by
 * `DinnerDeciderTests/InventoryLogicTests.swift` (15 tests).
 *
 * Everything here is free of Compose, Room and DataStore — and of `android.*`
 * entirely — so the UI layer stays thin and the tricky bits (dedupe, quantity
 * merges, cooking decrements, portion scaling) are covered by fast host-JVM
 * tests.
 *
 * Note on trimming: Swift trims `.whitespacesAndNewlines`; Kotlin's [String.trim]
 * trims every `Char.isWhitespace()` character. The two agree on everything the
 * model and the UI actually produce.
 */
object InventoryLogic {

    /**
     * Normalised key used to decide whether two items are "the same" item.
     * Case- and whitespace-insensitive on **name + brand**.
     *
     * Shape: `trim(lowercase(name)) + "|" + trim(lowercase(brand ?: ""))`.
     * The pipe separator and the empty-brand rule are not cosmetic — they are
     * pinned by the literals `"milk|horizon"` and `"eggs|"` in the Swift tests
     * and are the keys the confirm/cook plans are expressed in.
     */
    fun mergeKey(name: String, brand: String?): String {
        val n = name.trim().lowercase()
        val b = (brand ?: "").trim().lowercase()
        return "$n|$b"
    }

    /**
     * Name-only key, used to match recipe ingredients (which have no brand)
     * against inventory when cooking.
     */
    fun nameKey(name: String): String = name.trim().lowercase()

    // ------------------------------------------------------------------
    // Scan dedupe
    // ------------------------------------------------------------------

    /** One identified item plus how many crops resolved to it. */
    data class Grouped(
        val item: IdentifiedItem,
        val count: Int,
    )

    /**
     * Collapse repeated identifications (the same item seen in overlapping
     * crops) into unique rows, keeping the first occurrence's position and the
     * highest confidence seen for it. Order is preserved.
     */
    fun dedupe(items: List<IdentifiedItem>): List<IdentifiedItem> =
        dedupeWithCounts(items).map { it.item }

    /**
     * Like [dedupe] but also reports how many raw identifications backed each
     * unique row, so quantities can be merged sensibly on confirm.
     *
     * Three rules that are easy to get wrong:
     * - **First-seen order** of the keys is the output order.
     * - Confirmed replacement is on **strictly greater** confidence, so the first
     *   of two equally-confident sightings wins.
     * - **Brand is part of the identity** (via [mergeKey]), so Horizon milk and
     *   Fairlife milk are two rows.
     */
    fun dedupeWithCounts(items: List<IdentifiedItem>): List<Grouped> {
        val order = mutableListOf<String>()
        val chosen = mutableMapOf<String, IdentifiedItem>()
        val counts = mutableMapOf<String, Int>()

        for (item in items) {
            val key = mergeKey(item.name, item.brand)
            val existing = chosen[key]
            if (existing != null) {
                counts[key] = (counts[key] ?: 1) + 1
                // Keep the identification with the higher confidence.
                if (item.confidence > existing.confidence) {
                    chosen[key] = item
                }
            } else {
                order.add(key)
                chosen[key] = item
                counts[key] = 1
            }
        }
        return order.map { Grouped(item = chosen.getValue(it), count = counts[it] ?: 1) }
    }

    // ------------------------------------------------------------------
    // Merge on confirm
    // ------------------------------------------------------------------

    /** A lightweight view of an existing inventory row, for merge planning. */
    data class StockRef(
        val key: String,
        val quantity: Int,
    )

    /** One item being confirmed into inventory: its [mergeKey] and its quantity. */
    data class ConfirmEntry(
        val key: String,
        val quantity: Int,
    )

    /** The decision for one item being confirmed into inventory. */
    sealed interface ConfirmAction {
        /** Bump an existing row's quantity to [newQuantity]. */
        data class Increment(val key: String, val newQuantity: Int) : ConfirmAction

        /** Insert a brand new row with [quantity]. */
        data class Insert(val key: String, val quantity: Int) : ConfirmAction
    }

    /**
     * Decide, for each confirmed item, whether to bump an existing inventory row
     * (duplicate names merge quantities) or insert a new one.
     *
     * The running tally spans **the whole batch**, not just the pre-existing
     * stock: two confirmed entries with the same key produce an INSERT followed
     * by an INCREMENT, and both are emitted in order. Never collapse them — the
     * caller replays them as database operations, so dropping the insert loses
     * the row and dropping the increment loses the quantity.
     *
     * `INSERT` carries the entry's **own** quantity; `INCREMENT` carries the
     * **running total**.
     */
    fun confirmPlan(
        confirming: List<ConfirmEntry>,
        existing: List<StockRef>,
    ): List<ConfirmAction> {
        val quantities = mutableMapOf<String, Int>()
        val known = mutableSetOf<String>()
        for (ref in existing) {
            quantities[ref.key] = ref.quantity
            known.add(ref.key)
        }

        val actions = mutableListOf<ConfirmAction>()
        for (entry in confirming) {
            val current = quantities[entry.key] ?: 0
            val updated = current + entry.quantity
            quantities[entry.key] = updated
            if (known.contains(entry.key)) {
                actions.add(ConfirmAction.Increment(key = entry.key, newQuantity = updated))
            } else {
                known.add(entry.key)
                actions.add(ConfirmAction.Insert(key = entry.key, quantity = entry.quantity))
            }
        }
        return actions
    }

    // ------------------------------------------------------------------
    // Cooking (quantity decrement)
    // ------------------------------------------------------------------

    /** The result of cooking a recipe for one inventory row. */
    data class CookChange(
        val key: String,
        val newQuantity: Int,
        /** True when the row hit zero and should be removed. */
        val removed: Boolean,
    )

    /**
     * Given the keys of the ingredients actually used (the ones the user has)
     * and the current stock, decrement each matching row by one.
     *
     * - Keys here are **name-only** ([nameKey]) because recipe ingredients carry
     *   no brand.
     * - A key repeated in [usedKeys] decrements **repeatedly**.
     * - Output is in **stock** order, not [usedKeys] order.
     * - Quantities clamp at 0; rows that reach zero get `removed = true`.
     * - Rows not used are left untouched and are **not** returned.
     */
    fun cookPlan(usedKeys: List<String>, stock: List<StockRef>): List<CookChange> {
        val quantities = mutableMapOf<String, Int>()
        val order = mutableListOf<String>()
        for (ref in stock) {
            quantities[ref.key] = ref.quantity
            order.add(ref.key)
        }

        val touched = mutableSetOf<String>()
        for (key in usedKeys) {
            val current = quantities[key] ?: continue
            quantities[key] = current - 1
            touched.add(key)
        }

        return order.mapNotNull { key ->
            if (!touched.contains(key)) return@mapNotNull null
            val quantity = maxOf(quantities[key] ?: 0, 0)
            CookChange(key = key, newQuantity = quantity, removed = quantity <= 0)
        }
    }

    // ------------------------------------------------------------------
    // Portion scaling
    // ------------------------------------------------------------------

    /**
     * The prompt line describing how many people to cook for. `null` for an unset
     * or nonsensical size so the prompt stays compact.
     *
     * Verbatim strings — they ship to the model and are asserted character for
     * character (`shared/prompts/recipes.md`, "Portion line (verbatim)").
     */
    fun portionLine(householdSize: Int): String? {
        if (householdSize < 1) return null
        if (householdSize == 1) {
            return "Cooking for 1 person; use single-serving portions."
        }
        return "Cooking for $householdSize people; scale ingredient amounts to serve $householdSize."
    }
}
