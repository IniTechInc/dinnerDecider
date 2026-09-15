package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
)

func ptr(s string) *string { return &s }

func mustInsert(t *testing.T, db *DB, item contract.InventoryItem) InventoryRow {
	t.Helper()
	row, err := db.InsertInventoryItem(context.Background(), item)
	if err != nil {
		t.Fatalf("InsertInventoryItem(%q): %v", item.Name, err)
	}
	return row
}

func quantityOf(t *testing.T, db *DB, name string, brand *string) (int, bool) {
	t.Helper()
	rows, err := db.ListInventoryItems(context.Background(), "")
	if err != nil {
		t.Fatalf("ListInventoryItems: %v", err)
	}
	want := contract.MergeKey(name, brand)
	for _, row := range rows {
		if contract.MergeKey(row.Name, row.Brand) == want {
			return row.Quantity, true
		}
	}
	return 0, false
}

func TestInventoryCRUDRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	added := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

	row := mustInsert(t, db, contract.InventoryItem{
		Name:          "Whole Milk",
		Brand:         ptr("Horizon"),
		CategoryRaw:   "dairy",
		Quantity:      2,
		DateAdded:     added,
		SourcePhotoID: ptr("photo-7"),
		IsStaple:      true,
	})
	if row.ID == "" {
		t.Fatal("InsertInventoryItem returned an empty id")
	}

	got, err := db.GetInventoryItem(ctx, row.ID)
	if err != nil {
		t.Fatalf("GetInventoryItem: %v", err)
	}
	if got.Name != "Whole Milk" || got.Brand == nil || *got.Brand != "Horizon" {
		t.Errorf("name/brand round trip = %q/%v", got.Name, got.Brand)
	}
	if got.CategoryRaw != "dairy" || got.Category() != contract.CategoryDairy {
		t.Errorf("category round trip = %q", got.CategoryRaw)
	}
	if got.Quantity != 2 {
		t.Errorf("quantity = %d, want 2", got.Quantity)
	}
	if !got.DateAdded.Equal(added) {
		t.Errorf("dateAdded = %v, want %v", got.DateAdded, added)
	}
	if got.SourcePhotoID == nil || *got.SourcePhotoID != "photo-7" {
		t.Errorf("sourcePhotoID = %v", got.SourcePhotoID)
	}
	if !got.IsStaple {
		t.Error("isStaple did not round trip")
	}

	got.Quantity = 9
	got.IsStaple = false
	if err := db.UpdateInventoryItem(ctx, got); err != nil {
		t.Fatalf("UpdateInventoryItem: %v", err)
	}
	after, err := db.GetInventoryItem(ctx, row.ID)
	if err != nil {
		t.Fatalf("GetInventoryItem after update: %v", err)
	}
	if after.Quantity != 9 || after.IsStaple {
		t.Errorf("after update: quantity = %d, isStaple = %v", after.Quantity, after.IsStaple)
	}

	if err := db.DeleteInventoryItem(ctx, row.ID); err != nil {
		t.Fatalf("DeleteInventoryItem: %v", err)
	}
	if _, err := db.GetInventoryItem(ctx, row.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetInventoryItem after delete = %v, want ErrNotFound", err)
	}
	if err := db.DeleteInventoryItem(ctx, row.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second DeleteInventoryItem = %v, want ErrNotFound", err)
	}
	if err := db.UpdateInventoryItem(ctx, got); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateInventoryItem on a deleted row = %v, want ErrNotFound", err)
	}
}

