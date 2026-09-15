package contract

import (
	"strconv"
	"strings"
)

// This file ports `enum InventoryLogic` from
// DinnerDecider/Services/InventoryLogic.swift: the pure, unit-testable rules for
// the inventory. No I/O, no storage, no clock. The store APPLIES these plans; it
// never re-derives them (plan Step 44), which is what keeps the merge-on-confirm
// and cook-decrement arithmetic covered by fast tests on every platform.
//
// A note on trimming that runs through the whole file. Swift trims with
// `.whitespacesAndNewlines`, so a name arriving as "Milk\n" from an OCR line or
// a pasted recipe ingredient normalizes to "milk". Go's strings.TrimSpace covers
// both the space characters and the line breaks, so it is the faithful
// equivalent; strings.Trim(s, " ") would NOT be, and would silently produce a
// second inventory row for the same food.

// MergeKey is the normalized key deciding whether two items are "the same" item.
//
// Case- and whitespace-insensitive on name PLUS brand:
//
//	trim(lower(name)) + "|" + trim(lower(brand))
//
// Three things about this format are load-bearing and pinned by the literals in
// the tests ("milk|horizon", "eggs|"):
//
//   - The separator is a single "|". Concatenating without one would collide
//     ("milk" + "horizon" == "mil" + "khorizon").
//   - An absent brand contributes the EMPTY string, not "nil" or "none", so the
//     key still ends in "|". A brandless "eggs" is "eggs|", and that exact
//     string is what confirmPlan and the persisted rows are keyed on.
//   - Brand is part of the identity. Horizon milk and Fairlife milk are two
//     inventory rows, not one merged row with quantity 2. Dropping brand from
//     the key silently merges the user's two cartons.
//
// A nil brand and a brand of "" and a brand of "  " all produce the same key.
// That is deliberate: the three are indistinguishable as a shopping identity,
// even though the wire type keeps them distinguishable at decode time.
func MergeKey(name string, brand *string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	b := ""
	if brand != nil {
		b = strings.ToLower(strings.TrimSpace(*brand))
	}
	return n + "|" + b
}

// NameKey is the name-only key, used to match recipe ingredients (which carry no
// brand) against inventory rows when cooking.
//
// Deliberately NOT MergeKey(name, nil): that would append the "|" separator and
// then never match anything keyed by name alone. The two key spaces are
// separate, and CookPlan operates in this one.
func NameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// Grouped is one identified item plus how many raw identifications resolved to
// it. Port of `InventoryLogic.Grouped`.
type Grouped struct {
	// Item is the WINNING identification for this key, which is not necessarily
	// the first one seen: see DedupeWithCounts.
	Item IdentifiedItem
	// Count is how many raw identifications collapsed into this row. Used to
	// merge quantities on confirm, so three crops seeing the same carton add one
	// row of quantity 3 rather than three rows of quantity 1.
	Count int
}

// Dedupe collapses repeated identifications (the same item seen in overlapping
// crops, or in both the whole-image pass and a crop) into unique rows.
//
// First-seen order is preserved and the highest-confidence identification wins.
// The scan pipeline calls this after EVERY append, so the order it returns is
// the order the user watches items appear in; re-sorting here would make the
// list jump around mid-scan.
func Dedupe(items []IdentifiedItem) []IdentifiedItem {
	grouped := DedupeWithCounts(items)
	out := make([]IdentifiedItem, 0, len(grouped))
	for _, g := range grouped {
		out = append(out, g.Item)
	}
	return out
}

// DedupeWithCounts is Dedupe plus the tally of how many raw identifications
// backed each unique row.
//
// Three behaviors are pinned by InventoryLogicTests and must not drift:
//
//   - ORDER is first-seen. The position of a key is fixed by its FIRST
//     occurrence, even when a later occurrence replaces the stored item. Go's
//     map iteration is randomized, so the separate order slice is not stylistic
//     here: ranging a map would shuffle the user's list on every scan tick.
//   - CONFIDENCE ties keep the EARLIER item. The comparison is STRICTLY greater
//     (Swift: `item.confidence > existing.confidence`). Using >= would swap in
//     the later of two equally-confident identifications and change which
//     brand/name spelling the user sees, for no gain.
//   - IDENTITY is name AND brand, via MergeKey.
//
// The returned slice is never nil, and never contains a zero Count.
func DedupeWithCounts(items []IdentifiedItem) []Grouped {
	order := make([]string, 0, len(items))
	chosen := make(map[string]IdentifiedItem, len(items))
	counts := make(map[string]int, len(items))

	for _, item := range items {
		key := MergeKey(item.Name, item.Brand)
		existing, seen := chosen[key]
		if seen {
			counts[key]++
			// Strictly greater: a tie keeps the earlier identification.
			if item.Confidence > existing.Confidence {
				chosen[key] = item
			}
			continue
		}
		order = append(order, key)
		chosen[key] = item
		counts[key] = 1
	}

	out := make([]Grouped, 0, len(order))
	for _, key := range order {
		out = append(out, Grouped{Item: chosen[key], Count: counts[key]})
	}
	return out
}

