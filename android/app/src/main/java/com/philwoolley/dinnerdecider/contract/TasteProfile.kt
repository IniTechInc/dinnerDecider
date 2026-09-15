package com.philwoolley.dinnerdecider.contract

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/**
 * The user's taste preferences, collected by the first-launch wizard and editable
 * from Settings.
 *
 * Port of `DinnerDecider/Models/TasteProfile.swift:8-34`; schema of record
 * `shared/schema/taste-profile.schema.json`. Unlike the database models this
 * schema **is** the real on-disk shape: iOS encodes the struct with `JSONEncoder`
 * and stores the blob under the key `tasteProfile`.
 *
 * Each category is a list of tapped pills plus a free-text "other" field, so people
 * can add anything the presets missed.
 *
 * ## Always write all thirteen keys, and never write `null` for a list
 *
 * Two independent traps with the same consequence — **the entire profile is lost,
 * allergies included** — and neither one fails a port's own round-trip test.
 *
 * 1. **A missing key.** Swift's synthesized `Decodable` does *not* fall back to a
 *    property's default for a missing key on a non-optional property. A partial
 *    blob fails to decode entirely, `TasteProfile.load()` swallows the error with
 *    `try?` and returns nil, and the profile is silently gone. Kotlin's defaults
 *    make *reading* a partial blob more forgiving, which is safe in the direction
 *    that matters; **writing** one is not.
 * 2. **A `null` list.** The six list properties are `"type": "array"` in the schema
 *    and Swift decodes them into non-optional `List<String>`, so a JSON `null`
 *    fails the decode exactly as a missing key does. This was found the hard way
 *    porting to Go, where a nil slice marshals to `null` rather than `[]`: the Go
 *    writer passed its own round-trip and produced a blob iOS could not read.
 *
 * [json] is configured with `encodeDefaults = true` precisely because
 * kotlinx.serialization's default is the *first* trap: without it, encoding a
 * default-constructed profile emits `{}`. Always encode through [encode] (or
 * another `Json` with `encodeDefaults = true`); never hand-roll a writer that can
 * omit a key or emit `null` for an empty list.
 */
