package contract

import "strings"

// This file holds the three prompts, byte-identical to the shipped iOS ones.
//
// Sources of record:
//   - identify-item and identify-all-items: DinnerDecider/Services/LLM/GemmaLLMService.swift,
//     specified by shared/prompts/identify-item.md and shared/prompts/identify-all-items.md.
//   - recipes: AppModel.recipePrompt(itemNames:prefs:mood:tasteProfile:) plus
//     TasteProfile.promptFragment() and InventoryLogic.portionLine(householdSize:),
//     specified by shared/prompts/recipes.md.
//
// Every function here is pure: no globals, no clock, no I/O. On iOS recipePrompt
// is "nonisolated static" for exactly that reason and its tests depend on it.
//
// Two rules govern edits to this file.
//
// 1. The prompts are NOT free text. The runtime context is 1024 tokens TOTAL,
//    shared by the prompt, roughly 256 vision tokens per image, and the reply.
//    That context is wired GPU memory, and it is what jetsammed the iOS app.
//    Lengthening a prompt spends the scarcest resource in the product.
//
// 2. House style forbids the U+2014 em dash in any emitted prompt text, for the
//    recipe prompt (RecipePromptTests.testPromptNeverContainsEmDash) and for the
//    taste fragment (TasteProfileTests.testPromptFragmentNeverContainsEmDash).
//    This file therefore avoids that character in its comments too, so a bare
//    grep over the file is a valid check.

// ocrNoneSentinel is substituted when the trimmed OCR text is empty.
//
// It keeps the sentence grammatical: "Text found on the packaging: none." reads
// as an answer, where "Text found on the packaging: ." reads as a truncation and
// measurably confuses the model.
const ocrNoneSentinel = "none"

// RenderOCRText trims the OCR result and substitutes the literal "none" when
// nothing is left.
//
// Swift trims with .whitespacesAndNewlines here, so strings.TrimSpace is the
// faithful equivalent. Note that only the ENDS are trimmed: OCR text containing
// an internal newline would put that newline inside the prompt, on iOS and here
// alike. The Linux OCR adapter joins its lines with a single space precisely so
// that cannot happen.
func RenderOCRText(ocrText string) string {
	trimmed := strings.TrimSpace(ocrText)
	if trimmed == "" {
		return ocrNoneSentinel
	}
	return trimmed
}

// The two vision prompts are split at the same points the Swift multi-line
// literals break. Every Swift line ends in a backslash, so the rendered prompt
// is ONE LINE with no embedded newlines; adjacent Go string literals joined with
// "+" reproduce that exactly. Never turn one of these joins into a newline.

const identifyItemPromptHead = `You identify a single grocery item from the photo. ` +
	`Common items include: milk, eggs, butter, cheese, yogurt, chicken, beef, lettuce, tomatoes, onions, carrots, bread, rice, pasta, cereal, juice, soda, ketchup, mustard, olive oil, salt, pepper, ice cream, frozen pizza. ` +
	`If no grocery item is clearly visible, use name "unknown" with confidence 0. ` +
	`Text found on the packaging: `

const identifyItemPromptTail = `. ` +
	`Respond ONLY with JSON, no other words: ` +
	`{"name": "...", "brand": "... or null", ` +
	`"category": "produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other", ` +
	`"confidence": 0-1}`

// IdentifyItemPrompt renders the single-crop prompt.
//
// Rendered length with OCR "none" is exactly 558 bytes, with no trailing
// newline. That number is the cheap regression check recorded in
// shared/prompts/identify-item.md and asserted by the tests.
//
// Three clauses are load-bearing and must not be trimmed as padding:
//
//   - The 25 item vocabulary anchor keeps the model on everyday grocery nouns
//     instead of hyper-specific product names it cannot actually read, and keeps
//     this prompt's naming identical to IdentifyAllItemsPrompt so the two passes
//     dedupe against each other (dedupe is a normalized name plus brand match).
//   - The "unknown" escape hatch is what the pipeline's noise filter matches on.
//     Without the sentence the model invents a plausible grocery item for a crop
//     that is just fridge shelf, and the filter has nothing to catch.
//   - "Respond ONLY with JSON, no other words:" plus the literal shape. Do NOT
//     replace this with a grammar or structured-output constraint: on a resident
//     model the grammar sampler never resets and the second call of a scan
//     returns zero tokens.
func IdentifyItemPrompt(ocrText string) string {
	return identifyItemPromptHead + RenderOCRText(ocrText) + identifyItemPromptTail
}

