package com.philwoolley.dinnerdecider.data.dao

import androidx.room.Dao
import androidx.room.Delete
import androidx.room.Insert
import androidx.room.OnConflictStrategy
import androidx.room.Query
import androidx.room.Update
import com.philwoolley.dinnerdecider.data.entity.PlannedMealEntity
import kotlinx.coroutines.flow.Flow

/**
 * Data access for `planned_meals`.
 *
 * Ordered by `plannedDate` ascending: a meal plan reads forwards in time.
 * [observeFrom] exists so the plan screen can show upcoming meals without pulling
 * a year of history into memory, and takes its boundary as a parameter rather
 * than calling `Instant.now()` in SQL — a query that depends on the wall clock
 * cannot be tested deterministically.
 */
@Dao
interface PlannedMealDao {

    @Query("SELECT * FROM planned_meals ORDER BY plannedDate ASC")
    fun observeAll(): Flow<List<PlannedMealEntity>>

    /** Meals planned at or after [fromEpochMillis], soonest first. */
    @Query("SELECT * FROM planned_meals WHERE plannedDate >= :fromEpochMillis ORDER BY plannedDate ASC")
    fun observeFrom(fromEpochMillis: Long): Flow<List<PlannedMealEntity>>

    @Query("SELECT * FROM planned_meals ORDER BY plannedDate ASC")
    suspend fun getAll(): List<PlannedMealEntity>

    /** By identity — this is what the `dinnerdecider://meal/<id>` deep link resolves. */
    @Query("SELECT * FROM planned_meals WHERE id = :id")
    suspend fun getById(id: String): PlannedMealEntity?

    @Query("SELECT * FROM planned_meals WHERE id = :id")
    fun observeById(id: String): Flow<PlannedMealEntity?>

    @Insert(onConflict = OnConflictStrategy.REPLACE)
    suspend fun upsert(meal: PlannedMealEntity)

    @Update
    suspend fun update(meal: PlannedMealEntity)

    /** Null clears the link, which is what deleting the calendar event means. */
    @Query("UPDATE planned_meals SET calendarEventId = :calendarEventId WHERE id = :id")
    suspend fun setCalendarEventId(id: String, calendarEventId: String?)

    @Delete
    suspend fun delete(meal: PlannedMealEntity)

    @Query("DELETE FROM planned_meals WHERE id = :id")
    suspend fun deleteById(id: String)

    @Query("DELETE FROM planned_meals")
    suspend fun deleteAll()
}
