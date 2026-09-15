package contract

import (
	"strings"
	"testing"
)

// TestFoodCategoryRawValues pins the nine raw values and their CaseIterable
// order. These strings are the wire and storage form; a typo here would not
// fail any parse test, it would just file everything under Other forever.
func TestFoodCategoryRawValues(t *testing.T) {
	want := []string{
		"produce", "dairy", "meat", "pantry", "snack",
		"beverage", "condiment", "frozen", "other",
	}
	if len(AllFoodCategories) != len(want) {
		t.Fatalf("want %d categories, got %d", len(want), len(AllFoodCategories))
	}
	for i, c := range AllFoodCategories {
		if c.String() != want[i] {
			t.Errorf("category %d: got %q want %q", i, c.String(), want[i])
		}
	}
}

// TestFoodCategoryPromptAlternationUnchanged guards the coupling
// categories.md calls out: the nine raw values appear in both vision prompts as
// a literal alternation, so adding or renaming one must change those prompt
// strings in the same commit or the model keeps emitting the old set.
func TestFoodCategoryPromptAlternationUnchanged(t *testing.T) {
	const want = "produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other"
	parts := make([]string, 0, len(AllFoodCategories))
	for _, c := range AllFoodCategories {
		parts = append(parts, c.String())
	}
	if got := strings.Join(parts, "|"); got != want {
		t.Errorf("prompt alternation changed:\n got %q\nwant %q\nUpdate both vision prompts in the same commit.", got, want)
	}
}

// TestFoodCategoryDisplayNames pins the three plural display names. The raw
// value is persisted data and the display name is UI copy; they are allowed to
// disagree, and regularising them would be a change to shipped copy.
func TestFoodCategoryDisplayNames(t *testing.T) {
	cases := []struct {
		category FoodCategory
		want     string
	}{
		{CategoryProduce, "Produce"},
		{CategoryDairy, "Dairy"},
		{CategoryMeat, "Meat"},
		{CategoryPantry, "Pantry"},
		{CategorySnack, "Snacks"},
		{CategoryBeverage, "Beverages"},
		{CategoryCondiment, "Condiments"},
		{CategoryFrozen, "Frozen"},
		{CategoryOther, "Other"},
	}
	for _, tc := range cases {
		if got := tc.category.DisplayName(); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.category, got, tc.want)
		}
	}

	// Stated the other way round so a "tidy-up" that singularises them fails
	// loudly rather than quietly.
	if CategorySnack.DisplayName() == "Snack" {
		t.Error(`snack must display as "Snacks", not "Snack"`)
	}
	if CategoryBeverage.DisplayName() == "Beverage" {
		t.Error(`beverage must display as "Beverages", not "Beverage"`)
	}
	if CategoryCondiment.DisplayName() == "Condiment" {
		t.Error(`condiment must display as "Condiments", not "Condiment"`)
	}
}

func TestFoodCategoryFromModelString(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  FoodCategory
	}{
		{"exact", "dairy", CategoryDairy},
		{"title case", "Dairy", CategoryDairy},
		{"upper case", "PRODUCE", CategoryProduce},
		{"mixed case", "CoNdImEnT", CategoryCondiment},
		{"unrecognized", "beverages", CategoryOther},
		{"empty", "", CategoryOther},
		{"leading space is not trimmed", " dairy", CategoryOther},
		{"trailing space is not trimmed", "dairy ", CategoryOther},
		{"no synonyms", "vegetables", CategoryOther},
		{"no fuzzy matching", "dairy products", CategoryOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FromModelString(tc.input); got != tc.want {
				t.Errorf("FromModelString(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}

	// Every raw value round-trips through its own upper-cased form.
	for _, c := range AllFoodCategories {
		if got := FromModelString(strings.ToUpper(c.String())); got != c {
			t.Errorf("FromModelString(%q) = %q, want %q", strings.ToUpper(c.String()), got, c)
		}
	}
}

func TestFoodCategoryFromStorage(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  FoodCategory
	}{
		{"exact", "dairy", CategoryDairy},
		{"unrecognized", "beverages", CategoryOther},
		{"empty", "", CategoryOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FromStorage(tc.input); got != tc.want {
				t.Errorf("FromStorage(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}

	for _, c := range AllFoodCategories {
		if got := FromStorage(c.String()); got != c {
			t.Errorf("FromStorage(%q) = %q, want %q", c, got, c)
		}
	}
}

// TestFoodCategoryCaseSensitivityAsymmetry is the whole point of having two
// mapping functions. FromModelString lowercases because it reads untrusted
// model text; FromStorage does not, because it reads a value the enum itself
// wrote. Making them symmetric in either direction is a contract break.
func TestFoodCategoryCaseSensitivityAsymmetry(t *testing.T) {
	const mixedCase = "Dairy"

	if got := FromModelString(mixedCase); got != CategoryDairy {
		t.Errorf("FromModelString(%q) = %q, want %q: model text MUST be lowercased", mixedCase, got, CategoryDairy)
	}
	if got := FromStorage(mixedCase); got != CategoryOther {
		t.Errorf("FromStorage(%q) = %q, want %q: storage must NOT be lowercased", mixedCase, got, CategoryOther)
	}
	if FromModelString(mixedCase) == FromStorage(mixedCase) {
		t.Fatal("the two mappings must disagree on mixed-case input; symmetry here is the bug")
	}

	// The asymmetry is only safe because confirming an item into inventory
	// persists the canonical raw value, so a mixed-case string never reaches
	// the store in the first place.
	scanned := IdentifiedItem{Name: "Whole Milk", Category: mixedCase, Confidence: 0.9}
	row := InventoryItem{Name: scanned.Name}
	row.SetCategory(scanned.FoodCategory())

	if row.CategoryRaw != "dairy" {
		t.Errorf("stored categoryRaw = %q, want %q: persist the canonical raw value, not the model's string", row.CategoryRaw, "dairy")
	}
	if got := row.Category(); got != CategoryDairy {
		t.Errorf("round-trip through storage = %q, want %q", got, CategoryDairy)
	}
}

// TestFoodCategoryNeverErrorsOrDropsItems states the normative rule directly:
// an unrecognized category is a display concern, not a validation failure.
func TestFoodCategoryNeverErrorsOrDropsItems(t *testing.T) {
	junk := []string{"", " ", "🍎", "DAIRY PRODUCTS", "null", "123", strings.Repeat("x", 4096)}
	for _, s := range junk {
		if got := FromModelString(s); got != CategoryOther {
			t.Errorf("FromModelString(%q) = %q, want other", s, got)
		}
		if got := FromStorage(s); got != CategoryOther {
			t.Errorf("FromStorage(%q) = %q, want other", s, got)
		}
	}

	// A junk category still decodes into a usable item: nothing is dropped.
	item, ok := UnmarshalIdentifiedItem([]byte(`{"name": "Mystery Jar", "brand": null, "category": "condiments!", "confidence": 0.4}`))
	if !ok {
		t.Fatal("an unrecognized category must not fail the decode")
	}
	if item.FoodCategory() != CategoryOther {
		t.Errorf("got %q, want other", item.FoodCategory())
	}
	if item.Name != "Mystery Jar" {
		t.Errorf("got %q, want Mystery Jar", item.Name)
	}
}
