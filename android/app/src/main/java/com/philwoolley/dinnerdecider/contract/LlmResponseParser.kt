package com.philwoolley.dinnerdecider.contract

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

// -----------------------------------------------------------------------------
// PROVISIONAL WIRE MODELS — RECONCILE AT MERGE.
//
// `IdentifiedItem`, `IngredientLine`, `RecipeSuggestion` and `RecipeBundleResponse`
// are the property of plan Step 7 (`contract/ContractModels.kt`), which is being
// written in a PARALLEL worktree and is therefore not available here. The parser
// and the conformance runner cannot be written or proved without them, so the
// minimal @Serializable surface each one needs is declared here.
//
// AT MERGE: delete these four declarations and keep ContractModels.kt's. They are
// deliberately field-for-field identical to the Swift sources and to
// shared/schema/*.json, so the only expected difference is added convenience
// members (e.g. `IdentifiedItem.foodCategory`) that live on the Step 7 version.
//
//   Swift sources of record:
//     DinnerDecider/Services/LLM/LLMService.swift:7-17   (IdentifiedItem)
//     DinnerDecider/Models/Recipe.swift:5-27             (IngredientLine,
//                                                         RecipeSuggestion,
//                                                         RecipeBundleResponse)
// -----------------------------------------------------------------------------

/**
 * One grocery item recognized by the vision model. Wire type only.
 *
 * `brand` carries a default, so an ABSENT key and an explicit `null` both decode
 * to null — matching Swift's `decodeIfPresent`. `name`, `category` and
 * `confidence` deliberately carry NO default: a missing one raises
 * `MissingFieldException`, which [LlmResponseParser.decode] swallows into null.
 * That is what makes the `object-missing-required-fields` fixture expect null.
 *
 * `category` is never validated at decode time; any string decodes and the enum
 * mapping happens later with an `other` fallback (shared/taxonomy/categories.md).
 */
@Serializable
data class IdentifiedItem(
    val name: String,
    val brand: String? = null,
    val category: String,
    val confidence: Double,
)

/** One ingredient row in a suggested recipe. */
@Serializable
data class IngredientLine(
    val name: String,
    val hasIt: Boolean,
)

/**
 * One suggested dish.
 *
 * `timeMinutes` is an **Int**. A JSON float there fails the decode of the whole
 * bundle, exactly as Swift's `Codable` does — see the supplementary assertion in
 * `ConformanceTest`.
 */
@Serializable
data class RecipeSuggestion(
    val name: String,
    val ingredients: List<IngredientLine>,
    val steps: List<String>,
    val timeMinutes: Int,
    val missingItems: List<String>,
)

/** The two recipe buckets returned by a single model call. */
@Serializable
data class RecipeBundleResponse(
    val makeNow: List<RecipeSuggestion>,
    val almostThere: List<RecipeSuggestion>,
)

/**
 * Pure, unit-testable helpers for pulling structured JSON out of a model's reply.
 *
 * Port of `DinnerDecider/Services/LLM/LLMResponseParser.swift`, specified by
 * `shared/parser/response-parsing.md` and pinned by the 27 golden fixtures in
 * `shared/conformance/cases.json`.
 *
 * Language models wrap their JSON in prose ("Sure, here you go:"), in markdown
 * fences, and — with this particular Gemma 4 build — in a thinking-channel
 * preamble. This salvages the payload without a schema-constrained decoder,
 * because a schema-constrained decoder is **not usable here**: the grammar
 * sampler in the runtime is never reset between calls, so a resident model
 * returns output only on the FIRST call and then goes terminal. Never add a
 * grammar / GBNF / responseFormat path (`response-parsing.md`, PROHIBITION).
 *
 * Every function is **total**: no throws, no panics, no partial results. Failure
 * is always a null return, and every caller treats null as "this call
 * contributed nothing".
 *
 * Zero `android.*` imports on purpose — this runs on the host JVM so the parity
 * proof needs no emulator and no device.
 */
object LlmResponseParser {

    /** The close marker of Gemma 4's chain-of-thought channel. */
    private const val CHANNEL_CLOSE_MARKER = "<channel|>"

    /**
     * The four fence literals, stripped **in this order**.
     *
     * Order is load-bearing: the language-tagged variants must go before the bare
     * ``` or you are left with orphaned `json` tokens. Only these three casings
     * are handled — a hypothetical ```jSoN leaves a stray `jSoN` behind, which is
     * harmless because the brace scanner ignores everything outside the brackets.
     */
    private val FENCES = listOf("```json", "```JSON", "```Json", "```")

    /**
     * The JSON configuration. **All three settings matter.**
     *
     * - `ignoreUnknownKeys = true` — unknown extra keys are ignored, not an error
     *   (Swift `Codable` ignores them too).
     * - `isLenient = false` — strict JSON. This is specifically what makes
     *   `{ not: valid json }` *extract* successfully (it is balanced) and then
     *   *fail* to decode. Fixtures `object-unquoted-keys-extracts` and
     *   `object-unquoted-keys-decodes-null` are two observations of that same
     *   input and both must hold.
     * - `coerceInputValues = false` — a wrong-typed or null value for a
     *   non-nullable field must fail the decode, not silently become a default.
     *   Coercion would let `{"name": "Milk"}`-shaped junk through.
     *
     * Public because the reified `decode`/`decodeArray` helpers below are inline.
     */
    val json: Json = Json {
        ignoreUnknownKeys = true
        isLenient = false
        coerceInputValues = false
    }