// isStaple must default to false, and a missing brand must be NULL rather than
// the empty string — the contract keeps absent and "" distinguishable.
func TestInventoryDefaultsAndNullBrand(t *testing.T) {
	db := openTestDB(t)
	row := mustInsert(t, db, contract.InventoryItem{Name: "Eggs", CategoryRaw: "produce", Quantity: 1})

	got, err := db.GetInventoryItem(context.Background(), row.ID)
	if err != nil {
		t.Fatalf("GetInventoryItem: %v", err)
	}
	if got.IsStaple {
		t.Error("isStaple defaulted to true")
	}
	if got.Brand != nil {
		t.Errorf("brand = %v, want nil", got.Brand)
	}
	if got.DateAdded.IsZero() {
		t.Error("dateAdded was not defaulted to now")
	}

	var brand sql.NullString
	if err := db.SQL().QueryRow(`SELECT brand FROM inventory_item WHERE id = ?`, row.ID).Scan(&brand); err != nil {
		t.Fatalf("reading brand column: %v", err)
	}
	if brand.Valid {
		t.Errorf("brand column = %q, want NULL", brand.String)
	}
}

// The column may only ever hold one of the nine canonical raw values, and never
// an ordinal. An unrecognized value files under `other`, exactly as the
// storage-side mapping rule says — without lowercasing, which is the documented
// asymmetry against the model-side mapping.
func TestInventoryCategoryIsCanonicalized(t *testing.T) {
	db := openTestDB(t)
	cases := []struct{ in, want string }{
		{"dairy", "dairy"},
		{"Dairy", "other"},
		{"beverages", "other"},
		{"", "other"},
		{"3", "other"},
	}
	for _, tc := range cases {
		row := mustInsert(t, db, contract.InventoryItem{Name: "x", CategoryRaw: tc.in, Quantity: 1})
		got, err := db.GetInventoryItem(context.Background(), row.ID)
		if err != nil {
			t.Fatalf("GetInventoryItem: %v", err)
		}
		if got.CategoryRaw != tc.want {
			t.Errorf("categoryRaw for input %q = %q, want %q", tc.in, got.CategoryRaw, tc.want)
		}
	}
}

func TestListInventoryItemsIsNeverNil(t *testing.T) {
	db := openTestDB(t)
	rows, err := db.ListInventoryItems(context.Background(), OrderByName)
	if err != nil {
		t.Fatalf("ListInventoryItems: %v", err)
	}
	if rows == nil {
		t.Error("an empty inventory returned a nil slice; it marshals to JSON null")
	}
	if len(rows) != 0 {
		t.Errorf("len = %d, want 0", len(rows))
	}
}

func TestListInventoryItemsOrdering(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mustInsert(t, db, contract.InventoryItem{Name: "banana", CategoryRaw: "produce", Quantity: 1, DateAdded: base})
	mustInsert(t, db, contract.InventoryItem{Name: "Apple", CategoryRaw: "snack", Quantity: 1, DateAdded: base.Add(time.Hour)})
	mustInsert(t, db, contract.InventoryItem{Name: "cheese", CategoryRaw: "dairy", Quantity: 1, DateAdded: base.Add(2 * time.Hour)})

	names := func(order InventoryOrder) []string {
		rows, err := db.ListInventoryItems(context.Background(), order)
		if err != nil {
			t.Fatalf("ListInventoryItems(%q): %v", order, err)
		}
		var out []string
		for _, row := range rows {
			out = append(out, row.Name)
		}
		return out
	}

	// Case-insensitive alphabetical: "Apple" before "banana".
	if got := names(OrderByName); got[0] != "Apple" || got[1] != "banana" || got[2] != "cheese" {
		t.Errorf("OrderByName = %v", got)
	}
	// Newest first.
	if got := names(OrderByDateAdded); got[0] != "cheese" || got[2] != "banana" {
		t.Errorf("OrderByDateAdded = %v", got)
	}
	// Category raw value, then name: dairy, produce, snack.
	if got := names(OrderByCategory); got[0] != "cheese" || got[1] != "banana" || got[2] != "Apple" {
		t.Errorf("OrderByCategory = %v", got)
	}
	// An unrecognized stored preference falls back rather than failing.
	if got := names("no-such-sort"); len(got) != 3 {
		t.Errorf("fallback order returned %v", got)
	}
}

