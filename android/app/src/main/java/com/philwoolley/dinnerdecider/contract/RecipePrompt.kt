package com.philwoolley.dinnerdecider.contract

/**
 * The only two things the prompt needs from a taste profile.
 *
 * - [hasContent] gates whether the fragment is included **at all**. It must be
 *   true if any of the six pill lists is non-empty **or** any of the six "other"
 *   free-text fields is non-blank. An earlier iOS version checked only
 *   favorites/dislikes and silently dropped allergies-only profiles — logged in
 *   the source as a **safety** bug, not a cosmetic one.
 * - [promptFragment] is the verbatim block described in
 *   `shared/prompts/recipes.md`, whose spice line is **always** emitted.
 */
interface TastePromptSource {
    val hasContent: Boolean

    fun promptFragment(): String
}

/**
 * Assembles the text-only `recipes` prompt.
 *
 * Port of `AppModel.recipePrompt(itemNames:prefs:mood:tasteProfile:)`
 * (`DinnerDecider/Services/AppModel.swift:513-558`), specified by
 * `shared/prompts/recipes.md` and pinned by
 * `DinnerDeciderTests/RecipePromptTests.swift` (8 tests).
 *
 * A **pure function of its arguments** with no global reads — `nonisolated
 * static` on iOS, an `object` function here, zero `android.*` imports. Its tests
 * depend on that.
 */
object RecipePrompt {


    /**
     * Build the prompt.
     *
     * Lines are joined with `\n` in exactly this order — the taste fragment sits
     * **before** the JSON schema clause "so it reads as context, not an
     * afterthought" (`testPromptIncludesTasteProfileAllergyLineBeforeSchema`):
     *
     * 1. `You are a helpful home cooking assistant.` — always
     * 2. `Inventory: {comma list}.` — always (may be empty, producing `Inventory: .`)
     * 3. mood line — when the trimmed mood is non-empty
     * 4. taste fragment — when a profile exists and `hasContent`
     * 5. `Diet: ...` — when `diet != "none"`
     * 6. `Avoid (allergies): ...` — when trimmed non-empty
     * 7. `Preferred cuisines: ...` — when trimmed non-empty
     * 8. portion line — when [InventoryLogic.portionLine] returns non-null
     * 9. the real-dishes clause — always
     * 10. the JSON schema clause — always
     *
     * The inventory is a **comma list, not prose**, deliberately: the whole call
     * shares a small runtime context with the reply, and `Inventory: Eggs, Milk,
     * Cheese.` is the asserted shape.
     *
     * Lines 9 and 10 are assembled by **concatenation with single spaces at the
     * join points**. Each is a single line — do not wrap them, and do not let a
     * formatter turn a join point into a newline.
     */
    fun build(
        itemNames: List<String>,
        prefs: RecipePreferences,
        mood: String = "",
        tasteProfile: TastePromptSource? = null,
    ): String {
        val inventory = itemNames.joinToString(separator = ", ")

        val lines = mutableListOf<String>()
        lines.add("You are a helpful home cooking assistant.")
        lines.add("Inventory: $inventory.")

        val trimmedMood = mood.trim()
        if (trimmedMood.isNotEmpty()) {
            lines.add("The user says: \"$trimmedMood\". Tailor your recipe suggestions to match this mood or craving.")
        }
        if (tasteProfile != null && tasteProfile.hasContent) {
            lines.add(tasteProfile.promptFragment())
        }
        if (prefs.diet != DietPreference.NONE.raw) {
            lines.add("Diet: ${prefs.diet}.")
        }
        if (prefs.allergies.trim().isNotEmpty()) {
            lines.add("Avoid (allergies): ${prefs.allergies}.")
        }
        if (prefs.cuisines.trim().isNotEmpty()) {
            lines.add("Preferred cuisines: ${prefs.cuisines}.")
        }
        InventoryLogic.portionLine(householdSize = prefs.householdSize)?.let { lines.add(it) }

        // The real-dishes clause. A deliberate bug fix (commit 45b99c3): the
        // prior wording told the model that makeNow "uses only inventory items",
        // so it worked backwards from the inventory and fabricated a plausible
        // dish name to fit it — the field-reported "cheesy Italian dip" from
        // ketchup and cheddar. Pick a REAL dish first, then mark honestly which
        // of its genuine ingredients the user has. The salt/pepper/water/oil
        // sentence stops staples being listed as missing, which made every dish
        // look unmakeable.
        lines.add(
            "Suggest real, well-known dishes only, with their genuine standard ingredients. " +
                "Never invent a dish or force a dish to use only the inventory. " +
                "Assume salt, pepper, water and cooking oil are always on hand.",
        )

        // The JSON schema clause. The literal skeleton (with empty-string and 0
        // placeholders) is more reliable than prose field descriptions, and
        // "almostThere":[...] is abbreviated on purpose to keep the prompt short.
        // "hasIt true only if it is in the inventory" is the honesty rule the
        // have/missing display and the add-missing-to-shopping-list button both
        // depend on.
        lines.add(
            "Respond ONLY with JSON of the form " +
                "{\"makeNow\":[{\"name\":\"\",\"ingredients\":[{\"name\":\"\",\"hasIt\":true}]," +
                "\"steps\":[\"\"],\"timeMinutes\":0,\"missingItems\":[]}],\"almostThere\":[...]}. " +
                "For every ingredient set hasIt true only if it is in the inventory. " +
                "makeNow is for dishes whose ingredients are all in the inventory. " +
                "almostThere is for dishes missing up to 3 ingredients; list each missing one in missingItems.",
        )

        return lines.joinToString(separator = "\n")
    }
}
