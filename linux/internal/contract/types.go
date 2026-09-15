// Package contract holds the cross-platform DinnerDecider contract: the wire
// and persisted types, the food taxonomy, and (in later steps) the pure parity
// core — parser, model lifecycle, scan budget, scan outcome and logic helpers.
//
// Everything here is a faithful port of the shipped iOS behavior as captured in
// ../../../shared. Where Go and Swift disagree, Swift wins and the divergence is
// documented at the point it is bridged. Nothing in this package performs I/O.
package contract

import (
	"encoding/json"
	"time"
)

// ScanPhase is where the scan pipeline currently is, as reported to the UI.
//
// Six values, mirroring `enum ScanPhase` in
// DinnerDecider/Services/AppModel.swift. `Failed` is NOT optional and NOT a
// Linux invention: it shipped on iOS in b9268ef to close the TestFlight P2
// report where every model call throwing looked exactly like a scan of an empty
// fridge, and the UI told the user to add more light.
//
// The constants are deliberately unprefixed. They are the names the rest of the
// contract package spells out — contract.TerminalPhase returns `Finished`,
// `Failed` or `Empty` — and the plan's pseudocode uses them bare.
//
// The raw string is the wire form (the /api/scan/{id} snapshot's `phase` field).
// Serialize the string, never an ordinal.
type ScanPhase string

const (
	// Idle: no scan running.
	Idle ScanPhase = "idle"
	// Scanning: a scan is in flight. Progress travels alongside, not inside,
	// the phase (Swift's associated ScanProgress value has no Go analogue here).
	Scanning ScanPhase = "scanning"
	// Finished: at least one item was found. Partial success still shows what
	// was found, even when some calls failed alongside it.
	Finished ScanPhase = "finished"
	// Empty: every model call answered, and none of them saw food.
	Empty ScanPhase = "empty"
	// Failed: nothing was found because the model calls themselves failed.
	// Distinct from Empty: the fridge may well be full, we just never got an
	// answer. The user-facing copy for these two must never be merged.
	Failed ScanPhase = "failed"
	// Cancelled: the user cancelled part way through.
	Cancelled ScanPhase = "cancelled"
)

// String returns the raw wire value.
func (p ScanPhase) String() string { return string(p) }

// IdentifiedItem is one grocery item recognized by the vision model.
//
// Wire type only: this is what the model emits and what the scan pipeline
// consumes; it is never persisted directly. Source of record:
// DinnerDecider/Services/LLM/LLMService.swift:7-17 and
// shared/schema/identified-item.schema.json.
//
// Decode it with UnmarshalIdentifiedItem, never with a bare json.Unmarshal —
// see that function for why.
type IdentifiedItem struct {
	// Name is the item name as the model reports it, e.g. "Whole Milk". The
	// literal value "unknown" (compared case-insensitively) is the model's
	// no-item escape hatch and is filtered out by the pipeline.
	Name string `json:"name"`
	// Brand is the brand read off the packaging, or nil when there is none or
	// it is unreadable.
	//
	// A pointer on purpose. Swift decodes this with decodeIfPresent, so an
	// ABSENT key and an explicit null are both valid and both produce nil —
	// and both must stay distinguishable from the empty string, which is a
	// third, different thing (the conformance fixtures pin this).
	Brand *string `json:"brand"`
	// Category is the raw category string from the model. NOT validated at
	// decode time: any string decodes successfully and is mapped onto
	// FoodCategory later, lowercased, falling back to "other".
	Category string `json:"category"`
	// Confidence is the model's confidence, prompted as 0-1 but NOT
	// range-validated at decode time (Swift holds a plain Double). The pipeline
	// drops any item with confidence < 0.15.
	Confidence float64 `json:"confidence"`
}

// InventoryItem is a row of the user's food inventory. PERSISTED.
//
// Source of record: DinnerDecider/Models/InventoryItem.swift:50-92 and
// shared/schema/inventory-item.schema.json. On iOS this is a SwiftData @Model
// class and is not Codable, so there is no shipped JSON encoding; the tags here
// reproduce the persisted field set name-for-name so a port's store stays
// compatible.
type InventoryItem struct {
	// Name is half of the merge key.
	Name string `json:"name"`
	// Brand is the other half of the merge key: two rows with the same name but
	// different brands are DISTINCT items.
	Brand *string `json:"brand"`
	// CategoryRaw is the category stored as a raw string, deliberately, so the
	// store keeps a stable value even if the enum is reordered or renamed
	// later. Persist FoodCategory's raw value, never an ordinal and never the
	// model's original mixed-case string.
	CategoryRaw string `json:"categoryRaw"`
	// Quantity is how many of this item are on hand. Duplicate merge keys stack
	// on confirm; cooking a recipe decrements by 1 per use. A row reaching 0 is
	// flagged for removal.
	Quantity int `json:"quantity"`
	// DateAdded drives the `dateAdded` inventory sort option.
	//
	// Linux-port decision for the TODO(unresolved) in
	// inventory-item.schema.json: RFC 3339, UTC. Go's time.Time marshals as
	// RFC 3339 already; the store writes TEXT. Recorded in docs/PERSISTENCE.md.
	DateAdded time.Time `json:"dateAdded"`
	// SourcePhotoID identifies the photo this item came from, or nil for manual
	// entry.
	SourcePhotoID *string `json:"sourcePhotoID"`
	// IsStaple marks something the user almost always has (salt, oil), shown
	// collapsed so it does not clutter the main list. MUST default to false.
	IsStaple bool `json:"isStaple"`
}

