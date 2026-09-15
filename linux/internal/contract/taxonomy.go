package contract

import "strings"

// FoodCategory is the closed set of food buckets used across the app.
//
// Source of record: DinnerDecider/Models/InventoryItem.swift:7-48 and
// shared/taxonomy/categories.md. Nine values. The RAW STRING is the wire and
// storage form; the Go constant name happens to match it in every case, but
// ports must serialize the string, never an ordinal or an index.
//
// The fallback to Other is silent by design, which has a consequence worth
// stating plainly: a port with a typo in one value (say "beverages") fails no
// parsing test. It simply files every drink under Other, forever, and nobody
// notices until someone looks at the inventory screen. The nine strings below
// are copied literally from the table in categories.md.
type FoodCategory string

// The nine categories, in CaseIterable order — which is also the order the UI
// iterates.
const (
	CategoryProduce   FoodCategory = "produce"
	CategoryDairy     FoodCategory = "dairy"
	CategoryMeat      FoodCategory = "meat"
	CategoryPantry    FoodCategory = "pantry"
	CategorySnack     FoodCategory = "snack"
	CategoryBeverage  FoodCategory = "beverage"
	CategoryCondiment FoodCategory = "condiment"
	CategoryFrozen    FoodCategory = "frozen"
	CategoryOther     FoodCategory = "other"
)

// AllFoodCategories lists the nine categories in CaseIterable order.
var AllFoodCategories = []FoodCategory{
	CategoryProduce,
	CategoryDairy,
	CategoryMeat,
	CategoryPantry,
	CategorySnack,
	CategoryBeverage,
	CategoryCondiment,
	CategoryFrozen,
	CategoryOther,
}

// foodCategoryByRawValue indexes AllFoodCategories by raw value. Matching is
// exact: no trimming, no fuzzy matching, no synonym table. Do not add any.
var foodCategoryByRawValue = func() map[string]FoodCategory {
	byRaw := make(map[string]FoodCategory, len(AllFoodCategories))
	for _, c := range AllFoodCategories {
		byRaw[string(c)] = c
	}
	return byRaw
}()

// String returns the raw value. This is what gets persisted and put on the wire.
func (c FoodCategory) String() string { return string(c) }

// DisplayName is UI copy, and it is allowed to disagree with the raw value.
//
// Three display names are plural where the raw value is singular: snack ->
// "Snacks", beverage -> "Beverages", condiment -> "Condiments". Do NOT
// regularise this. The raw value is persisted data; the display name is copy.
//
// An unrecognized value displays as "Other", matching the enum's own fallback.
func (c FoodCategory) DisplayName() string {
	switch c {
	case CategoryProduce:
		return "Produce"
	case CategoryDairy:
		return "Dairy"
	case CategoryMeat:
		return "Meat"
	case CategoryPantry:
		return "Pantry"
	case CategorySnack:
		return "Snacks"
	case CategoryBeverage:
		return "Beverages"
	case CategoryCondiment:
		return "Condiments"
	case CategoryFrozen:
		return "Frozen"
	default:
		return "Other"
	}
}

// FromModelString maps untrusted model output onto a FoodCategory.
//
// Mirrors LLMService.swift:14-16 — `FoodCategory(rawValue: category.lowercased())
// ?? .other`. The model frequently returns "Dairy" or "PRODUCE", so the value is
// lowercased first; matching is otherwise exact.
//
// It never errors and never drops the item. An unrecognized category is a
// display concern, not a validation failure: the item still enters the
// inventory, filed under Other.
func FromModelString(s string) FoodCategory {
	if c, ok := foodCategoryByRawValue[strings.ToLower(s)]; ok {
		return c
	}
	return CategoryOther
}

// FromStorage maps a persisted categoryRaw string onto a FoodCategory.
//
// Mirrors InventoryItem.swift:65-68 — `FoodCategory(rawValue: categoryRaw) ??
// .other`. It deliberately does NOT lowercase: the value it reads was written by
// the enum's own raw value and is already canonical.
//
// That asymmetry against FromModelString is the contract, not an oversight.
// Normalization belongs at the boundary where untrusted model text enters, not
// deeper. It stays safe because confirming an IdentifiedItem into inventory
// persists FoodCategory's raw value, never the model's original string — so a
// mixed-case string never reaches the store. A port that lowercases here would
// paper over exactly the bug that rule is meant to expose.
//
// It never errors and never drops the row.
func FromStorage(s string) FoodCategory {
	if c, ok := foodCategoryByRawValue[s]; ok {
		return c
	}
	return CategoryOther
}

// FoodCategory maps this item's raw model category onto the typed enum.
// Untrusted text, so it lowercases: see FromModelString.
func (i IdentifiedItem) FoodCategory() FoodCategory {
	return FromModelString(i.Category)
}

// Category is the typed accessor over the stored raw category string.
// Already-canonical text, so it does not lowercase: see FromStorage.
func (i InventoryItem) Category() FoodCategory {
	return FromStorage(i.CategoryRaw)
}

// SetCategory writes back the canonical raw value. This is what keeps
// FromStorage safe.
func (i *InventoryItem) SetCategory(c FoodCategory) {
	i.CategoryRaw = string(c)
}
