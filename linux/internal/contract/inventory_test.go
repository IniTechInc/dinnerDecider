package contract

import (
	"reflect"
	"testing"
)

// Port of DinnerDeciderTests/InventoryLogicTests.swift (15 tests), plus Go-only
// tests for the behaviors that file pins implicitly and that a Go port can lose
// without noticing: map iteration order, a nil brand, and the strictly-greater
// confidence comparison.
//
// Helpers are prefixed `inv` so they cannot collide with the helpers in the
// sibling test files of this package.

// invItem mirrors the Swift test helper
// `item(_:brand:confidence:)`, whose defaults are brand nil and confidence 0.9.
func invItem(name string, brand *string, confidence float64) IdentifiedItem {
	return IdentifiedItem{Name: name, Brand: brand, Category: "other", Confidence: confidence}
}

// invBrand returns a pointer to a brand literal.
func invBrand(s string) *string { return &s }

// invNames maps items to their names, the Swift `result.map(\.name)`.
func invNames(items []IdentifiedItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Name)
	}
	return out
}

// MARK: - Keys

func TestMergeKeyIsCaseAndWhitespaceInsensitive(t *testing.T) {
	if got, want := MergeKey("  Whole Milk ", invBrand(" Horizon ")), MergeKey("whole milk", invBrand("horizon")); got != want {
		t.Fatalf("MergeKey padded = %q, want %q", got, want)
	}
}

func TestMergeKeyDistinguishesBrand(t *testing.T) {
	horizon := MergeKey("Milk", invBrand("Horizon"))
	fairlife := MergeKey("Milk", invBrand("Fairlife"))
	if horizon == fairlife {
		t.Fatalf("two brands of milk collapsed to one key %q; brand is part of the identity", horizon)
	}
}

// Go-only: the exact key format. "milk|horizon" and "eggs|" are the literals the
// Swift confirm tests are written against, so the separator and the empty-brand
// rule are contract, not implementation detail.
func TestMergeKeyLiteralFormat(t *testing.T) {
	if got, want := MergeKey("Milk", invBrand("Horizon")), "milk|horizon"; got != want {
		t.Errorf("MergeKey = %q, want %q", got, want)
	}
	if got, want := MergeKey("Eggs", nil), "eggs|"; got != want {
		t.Errorf("MergeKey with no brand = %q, want %q", got, want)
	}
}

// Go-only: Swift's `brand ?? ""` makes a nil brand, an empty brand and a blank
// brand one identity. A Go port that skipped the nil check would panic or emit a
// different key for the commonest case in the whole pipeline.
func TestMergeKeyNilEmptyAndBlankBrandAgree(t *testing.T) {
	nilBrand := MergeKey("Eggs", nil)
	empty := MergeKey("Eggs", invBrand(""))
	blank := MergeKey("Eggs", invBrand("   "))
	if nilBrand != empty || nilBrand != blank {
		t.Fatalf("nil=%q empty=%q blank=%q, want all equal", nilBrand, empty, blank)
	}
}

func TestNameKeyIgnoresBrand(t *testing.T) {
	if got, want := NameKey("  Olive Oil "), "olive oil"; got != want {
		t.Fatalf("NameKey = %q, want %q", got, want)
	}
}

// Go-only: NameKey is a DIFFERENT key space from MergeKey. Returning
// MergeKey(name, nil) here would append "|" and never match a cook plan's stock.
func TestNameKeyIsNotAMergeKey(t *testing.T) {
	if got := NameKey("Eggs"); got == MergeKey("Eggs", nil) {
		t.Fatalf("NameKey(%q) = %q, which collides with the MergeKey space", "Eggs", got)
	}
}

// MARK: - Dedupe

func TestDedupeCollapsesDuplicatesPreservingOrder(t *testing.T) {
	input := []IdentifiedItem{
		invItem("Milk", invBrand("Horizon"), 0.9),
		invItem("Eggs", nil, 0.9),
		invItem("milk", invBrand("horizon"), 0.9),
	}
	got := invNames(Dedupe(input))
	want := []string{"Milk", "Eggs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Dedupe names = %v, want %v", got, want)
	}
}

