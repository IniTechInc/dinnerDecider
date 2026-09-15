package com.philwoolley.dinnerdecider.contract

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.boolean
import kotlinx.serialization.json.double
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

/**
 * THE PARITY PROOF.
 *
 * Runs the 27 golden fixtures in `shared/conformance/cases.json` — the same file
 * the iOS suite was extracted from and the same file any other port reads — against
 * the Kotlin [LlmResponseParser]. Passing all 27 is the acceptance gate for the
 * parser half of the Android port.
 *
 * **The fixture file is read directly from the repo**, located through the
 * `dd.shared.dir` system property that `android/app/build.gradle.kts` sets on every
 * `Test` task. It is deliberately NOT copied into `src/test/resources`: a copy
 * invites drift, and reading the one true file makes editing-to-pass structurally
 * impossible — any "fix" would show up as a diff to a shared, cross-platform
 * contract file that this task is forbidden to touch.
 *
 * Rules taken verbatim from `shared/conformance/README.md`:
 * - Run **every** case. No skipping, no filtering, no "known failing" tags.
 * - **Never edit a case to make a port pass.** If the port disagrees with a
 *   fixture, either the port is wrong or the Swift changed — and then the Swift
 *   test and the fixture move in the same commit.
 * - Report failures by `id`, so a failure is traceable to the named Swift test.
 * - Strings compare exactly, including whitespace. **No trimming.**
 * - Numbers compare within `floatTolerance`; `timeMinutes` is an integer and
 *   compares exactly.
 * - `brand` absent and `brand: null` are the same result; an empty *string* brand
 *   is not null.
 * - Arrays are order- and length-sensitive.
 * - **Null and empty are never conflated.**
 *
 * No `android.*` imports: this runs on the host JVM, no emulator, no device.
 */
class ConformanceTest {

    // ---------------------------------------------------------------------
    // Suite loading
    // ---------------------------------------------------------------------

    private val suiteJson = Json { ignoreUnknownKeys = true; isLenient = false }

    private data class Suite(
        val floatTolerance: Double,
        val cases: List<JsonObject>,
    )

    private fun sharedDir(): File {
        val property = System.getProperty("dd.shared.dir")
        assertNotNull(
            "System property dd.shared.dir is not set. It is configured by " +
                "android/app/build.gradle.kts (tasks.withType<Test>) and points at the repo's " +
                "shared/ directory. Run the suite through Gradle, not a bare JUnit launcher.",
            property,
        )
        val dir = File(property!!)
        assertTrue("dd.shared.dir does not exist: ${dir.absolutePath}", dir.isDirectory)
        return dir
    }

    private fun loadSuite(): Suite {
        val file = File(sharedDir(), "conformance/cases.json")
        assertTrue(
            "Conformance fixtures not found at ${file.absolutePath}. This file is the " +
                "cross-platform source of truth and must be read from the repo, never copied.",
            file.isFile,
        )
        val root = suiteJson.parseToJsonElement(file.readText()).jsonObject
        return Suite(
            floatTolerance = root["floatTolerance"]!!.jsonPrimitive.double,
            cases = root["cases"]!!.jsonArray.map { it.jsonObject },
        )
    }

    // ---------------------------------------------------------------------
    // The gate
    // ---------------------------------------------------------------------

    @Test
    fun conformanceSuitePassesEveryCase() {
        val suite = loadSuite()

        // A truncated or filtered run must fail loudly rather than report a
        // green "all cases passed" over a handful of them.
        assertEquals(
            "shared/conformance/cases.json must hold exactly 27 fixtures. " +
                "If the count legitimately changed, the Swift tests and this expectation " +
                "move in the same commit as the fixture.",
            27,
            suite.cases.size,
        )

        val failures = mutableListOf<String>()
        val seenIds = mutableSetOf<String>()

        for (case in suite.cases) {
            val id = case["id"]!!.jsonPrimitive.content
            if (!seenIds.add(id)) {
                failures += "$id: duplicate case id (ids must be unique)"
                continue
            }
            failures += evaluate(case, suite.floatTolerance)
        }

        assertTrue(
            buildString {
                append("Conformance failures (${failures.size}) out of ${suite.cases.size} cases:\n")
                failures.forEach { append("  - ").append(it).append('\n') }
                append("\nDo NOT edit shared/conformance/cases.json to make these pass.")
            },
            failures.isEmpty(),
        )
    }