    /**
     * Strip Gemma 4's "thinking" channel preamble.
     *
     * Rule, exactly: find the **LAST** occurrence of the literal `<channel|>`; if
     * found return everything after it, otherwise return the text unchanged.
     *
     * - Only the **close** marker is searched for. The open marker
     *   `<|channel>thought` is never searched for and never needs to be present.
     * - **LAST, not first** — nested or repeated reasoning blocks all get
     *   discarded along with any decoy JSON they contain.
     * - **No trimming.** `"<channel|>  {...}"` yields `"  {...}"`; the bracket
     *   scanner skips leading junk anyway.
     * - Text with no marker is returned **byte-identical**.
     */
    fun stripReasoning(text: String): String {
        val index = text.lastIndexOf(CHANNEL_CLOSE_MARKER)
        if (index < 0) return text
        return text.substring(index + CHANNEL_CLOSE_MARKER.length)
    }

    /**
     * Remove markdown code fences so they do not confuse the scanner.
     *
     * Unconditional plain-substring replacement across the whole string, in
     * [FENCES] order — not a paired-fence parse. Nothing else is stripped: prose
     * before and after stays, and the scanner skips it.
     */
    fun stripCodeFences(text: String): String {
        var result = text
        for (fence in FENCES) {
            result = result.replace(fence, "")
        }
        return result
    }

    /**
     * Return the first balanced `{ ... }` JSON object found in [text], or null.
     *
     * Braces inside string literals (and escaped quotes) do not count, so
     * `{"name": "a}b"}` extracts whole.
     */
    fun firstJSONObject(text: String): String? = extractBalanced(clean(text), '{', '}')

    /**
     * Return the first balanced `[ ... ]` JSON array found in [text], or null.
     *
     * **Independent of [firstJSONObject]; there is no fallback between the two.**
     * An object reply where an array was expected finds no `[` at all and is a
     * hard miss (`array-object-when-array-expected`).
     */
    fun firstJSONArray(text: String): String? = extractBalanced(clean(text), '[', ']')

    /** Decode [T] from the first JSON object embedded in [response], or null. */
    inline fun <reified T> decode(response: String): T? {
        val extracted = firstJSONObject(response) ?: return null
        return runCatching { json.decodeFromString<T>(extracted) }.getOrNull()
    }

    /**
     * Decode a list of [T] from the first JSON array embedded in [response].
     *
     * `[]` decodes to an **empty list, not null**: "the model saw nothing" and
     * "the model was unreadable" are different outcomes and must stay different.
     */
    inline fun <reified T> decodeArray(response: String): List<T>? {
        val extracted = firstJSONArray(response) ?: return null
        return runCatching { json.decodeFromString<List<T>>(extracted) }.getOrNull()
    }

    /**
     * The fixed order of operations: **reasoning first, then fences.**
     *
     * Not arbitrary. The fence markers may live *inside* the answer that follows
     * the channel close marker, and the reasoning text may itself contain fenced
     * pseudo-JSON. Stripping reasoning first discards that decoy wholesale.
     * Pinned by `object-thinking-channel-inside-code-fence`.
     */
    private fun clean(text: String): String = stripCodeFences(stripReasoning(text))

    /**
     * One balanced-value scanner, parameterised only by the bracket pair.
     *
     * Contractual behaviors:
     * 1. **First balanced value only.** `{"a": {"nested": 1}} trailing {"b": 2}`
     *    yields `{"a": {"nested": 1}}`; trailing content is ignored, not an error.
     * 2. **Brackets inside string literals do not count.** Escape handling is a
     *    single lookahead flag, so `\"` does not end the string.
     * 3. **Unbalanced input returns null**, never a truncated prefix.
     * 4. `depth` only reaches 0 at the matching close, so a stray leading close
     *    bracket before the first open is impossible by construction — the scan
     *    starts at the first open.
     *
     * Char-based rather than byte-based (Swift iterates a `[Character]` array).
     * Every fixture is ASCII, so the two are identical here.
     */
    private fun extractBalanced(cleaned: String, open: Char, close: Char): String? {
        val start = cleaned.indexOf(open)
        if (start < 0) return null

        var depth = 0
        var inString = false
        var escaped = false
        var index = start

        while (index < cleaned.length) {
            val character = cleaned[index]
            if (inString) {
                when {
                    escaped -> escaped = false
                    character == '\\' -> escaped = true
                    character == '"' -> inString = false
                }
            } else {
                when (character) {
                    '"' -> inString = true
                    open -> depth += 1
                    close -> {
                        depth -= 1
                        if (depth == 0) return cleaned.substring(start, index + 1)
                    }
                }
            }
            index += 1
        }
        return null
    }
}
