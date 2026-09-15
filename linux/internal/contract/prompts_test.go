package contract

import (
	"strconv"
	"strings"
	"testing"
)

// Ports of DinnerDeciderTests/RecipePromptTests.swift (8 tests) and
// DinnerDeciderTests/TasteProfileTests.swift (9 tests), plus the vision-prompt
// assertions that live in shared/prompts/*.md rather than in a Swift test file:
// the 558 and 491 rendered lengths, the single-line rule and the OCR sentinel.
//
// Helpers are prefixed `prompt` so they cannot collide with the sibling test
// files in this package.

// emDash is U+2014. House style forbids it in any emitted prompt text. It is
// written as an escape here so a grep over prompts.go stays a valid check.
const emDash = "\u2014"

// promptPrefs mirrors the Swift test helper, whose defaults are diet none, empty
// allergies and cuisines, and household 2.
func promptPrefs() RecipePreferences {
	return RecipePreferences{Diet: DietPreferenceNone, HouseholdSize: 2}
}

// MARK: - identify-item / identify-all-items

// The rendered length with OCR "none" is the cheap regression check recorded in
// shared/prompts/identify-item.md and identify-all-items.md. It catches a lost
// space, a doubled space or a dropped clause without anyone having to diff two
// 500 byte strings by eye.
func TestVisionPromptRenderedLengths(t *testing.T) {
	if got := len(IdentifyItemPrompt("none")); got != 558 {
		t.Errorf("len(IdentifyItemPrompt(\"none\")) = %d, want 558", got)
	}
	if got := len(IdentifyAllItemsPrompt("none")); got != 491 {
		t.Errorf("len(IdentifyAllItemsPrompt(\"none\")) = %d, want 491", got)
	}
}

// The Swift literals end every line in a backslash, so the rendered prompt is
// ONE LINE. A port that keeps the source line breaks would send a prompt that
// looks fine in a log and tokenizes differently.
func TestVisionPromptsAreOneLineWithNoTrailingNewline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prompt string
	}{
		{"identify-item", IdentifyItemPrompt("none")},
		{"identify-all-items", IdentifyAllItemsPrompt("none")},
		{"identify-item with OCR", IdentifyItemPrompt("Horizon Organic Whole Milk")},
		{"identify-all-items with OCR", IdentifyAllItemsPrompt("Horizon Organic Whole Milk")},
	} {
		if strings.Contains(tc.prompt, "\n") {
			t.Errorf("%s contains a newline: %q", tc.name, tc.prompt)
		}
		if strings.HasSuffix(tc.prompt, "\n") {
			t.Errorf("%s ends in a newline", tc.name)
		}
	}
}

// Byte-identical to shared/prompts/identify-item.md's verbatim template. If this
// fails, the prompt changed; decide deliberately, then update the doc and the
// length assertion together.
func TestIdentifyItemPromptIsByteIdentical(t *testing.T) {
	want := `You identify a single grocery item from the photo. ` +
		`Common items include: milk, eggs, butter, cheese, yogurt, chicken, beef, lettuce, tomatoes, onions, carrots, bread, rice, pasta, cereal, juice, soda, ketchup, mustard, olive oil, salt, pepper, ice cream, frozen pizza. ` +
		`If no grocery item is clearly visible, use name "unknown" with confidence 0. ` +
		`Text found on the packaging: none. ` +
		`Respond ONLY with JSON, no other words: ` +
		`{"name": "...", "brand": "... or null", "category": "produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other", "confidence": 0-1}`
	if got := IdentifyItemPrompt(""); got != want {
		t.Fatalf("identify-item prompt drifted.\n got: %q\nwant: %q", got, want)
	}
}

func TestIdentifyAllItemsPromptIsByteIdentical(t *testing.T) {
	want := `List ALL food and grocery items visible in this photo. ` +
		`Common items include: milk, eggs, butter, cheese, yogurt, chicken, beef, lettuce, tomatoes, onions, carrots, bread, rice, pasta, cereal, juice, soda, ketchup, mustard, olive oil, salt, pepper, ice cream, frozen pizza. ` +
		`Text found in the image: none. ` +
		`Respond ONLY with a JSON array, no other words: ` +
		`[{"name": "...", "brand": "... or null", "category": "produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other", "confidence": 0-1}]`
	if got := IdentifyAllItemsPrompt(""); got != want {
		t.Fatalf("identify-all-items prompt drifted.\n got: %q\nwant: %q", got, want)
	}
}