    /**
     * Supplementary assertions — **not** fixtures, and deliberately not added to
     * `cases.json`.
     *
     * `shared/parser/response-parsing.md` and `shared/schema/recipe.schema.json`
     * both state the rule that a **float `timeMinutes` fails the whole recipe
     * bundle**, but no fixture pins it. Swift decodes `timeMinutes` as `Int`, so a
     * JSON float throws and the bundle decodes to nil; the user sees the
     * unreadable-recipes error rather than a silently truncated time.
     *
     * Adding a fixture for this would mean editing a shared cross-platform file
     * from a port task, which is forbidden — and `cases.json` only accepts cases
     * backed by a real Swift test or a real captured model reply. So the rule is
     * enforced here, against the Kotlin parser only, and flagged as a candidate
     * for a proper fixture in a follow-up that touches Swift too.
     */
    @Test
    fun supplementaryFloatTimeMinutesFailsTheWholeBundle() {
        val integral = """{"makeNow":[{"name":"Toast","ingredients":[],"steps":[],""" +
            """"timeMinutes":5,"missingItems":[]}],"almostThere":[]}"""
        val floating = """{"makeNow":[{"name":"Toast","ingredients":[],"steps":[],""" +
            """"timeMinutes":5.0,"missingItems":[]}],"almostThere":[]}"""

        // Control: the same bundle with an integer decodes fine, so the float case
        // below is failing for the right reason.
        assertNotNull(
            "Control bundle with an integer timeMinutes must decode",
            LlmResponseParser.decode<RecipeBundleResponse>(integral),
        )
        assertNull(
            "A float timeMinutes must fail the WHOLE bundle, not just that recipe",
            LlmResponseParser.decode<RecipeBundleResponse>(floating),
        )

        // The float payload is still balanced, so extraction succeeds: the failure
        // is in the decode, exactly like the `{ not: valid json }` pair of fixtures.
        assertEquals(floating, LlmResponseParser.firstJSONObject(floating))
    }

    /**
     * `firstJSONArray` has no fixture of its own — `cases.json` covers it
     * transitively through the six `decodeIdentifiedItemArray` cases. The runner
     * below implements it as a first-class operation anyway (all six operations in
     * `shared/conformance/README.md` are implemented), and this pins the two
     * behaviors the array path adds over the object path.
     */
    @Test
    fun supplementaryArrayExtractorDoesNotFallBackToObjects() {
        // No `[` anywhere: a hard miss, never a fallback to the object scanner.
        assertNull(LlmResponseParser.firstJSONArray("""{"name": "Milk"}"""))
        // First balanced array only; trailing content ignored, not an error.
        assertEquals("[1, [2]]", LlmResponseParser.firstJSONArray("junk [1, [2]] tail [3]"))
    }

    // ---------------------------------------------------------------------
    // Case evaluation
    // ---------------------------------------------------------------------

    /** Returns the failure descriptions for one case; empty means it passed. */
    private fun evaluate(case: JsonObject, tolerance: Double): List<String> {
        val id = case["id"]!!.jsonPrimitive.content
        val operation = case["operation"]!!.jsonPrimitive.content
        val input = case["input"]!!.jsonPrimitive.content
        val expectNull = case["expectNull"]!!.jsonPrimitive.boolean
        val expected: JsonElement? = case["expected"]?.takeUnless { it is JsonNull }

        // A missing `expected` with expectNull:false is a MALFORMED SUITE, never
        // "expect null" (shared/conformance/README.md, File format).
        if (!expectNull && expected == null) {
            return listOf("$id: MALFORMED SUITE — expectNull is false but `expected` is absent")
        }

        return when (operation) {
            // -- string-returning operations (never null) ------------------
            "stripReasoning" -> compareNonNullString(id, LlmResponseParser.stripReasoning(input), expectNull, expected)
            "stripCodeFences" -> compareNonNullString(id, LlmResponseParser.stripCodeFences(input), expectNull, expected)

            // -- nullable-string operations --------------------------------
            "firstJSONObject" -> compareNullableString(id, LlmResponseParser.firstJSONObject(input), expectNull, expected)
            "firstJSONArray" -> compareNullableString(id, LlmResponseParser.firstJSONArray(input), expectNull, expected)

            // -- decoding operations ---------------------------------------
            "decodeIdentifiedItem" -> {
                val got = LlmResponseParser.decode<IdentifiedItem>(input)
                nullness(id, got, expectNull) ?: compareIdentifiedItem(id, got!!, expected!!.jsonObject, tolerance)
            }

            "decodeIdentifiedItemArray" -> {
                val got = LlmResponseParser.decodeArray<IdentifiedItem>(input)
                nullness(id, got, expectNull) ?: compareItemArray(id, got!!, expected!!.jsonArray, tolerance)
            }

            "decodeRecipeBundle" -> {
                val got = LlmResponseParser.decode<RecipeBundleResponse>(input)
                nullness(id, got, expectNull) ?: compareRecipeBundle(id, got!!, expected!!.jsonObject)
            }

            else -> listOf("$id: unknown operation '$operation' — the runner must implement every operation in the suite")
        }
    }