func TestDedupeWithCountsTalliesDuplicates(t *testing.T) {
	input := []IdentifiedItem{
		invItem("Milk", nil, 0.9),
		invItem("Milk", nil, 0.9),
		invItem("Eggs", nil, 0.9),
	}
	grouped := DedupeWithCounts(input)
	if len(grouped) != 2 {
		t.Fatalf("len(grouped) = %d, want 2", len(grouped))
	}
	if grouped[0].Count != 2 {
		t.Errorf("grouped[0].Count = %d, want 2", grouped[0].Count)
	}
	if grouped[1].Count != 1 {
		t.Errorf("grouped[1].Count = %d, want 1", grouped[1].Count)
	}
}

func TestDedupeKeepsHighestConfidence(t *testing.T) {
	input := []IdentifiedItem{
		invItem("Milk", nil, 0.4),
		invItem("Milk", nil, 0.95),
	}
	result := Dedupe(input)
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if diff := result[0].Confidence - 0.95; diff > 0.0001 || diff < -0.0001 {
		t.Fatalf("confidence = %v, want 0.95 (accuracy 0.0001)", result[0].Confidence)
	}
}

// Go-only: the comparison is STRICTLY greater, so equal confidences keep the
// EARLIER identification. `>=` would silently swap in the later spelling.
func TestDedupeConfidenceTieKeepsTheEarlierItem(t *testing.T) {
	input := []IdentifiedItem{
		invItem("Whole Milk", nil, 0.5),
		invItem("whole milk", nil, 0.5),
	}
	result := Dedupe(input)
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if got, want := result[0].Name, "Whole Milk"; got != want {
		t.Fatalf("tie winner = %q, want %q (ties keep the earlier item)", got, want)
	}
}

// Go-only: the winning item replaces the stored one but must NOT move; the
// position is fixed by the first occurrence of the key.
func TestDedupeHigherConfidenceLaterDoesNotReorder(t *testing.T) {
	input := []IdentifiedItem{
		invItem("Milk", nil, 0.2),
		invItem("Eggs", nil, 0.9),
		invItem("Milk", nil, 0.99),
	}
	got := invNames(Dedupe(input))
	want := []string{"Milk", "Eggs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %v, want %v (first-seen order survives a later winner)", got, want)
	}
}

// Go-only: map iteration in Go is randomized, so an implementation that ranged
// its map instead of a separate order slice would pass a two-key test roughly
// half the time. Many keys, many runs.
func TestDedupePreservesFirstSeenOrderAcrossManyKeys(t *testing.T) {
	names := []string{"Milk", "Eggs", "Butter", "Cheese", "Yogurt", "Chicken", "Beef", "Lettuce"}
	input := make([]IdentifiedItem, 0, len(names)*2)
	for _, name := range names {
		input = append(input, invItem(name, nil, 0.9))
	}
	// Second pass over the same names: every one is now a duplicate.
	for _, name := range names {
		input = append(input, invItem(name, nil, 0.9))
	}
	for run := 0; run < 32; run++ {
		got := invNames(Dedupe(input))
		if !reflect.DeepEqual(got, names) {
			t.Fatalf("run %d: names = %v, want %v", run, got, names)
		}
	}
}

// Go-only: brand is part of the identity, so two brands of milk stay two rows.
func TestDedupeTreatsBrandAsPartOfIdentity(t *testing.T) {
	input := []IdentifiedItem{
		invItem("Milk", invBrand("Horizon"), 0.9),
		invItem("Milk", invBrand("Fairlife"), 0.9),
	}
	grouped := DedupeWithCounts(input)
	if len(grouped) != 2 {
		t.Fatalf("len(grouped) = %d, want 2 (two brands are two rows)", len(grouped))
	}
	for i, g := range grouped {
		if g.Count != 1 {
			t.Errorf("grouped[%d].Count = %d, want 1", i, g.Count)
		}
	}
}

// Go-only: empty in, empty (non-nil) out. Nothing in this file uses a nil slice
// to signal anything, unlike the parser where nil is a real outcome.
func TestDedupeOfNothingIsEmptyNotNil(t *testing.T) {
	if got := Dedupe(nil); got == nil || len(got) != 0 {
		t.Errorf("Dedupe(nil) = %#v, want an empty non-nil slice", got)
	}
	if got := DedupeWithCounts(nil); got == nil || len(got) != 0 {
		t.Errorf("DedupeWithCounts(nil) = %#v, want an empty non-nil slice", got)
	}
}

