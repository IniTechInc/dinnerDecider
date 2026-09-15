package com.philwoolley.dinnerdecider.data.dao

import androidx.room.Dao
import androidx.room.Delete
import androidx.room.Insert
import androidx.room.OnConflictStrategy
import androidx.room.Query
import androidx.room.Update
import com.philwoolley.dinnerdecider.data.entity.InventoryItemEntity
import kotlinx.coroutines.flow.Flow

/**
 * Data access for `inventory_items`.
 *
 * Deliberately narrow: this layer does rows, not rules. Grouping by category,
 * the staples split, search and the three sort orders all live in
 * [com.philwoolley.dinnerdecider.data.repo.InventoryRepository], because they are
 * behavior the iOS `InventoryView` defines and they need to be testable without a
 * database. Merge keys never appear in a query — they are computed
 * (`InventoryLogic.mergeKey`), so matching happens in the repository over a
 * snapshot, exactly as `confirmPlan` and `cookPlan` expect.
 */
@Dao
interface InventoryDao {

    /**
     * Every row, name-ordered, as a [Flow]. Room re-emits on any write to the
     * table, so the UI never polls.
     *
     * `COLLATE NOCASE` so "apples" and "Apples" sit together rather than in two
     * ASCII-separated blocks, matching what a user reading the list expects.
     */
    @Query("SELECT * FROM inventory_items ORDER BY name COLLATE NOCASE ASC")
    fun observeAll(): Flow<List<InventoryItemEntity>>

    /** A one-shot snapshot, for the confirm/cook planners. */
    @Query("SELECT * FROM inventory_items ORDER BY name COLLATE NOCASE ASC")
    suspend fun getAll(): List<InventoryItemEntity>

    @Query("SELECT * FROM inventory_items WHERE id = :id")
    suspend fun getById(id: Long): InventoryItemEntity?

    @Query("SELECT COUNT(*) FROM inventory_items")
    fun observeCount(): Flow<Int>

    @Insert(onConflict = OnConflictStrategy.ABORT)
    suspend fun insert(item: InventoryItemEntity): Long

    @Insert(onConflict = OnConflictStrategy.ABORT)
    suspend fun insertAll(items: List<InventoryItemEntity>): List<Long>

    @Update
    suspend fun update(item: InventoryItemEntity)

    @Update
    suspend fun updateAll(items: List<InventoryItemEntity>)

    @Query("UPDATE inventory_items SET quantity = :quantity WHERE id = :id")
    suspend fun setQuantity(id: Long, quantity: Int)

    @Query("UPDATE inventory_items SET isStaple = :isStaple WHERE id = :id")
    suspend fun setStaple(id: Long, isStaple: Boolean)

    @Query("UPDATE inventory_items SET categoryRaw = :categoryRaw WHERE id = :id")
    suspend fun setCategoryRaw(id: Long, categoryRaw: String)

    @Delete
    suspend fun delete(item: InventoryItemEntity)

    @Query("DELETE FROM inventory_items WHERE id IN (:ids)")
    suspend fun deleteByIds(ids: List<Long>)

    @Query("DELETE FROM inventory_items")
    suspend fun deleteAll()
}
