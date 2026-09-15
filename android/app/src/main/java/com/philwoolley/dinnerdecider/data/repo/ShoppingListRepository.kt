package com.philwoolley.dinnerdecider.data.repo

import com.philwoolley.dinnerdecider.contract.ShoppingListLogic
import com.philwoolley.dinnerdecider.data.dao.ShoppingListDao
import com.philwoolley.dinnerdecider.data.entity.ShoppingListItemEntity
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.map
import java.time.Instant

/**
 * The shopping list aggregate.
 *
 * Every rule already exists as a pure function in [ShoppingListLogic]; this is
 * where they meet the database. Two of them are easy to lose by "simplifying":
 *
 * - **Dedupe is case-insensitive with first-seen casing preserved.** That is not
 *   expressible as a SQL `UNIQUE` constraint — `UNIQUE(name)` is case-*sensitive*
 *   and would store both "Milk" and "milk". So [addNames] reads the existing
 *   names and filters before inserting.
 * - **The export format is contractual**, down to the exact string an empty list
 *   produces. See the `$comment` in
 *   `shared/schema/shopping-list-item.schema.json`.
 */
class ShoppingListRepository(private val dao: ShoppingListDao) {

    fun observeItems(): Flow<List<ShoppingListItemEntity>> = dao.observeAll()

    fun observeCheckedCount(): Flow<Int> = dao.observeCheckedCount()

    /** The export text, recomputed whenever the list changes. */
    fun observeExportText(): Flow<String> = dao.observeAll().map(::exportTextFor)

    /**
     * Add names, skipping anything already on the list case-insensitively and
     * anything blank.
     *
     * `ShoppingListLogic.newItems` does the work, including the trim asymmetry its
     * tests pin: candidates are trimmed before comparison, existing entries are
     * trimmed too, and the final filter compares the lower-cased *already-trimmed*
     * aggregate output.
     *
     * @return the names actually inserted, in first-seen order and original casing.
     */
    suspend fun addNames(
        names: List<String>,
        isManual: Boolean = false,
        now: Instant = Instant.now(),
    ): List<String> {
        val toAdd = ShoppingListLogic.newItems(adding = names, existing = dao.getAllNames())
        if (toAdd.isEmpty()) return emptyList()
        dao.insertAll(
            toAdd.mapIndexed { index, name ->
                ShoppingListItemEntity(
                    name = name,
                    isManual = isManual,
                    // Nudge each by a millisecond so a batch keeps its order under
                    // `ORDER BY dateAdded`, rather than relying on the id tiebreak.
                    dateAdded = now.plusMillis(index.toLong()),
                )
            },
        )
        return toAdd
    }

    /** One manually typed line. Blank input is dropped, never stored. */
    suspend fun addManual(name: String, now: Instant = Instant.now()): Boolean =
        addNames(listOf(name), isManual = true, now = now).isNotEmpty()

    suspend fun setChecked(id: Long, isChecked: Boolean) = dao.setChecked(id, isChecked)

    suspend fun toggle(item: ShoppingListItemEntity) = dao.setChecked(item.id, !item.isChecked)

    suspend fun uncheckAll() = dao.uncheckAll()

    suspend fun clearChecked() = dao.deleteChecked()

    suspend fun delete(item: ShoppingListItemEntity) = dao.delete(item)

    suspend fun deleteAll() = dao.deleteAll()

    /** The current export text, as a one-shot. */
    suspend fun exportText(): String = exportTextFor(dao.getAll())

    companion object {
        /**
         * Render the contractual plain-text export.
         *
         * Header `Shopping List`, a blank line, then `[x] Name` / `[ ] Name` per
         * line, trimmed of trailing whitespace — and an **empty list is exactly**
         * `"Shopping List\n\n(empty)"`. [ShoppingListLogic.exportText] owns the
         * formatting; this only maps rows onto its input type.
         */
        fun exportTextFor(items: List<ShoppingListItemEntity>): String =
            ShoppingListLogic.exportText(
                items.map { ShoppingListLogic.Line(name = it.name, isChecked = it.isChecked) },
            )
    }
}