// MARK: - Confirm / merge quantities

func TestConfirmInsertsWhenNoExistingMatch(t *testing.T) {
	plan := ConfirmPlan([]ConfirmEntry{{Key: "milk|", Quantity: 1}}, nil)
	want := []ConfirmAction{{Kind: ConfirmInsert, Key: "milk|", Quantity: 1}}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("plan = %+v, want %+v", plan, want)
	}
}

func TestConfirmIncrementsExistingQuantity(t *testing.T) {
	plan := ConfirmPlan(
		[]ConfirmEntry{{Key: "milk|horizon", Quantity: 1}},
		[]StockRef{{Key: "milk|horizon", Quantity: 2}},
	)
	want := []ConfirmAction{{Kind: ConfirmIncrement, Key: "milk|horizon", Quantity: 3}}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("plan = %+v, want %+v", plan, want)
	}
}

// Two confirmed items with the same key: first inserts, second bumps. BOTH
// actions are emitted; collapsing them into one insert of quantity 2 is the bug
// this test exists to catch.
func TestConfirmStacksRepeatedKeysAcrossTheBatch(t *testing.T) {
	plan := ConfirmPlan(
		[]ConfirmEntry{{Key: "eggs|", Quantity: 1}, {Key: "eggs|", Quantity: 1}},
		nil,
	)
	want := []ConfirmAction{
		{Kind: ConfirmInsert, Key: "eggs|", Quantity: 1},
		{Kind: ConfirmIncrement, Key: "eggs|", Quantity: 2},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("plan = %+v, want %+v", plan, want)
	}
}

// Go-only: the running tally spans the whole batch AND starts from the existing
// stock, so a third helping of eggs on top of two in the fridge reads 3 then 4.
func TestConfirmRunningTallySpansTheWholeBatchOverExistingStock(t *testing.T) {
	plan := ConfirmPlan(
		[]ConfirmEntry{{Key: "eggs|", Quantity: 1}, {Key: "eggs|", Quantity: 1}},
		[]StockRef{{Key: "eggs|", Quantity: 2}},
	)
	want := []ConfirmAction{
		{Kind: ConfirmIncrement, Key: "eggs|", Quantity: 3},
		{Kind: ConfirmIncrement, Key: "eggs|", Quantity: 4},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("plan = %+v, want %+v", plan, want)
	}
}

// Go-only: an existing row of quantity 0 still counts as KNOWN, so it is
// incremented rather than inserted a second time. A single map keyed on quantity
// would get this wrong.
func TestConfirmTreatsAZeroQuantityRowAsExisting(t *testing.T) {
	plan := ConfirmPlan(
		[]ConfirmEntry{{Key: "milk|", Quantity: 2}},
		[]StockRef{{Key: "milk|", Quantity: 0}},
	)
	want := []ConfirmAction{{Kind: ConfirmIncrement, Key: "milk|", Quantity: 2}}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("plan = %+v, want %+v", plan, want)
	}
}

// Go-only: one action per entry, in the order given, and unrelated stock is
// never touched.
func TestConfirmEmitsOneActionPerEntryInOrder(t *testing.T) {
	plan := ConfirmPlan(
		[]ConfirmEntry{
			{Key: "milk|horizon", Quantity: 1},
			{Key: "eggs|", Quantity: 1},
			{Key: "milk|horizon", Quantity: 1},
		},
		[]StockRef{{Key: "butter|", Quantity: 5}},
	)
	want := []ConfirmAction{
		{Kind: ConfirmInsert, Key: "milk|horizon", Quantity: 1},
		{Kind: ConfirmInsert, Key: "eggs|", Quantity: 1},
		{Kind: ConfirmIncrement, Key: "milk|horizon", Quantity: 2},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("plan = %+v, want %+v", plan, want)
	}
}

// MARK: - Cooking decrement

func TestCookPlanDecrementsUsedItems(t *testing.T) {
	changes := CookPlan(
		[]string{"eggs", "cheese"},
		[]StockRef{
			{Key: "eggs", Quantity: 6},
			{Key: "cheese", Quantity: 2},
			{Key: "milk", Quantity: 1},
		},
	)
	want := []CookChange{
		{Key: "eggs", NewQuantity: 5, Removed: false},
		{Key: "cheese", NewQuantity: 1, Removed: false},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %+v, want %+v", changes, want)
	}
}