const identifyAllItemsPromptHead = `List ALL food and grocery items visible in this photo. ` +
	`Common items include: milk, eggs, butter, cheese, yogurt, chicken, beef, lettuce, tomatoes, onions, carrots, bread, rice, pasta, cereal, juice, soda, ketchup, mustard, olive oil, salt, pepper, ice cream, frozen pizza. ` +
	`Text found in the image: `

const identifyAllItemsPromptTail = `. ` +
	`Respond ONLY with a JSON array, no other words: ` +
	`[{"name": "...", "brand": "... or null", ` +
	`"category": "produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other", ` +
	`"confidence": 0-1}]`

// IdentifyAllItemsPrompt renders the whole-image (or whole-tile) prompt.
//
// Rendered length with OCR "none" is exactly 491 bytes, no trailing newline.
//
// Deliberate differences from IdentifyItemPrompt, none of them cosmetic:
//
//   - "ALL" is capitalised. This pass exists to catch items that fall BETWEEN
//     crop boxes, so its whole job is recall.
//   - "Text found in the image", not "on the packaging": a whole photo's OCR
//     spans many labels.
//   - No "unknown" escape hatch. The array form already lets the model answer
//     "nothing here" with []. The pipeline still applies the same unknown and
//     confidence filter to every element, because models emit unknown rows
//     anyway: port the filter, not the omission.
//   - The example is wrapped in array brackets. Those brackets are what make the
//     model emit a top-level array rather than an object with an "items" key,
//     and the parser's array path requires a genuine top-level "[".
func IdentifyAllItemsPrompt(ocrText string) string {
	return identifyAllItemsPromptHead + RenderOCRText(ocrText) + identifyAllItemsPromptTail
}

// DietPreferenceNone is the raw value of DietPreference.none, the sentinel that
// SUPPRESSES the diet line. It is a real stored value, not an absence, so the
// comparison is against this string and not against "".
const DietPreferenceNone = "none"

// RecipePreferences is the snapshot of user preferences the recipe prompt reads.
//
// Port of `struct RecipePreferences` (DinnerDecider/Services/Preferences.swift).
// A VALUE passed in, never read from storage here: that is what keeps
// RecipePrompt pure and its tests free of a settings store.
type RecipePreferences struct {
	// Diet is a DietPreference raw value: none, vegetarian, vegan, pescatarian,
	// keto or glutenFree. Anything other than DietPreferenceNone is emitted
	// verbatim, unvalidated.
	Diet string
	// Allergies is free text. Emitted RAW when its trimmed form is non-empty:
	// the trim decides inclusion, it does not clean the value.
	Allergies string
	// Cuisines is free text, with the same raw-emission rule as Allergies.
	Cuisines string
	// HouseholdSize drives PortionLine. Sizes below 1 omit the line.
	HouseholdSize int
}

// The two always-present closing clauses. Swift builds each by "+" concatenation
// with single spaces at the join points, and each is a SINGLE line. Do not wrap
// them and do not let a formatter turn a join point into a newline.

const recipeRealDishesClause = `Suggest real, well-known dishes only, with their genuine standard ingredients. ` +
	`Never invent a dish or force a dish to use only the inventory. ` +
	`Assume salt, pepper, water and cooking oil are always on hand.`

const recipeJSONSchemaClause = `Respond ONLY with JSON of the form ` +
	`{"makeNow":[{"name":"","ingredients":[{"name":"","hasIt":true}],` +
	`"steps":[""],"timeMinutes":0,"missingItems":[]}],"almostThere":[...]}. ` +
	`For every ingredient set hasIt true only if it is in the inventory. ` +
	`makeNow is for dishes whose ingredients are all in the inventory. ` +
	`almostThere is for dishes missing up to 3 ingredients; list each missing one in missingItems.`