func TestStockSnapshots(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mustInsert(t, db, contract.InventoryItem{
		Name: " Milk ", Brand: ptr("Horizon"), CategoryRaw: "dairy", Quantity: 2, DateAdded: base})
	mustInsert(t, db, contract.InventoryItem{
		Name: "Eggs", CategoryRaw: "produce", Quantity: 6, DateAdded: base.Add(time.Hour)})

	merge, err := db.MergeKeyStock(context.Background())
	if err != nil {
		t.Fatalf("MergeKeyStock: %v", err)
	}
	want := []contract.StockRef{{Key: "milk|horizon", Quantity: 2}, {Key: "eggs|", Quantity: 6}}
	if len(merge) != len(want) {
		t.Fatalf("MergeKeyStock = %v, want %v", merge, want)
	}
	for i := range want {
		if merge[i] != want[i] {
			t.Errorf("MergeKeyStock[%d] = %v, want %v", i, merge[i], want[i])
		}
	}

	name, err := db.NameKeyStock(context.Background())
	if err != nil {
		t.Fatalf("NameKeyStock: %v", err)
	}
	if name[0].Key != "milk" || name[1].Key != "eggs" {
		t.Errorf("NameKeyStock = %v", name)
	}
}

func TestApplyConfirmPlanIncrementsExistingRow(t *testing.T) {
	db := openTestDB(t)
	mustInsert(t, db, contract.InventoryItem{
		Name: "Milk", Brand: ptr("Horizon"), CategoryRaw: "dairy", Quantity: 2})

	// What ConfirmPlan produces for one more Horizon milk against a stock of 2.
	actions := []contract.ConfirmAction{{Kind: contract.ConfirmIncrement, Key: "milk|horizon", Quantity: 3}}
	items := []ConfirmItem{{Name: "Milk", Brand: ptr("Horizon"), Category: contract.CategoryDairy}}

	if err := db.ApplyConfirmPlan(context.Background(), actions, items); err != nil {
		t.Fatalf("ApplyConfirmPlan: %v", err)
	}
	if q, ok := quantityOf(t, db, "Milk", ptr("Horizon")); !ok || q != 3 {
		t.Errorf("quantity = %d (found=%v), want 3", q, ok)
	}
	rows, _ := db.ListInventoryItems(context.Background(), "")
	if len(rows) != 1 {
		t.Errorf("rows = %d, want 1: an increment must not also insert", len(rows))
	}
}