// ShoppingListItem is a single line on the shopping list. PERSISTED so the list
// survives relaunch. Source of record:
// DinnerDecider/Models/ShoppingListItem.swift:9-27 and
// shared/schema/shopping-list-item.schema.json.
type ShoppingListItem struct {
	// Name is the ingredient name as it will be shopped for. Deduplicated
	// case-insensitively against the existing list before insertion, with the
	// FIRST-SEEN casing preserved. Blank and whitespace-only names are dropped,
	// never stored.
	Name string `json:"name"`
	// IsChecked drives check-off, "clear checked", and the export marker.
	IsChecked bool `json:"isChecked"`
	// DateAdded is when the line was added. Same RFC 3339 UTC decision as
	// InventoryItem.DateAdded.
	DateAdded time.Time `json:"dateAdded"`
	// IsManual is true when the user typed this in by hand rather than adding
	// it from a recipe.
	IsManual bool `json:"isManual"`
}

// IngredientLine is one ingredient row in a suggested recipe.
// Source: DinnerDecider/Models/Recipe.swift:5-9.
type IngredientLine struct {
	// Name is matched against inventory by the name-only key (trimmed and
	// lowercased) when cooking decrements stock. Identifiable by name on iOS,
	// so names should be unique within a recipe.
	Name string `json:"name"`
	// HasIt is true only if the ingredient is in the user's inventory. The
	// prompt demands this be honest; the have/missing display and the
	// add-to-shopping-list flow both depend on it.
	HasIt bool `json:"hasIt"`
}

// RecipeSuggestion is one suggested dish. NOT persisted; produced on the fly by
// the model. Source: DinnerDecider/Models/Recipe.swift:12-20.
type RecipeSuggestion struct {
	// Name must be a real, well-known dish. Identifiable by name on iOS, so
	// names should be unique within a bundle.
	Name string `json:"name"`
	// Ingredients are the dish's genuine standard ingredients, each marked
	// have/missing.
	Ingredients []IngredientLine `json:"ingredients"`
	// Steps are the cooking steps, in order.
	Steps []string `json:"steps"`
	// TimeMinutes is the total time in minutes.
	//
	// An int on purpose. Swift decodes this as Int, so a JSON float here fails
	// the whole bundle decode and surfaces to the user as the
	// unreadable-recipes error. Go's encoding/json fails the same way, which is
	// the behavior to keep.
	TimeMinutes int `json:"timeMinutes"`
	// MissingItems are names the user would need to buy. Empty for makeNow
	// dishes. Feeds the shopping-list aggregation.
	MissingItems []string `json:"missingItems"`
}

// RecipeBundleResponse is the whole reply to the recipes prompt: two buckets of
// suggestions from one model call. Source:
// DinnerDecider/Models/Recipe.swift:23-27 and shared/schema/recipe.schema.json.
type RecipeBundleResponse struct {
	// MakeNow holds dishes whose ingredients are all in the inventory. Their
	// MissingItems should be empty.
	MakeNow []RecipeSuggestion `json:"makeNow"`
	// AlmostThere holds dishes missing up to 3 ingredients; each missing one is
	// listed in MissingItems.
	AlmostThere []RecipeSuggestion `json:"almostThere"`
}

// PlannedMeal is a RecipeSuggestion saved to the meal plan. PERSISTED.
// Source: DinnerDecider/Models/PlannedMeal.swift.
//
// It flattens the recipe deliberately so a deep link can render the meal
// without the model regenerating it: ingredients collapse to names only, and
// HasIt/MissingItems are dropped.
type PlannedMeal struct {
	// ID is a stable identity generated at creation, formatted as a UUID.
	// Hand-rolled UUIDv4 (crypto/rand plus version and variant bits) per plan
	// §1.1 — this port does not take a UUID dependency.
	ID string `json:"id"`
	// RecipeName is copied from RecipeSuggestion.Name.
	RecipeName string `json:"recipeName"`
	// IngredientNames is RecipeSuggestion.Ingredients mapped to their names.
	// HasIt is intentionally not carried over.
	IngredientNames []string `json:"ingredientNames"`
	// Steps are the cooking steps, in order.
	Steps []string `json:"steps"`
	// TimeMinutes is the total time in minutes.
	TimeMinutes int `json:"timeMinutes"`
	// PlannedDate is when the meal is planned for. Same RFC 3339 UTC decision
	// as InventoryItem.DateAdded.
	PlannedDate time.Time `json:"plannedDate"`
	// CalendarEventId is the platform calendar event identifier
	// (EKEvent.eventIdentifier on iOS). Nullable, and unused on Linux — the
	// column is kept so the field set stays compatible.
	CalendarEventId *string `json:"calendarEventId"`
}