    /**
     * Null / non-null check shared by the decoding operations.
     *
     * Returns a failure list when the nullness itself is wrong, or null when it is
     * right and the caller should go on to compare values. `[]` and null are never
     * conflated: an empty list reaching here is non-null and is compared normally.
     */
    private fun nullness(id: String, got: Any?, expectNull: Boolean): List<String>? = when {
        expectNull && got != null -> listOf("$id: expected null, got $got")
        !expectNull && got == null -> listOf("$id: expected a value, got null")
        expectNull -> emptyList()
        else -> null
    }

    private fun compareNonNullString(
        id: String,
        got: String,
        expectNull: Boolean,
        expected: JsonElement?,
    ): List<String> {
        if (expectNull) {
            return listOf("$id: MALFORMED SUITE — this operation never returns null")
        }
        val want = expected!!.jsonPrimitive.content
        // Exact equality, including whitespace. No trimming.
        return if (got == want) emptyList() else listOf("$id: string mismatch\n      expected: ${quote(want)}\n      actual:   ${quote(got)}")
    }

    private fun compareNullableString(
        id: String,
        got: String?,
        expectNull: Boolean,
        expected: JsonElement?,
    ): List<String> {
        nullness(id, got, expectNull)?.let { return it }
        val want = expected!!.jsonPrimitive.content
        return if (got == want) emptyList() else listOf("$id: string mismatch\n      expected: ${quote(want)}\n      actual:   ${quote(got)}")
    }

    private fun compareItemArray(
        id: String,
        got: List<IdentifiedItem>,
        expected: JsonArray,
        tolerance: Double,
    ): List<String> {
        // Order- and length-sensitive.
        if (got.size != expected.size) {
            return listOf("$id: array length mismatch — expected ${expected.size}, got ${got.size}")
        }
        val failures = mutableListOf<String>()
        got.forEachIndexed { index, item ->
            failures += compareIdentifiedItem("$id[$index]", item, expected[index].jsonObject, tolerance)
        }
        return failures
    }

    private fun compareIdentifiedItem(
        id: String,
        got: IdentifiedItem,
        expected: JsonObject,
        tolerance: Double,
    ): List<String> {
        val failures = mutableListOf<String>()
        failures += eqString(id, "name", got.name, expected)
        // `brand` absent and `brand: null` are the same result; an empty STRING
        // brand is not null, so this is a nullable-string comparison, not a blank
        // check.
        failures += eqNullableString(id, "brand", got.brand, expected)
        failures += eqString(id, "category", got.category, expected)
        failures += eqDouble(id, "confidence", got.confidence, expected, tolerance)
        return failures
    }

    private fun compareRecipeBundle(
        id: String,
        got: RecipeBundleResponse,
        expected: JsonObject,
    ): List<String> {
        val failures = mutableListOf<String>()
        failures += compareSuggestions("$id.makeNow", got.makeNow, expected["makeNow"]?.jsonArray)
        failures += compareSuggestions("$id.almostThere", got.almostThere, expected["almostThere"]?.jsonArray)
        return failures
    }