// StockRef is a lightweight view of an existing inventory row, for merge and
// cook planning. Port of `InventoryLogic.StockRef`.
//
// Deliberately not InventoryItem: the planners need a key and a quantity and
// nothing else, which is what keeps them pure and trivially testable.
type StockRef struct {
	// Key is a MergeKey for ConfirmPlan, and a NameKey for CookPlan. The two
	// planners work in different key spaces; do not mix them in one slice.
	Key string
	// Quantity is the row's current quantity.
	Quantity int
}

// ConfirmEntry is one item being confirmed into inventory: its MergeKey and the
// quantity being added.
//
// Swift passes `[(key: String, quantity: Int)]`; Go has no named tuples, so this
// is the struct standing in for it.
type ConfirmEntry struct {
	Key      string
	Quantity int
}

// ConfirmActionKind distinguishes the two outcomes of confirming one item.
//
// Prefixed like LoadDecision and UnloadDecision, for the same reason: the
// package is flat and a bare `Insert` would say nothing about what it inserts.
type ConfirmActionKind int

const (
	// ConfirmInsert: create a brand new inventory row.
	//
	// The zero value on purpose. An accidentally-zero action inserts a row,
	// which is visible and reversible; if ConfirmIncrement were the zero value
	// the same accident would rewrite an existing row's quantity to 0 and the
	// applier would delete the user's food.
	ConfirmInsert ConfirmActionKind = iota
	// ConfirmIncrement: bump an existing row's quantity.
	ConfirmIncrement
)

// String returns the kind name, for logs and test failures.
func (k ConfirmActionKind) String() string {
	switch k {
	case ConfirmInsert:
		return "insert"
	case ConfirmIncrement:
		return "increment"
	}
	return "unknown"
}

// ConfirmAction is the decision for one item being confirmed into inventory.
//
// Port of `enum ConfirmAction { case increment(key:newQuantity:), insert(key:quantity:) }`.
// Go has no payload-carrying enums, so this is a comparable struct whose
// Quantity field MEANS DIFFERENT THINGS per kind:
//
//	ConfirmInsert    -> Quantity is THIS ENTRY'S OWN quantity.
//	ConfirmIncrement -> Quantity is the row's NEW RUNNING TOTAL.
//
// That asymmetry is Swift's, not an artifact of flattening the enum: the insert
// case is named `quantity` and the increment case `newQuantity`. An applier that
// treats the increment value as a delta double-counts; one that treats the
// insert value as a total is correct only by coincidence (a new row starts at 0).
type ConfirmAction struct {
	Kind ConfirmActionKind
	// Key is the MergeKey of the row being inserted or bumped.
	Key string
	// Quantity is kind-dependent: see the type comment.
	Quantity int
}

