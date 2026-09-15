package com.philwoolley.dinnerdecider.data.repo

import com.philwoolley.dinnerdecider.contract.FoodCategory
import com.philwoolley.dinnerdecider.contract.IdentifiedItem
import com.philwoolley.dinnerdecider.contract.InventoryLogic
import com.philwoolley.dinnerdecider.contract.InventorySort
import com.philwoolley.dinnerdecider.data.dao.InventoryDao
import com.philwoolley.dinnerdecider.data.entity.InventoryItemEntity
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.map
import java.time.Instant

/**
 * One section of the inventory list: a heading and the rows under it.
 *
 * Port of `InventoryView.CategoryGroup` (`InventoryView.swift:43-47`).
 */
data class InventoryGroup(
    val id: String,
    val title: String,
    val items: List<InventoryItemEntity>,
)

/**
 * The whole inventory screen's data, already grouped, sorted and split.
 *
 * Returned as one value so a composable renders it rather than deriving it. The
 * derivations are what `InventoryView.swift:26-64` does in computed properties,
 * and they are the reason this repository exists: they are behavior, they have
 * edge cases, and a composable is a bad place to test either.
 */
data class InventoryListing(
    val groups: List<InventoryGroup>,
    val staples: List<InventoryItemEntity>,
    /** True when the store is empty, as opposed to the search matching nothing. */
    val isEmpty: Boolean,
    /** True when there are rows but the search excluded all of them. */
    val hasNoMatches: Boolean,
)

/**
 * The inventory aggregate.
 *
 * Thin by design: every rule it applies already exists as a pure function in
 * [InventoryLogic], and this class is where those functions meet the database.
 * Query, grouping, sorting and mutation logic live here rather than inside
 * composables — which is also what keeps this file one owner's, so the screen
 * steps stay partitionable.
 */
class InventoryRepository(private val dao: InventoryDao) {

    /** Every row, name-ordered. The raw feed, for callers doing their own shaping. */
    fun observeItems(): Flow<List<InventoryItemEntity>> = dao.observeAll()

    fun observeCount(): Flow<Int> = dao.observeCount()

    /**
     * The inventory screen's data, derived exactly as `InventoryView` derives it.
     *
     * @param search matched case-insensitively against **name and brand**, after
     *   trimming. An empty or whitespace-only query matches everything.
     * @param sort one of the three shipped orders; see [group].
     */
    fun observeListing(
        search: String = "",
        sort: InventorySort = InventorySort.CATEGORY,
    ): Flow<InventoryListing> = dao.observeAll().map { items -> listing(items, search, sort) }

    /**
     * The pure derivation, exposed so it can be tested without a database.
     *
     * Order of operations matches iOS: filter by search first, then split staples
     * out of the *matching* set, then group what remains. A staple that does not
     * match the search does not appear in the staples section either.
     */
    fun listing(
        items: List<InventoryItemEntity>,
        search: String,
        sort: InventorySort,
    ): InventoryListing {
        val matching = filter(items, search)
        val staples = matching.filter { it.isStaple }.sortedBy { it.name.lowercase() }
        val everyday = matching.filterNot { it.isStaple }
        return InventoryListing(
            groups = group(everyday, sort),
            staples = staples,
            isEmpty = items.isEmpty(),
            hasNoMatches = items.isNotEmpty() && matching.isEmpty(),
        )
    }

    /** Case-insensitive contains over name and brand. `InventoryView.swift:26-33`. */
    fun filter(items: List<InventoryItemEntity>, search: String): List<InventoryItemEntity> {
        val query = search.trim().lowercase()
        if (query.isEmpty()) return items
        return items.filter { item ->
            item.name.lowercase().contains(query) ||
                item.brand?.lowercase()?.contains(query) == true
        }
    }

    /**
     * Group and sort, one arm per [InventorySort]. `InventoryView.swift:49-64`.
     *
     * - [InventorySort.CATEGORY]: one section per category, in **enum declaration
     *   order** — not alphabetical and not by size — with empty categories omitted
     *   and rows name-sorted inside each. The declaration order is the shipped
     *   order the user sees.
     * - [InventorySort.NAME]: one "All items" section, name-sorted.
     * - [InventorySort.DATE_ADDED]: one "Newest first" section, **descending**.
     */
    fun group(items: List<InventoryItemEntity>, sort: InventorySort): List<InventoryGroup> =
        when (sort) {
            InventorySort.CATEGORY -> {
                val byCategory = items.groupBy { it.category }
                FoodCategory.entries.mapNotNull { category ->
                    val group = byCategory[category]
                    if (group.isNullOrEmpty()) {
                        null
                    } else {
                        InventoryGroup(
                            id = category.raw,
                            title = category.displayName,
                            items = group.sortedBy { it.name.lowercase() },
                        )
                    }
                }
            }

            InventorySort.NAME -> listOf(
                InventoryGroup("all", "All items", items.sortedBy { it.name.lowercase() }),
            )

            InventorySort.DATE_ADDED -> listOf(
                InventoryGroup("all", "Newest first", items.sortedByDescending { it.dateAdded }),
            )
        }

