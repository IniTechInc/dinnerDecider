package com.philwoolley.dinnerdecider.contract

/**
 * Pure shopping-list helpers. Port of `DinnerDecider/Services/ShoppingListLogic.swift`.
 *
 * Kept free of any persistence type so the aggregation, the already-on-the-list
 * filter and the share-sheet rendering are all testable on the host JVM.
 *
 * Swift trims with `.whitespacesAndNewlines`; Kotlin's [String.trim] trims on
 * `Char.isWhitespace()`, which covers the same spaces, tabs and newlines for every
 * input this sees.
 */
object ShoppingListLogic {

    /**
     * De-duplicate a set of ingredient names case-insensitively, dropping blanks and
     * **preserving first-seen order and first-seen casing**.
     *
     * Used when adding an "almost there" recipe's missing items to the list. The
     * lower-cased name is the identity; the string that is kept is the trimmed
     * original of the first sighting, so "Onion" then "onion" then "ONION" yields
     * one entry reading `Onion`.
     */
    fun aggregate(names: List<String>): List<String> {
        val seen = mutableSetOf<String>()
        val result = mutableListOf<String>()
        for (raw in names) {
            val name = raw.trim()
            if (name.isEmpty()) continue
            val key = name.lowercase()
            if (seen.add(key)) {
                result.add(name)
            }
        }
        return result
    }

    /**
     * Given the names already on the list, return only the new ones to add.
     *
     * Note the deliberate asymmetry, carried over verbatim from Swift: the
     * **existing** entries are trimmed *and* lower-cased to build the key set, while
     * the final filter only lower-cases - because [aggregate] has already trimmed
     * its output. Trimming twice would be harmless; trimming neither would not, so
     * the asymmetry is the shape of the original and is kept rather than "tidied".
     */
    fun newItems(adding: List<String>, existing: List<String>): List<String> {
        val existingKeys = existing.map { it.trim().lowercase() }.toSet()
        return aggregate(adding).filter { it.lowercase() !in existingKeys }
    }

    /** A plain line for export, decoupled from persistence for testing. */
    data class Line(val name: String, val isChecked: Boolean)

    /**
     * Render the list as plain text for the share sheet. Checked items are marked
     * done so a shared list still reads clearly.
     *
     * Format: the header `Shopping List`, a blank line, then one `[x] Name` or
     * `[ ] Name` per line, with the whole string trimmed (which removes the trailing
     * newline left by the last row).
     *
     * An **empty list exports exactly** `"Shopping List\n\n(empty)"` - a distinct
     * early return, not the general path with zero rows, which would collapse to
     * just the header.
     */
    fun exportText(lines: List<Line>): String {
        if (lines.isEmpty()) return "Shopping List\n\n(empty)"
        val out = StringBuilder("Shopping List\n\n")
        for (line in lines) {
            val box = if (line.isChecked) "[x]" else "[ ]"
            out.append(box).append(' ').append(line.name).append('\n')
        }
        return out.toString().trim()
    }
}