// RecipePrompt assembles the text-only recipes prompt.
//
// taste is nil when no profile has been saved. Lines are joined with "\n" in
// exactly this order:
//
//	#1   "You are a helpful home cooking assistant."      always
//	#2   "Inventory: {comma list}."                       always
//	#3   the mood line                                    trimmed mood non-empty
//	#4   the taste fragment                               taste != nil && HasContent()
//	#5   "Diet: {diet}."                                  diet != DietPreferenceNone
//	#6   "Avoid (allergies): {allergies}."                trimmed allergies non-empty
//	#7   "Preferred cuisines: {cuisines}."                trimmed cuisines non-empty
//	#8   the portion line                                 PortionLine reports one
//	#9   the real-dishes clause                           always
//	#10  the JSON schema clause                           always
//
// ORDER IS ASSERTED, not incidental: the taste fragment sits BEFORE the JSON
// schema clause "so it reads as context, not an afterthought"
// (RecipePromptTests.testPromptIncludesTasteProfileAllergyLineBeforeSchema). For
// an allergies-only profile that ordering is the difference between the model
// reading the allergy as a constraint and reading it as a trailing note.
//
// The inventory is a COMMA LIST, not prose, deliberately: the whole call shares
// the small runtime context with its reply, and "Inventory: Eggs, Milk, Cheese."
// is the asserted shape. An empty inventory renders "Inventory: ." and is not a
// special case.
//
// THE REAL-DISHES CLAUSE IS A BUG FIX, NOT DECORATION (commit 45b99c3).
// TestFlight feedback 3: the generator invented a "cheesy Italian dip" from
// ketchup plus cheddar. The prior wording told the model that makeNow "uses only
// inventory items", so it worked backwards from the inventory and fabricated a
// plausible dish name to fit. The fix inverts the framing: pick a REAL dish
// first, then mark honestly which of its genuine ingredients the user has. The
// tests pin all four halves of that fix, including the negative assertion that
// the prompt must NOT contain "uses only inventory items". Anyone "helpfully"
// restoring the inventory-only constraint reintroduces the fake dishes.
func RecipePrompt(itemNames []string, prefs RecipePreferences, mood string, taste *TasteProfile) string {
	inventory := strings.Join(itemNames, ", ")

	lines := make([]string, 0, 10)
	lines = append(lines, "You are a helpful home cooking assistant.")
	lines = append(lines, "Inventory: "+inventory+".")

	// Mood is the one site Swift trims with .whitespacesAndNewlines, and it is
	// the one site whose TRIMMED value is interpolated.
	if trimmedMood := strings.TrimSpace(mood); trimmedMood != "" {
		lines = append(lines, `The user says: "`+trimmedMood+`". Tailor your recipe suggestions to match this mood or craving.`)
	}
	if taste != nil && taste.HasContent() {
		lines = append(lines, TasteProfileFragment(*taste))
	}
	if prefs.Diet != DietPreferenceNone {
		lines = append(lines, "Diet: "+prefs.Diet+".")
	}
	// Allergies and cuisines interpolate the RAW value; the trim only decides
	// whether the line appears at all. Emitting the trimmed value instead would
	// be tidier and would not match the shipped prompt.
	if trimSwiftWhitespace(prefs.Allergies) != "" {
		lines = append(lines, "Avoid (allergies): "+prefs.Allergies+".")
	}
	if trimSwiftWhitespace(prefs.Cuisines) != "" {
		lines = append(lines, "Preferred cuisines: "+prefs.Cuisines+".")
	}
	if portion, ok := PortionLine(prefs.HouseholdSize); ok {
		lines = append(lines, portion)
	}

	lines = append(lines, recipeRealDishesClause)
	lines = append(lines, recipeJSONSchemaClause)
	return strings.Join(lines, "\n")
}

// HasContent reports whether the user has answered anything at all in the taste
// wizard. It GATES whether the profile reaches the model.
//
// It must cover ALL SIX pill lists and ALL SIX free-text fields. An earlier iOS
// version checked only favorites and dislikes, which silently dropped
// allergies-only profiles: that is logged in the source as a SAFETY bug, not a
// cosmetic one, and TasteProfileTests.testHasContentTrueForAllergiesOnly locks
// it down. A port that checks fewer categories reintroduces the safety bug.
//
// SpiceLevel is deliberately NOT content. It always has a value (3 by default),
// so counting it would make every profile, including one the user never opened,
// report content and push a meaningless spice line into every prompt.
func (p TasteProfile) HasContent() bool {
	pillGroups := [][]string{
		p.FavoriteFoods,
		p.DislikedFoods,
		p.CuisinesLoved,
		p.CuisinesAvoided,
		p.Allergies,
		p.TexturesAvoided,
	}
	for _, pills := range pillGroups {
		if len(pills) > 0 {
			return true
		}
	}
	otherFields := []string{
		p.FavoriteFoodsOther,
		p.DislikedFoodsOther,
		p.CuisinesLovedOther,
		p.CuisinesAvoidedOther,
		p.AllergiesOther,
		p.TexturesAvoidedOther,
	}
	for _, other := range otherFields {
		if trimSwiftWhitespace(other) != "" {
			return true
		}
	}
	return false
}