    private fun compareSuggestions(
        id: String,
        got: List<RecipeSuggestion>,
        expected: JsonArray?,
    ): List<String> {
        if (expected == null) return listOf("$id: MALFORMED SUITE — expected bucket is absent")
        if (got.size != expected.size) {
            return listOf("$id: length mismatch — expected ${expected.size}, got ${got.size}")
        }
        val failures = mutableListOf<String>()
        got.forEachIndexed { index, suggestion ->
            val want = expected[index].jsonObject
            val path = "$id[$index]"
            failures += eqString(path, "name", suggestion.name, want)
            // timeMinutes is an INTEGER and compares EXACTLY — no tolerance.
            failures += eqInt(path, "timeMinutes", suggestion.timeMinutes, want)
            failures += eqStringList(path, "steps", suggestion.steps, want)
            failures += eqStringList(path, "missingItems", suggestion.missingItems, want)

            val wantIngredients = want["ingredients"]?.jsonArray
            if (wantIngredients == null) {
                failures += "$path.ingredients: MALFORMED SUITE — absent"
            } else if (suggestion.ingredients.size != wantIngredients.size) {
                failures += "$path.ingredients: length mismatch — expected ${wantIngredients.size}, got ${suggestion.ingredients.size}"
            } else {
                suggestion.ingredients.forEachIndexed { line, ingredient ->
                    val wantLine = wantIngredients[line].jsonObject
                    failures += eqString("$path.ingredients[$line]", "name", ingredient.name, wantLine)
                    failures += eqBoolean("$path.ingredients[$line]", "hasIt", ingredient.hasIt, wantLine)
                }
            }
        }
        return failures
    }

    // ---------------------------------------------------------------------
    // Field comparators
    // ---------------------------------------------------------------------

    private fun eqString(id: String, field: String, got: String, expected: JsonObject): List<String> {
        val want = expected[field]?.takeUnless { it is JsonNull }?.jsonPrimitive?.content
            ?: return listOf("$id.$field: MALFORMED SUITE — expected value absent or null for a non-optional field")
        return if (got == want) emptyList() else listOf("$id.$field: expected ${quote(want)}, got ${quote(got)}")
    }

    private fun eqNullableString(id: String, field: String, got: String?, expected: JsonObject): List<String> {
        // Absent key and explicit JSON null are the same expectation.
        val want = expected[field]?.takeUnless { it is JsonNull }?.jsonPrimitive?.content
        return if (got == want) emptyList() else listOf("$id.$field: expected ${quote(want)}, got ${quote(got)}")
    }

    private fun eqDouble(
        id: String,
        field: String,
        got: Double,
        expected: JsonObject,
        tolerance: Double,
    ): List<String> {
        val want = expected[field]?.takeUnless { it is JsonNull }?.jsonPrimitive?.double
            ?: return listOf("$id.$field: MALFORMED SUITE — expected value absent or null")
        return if (kotlin.math.abs(got - want) <= tolerance) emptyList()
        else listOf("$id.$field: expected $want +/- $tolerance, got $got")
    }

    private fun eqInt(id: String, field: String, got: Int, expected: JsonObject): List<String> {
        val primitive = expected[field]?.takeUnless { it is JsonNull }?.jsonPrimitive
            ?: return listOf("$id.$field: MALFORMED SUITE — expected value absent or null")
        val want = runCatching { primitive.int }.getOrElse {
            return listOf("$id.$field: MALFORMED SUITE — expected value ${primitive.content} is not an integer")
        }
        return if (got == want) emptyList() else listOf("$id.$field: expected exactly $want, got $got")
    }

    private fun eqBoolean(id: String, field: String, got: Boolean, expected: JsonObject): List<String> {
        val want = expected[field]?.takeUnless { it is JsonNull }?.jsonPrimitive?.boolean
            ?: return listOf("$id.$field: MALFORMED SUITE — expected value absent or null")
        return if (got == want) emptyList() else listOf("$id.$field: expected $want, got $got")
    }

    private fun eqStringList(id: String, field: String, got: List<String>, expected: JsonObject): List<String> {
        val want = expected[field]?.takeUnless { it is JsonNull }?.jsonArray
            ?: return listOf("$id.$field: MALFORMED SUITE — expected array absent or null")
        // [] and null are never conflated, and order matters.
        if (got.size != want.size) {
            return listOf("$id.$field: length mismatch — expected ${want.size}, got ${got.size}")
        }
        val failures = mutableListOf<String>()
        got.forEachIndexed { index, value ->
            val expectedValue = (want[index] as JsonPrimitive).content
            if (value != expectedValue) {
                failures += "$id.$field[$index]: expected ${quote(expectedValue)}, got ${quote(value)}"
            }
        }
        return failures
    }

    /** Render a value with visible whitespace so exact-string failures are readable. */
    private fun quote(value: String?): String =
        value?.let { "\"" + it.replace("\\", "\\\\").replace("\n", "\\n").replace("\"", "\\\"") + "\"" } ?: "null"
}
