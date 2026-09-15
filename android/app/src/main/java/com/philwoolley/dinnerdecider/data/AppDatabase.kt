package com.philwoolley.dinnerdecider.data

import android.content.Context
import androidx.room.Database
import androidx.room.Room
import androidx.room.RoomDatabase
import androidx.room.TypeConverters
import com.philwoolley.dinnerdecider.data.dao.InventoryDao
import com.philwoolley.dinnerdecider.data.dao.PlannedMealDao
import com.philwoolley.dinnerdecider.data.dao.ShoppingListDao
import com.philwoolley.dinnerdecider.data.entity.InventoryItemEntity
import com.philwoolley.dinnerdecider.data.entity.PlannedMealEntity
import com.philwoolley.dinnerdecider.data.entity.ShoppingListItemEntity

/**
 * The app's only database: inventory, shopping list, meal plan.
 *
 * OWNERSHIP: this file is created once (plan Step 30, after the entities and DAOs
 * of Step 29) and has a single owner. Adding an entity means editing it, which is
 * why every entity landed first.
 *
 * ## `version = 1` and no migrations
 *
 * This is greenfield. `shared/` records three forward-compatibility obligations
 * that look like migrations and are not — they are decisions taken *now* so that
 * a later migration is possible at all:
 *
 * 1. `categoryRaw` is a `String`, **never an ordinal** ([InventoryItemEntity]).
 * 2. `isStaple` defaults `false`, with a column default so a future `ALTER TABLE`
 *    stays automatic.
 * 3. The taste profile writes all thirteen keys every time — not in this database
 *    (it lives in DataStore), but the same class of obligation.
 *
 * ## `exportSchema = true`
 *
 * The schema JSON is written to `android/app/schemas` (configured in
 * `app/build.gradle.kts` via `ksp { arg("room.schemaLocation", ...) }`) and is
 * **committed**. Without it, Room cannot auto-generate or validate a migration
 * later, and a version bump becomes a manual reconstruction of what version 1
 * looked like.
 *
 * ## The file name is a contract
 *
 * [DATABASE_NAME] is `dinnerdecider.db` because `res/xml/data_extraction_rules.xml`
 * already names it: `<device-transfer><include domain="database"
 * path="dinnerdecider.db" /></device-transfer>`. A device transfer is a same-user,
 * physically adjacent handoff, so the inventory and shopping list are fine to
 * carry over — but only if the file is called what the rules say it is. A
 * mismatched name means the transfer silently carries nothing, and nothing fails.
 */
@Database(
    entities = [
        InventoryItemEntity::class,
        ShoppingListItemEntity::class,
        PlannedMealEntity::class,
    ],
    version = 1,
    exportSchema = true,
)
@TypeConverters(Converters::class)
abstract class AppDatabase : RoomDatabase() {

    abstract fun inventoryDao(): InventoryDao

    abstract fun shoppingListDao(): ShoppingListDao

    abstract fun plannedMealDao(): PlannedMealDao

    companion object {
        /**
         * **Contract**, not a preference: `res/xml/data_extraction_rules.xml`
         * names this exact file in its `<device-transfer>` include.
         */
        const val DATABASE_NAME: String = "dinnerdecider.db"

        /**
         * Build the real, on-disk database.
         *
         * No `fallbackToDestructiveMigration()`. At version 1 it would never fire,
         * and leaving it in means the first real migration silently deletes the
         * user's inventory instead of failing loudly in development.
         */
        fun build(context: Context): AppDatabase =
            Room.databaseBuilder(
                context.applicationContext,
                AppDatabase::class.java,
                DATABASE_NAME,
            ).build()
    }
}