    // MARK: - Mutations

    suspend fun getById(id: Long): InventoryItemEntity? = dao.getById(id)

    suspend fun add(item: InventoryItemEntity): Long = dao.insert(item)

    suspend fun update(item: InventoryItemEntity) = dao.update(item)

    suspend fun setQuantity(id: Long, quantity: Int) = dao.setQuantity(id, quantity.coerceAtLeast(0))

    suspend fun setStaple(id: Long, isStaple: Boolean) = dao.setStaple(id, isStaple)

    suspend fun setCategory(id: Long, category: FoodCategory) = dao.setCategoryRaw(id, category.raw)

    suspend fun delete(item: InventoryItemEntity) = dao.delete(item)

    suspend fun deleteByIds(ids: List<Long>) = dao.deleteByIds(ids)

    suspend fun deleteAll() = dao.deleteAll()

    /**
     * Apply `InventoryLogic.confirmPlan` to the store.
     *
     * The planner is pure and already knows the subtlety
     * (`testConfirmStacksRepeatedKeysAcrossTheBatch`): two confirmed entries with
     * the same key produce `INSERT(key, 1)` then `INCREMENT(key, 2)` — the running
     * tally spans the batch, `INSERT` carries the entry's own quantity and
     * `INCREMENT` carries the running total. **Both actions are applied, in
     * order, never collapsed**, so this method walks the list as given.
     *
     * @param confirming the deduped sightings the user confirmed, in UI order.
     * @return the number of rows inserted.
     */
    suspend fun applyConfirmed(
        confirming: List<ConfirmedItem>,
        now: Instant = Instant.now(),
    ): Int {
        val existing = dao.getAll()
        val stock = existing.map { InventoryLogic.StockRef(it.mergeKey, it.quantity) }
        val rowsByKey = existing.associateBy { it.mergeKey }.toMutableMap()
        val detailsByKey = confirming.associateBy { InventoryLogic.mergeKey(it.name, it.brand) }

        val actions = InventoryLogic.confirmPlan(
            confirming = confirming.map {
                InventoryLogic.ConfirmEntry(
                    key = InventoryLogic.mergeKey(it.name, it.brand),
                    quantity = it.quantity,
                )
            },
            existing = stock,
        )

        var inserted = 0
        for (action in actions) {
            when (action) {
                is InventoryLogic.ConfirmAction.Insert -> {
                    val details = detailsByKey[action.key] ?: continue
                    val id = dao.insert(
                        InventoryItemEntity(
                            name = details.name,
                            brand = details.brand,
                            categoryRaw = details.category.raw,
                            quantity = action.quantity,
                            dateAdded = now,
                            sourcePhotoId = details.sourcePhotoId,
                        ),
                    )
                    rowsByKey[action.key] = dao.getById(id) ?: continue
                    inserted++
                }

                is InventoryLogic.ConfirmAction.Increment -> {
                    val row = rowsByKey[action.key] ?: continue
                    dao.setQuantity(row.id, action.newQuantity)
                    rowsByKey[action.key] = row.copy(quantity = action.newQuantity)
                }
            }
        }
        return inserted
    }

    /**
     * Apply `InventoryLogic.cookPlan`: decrement one per use, clamp at 0, delete
     * the rows that reached 0.
     *
     * Ingredients carry no brand, so matching uses the **name-only** key. A
     * repeated ingredient decrements repeatedly — there is no de-duplication of
     * the used names, and adding one would silently under-consume.
     *
     * @return the keys whose rows were removed.
     */
    suspend fun applyCooked(ingredientNames: List<String>): List<String> {
        val rows = dao.getAll()
        // Ingredients match on the name-only key, so several branded rows can share
        // one key; the first in stock order is the one the plan refers to.
        val rowsByNameKey = rows.groupBy { it.nameKey }
        val stock = rows.map { InventoryLogic.StockRef(it.nameKey, it.quantity) }
        val changes = InventoryLogic.cookPlan(
            usedKeys = ingredientNames.map(InventoryLogic::nameKey),
            stock = stock,
        )

        val removed = mutableListOf<String>()
        for (change in changes) {
            val row = rowsByNameKey[change.key]?.firstOrNull() ?: continue
            if (change.removed) {
                dao.delete(row)
                removed.add(change.key)
            } else {
                dao.setQuantity(row.id, change.newQuantity)
            }
        }
        return removed
    }

    /**
     * A sighting the user confirmed, carrying everything a new row needs. The
     * quantity is the user's, not the dedupe count, because the review screen lets
     * them change it.
     */
    data class ConfirmedItem(
        val name: String,
        val brand: String?,
        val category: FoodCategory,
        val quantity: Int,
        val sourcePhotoId: String? = null,
    ) {
        companion object {
            fun from(item: IdentifiedItem, quantity: Int, sourcePhotoId: String? = null) =
                ConfirmedItem(
                    name = item.name,
                    brand = item.brand,
                    category = item.foodCategory,
                    quantity = quantity,
                    sourcePhotoId = sourcePhotoId,
                )
        }
    }
}