func TestApplyConfirmPlanInsertsAndPersistsCanonicalCategory(t *testing.T) {
	db := openTestDB(t)
	actions := []contract.ConfirmAction{{Kind: contract.ConfirmInsert, Key: "milk|horizon", Quantity: 1}}
	items := []ConfirmItem{{
		Name:          "  Whole Milk  ",
		Brand:         ptr("Horizon"),
		Category:      contract.FromModelString("DAIRY"),
		SourcePhotoID: ptr("photo-1"),
	}}
	if err := db.ApplyConfirmPlan(context.Background(), actions, items); err != nil {
		t.Fatalf("ApplyConfirmPlan: %v", err)
	}
	rows, err := db.ListInventoryItems(context.Background(), "")
	if err != nil {
		t.Fatalf("ListInventoryItems: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	// Trimmed on the way in, exactly as the shipped confirm does.
	if rows[0].Name != "Whole Milk" {
		t.Errorf("name = %q, want %q", rows[0].Name, "Whole Milk")
	}
	// The canonical lower-case rawValue, never the model's "DAIRY".
	if rows[0].CategoryRaw != "dairy" {
		t.Errorf("categoryRaw = %q, want dairy", rows[0].CategoryRaw)
	}
	if rows[0].Quantity != 1 {
		t.Errorf("quantity = %d, want the action's own quantity 1", rows[0].Quantity)
	}
}

// The batch-wide running tally is the whole point of emitting both actions:
// confirming the same item twice must end at 2. Dropping the insert loses the
// row; dropping the increment loses the quantity.
func TestApplyConfirmPlanInsertThenIncrementInOneBatch(t *testing.T) {
	db := openTestDB(t)
	actions := []contract.ConfirmAction{
		{Kind: contract.ConfirmInsert, Key: "milk|", Quantity: 1},
		{Kind: contract.ConfirmIncrement, Key: "milk|", Quantity: 2},
	}
	items := []ConfirmItem{
		{Name: "Milk", Category: contract.CategoryDairy},
		{Name: "Milk", Category: contract.CategoryDairy},
	}
	if err := db.ApplyConfirmPlan(context.Background(), actions, items); err != nil {
		t.Fatalf("ApplyConfirmPlan: %v", err)
	}
	rows, _ := db.ListInventoryItems(context.Background(), "")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Quantity != 2 {
		t.Errorf("quantity = %d, want 2", rows[0].Quantity)
	}
}

// A blank name is skipped but still consumes its slot, so the pairing with the
// actions stays aligned for everything after it.
func TestApplyConfirmPlanSkipsBlankNamesWithoutLosingAlignment(t *testing.T) {
	db := openTestDB(t)
	actions := []contract.ConfirmAction{
		{Kind: contract.ConfirmInsert, Key: "|", Quantity: 1},
		{Kind: contract.ConfirmInsert, Key: "eggs|", Quantity: 3},
	}
	items := []ConfirmItem{
		{Name: "   ", Category: contract.CategoryOther},
		{Name: "Eggs", Category: contract.CategoryProduce},
	}
	if err := db.ApplyConfirmPlan(context.Background(), actions, items); err != nil {
		t.Fatalf("ApplyConfirmPlan: %v", err)
	}
	rows, _ := db.ListInventoryItems(context.Background(), "")
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (the blank one skipped)", len(rows))
	}
	if rows[0].Name != "Eggs" || rows[0].Quantity != 3 || rows[0].CategoryRaw != "produce" {
		t.Errorf("row = %+v, want Eggs/3/produce", rows[0])
	}
}

// A present-but-empty brand is stored as NULL, matching
// `review.brand?.isEmpty == true ? nil : review.brand`.
func TestApplyConfirmPlanNormalizesEmptyBrandToNull(t *testing.T) {
	db := openTestDB(t)
	err := db.ApplyConfirmPlan(context.Background(),
		[]contract.ConfirmAction{{Kind: contract.ConfirmInsert, Key: "eggs|", Quantity: 1}},
		[]ConfirmItem{{Name: "Eggs", Brand: ptr(""), Category: contract.CategoryProduce}})
	if err != nil {
		t.Fatalf("ApplyConfirmPlan: %v", err)
	}
	rows, _ := db.ListInventoryItems(context.Background(), "")
	if rows[0].Brand != nil {
		t.Errorf("brand = %q, want nil", *rows[0].Brand)
	}
}

func TestApplyConfirmPlanRejectsMismatchedLengths(t *testing.T) {
	db := openTestDB(t)
	err := db.ApplyConfirmPlan(context.Background(),
		[]contract.ConfirmAction{{Kind: contract.ConfirmInsert, Key: "a|", Quantity: 1}},
		nil)
	if err == nil {
		t.Fatal("ApplyConfirmPlan accepted a length mismatch")
	}
}

// A failure part way through must leave the table exactly as it was: a confirm
// that inserted three rows and lost the fourth is worse than one that did
// nothing.
func TestApplyConfirmPlanIsAtomic(t *testing.T) {
	db := openTestDB(t)
	actions := []contract.ConfirmAction{
		{Kind: contract.ConfirmInsert, Key: "eggs|", Quantity: 1},
		{Kind: contract.ConfirmActionKind(99), Key: "boom|"},
	}
	items := []ConfirmItem{
		{Name: "Eggs", Category: contract.CategoryProduce},
		{Name: "Boom", Category: contract.CategoryOther},
	}
	if err := db.ApplyConfirmPlan(context.Background(), actions, items); err == nil {
		t.Fatal("ApplyConfirmPlan accepted an unknown action kind")
	}
	rows, _ := db.ListInventoryItems(context.Background(), "")
	if len(rows) != 0 {
		t.Errorf("rows = %d after a failed confirm, want 0", len(rows))
	}
}

func TestApplyCookPlanDecrementsAndRemoves(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mustInsert(t, db, contract.InventoryItem{Name: "Eggs", CategoryRaw: "produce", Quantity: 3, DateAdded: base})
	mustInsert(t, db, contract.InventoryItem{Name: "Butter", CategoryRaw: "dairy", Quantity: 1, DateAdded: base.Add(time.Hour)})
	mustInsert(t, db, contract.InventoryItem{Name: "Salt", CategoryRaw: "pantry", Quantity: 5, DateAdded: base.Add(2 * time.Hour)})

	// What CookPlan produces for one egg and one butter: stock order, butter
	// clamped to zero and flagged for removal, salt untouched and absent.
	changes := []contract.CookChange{
		{Key: "eggs", NewQuantity: 2, Removed: false},
		{Key: "butter", NewQuantity: 0, Removed: true},
	}
	if err := db.ApplyCookPlan(context.Background(), changes); err != nil {
		t.Fatalf("ApplyCookPlan: %v", err)
	}
	if q, ok := quantityOf(t, db, "Eggs", nil); !ok || q != 2 {
		t.Errorf("eggs = %d (found=%v), want 2", q, ok)
	}
	if _, ok := quantityOf(t, db, "Butter", nil); ok {
		t.Error("butter reached zero and should have been removed")
	}
	if q, ok := quantityOf(t, db, "Salt", nil); !ok || q != 5 {
		t.Errorf("salt = %d (found=%v), want an untouched 5", q, ok)
	}
}

// A change for something not in stock is skipped, not an error — the shipped
// call site does the same with `guard let item = ... else { continue }`.
func TestApplyCookPlanSkipsUnknownKeys(t *testing.T) {
	db := openTestDB(t)
	mustInsert(t, db, contract.InventoryItem{Name: "Eggs", CategoryRaw: "produce", Quantity: 3})
	err := db.ApplyCookPlan(context.Background(), []contract.CookChange{
		{Key: "saffron", NewQuantity: 0, Removed: true},
		{Key: "eggs", NewQuantity: 2},
	})
	if err != nil {
		t.Fatalf("ApplyCookPlan: %v", err)
	}
	if q, _ := quantityOf(t, db, "Eggs", nil); q != 2 {
		t.Errorf("eggs = %d, want 2", q)
	}
}

// Name-only keys mean two rows differing only by brand share a key, and the
// first row in the canonical order absorbs the change. Shipped behavior, not an
// accident: `inventory.first(where: { contract.NameKey($0.name) == change.key })`.
func TestApplyCookPlanResolvesDuplicateNamesToTheFirstRow(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first := mustInsert(t, db, contract.InventoryItem{
		Name: "Milk", Brand: ptr("Horizon"), CategoryRaw: "dairy", Quantity: 2, DateAdded: base})
	second := mustInsert(t, db, contract.InventoryItem{
		Name: "Milk", Brand: ptr("Fairlife"), CategoryRaw: "dairy", Quantity: 2, DateAdded: base.Add(time.Hour)})

	if err := db.ApplyCookPlan(context.Background(), []contract.CookChange{{Key: "milk", NewQuantity: 1}}); err != nil {
		t.Fatalf("ApplyCookPlan: %v", err)
	}
	got, err := db.GetInventoryItem(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("GetInventoryItem: %v", err)
	}
	if got.Quantity != 1 {
		t.Errorf("first row quantity = %d, want 1", got.Quantity)
	}
	other, err := db.GetInventoryItem(context.Background(), second.ID)
	if err != nil {
		t.Fatalf("GetInventoryItem: %v", err)
	}
	if other.Quantity != 2 {
		t.Errorf("second row quantity = %d, want an untouched 2", other.Quantity)
	}
}

func TestApplyPlansOnEmptyInputAreNoOps(t *testing.T) {
	db := openTestDB(t)
	if err := db.ApplyConfirmPlan(context.Background(), nil, nil); err != nil {
		t.Errorf("ApplyConfirmPlan(nil): %v", err)
	}
	if err := db.ApplyCookPlan(context.Background(), nil); err != nil {
		t.Errorf("ApplyCookPlan(nil): %v", err)
	}
}