@Serializable
data class TasteProfile(
    val favoriteFoods: List<String> = emptyList(),
    val favoriteFoodsOther: String = "",
    val dislikedFoods: List<String> = emptyList(),
    val dislikedFoodsOther: String = "",
    val cuisinesLoved: List<String> = emptyList(),
    val cuisinesLovedOther: String = "",
    val cuisinesAvoided: List<String> = emptyList(),
    val cuisinesAvoidedOther: String = "",
    /** SAFETY-RELEVANT. Rendered as "Allergies: X. Never include these." */
    val allergies: List<String> = emptyList(),
    val allergiesOther: String = "",
    val texturesAvoided: List<String> = emptyList(),
    val texturesAvoidedOther: String = "",
    /** 1 (mild) to 5 (bring it on); 3 is the default. */
    val spiceLevel: Int = DEFAULT_SPICE_LEVEL,
) : TastePromptSource {

    /**
     * True once the user has answered anything at all: any pill selected in **any**
     * of the six categories, or any of the six "other" free-text fields filled in.
     *
     * This gates whether the profile reaches the model at all, so it must cover
     * every category. An earlier iOS version checked only favorites and dislikes,
     * which silently dropped allergies-only profiles — **a safety bug**, not a
     * cosmetic one. Checking fewer than all twelve fields reintroduces it.
     *
     * The default spice level alone is not "content": a profile nobody touched must
     * not read as complete.
     */
    override val hasContent: Boolean
        get() {
            val pillGroups = listOf(
                favoriteFoods, dislikedFoods, cuisinesLoved,
                cuisinesAvoided, allergies, texturesAvoided,
            )
            if (pillGroups.any { it.isNotEmpty() }) return true
            val otherFields = listOf(
                favoriteFoodsOther, dislikedFoodsOther, cuisinesLovedOther,
                cuisinesAvoidedOther, allergiesOther, texturesAvoidedOther,
            )
            return otherFields.any { it.trim().isNotEmpty() }
        }

    /**
     * Combine a category's pills and its trimmed "other" text into one comma list.
     * Returns null when the category is empty, so callers skip the line entirely.
     */
    private fun combined(pills: List<String>, other: String): String? {
        val parts = pills.toMutableList()
        val trimmed = other.trim()
        if (trimmed.isNotEmpty()) parts.add(trimmed)
        return if (parts.isEmpty()) null else parts.joinToString(", ")
    }

    /**
     * Build the prompt fragment that describes this user's taste to the model.
     *
     * Lines are joined with `\n`, in this exact order, and **empty categories emit
     * no line at all**. The spice line is the exception: it is **always emitted**,
     * even for a completely empty profile.
     *
     * No em dash (U+2014) appears anywhere in the output, and none may be
     * introduced — the recipe prompt asserts its absence.
     */
    override fun promptFragment(): String {
        val lines = mutableListOf<String>()
        combined(favoriteFoods, favoriteFoodsOther)?.let {
            lines.add("Favorite foods: $it.")
        }
        combined(dislikedFoods, dislikedFoodsOther)?.let {
            lines.add("Foods the user dislikes: $it. Avoid these.")
        }
        combined(cuisinesLoved, cuisinesLovedOther)?.let {
            lines.add("Cuisines they enjoy: $it. Use these as flavor inspiration.")
        }
        combined(cuisinesAvoided, cuisinesAvoidedOther)?.let {
            lines.add("Cuisines they avoid: $it. Avoid those styles.")
        }
        combined(allergies, allergiesOther)?.let {
            lines.add("Allergies: $it. Never include these.")
        }
        combined(texturesAvoided, texturesAvoidedOther)?.let {
            lines.add("Textures/flavors they avoid: $it.")
        }
        val spiceDesc = when (spiceLevel) {
            1 -> "very mild, no spice at all"
            2 -> "mild, just a hint of spice"
            3 -> "medium spice"
            4 -> "spicy"
            5 -> "very spicy, the hotter the better"
            // Anything outside 1-5 falls back to the medium wording rather than
            // erroring: an out-of-range stored value must never break the prompt.
            else -> "medium spice"
        }
        lines.add("Spice preference: $spiceDesc.")
        return lines.joinToString("\n")
    }

    companion object {
        /** Storage key for the JSON blob, matching iOS. */
        const val STORAGE_KEY: String = "tasteProfile"

        /** 3 = medium. */
        const val DEFAULT_SPICE_LEVEL: Int = 3

        /**
         * The **only** encoder this type may be written with.
         *
         * `encodeDefaults = true` is load-bearing, not stylistic: kotlinx's default
         * omits properties equal to their default, so a default-constructed profile
         * would encode as `{}` — a blob iOS cannot decode at all. With it on, all
         * thirteen keys are always present and the six lists always render as `[]`,
         * never `null`.
         *
         * `ignoreUnknownKeys = true` matches the schema's `additionalProperties:
         * true` and lets a blob written by a newer version still load.
         */
        val json: Json = Json {
            encodeDefaults = true
            ignoreUnknownKeys = true
        }

        /** Encode to the on-disk blob. All thirteen keys, `[]` never `null`. */
        fun encode(profile: TasteProfile): String = json.encodeToString(serializer(), profile)

        /**
         * Decode a stored blob, returning null on anything unreadable.
         *
         * Deliberately more forgiving than iOS on *reading* — Kotlin's property
         * defaults cover a missing key, where Swift fails the whole decode. That
         * asymmetry is safe in the direction that matters (a partial blob still
         * yields the allergies it does carry). It is **not** a licence to write one.
         */
        fun decode(blob: String): TasteProfile? =
            runCatching { json.decodeFromString(serializer(), blob) }.getOrNull()
    }
}
