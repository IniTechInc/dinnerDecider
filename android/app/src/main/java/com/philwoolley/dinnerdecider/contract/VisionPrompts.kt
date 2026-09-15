package com.philwoolley.dinnerdecider.contract

/**
 * The two vision prompts and the runtime parameters they were tuned against.
 *
 * Contracts of record: `shared/prompts/identify-item.md`,
 * `shared/prompts/identify-all-items.md`, `shared/runtime/model-files.md`.
 * Source of record: `GemmaLLMService.swift:46-124`.
 *
 * ### The templates are single-line, and that is contractual
 * The Swift literals are multi-line strings whose every line ends in a backslash,
 * so the *rendered* prompt is **one line with no embedded newlines**. The Kotlin
 * raw strings below are therefore each a single physical line, however long that
 * makes them. A reformatter that wraps one of these strings changes the bytes the
 * model sees, which is a silent recognition regression with no failing test — hence
 * the length and no-newline assertions in `VisionPromptsTest`.
 *
 * Do **not** pad these prompts. The runtime context is 1024 tokens total and is
 * shared by prompt, ~256 vision tokens, and the generated reply.
 *
 * ### Do not replace either prompt with a grammar / structured-output constraint
 * `shared/parser/response-parsing.md` prohibits it, and the vendored client's
 * grammar sampler never resets: the second call on a resident model returns zero
 * tokens. Prose-plus-salvage parsing is the chosen path.
 */
object VisionPrompts {

    /** The token substituted with the OCR result, in both templates. */
    const val OCR_PLACEHOLDER: String = "{{OCR_TEXT}}"

    /**
     * What an empty OCR result renders as.
     *
     * The literal keeps the sentence grammatical ("Text found on the packaging:
     * none.") instead of emitting a dangling "Text found on the packaging: .",
     * which the model handles markedly worse.
     */
    const val NO_OCR_TEXT: String = "none"

    /**
     * Single cropped grocery image plus any OCR text read off that crop; returns
     * **one** `IdentifiedItem` JSON object.
     *
     * Verbatim from `shared/prompts/identify-item.md`. Rendered with
     * [OCR_PLACEHOLDER] = [NO_OCR_TEXT] it is **558 characters**, no trailing
     * newline.
     *
     * Two sentences in here are load-bearing and must not be trimmed as padding:
     * the 25-item "Common items include:" vocabulary anchor, which stops the model
     * inventing hyper-specific product names it cannot actually read; and the
     * `unknown` escape hatch, which the pipeline's noise filter matches on. Drop the
     * escape hatch and the model hallucinates a plausible grocery item for a crop
     * that is just fridge shelf, with nothing to filter it out.
     */
    const val IDENTIFY_ITEM_TEMPLATE: String =
        """You identify a single grocery item from the photo. Common items include: milk, eggs, butter, cheese, yogurt, chicken, beef, lettuce, tomatoes, onions, carrots, bread, rice, pasta, cereal, juice, soda, ketchup, mustard, olive oil, salt, pepper, ice cream, frozen pizza. If no grocery item is clearly visible, use name "unknown" with confidence 0. Text found on the packaging: {{OCR_TEXT}}. Respond ONLY with JSON, no other words: {"name": "...", "brand": "... or null", "category": "produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other", "confidence": 0-1}"""

    /**
     * Whole-image (or whole-tile) pass; returns a **JSON array** of `IdentifiedItem`.
     *
     * Verbatim from `shared/prompts/identify-all-items.md`. Rendered with
     * [OCR_PLACEHOLDER] = [NO_OCR_TEXT] it is **491 characters**, no trailing
     * newline.
     *
     * Used for the first call of every photo and for every crop that came from the
     * tile-grid fallback (a tile is a large region that legitimately holds several
     * items).
     *
     * The vocabulary anchor is kept **identical** to [IDENTIFY_ITEM_TEMPLATE] on
     * purpose: the two passes must agree on naming or the name+brand dedupe between
     * them stops working. The literal `[{...}]` shape is what makes the model emit a
     * top-level array rather than an object with an `items` key — and an object
     * reply is a hard miss, since the parser's array path requires a genuine
     * top-level `[`.
     */
    const val IDENTIFY_ALL_ITEMS_TEMPLATE: String =
        """List ALL food and grocery items visible in this photo. Common items include: milk, eggs, butter, cheese, yogurt, chicken, beef, lettuce, tomatoes, onions, carrots, bread, rice, pasta, cereal, juice, soda, ketchup, mustard, olive oil, salt, pepper, ice cream, frozen pizza. Text found in the image: {{OCR_TEXT}}. Respond ONLY with a JSON array, no other words: [{"name": "...", "brand": "... or null", "category": "produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other", "confidence": 0-1}]"""

    /**
     * Render the OCR substitution: trim, and substitute the literal [NO_OCR_TEXT]
     * when the trimmed result is empty. Port of `GemmaLLMService.swift:92-93` and
     * `:114-115`.
     */
    fun renderOcrText(ocrText: String): String =
        ocrText.trim().ifEmpty { NO_OCR_TEXT }