// combinedTasteCategory joins a category's pills and its trimmed "other" text
// into one comma list, reporting false when the category is empty so the caller
// skips the line.
//
// Port of TasteProfile.combined(_:_:). The pills are used AS GIVEN; only the
// free-text field is trimmed.
func combinedTasteCategory(pills []string, other string) (string, bool) {
	parts := make([]string, 0, len(pills)+1)
	parts = append(parts, pills...)
	if trimmed := trimSwiftWhitespace(other); trimmed != "" {
		parts = append(parts, trimmed)
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, ", "), true
}

// TasteProfileFragment builds the block describing this user's taste to the
// model. Port of TasteProfile.promptFragment().
//
// Lines are joined with "\n", and each category line is emitted only when that
// category has content. The SPICE LINE IS ALWAYS EMITTED, even for an otherwise
// empty profile (TasteProfileTests.testPromptFragmentAlwaysIncludesSpiceLine),
// which is why the fragment is never the empty string.
//
// The allergy line is safety-relevant and carries its own imperative ("Never
// include these."), separate from the recipe prompt's "Avoid (allergies)" line,
// which comes from the free-text preference instead. Both can appear.
func TasteProfileFragment(p TasteProfile) string {
	lines := make([]string, 0, 7)
	if fav, ok := combinedTasteCategory(p.FavoriteFoods, p.FavoriteFoodsOther); ok {
		lines = append(lines, "Favorite foods: "+fav+".")
	}
	if dislikes, ok := combinedTasteCategory(p.DislikedFoods, p.DislikedFoodsOther); ok {
		lines = append(lines, "Foods the user dislikes: "+dislikes+". Avoid these.")
	}
	if loved, ok := combinedTasteCategory(p.CuisinesLoved, p.CuisinesLovedOther); ok {
		lines = append(lines, "Cuisines they enjoy: "+loved+". Use these as flavor inspiration.")
	}
	if avoided, ok := combinedTasteCategory(p.CuisinesAvoided, p.CuisinesAvoidedOther); ok {
		lines = append(lines, "Cuisines they avoid: "+avoided+". Avoid those styles.")
	}
	if allergy, ok := combinedTasteCategory(p.Allergies, p.AllergiesOther); ok {
		lines = append(lines, "Allergies: "+allergy+". Never include these.")
	}
	if textures, ok := combinedTasteCategory(p.TexturesAvoided, p.TexturesAvoidedOther); ok {
		lines = append(lines, "Textures/flavors they avoid: "+textures+".")
	}
	lines = append(lines, "Spice preference: "+spiceDescription(p.SpiceLevel)+".")
	return strings.Join(lines, "\n")
}

// spiceDescription maps SpiceLevel (1 to 5, default 3) onto its wording.
//
// Anything outside 1 to 5 falls back to "medium spice" rather than erroring:
// this is a prompt, and a profile deserialized from a future or corrupted blob
// must still produce a usable one.
func spiceDescription(level int) string {
	switch level {
	case 1:
		return "very mild, no spice at all"
	case 2:
		return "mild, just a hint of spice"
	case 3:
		return "medium spice"
	case 4:
		return "spicy"
	case 5:
		return "very spicy, the hotter the better"
	}
	return "medium spice"
}

// trimSwiftWhitespace trims Swift's CharacterSet.whitespaces, which is the space
// separators plus the tab, and NOTABLY NOT the line breaks.
//
// This exists because the taste profile and the allergy and cuisine preference
// checks trim with .whitespaces while the mood and the OCR text trim with
// .whitespacesAndNewlines. strings.TrimSpace would collapse that distinction and
// would, for instance, drop an allergies value of "\n" that the shipped app
// keeps. Nobody types a lone newline into a single-line text field, so the
// difference is unreachable in practice, but the package rule is that Swift
// wins, and matching it exactly costs ten lines.
func trimSwiftWhitespace(s string) string {
	return strings.TrimFunc(s, isSwiftWhitespace)
}

// isSwiftWhitespace reports whether r is in Swift's CharacterSet.whitespaces:
// U+0009 (tab) plus every Unicode Zs space separator. Line breaks (U+000A to
// U+000D, U+0085, U+2028, U+2029) are excluded on purpose.
func isSwiftWhitespace(r rune) bool {
	switch r {
	case '\t', ' ', '\u00A0', '\u1680', '\u202F', '\u205F', '\u3000':
		return true
	}
	// U+2000 to U+200A: en quad through hair space.
	return r >= '\u2000' && r <= '\u200A'
}