// TasteProfile is the user's taste preferences, collected by the first-launch
// wizard and editable from Settings.
//
// PERSISTED AS JSON: unlike the SwiftData models, this schema IS the real
// on-disk shape (Swift encodes it and stores the blob in UserDefaults under
// "tasteProfile"). Source: DinnerDecider/Models/TasteProfile.swift:8-34 and
// shared/schema/taste-profile.schema.json.
//
// All 13 keys are required and none carries omitempty. Swift's synthesized
// Decodable init does not fall back to a property default for a missing key on
// a non-optional property: a partial blob fails to decode entirely and
// TasteProfile.load() returns nil, silently losing the profile INCLUDING
// allergies. Reading may be tolerant; writing must always emit all 13 keys, and
// must emit [] rather than null for the six lists.
type TasteProfile struct {
	FavoriteFoods        []string `json:"favoriteFoods"`
	FavoriteFoodsOther   string   `json:"favoriteFoodsOther"`
	DislikedFoods        []string `json:"dislikedFoods"`
	DislikedFoodsOther   string   `json:"dislikedFoodsOther"`
	CuisinesLoved        []string `json:"cuisinesLoved"`
	CuisinesLovedOther   string   `json:"cuisinesLovedOther"`
	CuisinesAvoided      []string `json:"cuisinesAvoided"`
	CuisinesAvoidedOther string   `json:"cuisinesAvoidedOther"`
	// Allergies is SAFETY-RELEVANT. Rendered as "Allergies: X. Never include
	// these." in the prompt fragment.
	Allergies            []string `json:"allergies"`
	AllergiesOther       string   `json:"allergiesOther"`
	TexturesAvoided      []string `json:"texturesAvoided"`
	TexturesAvoidedOther string   `json:"texturesAvoidedOther"`
	// SpiceLevel runs 1 (mild) to 5 (bring it on); 3 is the default. Values
	// outside 1-5 fall back to the "medium spice" wording rather than erroring.
	SpiceLevel int `json:"spiceLevel"`
}

// identifiedItemShadow is the decode shadow for IdentifiedItem.
//
// Every REQUIRED key is a pointer so a missing key, an explicit null and a
// present value are three distinguishable outcomes. encoding/json silently
// zero-fills a missing key into a non-pointer field, which would make
// {"name": "Milk"} decode "successfully" into an item with an empty category
// and a 0 confidence — Swift refuses that input outright, and the conformance
// fixture object-missing-required-fields expects null.
type identifiedItemShadow struct {
	Name       *string  `json:"name"`
	Brand      *string  `json:"brand"`
	Category   *string  `json:"category"`
	Confidence *float64 `json:"confidence"`
}

// UnmarshalIdentifiedItem decodes one IdentifiedItem from a JSON object.
//
// It reports ok=false — "no value" — for malformed JSON, for a JSON null, and
// for any input missing name, category or confidence (or supplying null for
// one of them). Only `brand` may be absent or null; both land on a nil
// IdentifiedItem.Brand, and neither is the same as "".
//
// This is the required-key rule Go does not give for free. Callers must not
// substitute json.Unmarshal into IdentifiedItem directly.
func UnmarshalIdentifiedItem(data []byte) (IdentifiedItem, bool) {
	var shadow identifiedItemShadow
	if err := json.Unmarshal(data, &shadow); err != nil {
		return IdentifiedItem{}, false
	}
	if shadow.Name == nil || shadow.Category == nil || shadow.Confidence == nil {
		return IdentifiedItem{}, false
	}
	return IdentifiedItem{
		Name:       *shadow.Name,
		Brand:      shadow.Brand,
		Category:   *shadow.Category,
		Confidence: *shadow.Confidence,
	}, true
}

// UnmarshalIdentifiedItems decodes a JSON array of IdentifiedItem.
//
// Two properties matter and are easy to lose:
//
//   - An empty array decodes to a NON-NIL empty slice with ok=true. A nil slice
//     must never be used to mean "parse failed": "saw nothing" and "was
//     unreadable" are different outcomes, and the conformance fixtures
//     array-empty and array-empty-output pin them separately.
//   - One bad element fails the WHOLE array, matching Swift's
//     JSONDecoder().decode([IdentifiedItem].self). Elements are not salvaged
//     individually.
//
// A JSON null reports ok=false; a JSON object where an array was expected
// reports ok=false.
func UnmarshalIdentifiedItems(data []byte) ([]IdentifiedItem, bool) {
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return nil, false
	}
	if raws == nil {
		// Input was the literal `null`. encoding/json allocates a non-nil
		// empty slice for `[]`, so this branch is unambiguous.
		return nil, false
	}
	items := make([]IdentifiedItem, 0, len(raws))
	for _, raw := range raws {
		item, ok := UnmarshalIdentifiedItem(raw)
		if !ok {
			return nil, false
		}
		items = append(items, item)
	}
	return items, true
}