    /** [IDENTIFY_ITEM_TEMPLATE] with the OCR text substituted. */
    fun identifyItem(ocrText: String): String =
        IDENTIFY_ITEM_TEMPLATE.replace(OCR_PLACEHOLDER, renderOcrText(ocrText))

    /** [IDENTIFY_ALL_ITEMS_TEMPLATE] with the OCR text substituted. */
    fun identifyAllItems(ocrText: String): String =
        IDENTIFY_ALL_ITEMS_TEMPLATE.replace(OCR_PLACEHOLDER, renderOcrText(ocrText))

    /**
     * Runtime parameters for the llama.cpp context these prompts run in.
     *
     * Tuned values, not library defaults. `shared/runtime/model-files.md` carries
     * the measurements behind each one.
     */
    object InferenceParams {

        /**
         * Logical batch size. **512. NEVER LOWER THIS.**
         *
         * It looks like a memory lever and is not. `batch` must be at least one
         * image's vision-token chunk; Gemma 4 emits roughly 256 image tokens per
         * 896x896 image. At 256 (shipped in builds 5 and 7) **every image chunk
         * failed to evaluate and scans found nothing** — recognition broke completely
         * while the app appeared to work, which is the worst failure mode this
         * project has. Commit `af11e8c`: "Fix empty scans on build 5: batch back to
         * 512, image vision chunks must fit one batch."
         *
         * On Android it is also a crash guard. `llama-context.cpp` checks the batch
         * against the image token count with `GGML_ASSERT`, which **aborts the
         * process** rather than raising a catchable error. The JNI shim therefore
         * pre-flights the token count and rejects the call before the assert can
         * fire, but the first line of defence is simply not lowering this number.
         */
        const val BATCH: Int = 512

        /**
         * The contract context size: **1024 tokens**, shared by prompt, ~256 vision
         * tokens, and the generated reply.
         *
         * This is what iOS ships and what both prompt documents are written against.
         * It went 2048 -> 1536 -> 1024 on iOS as escalation lever 1 against a field
         * crash, because KV cache there is **wired** Metal memory.
         */
        const val CONTEXT_CONTRACT: Int = 1024

        /**
         * The default context this port uses. Deliberately [CONTEXT_CONTRACT].
         *
         * See [CONTEXT_ANDROID] for the divergence that is *available* but not taken.
         */
        const val CONTEXT_DEFAULT: Int = CONTEXT_CONTRACT

        /**
         * A candidate Android-only context of **2048**. **NOT the default. Do not
         * make it the default without the measurement named below.**
         *
         * The argument for it: 1024 minus ~256 image tokens minus ~140 prompt tokens
         * leaves under 630 for a fully enumerated inventory array, with no auto-grow.
         * The iOS reason for 1024 was to shrink *wired* KV cache, and Android has no
         * wired ceiling — inference is CPU-only, the weights are file-backed and
         * evictable, and KV at 2048 on CPU is tens of megabytes.
         *
         * Why it is still not the default: `shared/README.md` rule 4 makes a memory
         * decision something you make explicitly and record, never something you
         * drift into. Flipping it requires an on-device PSS measurement, and that
         * measurement is **BLOCKED, not pending** — no 8 GB-or-larger Android device
         * is available, and the 6 GB Galaxy XCover6 Pro on hand cannot host the
         * model at all. See `docs/PORTING_NOTES.md`, section *Available test
         * hardware*. Do not substitute an estimate for the measurement.
         */
        const val CONTEXT_ANDROID: Int = 2048

        /**
         * Sampling temperature. Low on purpose: this is an extraction task, not a
         * creative one.
         */
        const val TEMPERATURE: Float = 0.2f

        /** Top-k sampling cutoff. */
        const val TOP_K: Int = 40

        /** Top-p (nucleus) sampling cutoff. */
        const val TOP_P: Float = 0.95f

        /**
         * Hard cap on generated tokens per call: **512**.
         *
         * A **port-level decision**, resolving a `TODO(unresolved)` in both prompt
         * documents. iOS configured no cap and no stop strings: it simply drained the
         * token stream to completion, which was tolerable on a GPU backend answering
         * in ~20 s. On Android inference is CPU-only, so an un-terminated generation
         * is an **unbounded hang** with no upper bound the user can wait out — the
         * same class of problem as an uncancellable native call.
         *
         * 512 is sized to [CONTEXT_CONTRACT]: 1024 total minus ~256 image tokens
         * minus ~140 prompt tokens leaves ~630, so a 512-token reply still fits
         * without evicting the prompt, while being far longer than any well-formed
         * reply these prompts ask for (a single-item object, or a modest array).
         * Hitting the cap means the model was rambling and the parser was going to
         * salvage or fail anyway.
         */
        const val MAX_PREDICT_TOKENS: Int = 512
    }
}