// ConfirmPlan decides, for each confirmed item, whether to bump an existing
// inventory row or insert a new one.
//
// The running tally spans the WHOLE BATCH, which is the subtle part. Two
// confirmed entries sharing a key are not independent: the first inserts, and
// the second increments the row the first just created, to the accumulated
// total. testConfirmStacksRepeatedKeysAcrossTheBatch pins exactly that:
//
//	confirming [eggs|:1, eggs|:1], existing [] ->
//	    [insert(eggs|, 1), increment(eggs|, 2)]
//
// BOTH actions are emitted, in order, and must never be collapsed into a single
// insert of quantity 2. The applier replays them in sequence against a store
// that has already performed the insert, so a collapsed plan and a re-derived
// plan disagree the moment the store is anything other than an in-memory map.
//
// `known` is tracked separately from the quantity map because a row can exist
// with quantity 0: "did this key exist" is not "is its quantity non-zero".
//
// The returned slice is never nil and has exactly one action per entry, in the
// order given.
func ConfirmPlan(confirming []ConfirmEntry, existing []StockRef) []ConfirmAction {
	// Running tally so two confirmed items with the same key stack correctly.
	quantities := make(map[string]int, len(existing)+len(confirming))
	known := make(map[string]struct{}, len(existing)+len(confirming))
	for _, ref := range existing {
		quantities[ref.Key] = ref.Quantity
		known[ref.Key] = struct{}{}
	}

	actions := make([]ConfirmAction, 0, len(confirming))
	for _, entry := range confirming {
		current := quantities[entry.Key]
		updated := current + entry.Quantity
		quantities[entry.Key] = updated
		if _, seen := known[entry.Key]; seen {
			actions = append(actions, ConfirmAction{
				Kind:     ConfirmIncrement,
				Key:      entry.Key,
				Quantity: updated,
			})
			continue
		}
		known[entry.Key] = struct{}{}
		actions = append(actions, ConfirmAction{
			Kind: ConfirmInsert,
			Key:  entry.Key,
			// The entry's own quantity, NOT `updated`. They happen to be equal
			// for a genuinely new key; keep the Swift wording so they stay
			// equal by construction rather than by luck.
			Quantity: entry.Quantity,
		})
	}
	return actions
}

// CookChange is the result of cooking a recipe, for one inventory row.
type CookChange struct {
	// Key is the NameKey of the row.
	Key string
	// NewQuantity is the post-cook quantity, clamped at 0.
	NewQuantity int
	// Removed is true when the row hit zero and should be deleted.
	Removed bool
}

// CookPlan decrements each used inventory row by one.
//
// usedKeys are NameKeys (recipe ingredients carry no brand) for the ingredients
// the user actually has; stock is the current inventory in display order.
//
// The pinned behaviors:
//
//   - Output is in STOCK order, not usedKeys order. The result drives a UI list
//     the user is already looking at, so it must follow what is on screen.
//   - Untouched rows are OMITTED entirely, not returned with an unchanged
//     quantity. An applier that wrote back every row would rewrite the whole
//     inventory to cook one omelette.
//   - Ingredients not in stock are ignored, silently. The caller has already
//     filtered to the ingredients the user has; a stray one is not an error.
//   - A repeated key decrements REPEATEDLY. Two eggs in a recipe take two eggs.
//   - The quantity is CLAMPED at 0 and Removed is `quantity <= 0`, so a row
//     driven negative by an over-hungry recipe is removed rather than persisted
//     as -1.
//
// The returned slice is never nil.
func CookPlan(usedKeys []string, stock []StockRef) []CookChange {
	quantities := make(map[string]int, len(stock))
	order := make([]string, 0, len(stock))
	for _, ref := range stock {
		quantities[ref.Key] = ref.Quantity
		order = append(order, ref.Key)
	}

	touched := make(map[string]struct{}, len(usedKeys))
	for _, key := range usedKeys {
		if _, inStock := quantities[key]; !inStock {
			continue
		}
		// Not `= quantities[key] - 1` guarded by a seen-set: a repeated key
		// must decrement once per occurrence.
		quantities[key]--
		touched[key] = struct{}{}
	}

	changes := make([]CookChange, 0, len(touched))
	for _, key := range order {
		if _, used := touched[key]; !used {
			continue
		}
		quantity := quantities[key]
		if quantity < 0 {
			quantity = 0
		}
		changes = append(changes, CookChange{
			Key:         key,
			NewQuantity: quantity,
			Removed:     quantity <= 0,
		})
	}
	return changes
}

// PortionLine is the recipe-prompt line describing how many people to cook for.
//
// Returns ok == false for an unset or nonsensical household size, in which case
// the caller OMITS THE LINE ENTIRELY rather than emitting an empty one - the
// prompt shares a 1024-token context with the reply, so a blank line is not
// free. Swift returns `String?` and RecipePrompt pattern-matches on it; the
// (value, ok) pair is the Go equivalent.
//
// The two strings are verbatim, including the semicolons. House style forbids em
// dashes in prompt text (RecipePromptTests.testPromptNeverContainsEmDash), and
// "1 person" is singular while every other size is plural.
func PortionLine(householdSize int) (string, bool) {
	if householdSize < 1 {
		return "", false
	}
	if householdSize == 1 {
		return "Cooking for 1 person; use single-serving portions.", true
	}
	return "Cooking for " + strconv.Itoa(householdSize) + " people; scale ingredient amounts to serve " + strconv.Itoa(householdSize) + ".", true
}