// An empty trimmed OCR result substitutes the literal "none", which keeps the
// sentence grammatical. OCR failures are swallowed into "" upstream, so this is
// the common path whenever tesseract is absent.
func TestVisionPromptsSubstituteNoneForEmptyOCR(t *testing.T) {
	reference := IdentifyItemPrompt("none")
	for _, ocr := range []string{"", " ", "   ", "\n", "\t\n ", "\r\n"} {
		if got := IdentifyItemPrompt(ocr); got != reference {
			t.Errorf("IdentifyItemPrompt(%q) did not substitute the none sentinel: %q", ocr, got)
		}
		if got, want := RenderOCRText(ocr), "none"; got != want {
			t.Errorf("RenderOCRText(%q) = %q, want %q", ocr, got, want)
		}
	}
}

// The OCR text is trimmed at the ENDS only, and the trimmed value is what is
// interpolated.
func TestVisionPromptsTrimOCRText(t *testing.T) {
	if got, want := RenderOCRText("  Horizon Milk \n"), "Horizon Milk"; got != want {
		t.Fatalf("RenderOCRText = %q, want %q", got, want)
	}
	if got := IdentifyItemPrompt("  Horizon Milk \n"); !strings.Contains(got, "Text found on the packaging: Horizon Milk. ") {
		t.Errorf("identify-item did not interpolate the trimmed OCR text: %q", got)
	}
	if got := IdentifyAllItemsPrompt(" Horizon Milk "); !strings.Contains(got, "Text found in the image: Horizon Milk. ") {
		t.Errorf("identify-all-items did not interpolate the trimmed OCR text: %q", got)
	}
}

// The escape hatch is what the pipeline's noise filter matches on. Without the
// sentence the model invents a plausible item for a crop of bare fridge shelf,
// and the filter has nothing to catch.
func TestIdentifyItemPromptKeepsTheUnknownEscapeHatch(t *testing.T) {
	got := IdentifyItemPrompt("none")
	if !strings.Contains(got, `use name "unknown" with confidence 0`) {
		t.Fatalf("identify-item lost the unknown escape hatch: %q", got)
	}
}

// The array prompt deliberately has NO escape hatch: [] already says "nothing
// here". The pipeline still filters unknown rows out of the array.
func TestIdentifyAllItemsPromptHasNoEscapeHatchAndDemandsAnArray(t *testing.T) {
	got := IdentifyAllItemsPrompt("none")
	if strings.Contains(got, "unknown") {
		t.Errorf("identify-all-items gained an unknown escape hatch: %q", got)
	}
	if !strings.Contains(got, "List ALL food and grocery items") {
		t.Errorf("identify-all-items lost its recall instruction: %q", got)
	}
	if !strings.Contains(got, "Respond ONLY with a JSON array, no other words: [{") {
		t.Errorf("identify-all-items lost the top-level array brackets: %q", got)
	}
}

// Both prompts spell out the closed FoodCategory set inline so the model picks
// from it rather than inventing a category that then falls back to "other".
func TestVisionPromptsCarryTheFullCategoryAlternation(t *testing.T) {
	const alternation = "produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other"
	if !strings.Contains(IdentifyItemPrompt("none"), alternation) {
		t.Error("identify-item lost the category alternation")
	}
	if !strings.Contains(IdentifyAllItemsPrompt("none"), alternation) {
		t.Error("identify-all-items lost the category alternation")
	}
}

// House style, applied to the vision prompts too.
func TestVisionPromptsNeverContainEmDash(t *testing.T) {
	if strings.Contains(IdentifyItemPrompt("Horizon Milk"), emDash) {
		t.Error("identify-item contains an em dash")
	}
	if strings.Contains(IdentifyAllItemsPrompt("Horizon Milk"), emDash) {
		t.Error("identify-all-items contains an em dash")
	}
}

// MARK: - recipes

func TestRecipePromptListsInventoryAsCommaList(t *testing.T) {
	prompt := RecipePrompt([]string{"Eggs", "Milk", "Cheese"}, promptPrefs(), "", nil)
	if !strings.Contains(prompt, "Inventory: Eggs, Milk, Cheese.") {
		t.Fatalf("inventory is not a comma list: %q", prompt)
	}
}

