package com.philwoolley.dinnerdecider.data.dao

import androidx.room.Dao
import androidx.room.Delete
import androidx.room.Insert
import androidx.room.OnConflictStrategy
import androidx.room.Query
import androidx.room.Update
import com.philwoolley.dinnerdecider.data.entity.ShoppingListItemEntity
import kotlinx.coroutines.flow.Flow

/**
 * Data access for `shopping_list_items`.
 *
 * Ordered by `dateAdded` **ascending**, matching iOS's
 * `@Query(sort: \ShoppingListItem.dateAdded)`: a shopping list reads in the order
 * things were added to it, so a line does not jump position when it is checked.
 *
 * Deduplication is **not** here. It is case-insensitive with first-seen casing
 * preserved (`ShoppingListLogic.newItems`), which no SQL `UNIQUE` constraint
 * expresses — a `UNIQUE(name)` is case-sensitive and would happily store both
 * "Milk" and "milk". The repository filters before inserting.
 */
@Dao
interface ShoppingListDao {

    @Query("SELECT * FROM shopping_list_items ORDER BY dateAdded ASC, id ASC")
    fun observeAll(): Flow<List<ShoppingListItemEntity>>

    @Query("SELECT * FROM shopping_list_items ORDER BY dateAdded ASC, id ASC")
    suspend fun getAll(): List<ShoppingListItemEntity>

    /** Just the names, for the case-insensitive dedupe in the repository. */
    @Query("SELECT name FROM shopping_list_items")
    suspend fun getAllNames(): List<String>

    @Query("SELECT COUNT(*) FROM shopping_list_items WHERE isChecked = 1")
    fun observeCheckedCount(): Flow<Int>

    @Insert(onConflict = OnConflictStrategy.ABORT)
    suspend fun insert(item: ShoppingListItemEntity): Long

    @Insert(onConflict = OnConflictStrategy.ABORT)
    suspend fun insertAll(items: List<ShoppingListItemEntity>): List<Long>

    @Update
    suspend fun update(item: ShoppingListItemEntity)

    @Query("UPDATE shopping_list_items SET isChecked = :isChecked WHERE id = :id")
    suspend fun setChecked(id: Long, isChecked: Boolean)

    @Query("UPDATE shopping_list_items SET isChecked = 0")
    suspend fun uncheckAll()

    @Delete
    suspend fun delete(item: ShoppingListItemEntity)

    @Query("DELETE FROM shopping_list_items WHERE isChecked = 1")
    suspend fun deleteChecked()

    @Query("DELETE FROM shopping_list_items")
    suspend fun deleteAll()
}