func TestCookPlanFlagsItemsThatReachZeroForRemoval(t *testing.T) {
	changes := CookPlan([]string{"milk"}, []StockRef{{Key: "milk", Quantity: 1}})
	want := []CookChange{{Key: "milk", NewQuantity: 0, Removed: true}}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %+v, want %+v", changes, want)
	}
}

func TestCookPlanIgnoresIngredientsNotInStock(t *testing.T) {
	changes := CookPlan([]string{"saffron"}, []StockRef{{Key: "milk", Quantity: 1}})
	if len(changes) != 0 {
		t.Fatalf("changes = %+v, want empty", changes)
	}
}

// Go-only: output follows STOCK order (what the user is looking at), not the
// order the recipe happened to list its ingredients in.
func TestCookPlanReturnsChangesInStockOrder(t *testing.T) {
	changes := CookPlan(
		[]string{"milk", "eggs"},
		[]StockRef{
			{Key: "eggs", Quantity: 6},
			{Key: "butter", Quantity: 1},
			{Key: "milk", Quantity: 3},
		},
	)
	want := []CookChange{
		{Key: "eggs", NewQuantity: 5, Removed: false},
		{Key: "milk", NewQuantity: 2, Removed: false},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %+v, want %+v (stock order, butter untouched)", changes, want)
	}
}

// Go-only: two eggs in a recipe take two eggs. A seen-set guard around the
// decrement would take one.
func TestCookPlanRepeatedKeyDecrementsRepeatedly(t *testing.T) {
	changes := CookPlan([]string{"eggs", "eggs", "eggs"}, []StockRef{{Key: "eggs", Quantity: 6}})
	want := []CookChange{{Key: "eggs", NewQuantity: 3, Removed: false}}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %+v, want %+v", changes, want)
	}
}

// Go-only: an over-hungry recipe clamps at 0 and removes the row; it never
// persists a negative quantity.
func TestCookPlanClampsAtZeroAndRemoves(t *testing.T) {
	changes := CookPlan([]string{"eggs", "eggs", "eggs"}, []StockRef{{Key: "eggs", Quantity: 1}})
	want := []CookChange{{Key: "eggs", NewQuantity: 0, Removed: true}}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %+v, want %+v", changes, want)
	}
}

// Go-only: rows nobody cooked with are omitted entirely, not returned unchanged.
func TestCookPlanOmitsUntouchedRows(t *testing.T) {
	changes := CookPlan(
		nil,
		[]StockRef{{Key: "eggs", Quantity: 6}, {Key: "milk", Quantity: 1}},
	)
	if changes == nil || len(changes) != 0 {
		t.Fatalf("changes = %#v, want an empty non-nil slice", changes)
	}
}

// MARK: - Portion scaling

func TestPortionLineSingleServing(t *testing.T) {
	got, ok := PortionLine(1)
	if !ok {
		t.Fatal("PortionLine(1) reported no line")
	}
	if want := "Cooking for 1 person; use single-serving portions."; got != want {
		t.Fatalf("PortionLine(1) = %q, want %q", got, want)
	}
}

func TestPortionLineMultiplePeople(t *testing.T) {
	got, ok := PortionLine(4)
	if !ok {
		t.Fatal("PortionLine(4) reported no line")
	}
	if want := "Cooking for 4 people; scale ingredient amounts to serve 4."; got != want {
		t.Fatalf("PortionLine(4) = %q, want %q", got, want)
	}
}

func TestPortionLineNilForZeroOrNegative(t *testing.T) {
	if got, ok := PortionLine(0); ok {
		t.Errorf("PortionLine(0) = %q, true; want no line", got)
	}
	if got, ok := PortionLine(-3); ok {
		t.Errorf("PortionLine(-3) = %q, true; want no line", got)
	}
}

// Go-only: 2 is the default household size, and it must take the plural branch.
func TestPortionLineDefaultHouseholdIsPlural(t *testing.T) {
	got, ok := PortionLine(2)
	if !ok {
		t.Fatal("PortionLine(2) reported no line")
	}
	if want := "Cooking for 2 people; scale ingredient amounts to serve 2."; got != want {
		t.Fatalf("PortionLine(2) = %q, want %q", got, want)
	}
}