// Go-only: an empty inventory is not a special case; it renders "Inventory: ."
func TestRecipePromptRendersAnEmptyInventory(t *testing.T) {
	prompt := RecipePrompt(nil, promptPrefs(), "", nil)
	if !strings.Contains(prompt, "\nInventory: .\n") {
		t.Fatalf("empty inventory did not render as \"Inventory: .\": %q", prompt)
	}
}

func TestRecipePromptIncludesPortionScalingForHousehold(t *testing.T) {
	prefs := promptPrefs()
	prefs.HouseholdSize = 4
	prompt := RecipePrompt([]string{"Eggs"}, prefs, "", nil)
	if !strings.Contains(prompt, "serve 4") {
		t.Fatalf("portion line missing: %q", prompt)
	}
}

// Go-only: a household size below 1 omits the line entirely rather than emitting
// a blank or a nonsense "Cooking for 0 people".
func TestRecipePromptOmitsPortionLineForUnsetHousehold(t *testing.T) {
	prefs := promptPrefs()
	prefs.HouseholdSize = 0
	prompt := RecipePrompt([]string{"Eggs"}, prefs, "", nil)
	if strings.Contains(prompt, "Cooking for") {
		t.Fatalf("portion line should be omitted for household 0: %q", prompt)
	}
}

func TestRecipePromptOmitsDietWhenNone(t *testing.T) {
	prompt := RecipePrompt([]string{"Eggs"}, promptPrefs(), "", nil)
	if strings.Contains(prompt, "Diet:") {
		t.Fatalf("diet line present for DietPreference.none: %q", prompt)
	}
}

