package com.philwoolley.dinnerdecider.contract

/**
 * Pure selection logic for the on-device model files: which GGUF weights file and
 * which vision projector to use, given a list of candidate filenames.
 *
 * Port of the static half of `DinnerDecider/Services/ModelFileLocator.swift`;
 * contract of record `shared/runtime/model-files.md`. The filesystem half (search
 * directories, existence checks) is a platform concern and lives outside this
 * file - deliberately, because the Android search order **diverges** from iOS for
 * security reasons and that divergence belongs with the code that touches disk.
 *
 * The runtime needs **both** files. Either one missing means no vision-capable
 * model and the app falls back to canned data with a visible badge, rather than
 * failing.
 */
object ModelFileSelection {

    /**
     * Prefix every acceptable weights file must start with.
     *
     * The prefix match is **case-sensitive** while the `.gguf` suffix check and the
     * `mmproj` exclusion are **case-insensitive**. That asymmetry is inherited from
     * `hasPrefix("gemma-4-E4B-it")` in Swift and is kept deliberately: `E4B` is part
     * of the model's real name, so a candidate spelled `gemma-4-e4b-it-...` is not
     * the file this runtime expects, while `.GGUF` from a case-preserving transfer
     * plainly is the same file. Tested explicitly, because "tidying" either half
     * changes which file loads.
     *
     * The prefix also means fine-tuned builds are automatically eligible as long as
     * they are named `gemma-4-E4B-it-finetuned-*.gguf` (keep the stock mmproj  - 
     * vision layers are frozen in training).
     */
    const val MODEL_PREFIX: String = "gemma-4-E4B-it"

    /**
     * Built-in weights preference, most-preferred first, used when the user has not
     * pinned a file.
     *
     * This is a **memory ranking, not a quality ranking**. Smaller quants win
     * because the iOS runtime held the whole model GPU-resident, so a smaller
     * weights file was directly less wired Metal memory - the thing that caused the
     * on-device jetsam.
     *
     * - `UD-IQ3_XXS` (3.46 GiB): saves ~0.82 GiB of wired vs Q4_0, emitted the
     *   cleanest and most compact JSON, and decoded *faster* than Q3_K_S on Apple
     *   Metal (10.3 vs 8.8 tok/s), so the usual "IQ is slow on Metal" caveat did not
     *   hold on that hardware. Preferred.
     * - `Q3_K_S` (3.60 GiB): saves ~0.68 GiB, also correct. The fallback.
     */
    val DEFAULT_MODEL_PREFERENCE: List<String> = listOf(
        "gemma-4-E4B-it-UD-IQ3_XXS.gguf",
        "gemma-4-E4B-it-Q3_K_S.gguf",
    )

    /** Preferred vision projector filename (the good, post-June-4 Q8_0 build). */
    const val PREFERRED_MMPROJ_NAME: String = "mmproj-gemma-4-E4B-it-Q8_0.gguf"

    /**
     * Preference key holding the user's pinned weights filename.
     *
     * A pin **survives app updates**, which is how a fine-tuned build stays selected
     * - and also how a stale Q4_0 pin kept jetsamming every load on iOS. Whatever
     * surfaces this must offer a one-tap clear.
     */
    const val SELECTED_MODEL_KEY: String = "selectedModelFileName"

    /** Whether a filename is an acceptable vision projector (`mmproj*.gguf`). */
    fun isMmproj(name: String): Boolean {
        val lower = name.lowercase()
        return lower.startsWith("mmproj") && lower.endsWith(".gguf")
    }

    /**
     * Choose the weights file from a list of candidate filenames.
     *
     * Eligible candidates start with [MODEL_PREFIX] (case-sensitively), end in
     * `.gguf` (case-insensitively) and do not contain `mmproj` (case-insensitively).
     * Selection order:
     * 1. the user's explicitly pinned [preferred] file, if it is present - **a pin
     *    always wins, even when it selects the memory hog**, because that is how a
     *    fine-tuned build is chosen from Settings;
     * 2. else the first available entry of [DEFAULT_MODEL_PREFERENCE];
     * 3. else the first eligible candidate - in candidate-list order, which is
     *    directory-enumeration order and therefore **not deterministic across
     *    platforms**. Do not depend on it;
     * 4. else null.
     *
     * A pin naming a file that is not present is ignored and falls through to 2-3.
     */
    fun chooseModelFile(candidates: List<String>, preferred: String?): String? {
        val eligible = candidates.filter {
            it.startsWith(MODEL_PREFIX) &&
                it.lowercase().endsWith(".gguf") &&
                !it.lowercase().contains("mmproj")
        }
        if (preferred != null && preferred in eligible) {
            return preferred
        }
        DEFAULT_MODEL_PREFERENCE.firstOrNull { it in eligible }?.let { return it }
        return eligible.firstOrNull()
    }

    /**
     * Choose the projector file from candidates, preferring the Q8_0 build.
     *
     * Both the prefix and suffix checks are **case-insensitive** here, unlike the
     * weights prefix. There is no user pin for the projector.
     */
    fun chooseMmprojFile(candidates: List<String>): String? {
        val eligible = candidates.filter(::isMmproj)
        if (PREFERRED_MMPROJ_NAME in eligible) {
            return PREFERRED_MMPROJ_NAME
        }
        return eligible.firstOrNull()
    }
}
