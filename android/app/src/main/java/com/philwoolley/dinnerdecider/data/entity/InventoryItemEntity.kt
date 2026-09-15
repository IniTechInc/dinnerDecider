package com.philwoolley.dinnerdecider.data.entity

import androidx.room.ColumnInfo
import androidx.room.Entity
import androidx.room.Index
import androidx.room.PrimaryKey
import com.philwoolley.dinnerdecider.contract.FoodCategory
import com.philwoolley.dinnerdecider.contract.InventoryLogic
import java.time.Instant

/**
 * A row of the user's food inventory.
 *
 * Mirrors `shared/schema/inventory-item.schema.json` field for field, which in
 * turn describes `DinnerDecider/Models/InventoryItem.swift:50-92`. On iOS this is
 * a SwiftData `@Model` and **not** `Codable`, so there is no shipped JSON
 * encoding of it; the schema documents the persisted field set so a port's store
 * stays compatible.
 *
 * ## `categoryRaw` is a String column, never an ordinal
 *
 * The Swift stores the category as a raw string "so SwiftData keeps a stable
 * value even if we reorder or rename cases later" (`InventoryItem.swift:54`), and
 * the schema makes it a requirement: *"Ports must persist the string, never an
 * ordinal."*
 *
 * Room would happily persist a [FoodCategory] as its ordinal through a type
 * converter, and that database would be **unrecoverable** the moment anyone
 * inserted a case anywhere but the end of the enum: every existing row would
 * silently re-label itself, with no failed migration and no error to notice. The
 * column is a `String` and [category] maps it, falling back to
 * [FoodCategory.OTHER] for anything unrecognized — same as
 * `FoodCategory(rawValue:) ?? .other`.
 *
 * ## `dateAdded` is epoch millis
 *
 * The schema's `"format": "date-time"` carries an explicit caveat: RFC 3339 is
 * *"this document's recommendation, not an extraction"* — no JSON encoding of
 * this type exists anywhere in the Swift, so there is no shipped wire format to
 * match. This port therefore chooses **epoch milliseconds in an INTEGER column**,
 * via [com.philwoolley.dinnerdecider.data.Converters]: it sorts correctly in SQL
 * (which the `dateAdded` inventory sort needs), it is timezone-free, and it does
 * not invite the parse failures a text date would. Recording the choice here is
 * the obligation; an RFC 3339 export layer can be added later without touching
 * the column.
 */
@Entity(
    tableName = "inventory_items",
    indices = [Index(value = ["name"]), Index(value = ["dateAdded"])],
)
data class InventoryItemEntity(
    @PrimaryKey(autoGenerate = true)
    val id: Long = 0L,

    /** Item name. Half of the merge key. */
    val name: String,

    /**
     * Brand, or null. The other half of the merge key: two rows with the same name
     * but different brands are **distinct** items.
     */
    val brand: String? = null,

    /** The category as a raw string. See the class KDoc — never an ordinal. */
    val categoryRaw: String = FoodCategory.OTHER.raw,

    /**
     * How many of this item are on hand. Merged on confirm (duplicate keys stack)
     * and decremented by 1 per use when a recipe is cooked. A row reaching 0 is
     * flagged for removal.
     */
    val quantity: Int = 1,

    /** When the row was created; drives the `dateAdded` inventory sort. */
    val dateAdded: Instant = Instant.now(),

    /** Identifier of the photo this item came from, or null for manual entry. */
    val sourcePhotoId: String? = null,

    /**
     * A staple the user almost always has (salt, oil), shown collapsed so it does
     * not clutter the main list.
     *
     * **Defaults to `false`**, as the schema requires. The Swift declares
     * `var isStaple: Bool = false` specifically to keep SwiftData's lightweight
     * migration happy for stores written before the field existed; the same
     * default here means a future Room migration adding a column can use
     * `DEFAULT 0` and stay automatic.
     */
    @ColumnInfo(defaultValue = "0")
    val isStaple: Boolean = false,
) {
    /** Typed accessor over the raw category string, falling back to `other`. */
    val category: FoodCategory get() = FoodCategory.fromStorage(categoryRaw)

    /**
     * `trim(lowercase(name)) + "|" + trim(lowercase(brand ?? ""))`.
     *
     * **Computed, never stored** — the schema is explicit about that, and
     * [InventoryLogic.mergeKey] is the single implementation.
     */
    val mergeKey: String get() = InventoryLogic.mergeKey(name, brand)

    /** The name-only key used to match recipe ingredients when cooking. */
    val nameKey: String get() = InventoryLogic.nameKey(name)
}