func TestRecipePromptIncludesDietAllergiesAndCuisines(t *testing.T) {
	prompt := RecipePrompt([]string{"Eggs"}, RecipePreferences{
		Diet:          "vegetarian",
		Allergies:     "peanuts",
		Cuisines:      "Thai",
		HouseholdSize: 2,
	}, "", nil)
	for _, want := range []string{
		"Diet: vegetarian.",
		"Avoid (allergies): peanuts.",
		"Preferred cuisines: Thai.",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestRecipePromptAlwaysDemandsJSON(t *testing.T) {
	prompt := RecipePrompt([]string{"Eggs"}, promptPrefs(), "", nil)
	for _, want := range []string{"Respond ONLY with JSON", "makeNow", "almostThere"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

// Field feedback: inventory-only generation produced fake dishes (a "cheesy
// Italian dip" from ketchup plus cheddar). The prompt must demand real dishes
// with genuine ingredients and honest hasIt marking against the inventory.
//
// The NEGATIVE assertion is the important half: it is the guard against someone
// helpfully restoring the pre-45b99c3 "uses only inventory items" constraint,
// which is what made the model work backwards from the inventory and fabricate a
// dish name to fit it.
func TestRecipePromptDemandsRealDishesWithHonestInventoryMarking(t *testing.T) {
	prompt := RecipePrompt([]string{"Eggs"}, promptPrefs(), "", nil)
	for _, want := range []string{
		"real, well-known dishes",
		"Never invent a dish",
		"hasIt true only if it is in the inventory",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "uses only inventory items") {
		t.Errorf("the inventory-only constraint is back; it is what invented the cheesy Italian dip:\n%s", prompt)
	}
}

// Go-only: the negative assertion must hold for every shape of prompt, not just
// the bare one, since the clause is assembled by concatenation.
func TestRecipePromptNeverSaysUsesOnlyInventoryItemsForAnyCombination(t *testing.T) {
	for _, tc := range promptCombinations() {
		if strings.Contains(tc.prompt, "uses only inventory items") {
			t.Errorf("%s: the inventory-only constraint is back:\n%s", tc.name, tc.prompt)
		}
	}
}

func TestRecipePromptNeverContainsEmDash(t *testing.T) {
	prompt := RecipePrompt([]string{"Eggs"}, RecipePreferences{
		Diet:          "keto",
		Allergies:     "shellfish",
		Cuisines:      "Italian",
		HouseholdSize: 3,
	}, "", nil)
	if strings.Contains(prompt, emDash) {
		t.Fatalf("prompt contains an em dash:\n%s", prompt)
	}
}

// Go-only: "for any preference combination", spelled out. Every conditional line
// on, off, and in combination, including all five spice levels.
func TestRecipePromptNeverContainsEmDashForAnyCombination(t *testing.T) {
	for _, tc := range promptCombinations() {
		if strings.Contains(tc.prompt, emDash) {
			t.Errorf("%s: prompt contains an em dash:\n%s", tc.name, tc.prompt)
		}
	}
}

// An allergies-only taste profile must reach the model, and the taste fragment
// must sit BEFORE the JSON schema instruction so it reads as context, not an
// afterthought.
func TestRecipePromptIncludesTasteProfileAllergyLineBeforeSchema(t *testing.T) {
	taste := TasteProfile{Allergies: []string{"Peanuts"}, SpiceLevel: 3}
	prompt := RecipePrompt([]string{"Eggs"}, promptPrefs(), "", &taste)

	if !strings.Contains(prompt, "Allergies: Peanuts. Never include these.") {
		t.Fatalf("prompt missing the allergy line:\n%s", prompt)
	}
	allergyAt := strings.Index(prompt, "Allergies: Peanuts.")
	schemaAt := strings.Index(prompt, "Respond ONLY with JSON")
	if allergyAt < 0 || schemaAt < 0 {
		t.Fatalf("prompt missing allergy or schema lines:\n%s", prompt)
	}
	if allergyAt >= schemaAt {
		t.Errorf("the taste fragment must precede the JSON schema clause (allergy at %d, schema at %d)", allergyAt, schemaAt)
	}
	// The real-dishes and schema guardrails must survive untouched.
	if !strings.Contains(prompt, "real, well-known dishes") {
		t.Errorf("the real-dishes clause was lost when a taste profile was added:\n%s", prompt)
	}
}

// Go-only: the exact assembly, every conditional line present, joined with "\n"
// in the documented order. This is the test that catches a reordering, which the
// substring assertions above cannot.
func TestRecipePromptAssemblyOrder(t *testing.T) {
	taste := TasteProfile{Allergies: []string{"Peanuts"}, SpiceLevel: 4}
	prompt := RecipePrompt(
		[]string{"Eggs", "Milk"},
		RecipePreferences{Diet: "keto", Allergies: "shellfish", Cuisines: "Italian", HouseholdSize: 3},
		"  something warm  ",
		&taste,
	)
	want := strings.Join([]string{
		"You are a helpful home cooking assistant.",
		"Inventory: Eggs, Milk.",
		`The user says: "something warm". Tailor your recipe suggestions to match this mood or craving.`,
		"Allergies: Peanuts. Never include these.",
		"Spice preference: spicy.",
		"Diet: keto.",
		"Avoid (allergies): shellfish.",
		"Preferred cuisines: Italian.",
		"Cooking for 3 people; scale ingredient amounts to serve 3.",
		recipeRealDishesClause,
		recipeJSONSchemaClause,
	}, "\n")
	if prompt != want {
		t.Fatalf("assembly drifted.\n got:\n%s\n\nwant:\n%s", prompt, want)
	}
}

// Go-only: the mood line is trimmed before interpolation and omitted when blank.
func TestRecipePromptMoodLine(t *testing.T) {
	for _, mood := range []string{"", "   ", "\n\t"} {
		if prompt := RecipePrompt([]string{"Eggs"}, promptPrefs(), mood, nil); strings.Contains(prompt, "The user says") {
			t.Errorf("mood %q produced a mood line:\n%s", mood, prompt)
		}
	}
	prompt := RecipePrompt([]string{"Eggs"}, promptPrefs(), "  comfort food\n", nil)
	if !strings.Contains(prompt, `The user says: "comfort food". Tailor your recipe suggestions to match this mood or craving.`) {
		t.Errorf("mood line missing or untrimmed:\n%s", prompt)
	}
}

// Go-only: no profile and an empty profile both omit the fragment, so a user who
// never opened the wizard does not get a bare spice line in every prompt.
func TestRecipePromptOmitsTasteFragmentWithoutContent(t *testing.T) {
	if prompt := RecipePrompt([]string{"Eggs"}, promptPrefs(), "", nil); strings.Contains(prompt, "Spice preference:") {
		t.Errorf("a nil profile produced a taste fragment:\n%s", prompt)
	}
	empty := TasteProfile{SpiceLevel: 3}
	if prompt := RecipePrompt([]string{"Eggs"}, promptPrefs(), "", &empty); strings.Contains(prompt, "Spice preference:") {
		t.Errorf("an empty profile produced a taste fragment:\n%s", prompt)
	}
}

// Go-only: the allergy and cuisine values are interpolated RAW; the trim only
// decides whether the line appears. Tidying them would not match the shipped
// prompt.
func TestRecipePromptEmitsRawAllergiesAndCuisines(t *testing.T) {
	prompt := RecipePrompt([]string{"Eggs"}, RecipePreferences{
		Diet:          DietPreferenceNone,
		Allergies:     " peanuts ",
		Cuisines:      " Thai ",
		HouseholdSize: 2,
	}, "", nil)
	if !strings.Contains(prompt, "Avoid (allergies):  peanuts .") {
		t.Errorf("allergies were trimmed before interpolation:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Preferred cuisines:  Thai .") {
		t.Errorf("cuisines were trimmed before interpolation:\n%s", prompt)
	}
}

// MARK: - TasteProfile.HasContent

// The regression that mattered: an allergies-only profile used to report "not
// complete" and was silently dropped from the prompt, which is a SAFETY issue.
func TestHasContentTrueForAllergiesOnly(t *testing.T) {
	profile := TasteProfile{Allergies: []string{"Peanuts"}}
	if !profile.HasContent() {
		t.Fatal("an allergies-only profile reported no content; that is the safety bug")
	}
}

func TestHasContentTrueForOtherTextOnly(t *testing.T) {
	profile := TasteProfile{FavoriteFoodsOther: "  ramen  "}
	if !profile.HasContent() {
		t.Fatal("a profile with only free text reported no content")
	}
}

func TestHasContentFalseWhenEmpty(t *testing.T) {
	// Default spice level alone is not "content".
	if (TasteProfile{SpiceLevel: 3}).HasContent() {
		t.Fatal("an untouched profile reported content")
	}
	if (TasteProfile{}).HasContent() {
		t.Fatal("the zero profile reported content")
	}
}

func TestHasContentFalseForWhitespaceOtherText(t *testing.T) {
	profile := TasteProfile{DislikedFoodsOther: "   "}
	if profile.HasContent() {
		t.Fatal("whitespace-only free text counted as content")
	}
}

// Go-only, and the point of the whole gate: ALL SIX pill lists and ALL SIX free
// text fields count. Checking fewer is how allergies-only profiles were silently
// dropped, which the source logs as a safety bug.
func TestHasContentCoversAllSixListsAndAllSixOtherFields(t *testing.T) {
	lists := map[string]func(*TasteProfile){
		"favoriteFoods":   func(p *TasteProfile) { p.FavoriteFoods = []string{"x"} },
		"dislikedFoods":   func(p *TasteProfile) { p.DislikedFoods = []string{"x"} },
		"cuisinesLoved":   func(p *TasteProfile) { p.CuisinesLoved = []string{"x"} },
		"cuisinesAvoided": func(p *TasteProfile) { p.CuisinesAvoided = []string{"x"} },
		"allergies":       func(p *TasteProfile) { p.Allergies = []string{"x"} },
		"texturesAvoided": func(p *TasteProfile) { p.TexturesAvoided = []string{"x"} },
	}
	others := map[string]func(*TasteProfile){
		"favoriteFoodsOther":   func(p *TasteProfile) { p.FavoriteFoodsOther = "x" },
		"dislikedFoodsOther":   func(p *TasteProfile) { p.DislikedFoodsOther = "x" },
		"cuisinesLovedOther":   func(p *TasteProfile) { p.CuisinesLovedOther = "x" },
		"cuisinesAvoidedOther": func(p *TasteProfile) { p.CuisinesAvoidedOther = "x" },
		"allergiesOther":       func(p *TasteProfile) { p.AllergiesOther = "x" },
		"texturesAvoidedOther": func(p *TasteProfile) { p.TexturesAvoidedOther = "x" },
	}
	if len(lists) != 6 || len(others) != 6 {
		t.Fatalf("this test must cover 6 lists and 6 other-fields, got %d and %d", len(lists), len(others))
	}
	for name, set := range lists {
		profile := TasteProfile{SpiceLevel: 3}
		set(&profile)
		if !profile.HasContent() {
			t.Errorf("pill list %q alone did not count as content", name)
		}
	}
	for name, set := range others {
		profile := TasteProfile{SpiceLevel: 3}
		set(&profile)
		if !profile.HasContent() {
			t.Errorf("free-text field %q alone did not count as content", name)
		}
	}
}

// MARK: - TasteProfileFragment

func TestTasteProfileFragmentJoinsPillsAndOtherText(t *testing.T) {
	profile := TasteProfile{
		FavoriteFoods:      []string{"Pizza", "Tacos"},
		FavoriteFoodsOther: "ramen",
		SpiceLevel:         3,
	}
	if got := TasteProfileFragment(profile); !strings.Contains(got, "Favorite foods: Pizza, Tacos, ramen.") {
		t.Fatalf("fragment did not join pills and free text:\n%s", got)
	}
}

func TestTasteProfileFragmentAllergyLinePresentWhenOnlyAllergiesSet(t *testing.T) {
	profile := TasteProfile{Allergies: []string{"Peanuts", "Shellfish"}, SpiceLevel: 3}
	if got := TasteProfileFragment(profile); !strings.Contains(got, "Allergies: Peanuts, Shellfish. Never include these.") {
		t.Fatalf("allergy line missing or reworded:\n%s", got)
	}
}

func TestTasteProfileFragmentOmitsEmptyCategories(t *testing.T) {
	profile := TasteProfile{Allergies: []string{"Peanuts"}, SpiceLevel: 3}
	got := TasteProfileFragment(profile)
	for _, unwanted := range []string{
		"Favorite foods:",
		"Foods the user dislikes:",
		"Cuisines they enjoy:",
		"Cuisines they avoid:",
		"Textures/flavors they avoid:",
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("fragment emitted an empty category %q:\n%s", unwanted, got)
		}
	}
}

// Even a completely empty profile still states a spice preference, which is why
// the fragment is never the empty string.
func TestTasteProfileFragmentAlwaysIncludesSpiceLine(t *testing.T) {
	if got := TasteProfileFragment(TasteProfile{}); !strings.Contains(got, "Spice preference:") {
		t.Fatalf("the zero profile lost its spice line: %q", got)
	}
	for level := -2; level <= 8; level++ {
		got := TasteProfileFragment(TasteProfile{SpiceLevel: level})
		if !strings.Contains(got, "Spice preference:") {
			t.Errorf("spice level %d lost the spice line: %q", level, got)
		}
	}
}

// Go-only: the wording table, including the out-of-range fallback. A blob from a
// future or corrupted write must still produce a usable prompt, never an error.
func TestTasteProfileFragmentSpiceWording(t *testing.T) {
	for _, tc := range []struct {
		level int
		want  string
	}{
		{1, "very mild, no spice at all"},
		{2, "mild, just a hint of spice"},
		{3, "medium spice"},
		{4, "spicy"},
		{5, "very spicy, the hotter the better"},
		{0, "medium spice"},
		{6, "medium spice"},
		{-1, "medium spice"},
		{99, "medium spice"},
	} {
		got := TasteProfileFragment(TasteProfile{SpiceLevel: tc.level})
		if want := "Spice preference: " + tc.want + "."; got != want {
			t.Errorf("spice level %d rendered %q, want %q", tc.level, got, want)
		}
	}
}

// Go-only: the exact fragment with every category on, joined with "\n" in order,
// with the spice line last.
func TestTasteProfileFragmentLineOrder(t *testing.T) {
	profile := TasteProfile{
		FavoriteFoods:        []string{"Pizza"},
		FavoriteFoodsOther:   "ramen",
		DislikedFoods:        []string{"Liver"},
		CuisinesLoved:        []string{"Italian"},
		CuisinesAvoided:      []string{"Fast food"},
		Allergies:            []string{"Peanuts"},
		AllergiesOther:       "kiwi",
		TexturesAvoided:      []string{"Mushy"},
		TexturesAvoidedOther: " slimy ",
		SpiceLevel:           5,
	}
	want := strings.Join([]string{
		"Favorite foods: Pizza, ramen.",
		"Foods the user dislikes: Liver. Avoid these.",
		"Cuisines they enjoy: Italian. Use these as flavor inspiration.",
		"Cuisines they avoid: Fast food. Avoid those styles.",
		"Allergies: Peanuts, kiwi. Never include these.",
		"Textures/flavors they avoid: Mushy, slimy.",
		"Spice preference: very spicy, the hotter the better.",
	}, "\n")
	if got := TasteProfileFragment(profile); got != want {
		t.Fatalf("fragment drifted.\n got:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestTasteProfileFragmentNeverContainsEmDash(t *testing.T) {
	profile := TasteProfile{
		FavoriteFoods:   []string{"Pizza"},
		DislikedFoods:   []string{"Liver"},
		CuisinesLoved:   []string{"Italian"},
		CuisinesAvoided: []string{"Fast food"},
		Allergies:       []string{"Peanuts"},
		TexturesAvoided: []string{"Mushy"},
		AllergiesOther:  "kiwi",
		SpiceLevel:      3,
	}
	if got := TasteProfileFragment(profile); strings.Contains(got, emDash) {
		t.Fatalf("fragment contains an em dash:\n%s", got)
	}
}

// Go-only: the fragment for every spice level, with and without categories,
// carries no em dash either.
func TestTasteProfileFragmentNeverContainsEmDashForAnyProfile(t *testing.T) {
	for level := 0; level <= 6; level++ {
		for _, profile := range []TasteProfile{
			{SpiceLevel: level},
			{Allergies: []string{"Peanuts"}, SpiceLevel: level},
			{FavoriteFoods: []string{"Pizza"}, DislikedFoodsOther: "liver", SpiceLevel: level},
		} {
			if got := TasteProfileFragment(profile); strings.Contains(got, emDash) {
				t.Errorf("spice %d: fragment contains an em dash:\n%s", level, got)
			}
		}
	}
}

// promptCase is one assembled recipe prompt plus a label for failure messages.
type promptCase struct {
	name   string
	prompt string
}

// promptCombinations builds a recipe prompt for every combination of the
// conditional lines: mood on/off, taste profile absent/empty/populated across
// the spice range, diet on/off, allergies on/off, cuisines on/off, and household
// sizes that both omit and emit the portion line.
func promptCombinations() []promptCase {
	moods := []string{"", "something warm"}
	tastes := []*TasteProfile{nil, {SpiceLevel: 3}}
	for level := 1; level <= 5; level++ {
		tastes = append(tastes, &TasteProfile{
			FavoriteFoods:        []string{"Pizza"},
			FavoriteFoodsOther:   "ramen",
			DislikedFoods:        []string{"Liver"},
			CuisinesLoved:        []string{"Italian"},
			CuisinesAvoided:      []string{"Fast food"},
			Allergies:            []string{"Peanuts"},
			AllergiesOther:       "kiwi",
			TexturesAvoided:      []string{"Mushy"},
			TexturesAvoidedOther: "slimy",
			SpiceLevel:           level,
		})
	}
	diets := []string{DietPreferenceNone, "vegetarian", "vegan", "pescatarian", "keto", "glutenFree"}
	allergies := []string{"", "shellfish"}
	cuisines := []string{"", "Italian"}
	households := []int{0, 1, 2, 4}
	inventories := [][]string{nil, {"Eggs"}, {"Eggs", "Milk", "Cheese"}}

	cases := make([]promptCase, 0, len(moods)*len(tastes)*len(diets)*len(allergies)*len(cuisines)*len(households)*len(inventories))
	for _, mood := range moods {
		for ti, taste := range tastes {
			for _, diet := range diets {
				for _, allergy := range allergies {
					for _, cuisine := range cuisines {
						for _, household := range households {
							for ii, inventory := range inventories {
								prefs := RecipePreferences{
									Diet:          diet,
									Allergies:     allergy,
									Cuisines:      cuisine,
									HouseholdSize: household,
								}
								cases = append(cases, promptCase{
									name: "mood=" + mood + " taste=" + strconv.Itoa(ti) +
										" diet=" + diet + " allergies=" + allergy +
										" cuisines=" + cuisine + " household=" + strconv.Itoa(household) +
										" inventory=" + strconv.Itoa(ii),
									prompt: RecipePrompt(inventory, prefs, mood, taste),
								})
							}
						}
					}
				}
			}
		}
	}
	return cases
}
