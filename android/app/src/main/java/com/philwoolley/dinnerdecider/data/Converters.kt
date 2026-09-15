package com.philwoolley.dinnerdecider.data

import androidx.room.TypeConverter
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.builtins.serializer
import kotlinx.serialization.json.Json
import java.time.Instant

/**
 * The only type converters the database has, and both of them encode a decision
 * recorded in `shared/schema/`.
 *
 * ## `Instant` <-> epoch milliseconds
 *
 * `shared/schema/inventory-item.schema.json` marks `dateAdded` as
 * `"format": "date-time"` and then says so explicitly in its own description:
 *
 * > TODO(unresolved): the canonical cross-platform wire encoding for `dateAdded`.
 * > No JSON encoding of InventoryItem exists in the Swift [...] so RFC 3339
 * > date-time below is this document's **recommendation, not an extraction**.
 *
 * There is therefore no shipped format to match, and this port chooses **epoch
 * milliseconds in an INTEGER column**:
 *
 * - It sorts correctly in SQL, which the `dateAdded` inventory sort and the
 *   shopping list's oldest-first order both rely on. A text date sorts correctly
 *   only if every writer agrees on padding and timezone offset forever.
 * - It is timezone-free and cannot fail to parse.
 * - It is what `Instant` already is, so the conversion is exact in both
 *   directions with no formatter and no locale.
 *
 * Sub-millisecond precision is discarded, which nothing in the app reads.
 * An RFC 3339 **export** layer can be added on top whenever a cross-platform wire
 * format is actually settled; it does not need this column to change.
 *
 * ## `List<String>` <-> JSON array
 *
 * `ingredientNames` and `steps` are `"type": "array"` in `recipe.schema.json` and
 * are stored as JSON arrays, via `kotlinx.serialization` — the same library the
 * response parser uses, so there is one JSON implementation in the app.
 *
 * The alternative, joining on a delimiter, breaks on the first cooking step that
 * contains the delimiter, and cooking steps are free text written by a language
 * model. A JSON array has no such character.
 *
 * Reads are total: an unreadable or null blob yields an empty list rather than
 * throwing, so one corrupt row cannot take down the meal plan screen.
 */
object Converters {

    private val json = Json { ignoreUnknownKeys = true }
    private val stringListSerializer = ListSerializer(String.serializer())

    /** Epoch millis. Null round-trips as null so a nullable column stays nullable. */
    @TypeConverter
    fun instantToEpochMillis(value: Instant?): Long? = value?.toEpochMilli()

    @TypeConverter
    fun epochMillisToInstant(value: Long?): Instant? = value?.let(Instant::ofEpochMilli)

    @TypeConverter
    fun stringListToJson(value: List<String>?): String =
        json.encodeToString(stringListSerializer, value ?: emptyList())

    @TypeConverter
    fun jsonToStringList(value: String?): List<String> {
        if (value.isNullOrBlank()) return emptyList()
        return runCatching { json.decodeFromString(stringListSerializer, value) }
            .getOrDefault(emptyList())
    }
}
