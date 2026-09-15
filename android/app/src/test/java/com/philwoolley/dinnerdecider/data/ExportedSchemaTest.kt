package com.philwoolley.dinnerdecider.data

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Test
import java.io.File

/**
 * Reads the schema Room actually exported and checks the three
 * forward-compatibility obligations `shared/` records.
 *
 * Why against the exported JSON rather than the Kotlin source: the source says
 * `val categoryRaw: String`, but what ends up in the user's database is whatever
 * Room decided the *column* should be, after type converters. A converter mapping
 * [com.philwoolley.dinnerdecider.contract.FoodCategory] to an ordinal would leave
 * the Kotlin looking fine and the column an INTEGER. This test reads the artifact.
 *
 * The file is produced by `kspDebugKotlin`, which `testDebugUnitTest` already
 * depends on. It is located relative to `dd.shared.dir`, the repo pointer the
 * build sets for the conformance runner, so no new build configuration is needed.
 */
class ExportedSchemaTest {

    private fun schemaFile(): File? {
        val sharedDir = System.getProperty("dd.shared.dir") ?: return null
        val repoRoot = File(sharedDir).parentFile ?: return null
        val file = File(
            repoRoot,
            "android/app/schemas/com.philwoolley.dinnerdecider.data.AppDatabase/1.json",
        )
        return file.takeIf { it.isFile }
    }

    private fun entities(): Map<String, JsonObject> {
        val file = schemaFile()
        assumeTrue("exported schema not found; run :app:kspDebugKotlin", file != null)
        val root = Json.parseToJsonElement(file!!.readText()).jsonObject
        return root["database"]!!.jsonObject["entities"]!!.jsonArray
            .associate { entity ->
                entity.jsonObject["tableName"]!!.jsonPrimitive.content to entity.jsonObject
            }
    }

    private fun field(table: String, column: String): JsonObject? =
        entities()[table]?.get("fields")?.jsonArray
            ?.map { it.jsonObject }
            ?.firstOrNull { it["columnName"]!!.jsonPrimitive.content == column }

    @Test
    fun theDatabaseIsVersionOneAndExportsItsSchema() {
        val file = schemaFile()
        assumeTrue(file != null)
        val root = Json.parseToJsonElement(file!!.readText()).jsonObject
        assertEquals(1, root["database"]!!.jsonObject["version"]!!.jsonPrimitive.content.toInt())
    }

    @Test
    fun allThreeAggregatesArePresent() {
        val tables = entities().keys
        assertTrue(tables.containsAll(setOf("inventory_items", "shopping_list_items", "planned_meals")))
    }

    /**
     * Obligation 1. An ordinal column breaks the moment anyone inserts a case
     * anywhere but the end of the enum: every existing row silently re-labels
     * itself, with no failed migration and nothing to notice. It is unrecoverable
     * because the old ordering is not recorded anywhere.
     */
    @Test
    fun categoryRawIsATextColumnNeverAnOrdinal() {
        val column = field("inventory_items", "categoryRaw")
        assertNotNull(column)
        assertEquals("TEXT", column!!["affinity"]!!.jsonPrimitive.content)
    }

    /** Obligation 2. The column default keeps a future ALTER TABLE automatic. */
    @Test
    fun isStapleDefaultsToFalseInTheColumnItself() {
        val column = field("inventory_items", "isStaple")
        assertNotNull(column)
        assertEquals("INTEGER", column!!["affinity"]!!.jsonPrimitive.content)
        assertEquals("0", column["defaultValue"]!!.jsonPrimitive.content)
    }

    /**
     * The recorded choice: epoch millis in an INTEGER column, because
     * `shared/schema/inventory-item.schema.json` marks RFC 3339 as "this
     * document's recommendation, not an extraction". A TEXT column here would mean
     * someone swapped in a formatter, and the `dateAdded` sort would start
     * depending on padding and timezone offsets.
     */
    @Test
    fun everyTimestampIsStoredAsEpochMillisInAnIntegerColumn() {
        listOf(
            "inventory_items" to "dateAdded",
            "shopping_list_items" to "dateAdded",
            "planned_meals" to "plannedDate",
        ).forEach { (table, column) ->
            val stored = field(table, column)
            assertNotNull("$table.$column missing", stored)
            assertEquals("$table.$column", "INTEGER", stored!!["affinity"]!!.jsonPrimitive.content)
        }
    }

    /** JSON arrays, so a cooking step containing any delimiter survives. */
    @Test
    fun listColumnsAreStoredAsTextJson() {
        listOf("ingredientNames", "steps").forEach { column ->
            val stored = field("planned_meals", column)
            assertNotNull("planned_meals.$column missing", stored)
            assertEquals("TEXT", stored!!["affinity"]!!.jsonPrimitive.content)
        }
    }
}
