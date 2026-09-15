package contract

import (
	"reflect"
	"strings"
	"testing"
)

// Port of DinnerDeciderTests/ShoppingListLogicTests.swift (5 tests), plus
// Go-only tests for the first-seen-casing rule, the trim/lowercase asymmetry in
// NewItems and the exact bytes of the export.

func TestAggregateDedupesCaseInsensitivelyPreservingOrder(t *testing.T) {
	got := Aggregate([]string{"Onion", "onion", "Chili Powder", "  ", "ONION"})
	want := []string{"Onion", "Chili Powder"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Aggregate = %q, want %q", got, want)
	}
}

func TestAggregateTrimsWhitespaceAndDropsBlanks(t *testing.T) {
	got := Aggregate([]string{"  Milk  ", "", "   "})
	want := []string{"Milk"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Aggregate = %q, want %q", got, want)
	}
}

// Go-only: the FIRST casing survives, not the lowercased comparison key. The
// user sees "Onion", never "onion", however many spellings the model emitted.
func TestAggregatePreservesFirstSeenCasing(t *testing.T) {
	got := Aggregate([]string{"ONION", "Onion", "onion"})
	want := []string{"ONION"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Aggregate = %q, want %q (first-seen casing wins)", got, want)
	}
}

// Go-only: newlines count as whitespace, so a name arriving as "Milk\n" from a
// model reply is the same row as "Milk".
func TestAggregateTrimsNewlinesNotJustSpaces(t *testing.T) {
	got := Aggregate([]string{"Milk\n", "\tmilk ", "\n"})
	want := []string{"Milk"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Aggregate = %q, want %q", got, want)
	}
}

// Go-only: empty in, empty (non-nil) out.
func TestAggregateOfNothingIsEmptyNotNil(t *testing.T) {
	if got := Aggregate(nil); got == nil || len(got) != 0 {
		t.Errorf("Aggregate(nil) = %#v, want an empty non-nil slice", got)
	}
	if got := NewItems(nil, nil); got == nil || len(got) != 0 {
		t.Errorf("NewItems(nil, nil) = %#v, want an empty non-nil slice", got)
	}
}

func TestNewItemsExcludesThingsAlreadyOnList(t *testing.T) {
	got := NewItems([]string{"Onion", "Garlic", "onion"}, []string{"ONION", "Salt"})
	want := []string{"Garlic"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NewItems = %q, want %q", got, want)
	}
}

// Go-only: the existing side IS trimmed, so a stored row saved with stray
// whitespace by an older build still suppresses the duplicate.
func TestNewItemsTrimsTheExistingSide(t *testing.T) {
	got := NewItems([]string{"Onion"}, []string{"  onion  "})
	if len(got) != 0 {
		t.Fatalf("NewItems = %q, want empty: a padded existing row must still match", got)
	}
}

// Go-only: candidates are de-duplicated against EACH OTHER before the existing
// filter, so a doubled suggestion adds one row, not two.
func TestNewItemsDedupesCandidatesAgainstEachOther(t *testing.T) {
	got := NewItems([]string{"  Garlic ", "garlic", "GARLIC"}, []string{"Salt"})
	want := []string{"Garlic"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NewItems = %q, want %q (trimmed by Aggregate, first casing kept)", got, want)
	}
}

func TestExportTextMarksCheckedItems(t *testing.T) {
	text := ExportText([]ShoppingLine{
		{Name: "Onion", IsChecked: false},
		{Name: "Milk", IsChecked: true},
	})
	if !strings.Contains(text, "Shopping List") {
		t.Errorf("export missing header: %q", text)
	}
	if !strings.Contains(text, "[ ] Onion") {
		t.Errorf("export missing unchecked line: %q", text)
	}
	if !strings.Contains(text, "[x] Milk") {
		t.Errorf("export missing checked line: %q", text)
	}
}

// Go-only: the exact bytes. Header, blank line, one line per item, and NO
// trailing newline (the whole string is trimmed at the end).
func TestExportTextExactShapeAndNoTrailingNewline(t *testing.T) {
	text := ExportText([]ShoppingLine{
		{Name: "Onion", IsChecked: false},
		{Name: "Milk", IsChecked: true},
	})
	want := "Shopping List\n\n[ ] Onion\n[x] Milk"
	if text != want {
		t.Fatalf("export = %q, want %q", text, want)
	}
	if strings.HasSuffix(text, "\n") {
		t.Errorf("export ends in a newline: %q", text)
	}
}

func TestExportTextHandlesEmptyList(t *testing.T) {
	if got, want := ExportText(nil), "Shopping List\n\n(empty)"; got != want {
		t.Fatalf("ExportText(nil) = %q, want %q", got, want)
	}
	if got, want := ExportText([]ShoppingLine{}), "Shopping List\n\n(empty)"; got != want {
		t.Fatalf("ExportText([]) = %q, want %q", got, want)
	}
}
