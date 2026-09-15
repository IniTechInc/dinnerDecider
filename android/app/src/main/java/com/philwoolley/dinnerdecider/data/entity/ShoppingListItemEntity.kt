package com.philwoolley.dinnerdecider.data.entity

import androidx.room.ColumnInfo
import androidx.room.Entity
import androidx.room.Index
import androidx.room.PrimaryKey
import java.time.Instant

/**
 * A single line on the shopping list.
 *
 * Mirrors `shared/schema/shopping-list-item.schema.json` field for field, which
 * describes `DinnerDecider/Models/ShoppingListItem.swift:9-27`. Items arrive here
 * either from an "almost there" recipe's `missingItems` or by manual entry.
 *
 * `dateAdded` is epoch millis for the reasons recorded on
 * [InventoryItemEntity] — the schema's `date-time` format carries the same
 * "recommendation, not an extraction" caveat.
 *
 * Note what is **not** enforced by the schema: uniqueness. Deduplication is
 * case-insensitive with first-seen casing preserved, and it lives in
 * [com.philwoolley.dinnerdecider.contract.ShoppingListLogic], not in a UNIQUE
 * index — a `UNIQUE(name)` would be case-*sensitive* and would let "Milk" and
 * "milk" both land, which is exactly what the logic exists to prevent.
 */
@Entity(
    tableName = "shopping_list_items",
    indices = [Index(value = ["dateAdded"])],
)
data class ShoppingListItemEntity(
    @PrimaryKey(autoGenerate = true)
    val id: Long = 0L,

    /**
     * Ingredient name as it will be shopped for. Blank and whitespace-only names
     * are dropped before they reach here, never stored.
     */
    val name: String,

    /**
     * Checked off in the UI. Drives check-off, "clear checked", and the export
     * marker (`[x]` vs `[ ]`).
     */
    @ColumnInfo(defaultValue = "0")
    val isChecked: Boolean = false,

    /** When the line was added. The list is ordered by this, oldest first. */
    val dateAdded: Instant = Instant.now(),

    /** True when the user typed this in rather than adding it from a recipe. */
    @ColumnInfo(defaultValue = "0")
    val isManual: Boolean = false,
)
