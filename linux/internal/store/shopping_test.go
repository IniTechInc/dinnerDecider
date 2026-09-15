package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
)

func TestShoppingCRUDRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	added := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	row, err := db.InsertShoppingItem(ctx, contract.ShoppingListItem{
		Name: "  Olive Oil  ", IsManual: true, DateAdded: added,
	})
	if err != nil {
		t.Fatalf("InsertShoppingItem: %v", err)
	}
	if row.Name != "Olive Oil" {
		t.Errorf("name = %q, want it trimmed to %q", row.Name, "Olive Oil")
	}

	got, err := db.GetShoppingItem(ctx, row.ID)
	if err != nil {
		t.Fatalf("GetShoppingItem: %v", err)
	}
	if got.Name != "Olive Oil" || got.IsChecked || !got.IsManual || !got.DateAdded.Equal(added) {
		t.Errorf("round trip = %+v", got)
	}

	got.Name = "Extra Virgin Olive Oil"
	got.IsChecked = true
	if err := db.UpdateShoppingItem(ctx, got); err != nil {
		t.Fatalf("UpdateShoppingItem: %v", err)
	}
	after, _ := db.GetShoppingItem(ctx, row.ID)
	if after.Name != "Extra Virgin Olive Oil" || !after.IsChecked {
		t.Errorf("after update = %+v", after)
	}

	if err := db.DeleteShoppingItem(ctx, row.ID); err != nil {
		t.Fatalf("DeleteShoppingItem: %v", err)
	}
	if _, err := db.GetShoppingItem(ctx, row.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
	if err := db.DeleteShoppingItem(ctx, row.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
	if err := db.SetShoppingItemChecked(ctx, row.ID, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("check-off of a deleted line = %v, want ErrNotFound", err)
	}
}

// "Blank and whitespace-only names are dropped, never stored."
func TestShoppingBlankNamesAreNeverStored(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	for _, name := range []string{"", "   ", "\t\n"} {
		if _, err := db.InsertShoppingItem(ctx, contract.ShoppingListItem{Name: name}); !errors.Is(err, ErrBlankName) {
			t.Errorf("InsertShoppingItem(%q) = %v, want ErrBlankName", name, err)
		}
	}
	// The bulk path drops them silently rather than failing the batch.
	inserted, err := db.InsertShoppingNames(ctx, []string{"Eggs", "   ", "Butter", ""}, false)
	if err != nil {
		t.Fatalf("InsertShoppingNames: %v", err)
	}
	if len(inserted) != 2 {
		t.Errorf("inserted %d rows, want 2", len(inserted))
	}
	rows, _ := db.ListShoppingItems(ctx)
	if len(rows) != 2 {
		t.Errorf("stored %d rows, want 2", len(rows))
	}
}

func TestShoppingListIsNeverNilAndIsOrdered(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	rows, err := db.ListShoppingItems(ctx)
	if err != nil {
		t.Fatalf("ListShoppingItems: %v", err)
	}
	if rows == nil {
		t.Error("an empty list returned a nil slice; it marshals to JSON null")
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, name := range []string{"first", "second", "third"} {
		if _, err := db.InsertShoppingItem(ctx, contract.ShoppingListItem{
			Name: name, DateAdded: base.Add(time.Duration(i) * time.Hour),
		}); err != nil {
			t.Fatalf("insert %q: %v", name, err)
		}
	}
	rows, _ = db.ListShoppingItems(ctx)
	if len(rows) != 3 || rows[0].Name != "first" || rows[2].Name != "third" {
		t.Errorf("order = %v", rows)
	}
}

func TestShoppingCheckOffAndClearChecked(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	var ids []string
	for i, name := range []string{"Eggs", "Butter", "Flour"} {
		row, err := db.InsertShoppingItem(ctx, contract.ShoppingListItem{
			Name: name, DateAdded: base.Add(time.Duration(i) * time.Hour)})
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		ids = append(ids, row.ID)
	}

	if err := db.SetShoppingItemChecked(ctx, ids[0], true); err != nil {
		t.Fatalf("SetShoppingItemChecked: %v", err)
	}
	if err := db.SetShoppingItemChecked(ctx, ids[2], true); err != nil {
		t.Fatalf("SetShoppingItemChecked: %v", err)
	}
	got, _ := db.GetShoppingItem(ctx, ids[0])
	if !got.IsChecked {
		t.Error("check-off did not stick")
	}
	// Unchecking works too.
	if err := db.SetShoppingItemChecked(ctx, ids[2], false); err != nil {
		t.Fatalf("un-check: %v", err)
	}

	n, err := db.ClearCheckedShoppingItems(ctx)
	if err != nil {
		t.Fatalf("ClearCheckedShoppingItems: %v", err)
	}
	if n != 1 {
		t.Errorf("cleared %d, want 1", n)
	}
	rows, _ := db.ListShoppingItems(ctx)
	if len(rows) != 2 || rows[0].Name != "Butter" {
		t.Errorf("remaining = %v", rows)
	}

	// Clearing again is a no-op, not an error.
	if n, err := db.ClearCheckedShoppingItems(ctx); err != nil || n != 0 {
		t.Errorf("second clear = %d, %v; want 0, nil", n, err)
	}
}

// The export format is contract, not presentation: the schema's $comment pins
// it line for line, and the empty list is an exact string.
func TestExportTextFormat(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	got, err := db.ExportText(ctx)
	if err != nil {
		t.Fatalf("ExportText: %v", err)
	}
	const wantEmpty = "Shopping List\n\n(empty)"
	if got != wantEmpty {
		t.Errorf("empty export = %q, want %q", got, wantEmpty)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	eggs, _ := db.InsertShoppingItem(ctx, contract.ShoppingListItem{Name: "Eggs", DateAdded: base})
	_, _ = db.InsertShoppingItem(ctx, contract.ShoppingListItem{Name: "Butter", DateAdded: base.Add(time.Hour)})
	if err := db.SetShoppingItemChecked(ctx, eggs.ID, true); err != nil {
		t.Fatalf("check off: %v", err)
	}

	got, err = db.ExportText(ctx)
	if err != nil {
		t.Fatalf("ExportText: %v", err)
	}
	const want = "Shopping List\n\n[x] Eggs\n[ ] Butter"
	if got != want {
		t.Errorf("export = %q, want %q", got, want)
	}
}

// The trailing newline of the last line is trimmed, and nothing else is.
func TestExportTextTrimsOnlyTheTrailingNewline(t *testing.T) {
	got := exportText([]ExportLine{{Name: "Eggs"}})
	const want = "Shopping List\n\n[ ] Eggs"
	if got != want {
		t.Errorf("exportText = %q, want %q", got, want)
	}
}

func TestExistingNamesFeedsTheAggregationSeam(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, _ = db.InsertShoppingItem(ctx, contract.ShoppingListItem{Name: "Olive Oil", DateAdded: base})
	_, _ = db.InsertShoppingItem(ctx, contract.ShoppingListItem{Name: "Eggs", DateAdded: base.Add(time.Hour)})

	names, err := db.ExistingNames(ctx)
	if err != nil {
		t.Fatalf("ExistingNames: %v", err)
	}
	if len(names) != 2 || names[0] != "Olive Oil" || names[1] != "Eggs" {
		t.Errorf("ExistingNames = %v", names)
	}
}

// The bulk path is not an aggregator: it inserts what it is given, because
// dedupe is pure logic that lives in the contract package and is pinned there.
func TestInsertShoppingNamesDoesNotDeduplicate(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if _, err := db.InsertShoppingNames(ctx, []string{"Eggs", "eggs"}, false); err != nil {
		t.Fatalf("InsertShoppingNames: %v", err)
	}
	rows, _ := db.ListShoppingItems(ctx)
	if len(rows) != 2 {
		t.Errorf("rows = %d, want 2: the store must not silently dedupe", len(rows))
	}
}
