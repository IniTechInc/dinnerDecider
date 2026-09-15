package contract

import "strings"

// This file ports `enum ShoppingListLogic` from
// DinnerDecider/Services/ShoppingListLogic.swift: the pure helpers for building
// and exporting the shopping list. No I/O and no storage, so the store can apply
// what these return without re-deriving it.
//
// Same trimming note as inventory.go: Swift trims with `.whitespacesAndNewlines`
// and strings.TrimSpace is the faithful equivalent.

// Aggregate de-duplicates ingredient names case-insensitively, dropping blanks
// and preserving first-seen order.
//
// Used when adding an "almost there" recipe's missingItems to the list, where
// the same ingredient routinely arrives twice from two suggestions with
// different capitalization.
//
// The casing that survives is the FIRST one seen, not the lowercased key. The
// key exists only to compare; the user sees "Onion", not "onion". That is what
// testAggregateDedupesCaseInsensitivelyPreservingOrder pins, and it is the
// reason the seen-set and the output list are separate structures.
//
// Blank-after-trim entries are dropped entirely rather than added as an empty
// row, so a model that emits a stray "" or "  " in missingItems cannot put a
// nameless line on the user's shopping list.
//
// The returned slice is never nil.
func Aggregate(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	result := make([]string, 0, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, name)
	}
	return result
}

// NewItems returns only the names not already on the list.
//
// Note the asymmetry between the two sides, which is Swift's and is deliberate:
//
//   - EXISTING entries are trimmed AND lowercased to build the comparison set.
//     They come from the store, where a row may well have been saved with
//     stray whitespace by an older build.
//   - The candidates are run through Aggregate FIRST, so they are already
//     trimmed by the time they are filtered; the filter therefore only
//     lowercases them. Trimming twice would be harmless but it is not what the
//     source does, and the second trim is what a reader would mistake for the
//     first.
//
// Candidates are also de-duplicated against each other by Aggregate before the
// existing-list filter runs, so ["Onion", "onion"] contributes at most one row.
//
// The returned slice is never nil.
func NewItems(adding []string, existing []string) []string {
	existingKeys := make(map[string]struct{}, len(existing))
	for _, name := range existing {
		existingKeys[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}

	candidates := Aggregate(adding)
	out := make([]string, 0, len(candidates))
	for _, name := range candidates {
		// Already trimmed by Aggregate; only the case fold is needed here.
		if _, have := existingKeys[strings.ToLower(name)]; have {
			continue
		}
		out = append(out, name)
	}
	return out
}

// ShoppingLine is a plain shopping-list line for export, decoupled from storage
// so the rendering is testable. Port of `ShoppingListLogic.Line`, renamed
// because this package is flat and a bare `Line` would say nothing.
type ShoppingLine struct {
	Name string
	// IsChecked marks an item already picked up. Checked items are exported,
	// marked done, rather than filtered out: a shared list reads better when it
	// shows what has been got as well as what is left.
	IsChecked bool
}

// ExportText renders the list as plain text for sharing.
//
// The shape, verbatim:
//
//	Shopping List
//	<blank line>
//	[ ] Onion
//	[x] Milk
//
// with NO trailing newline - the whole string is trimmed at the end, which is
// what removes the last line's "\n". The checkbox is "[x] " or "[ ] ", three
// characters plus one space, so both forms align.
//
// An EMPTY list is exactly "Shopping List\n\n(empty)". That early return is not
// an optimization: without it the loop would produce the header alone, the
// trailing trim would eat the blank line too, and the user would share a bare
// title with no indication that the list is empty rather than truncated.
func ExportText(lines []ShoppingLine) string {
	if len(lines) == 0 {
		return "Shopping List\n\n(empty)"
	}
	var out strings.Builder
	out.WriteString("Shopping List\n\n")
	for _, line := range lines {
		box := "[ ]"
		if line.IsChecked {
			box = "[x]"
		}
		out.WriteString(box)
		out.WriteString(" ")
		out.WriteString(line.Name)
		out.WriteString("\n")
	}
	return strings.TrimSpace(out.String())
}
