package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"strconv"
	"testing"
)

// This is THE PARITY PROOF.
//
// shared/conformance/cases.json holds 27 golden fixtures extracted verbatim from
// the shipped Swift unit tests. The Go parser is conformant when it runs those
// exact fixtures, unmodified, and matches every expected output. Because the
// parser is pure — no model, no network, no filesystem beyond reading this one
// file — the whole suite runs in milliseconds and is the cheapest possible proof
// that the port and the iOS app agree.
//
// TWO RULES, BOTH ABSOLUTE:
//
//  1. NEVER edit a case to make this pass. If the port disagrees with a fixture,
//     either the port is wrong or the Swift changed — and the latter means the
//     Swift test AND the fixture are updated together, in one commit.
//  2. NEVER t.Skip. A skipped parity proof is worse than none: it reports green
//     while proving nothing. A missing or unreadable fixture file is a FATAL
//     error here, deliberately.

// conformanceCasesPath is shared/conformance/cases.json relative to
// linux/internal/contract, matching sharedSchemaDir in types_test.go.
const conformanceCasesPath = "../../../shared/conformance/cases.json"

// conformanceCaseCount and conformanceSuiteVersion are pinned so a truncated,
// filtered or silently-regenerated suite fails loudly rather than passing with
// three cases.
const (
	conformanceCaseCount    = 27
	conformanceSuiteVersion = 1
)

// conformanceOperationCounts is the expected distribution of the 27 cases.
//
// NOTE the absence of "firstJSONArray": it has NO DIRECT FIXTURE. It is
// exercised transitively through all 8 decodeIdentifiedItemArray cases, which
// cannot reach a decoder without it. That is expected and is not a gap in the
// suite — direct coverage lives in parser_extra_test.go
// (TestFirstJSONArrayDirect and friends). Do not go hunting for missing cases.
var conformanceOperationCounts = map[string]int{
	"decodeIdentifiedItem":      8,
	"decodeIdentifiedItemArray": 8,
	"firstJSONObject":           6,
	"decodeRecipeBundle":        3,
	"stripReasoning":            2,
}

// conformanceSuite is the whole cases.json file.
type conformanceSuite struct {
	Version        int               `json:"version"`
	FloatTolerance float64           `json:"floatTolerance"`
	Cases          []conformanceCase `json:"cases"`
}

// conformanceCase is one fixture.
//
// Expected is a RawMessage so that an ABSENT "expected" key stays
// distinguishable from a present one. The README is explicit: a missing
// "expected" alongside "expectNull": false is a MALFORMED SUITE, not a
// roundabout way of saying "expect null". Treating it as the latter would let a
// fixture silently lose its expectation and still report green.
type conformanceCase struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	Source      string          `json:"source"`
	Operation   string          `json:"operation"`
	Input       string          `json:"input"`
	ExpectNull  bool            `json:"expectNull"`
	Expected    json.RawMessage `json:"expected"`
}

// conformanceItem is the expected shape of a decoded IdentifiedItem.
//
// Brand is a *string so the fixture's three-way distinction survives: an absent
// key and an explicit JSON null both land on nil and are the SAME result, while
// an empty string is a third, different thing that must never compare equal to
// either.
type conformanceItem struct {
	Name       string  `json:"name"`
	Brand      *string `json:"brand"`
	Category   string  `json:"category"`
	Confidence float64 `json:"confidence"`
}

// TestParserConformance runs all 27 golden fixtures.
func TestParserConformance(t *testing.T) {
	suite := loadConformanceSuite(t)
	validateConformanceSuite(t, suite)

	for _, c := range suite.Cases {
		// Subtest names are the case IDs, so a failure reports as
		// TestParserConformance/<id> and traces straight back to the Swift test
		// named in that case's `source`.
		t.Run(c.ID, func(t *testing.T) {
			runConformanceCase(t, c, suite.FloatTolerance)
		})
	}
}

// loadConformanceSuite reads and parses cases.json. A missing file is FATAL.
func loadConformanceSuite(t *testing.T) conformanceSuite {
	t.Helper()

	data, err := os.ReadFile(conformanceCasesPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("conformance fixtures not found at %s: %v\n"+
				"This file IS the parity proof. Do not skip it and do not stub it out: "+
				"restore shared/conformance/cases.json from the repository.",
				conformanceCasesPath, err)
		}
		t.Fatalf("reading %s: %v", conformanceCasesPath, err)
	}

	var suite conformanceSuite
	if err := json.Unmarshal(data, &suite); err != nil {
		t.Fatalf("parsing %s: %v", conformanceCasesPath, err)
	}
	return suite
}

// validateConformanceSuite fails the whole test — not one subtest — if the suite
// itself is wrong. Every check here guards against a green run that proves less
// than it claims.
func validateConformanceSuite(t *testing.T, suite conformanceSuite) {
	t.Helper()

	if suite.Version != conformanceSuiteVersion {
		t.Fatalf("conformance suite version = %d, want %d. The file format changed; "+
			"update this runner deliberately rather than loosening the check.",
			suite.Version, conformanceSuiteVersion)
	}
	if len(suite.Cases) != conformanceCaseCount {
		t.Fatalf("conformance suite has %d cases, want %d. A truncated or filtered "+
			"suite must fail loudly: every case runs, none is skipped or tagged known-failing.",
			len(suite.Cases), conformanceCaseCount)
	}
	if suite.FloatTolerance <= 0 || suite.FloatTolerance > 0.01 {
		t.Fatalf("floatTolerance = %v, which is not a usable tolerance. The suite "+
			"declares 1e-4; a tolerance of zero cannot match a float and a loose one "+
			"would hide real precision bugs.", suite.FloatTolerance)
	}

	seen := make(map[string]bool, len(suite.Cases))
	counts := make(map[string]int, len(conformanceOperationCounts))
	for i, c := range suite.Cases {
		if c.ID == "" {
			t.Fatalf("case #%d has no id; failures could not be traced back to a Swift test", i)
		}
		if seen[c.ID] {
			t.Fatalf("duplicate case id %q; ids must be unique so a failure names exactly one fixture", c.ID)
		}
		seen[c.ID] = true

		if !c.ExpectNull && c.Expected == nil {
			t.Fatalf("case %q (%s) has expectNull:false and no `expected` key. "+
				"That is a MALFORMED SUITE, not \"expect null\" — fix the fixture, "+
				"never reinterpret it.", c.ID, c.Source)
		}
		if c.ExpectNull && c.Expected != nil {
			t.Fatalf("case %q (%s) has expectNull:true and an `expected` key; "+
				"the two are mutually exclusive", c.ID, c.Source)
		}
		counts[c.Operation]++
	}

	for op, want := range conformanceOperationCounts {
		if counts[op] != want {
			t.Fatalf("operation %q has %d cases, want %d. The expected distribution is "+
				"8/8/6/3/2 across decodeIdentifiedItem, decodeIdentifiedItemArray, "+
				"firstJSONObject, decodeRecipeBundle and stripReasoning.",
				op, counts[op], want)
		}
	}
	for op := range counts {
		if _, known := conformanceOperationCounts[op]; !known {
			t.Fatalf("unknown operation %q in the suite; this runner dispatches on "+
				"operation and would silently not exercise it", op)
		}
	}
}

// runConformanceCase dispatches one fixture on its operation.
func runConformanceCase(t *testing.T, c conformanceCase, tolerance float64) {
	t.Helper()

	switch c.Operation {
	case "stripReasoning":
		// Documented as "string (never null)".
		if c.ExpectNull {
			t.Fatalf("case %s (%s): stripReasoning never returns null; the fixture is malformed", c.ID, c.Source)
		}
		// Byte-exact, no trimming: the marker leaves whatever whitespace
		// followed it, and text without a marker comes back byte-identical.
		want := conformanceExpectedString(t, c)
		got := StripReasoning(c.Input)
		if got != want {
			t.Errorf("case %s (%s): StripReasoning mismatch\n got: %s\nwant: %s",
				c.ID, c.Source, strconv.Quote(got), strconv.Quote(want))
		}

	case "firstJSONObject":
		got, ok := FirstJSONObject(c.Input)
		conformanceCheckExtraction(t, c, "FirstJSONObject", got, ok)

	case "firstJSONArray":
		got, ok := FirstJSONArray(c.Input)
		conformanceCheckExtraction(t, c, "FirstJSONArray", got, ok)

	case "decodeIdentifiedItem":
		got, ok := DecodeIdentifiedItem(c.Input)
		if c.ExpectNull {
			if ok {
				t.Errorf("case %s (%s): DecodeIdentifiedItem returned a value, want none: %+v",
					c.ID, c.Source, got)
			}
			return
		}
		if !ok {
			t.Fatalf("case %s (%s): DecodeIdentifiedItem returned no value, want %s",
				c.ID, c.Source, c.Expected)
		}
		var want conformanceItem
		conformanceUnmarshalExpected(t, c, &want)
		conformanceCompareItem(t, c, "", got, want, tolerance)

	case "decodeIdentifiedItemArray":
		got, ok := DecodeIdentifiedItemArray(c.Input)
		if c.ExpectNull {
			if ok {
				t.Errorf("case %s (%s): DecodeIdentifiedItemArray returned %d items, want no value",
					c.ID, c.Source, len(got))
			}
			return
		}
		if !ok {
			t.Fatalf("case %s (%s): DecodeIdentifiedItemArray returned no value, want %s",
				c.ID, c.Source, c.Expected)
		}
		// The nil-versus-empty trap, asserted rather than assumed: a successful
		// decode of "[]" must hand back a NON-NIL empty slice. "The model saw
		// nothing" and "the model was unreadable" are different outcomes, and
		// array-empty / array-empty-output exist to keep them apart.
		if got == nil {
			t.Errorf("case %s (%s): DecodeIdentifiedItemArray reported success with a nil slice; "+
				"a nil slice must never stand in for a parse failure", c.ID, c.Source)
		}
		var want []conformanceItem
		conformanceUnmarshalExpected(t, c, &want)
		// Arrays are order- and length-sensitive.
		if len(got) != len(want) {
			t.Fatalf("case %s (%s): got %d items, want %d", c.ID, c.Source, len(got), len(want))
		}
		for i := range want {
			conformanceCompareItem(t, c, fmt.Sprintf("[%d]", i), got[i], want[i], tolerance)
		}

	case "decodeRecipeBundle":
		got, ok := DecodeRecipeBundle(c.Input)
		if c.ExpectNull {
			if ok {
				t.Errorf("case %s (%s): DecodeRecipeBundle returned a value, want none "+
					"(makeNow=%d, almostThere=%d)", c.ID, c.Source, len(got.MakeNow), len(got.AlmostThere))
			}
			return
		}
		if !ok {
			t.Fatalf("case %s (%s): DecodeRecipeBundle returned no value, want a bundle", c.ID, c.Source)
		}
		var want RecipeBundleResponse
		conformanceUnmarshalExpected(t, c, &want)
		conformanceCompareBundle(t, c, got, want)

	default:
		t.Fatalf("case %s (%s): unknown operation %q", c.ID, c.Source, c.Operation)
	}
}

// conformanceCheckExtraction applies the shared rules for the two extractors:
// no value when expectNull, otherwise the BYTE-EXACT raw substring. No trimming,
// no re-serialization — the extractor hands back a slice of its input and the
// fixture pins it character for character.
func conformanceCheckExtraction(t *testing.T, c conformanceCase, fn, got string, ok bool) {
	t.Helper()

	if c.ExpectNull {
		if ok {
			t.Errorf("case %s (%s): %s returned %s, want no value",
				c.ID, c.Source, fn, strconv.Quote(got))
		}
		return
	}
	want := conformanceExpectedString(t, c)
	if !ok {
		t.Fatalf("case %s (%s): %s returned no value, want %s",
			c.ID, c.Source, fn, strconv.Quote(want))
	}
	if got != want {
		t.Errorf("case %s (%s): %s mismatch\n got: %s\nwant: %s",
			c.ID, c.Source, fn, strconv.Quote(got), strconv.Quote(want))
	}
}

// conformanceCompareItem compares one decoded IdentifiedItem against a fixture.
func conformanceCompareItem(t *testing.T, c conformanceCase, path string, got IdentifiedItem, want conformanceItem, tolerance float64) {
	t.Helper()

	if got.Name != want.Name {
		t.Errorf("case %s (%s): %sname = %s, want %s",
			c.ID, c.Source, conformancePrefix(path), strconv.Quote(got.Name), strconv.Quote(want.Name))
	}
	// brand absent == brand null != brand "".
	if !conformanceOptionalEqual(got.Brand, want.Brand) {
		t.Errorf("case %s (%s): %sbrand = %s, want %s",
			c.ID, c.Source, conformancePrefix(path),
			conformanceOptionalString(got.Brand), conformanceOptionalString(want.Brand))
	}
	if got.Category != want.Category {
		t.Errorf("case %s (%s): %scategory = %s, want %s",
			c.ID, c.Source, conformancePrefix(path), strconv.Quote(got.Category), strconv.Quote(want.Category))
	}
	// Floats compare within the suite's tolerance, matching the `accuracy:`
	// argument in the Swift assertions.
	if math.Abs(got.Confidence-want.Confidence) > tolerance {
		t.Errorf("case %s (%s): %sconfidence = %v, want %v (tolerance %v)",
			c.ID, c.Source, conformancePrefix(path), got.Confidence, want.Confidence, tolerance)
	}
}

// conformanceCompareBundle compares a decoded recipe bundle field by field.
//
// The bundle carries no floats, so every comparison here is exact — and
// timeMinutes is an integer that must compare EXACTLY, never within a tolerance.
func conformanceCompareBundle(t *testing.T, c conformanceCase, got, want RecipeBundleResponse) {
	t.Helper()

	conformanceCompareSuggestions(t, c, "makeNow", got.MakeNow, want.MakeNow)
	conformanceCompareSuggestions(t, c, "almostThere", got.AlmostThere, want.AlmostThere)
}

func conformanceCompareSuggestions(t *testing.T, c conformanceCase, bucket string, got, want []RecipeSuggestion) {
	t.Helper()

	if len(got) != len(want) {
		t.Errorf("case %s (%s): %s has %d suggestions, want %d", c.ID, c.Source, bucket, len(got), len(want))
		return
	}
	for i := range want {
		path := fmt.Sprintf("%s[%d]", bucket, i)
		g, w := got[i], want[i]

		if g.Name != w.Name {
			t.Errorf("case %s (%s): %s.name = %s, want %s",
				c.ID, c.Source, path, strconv.Quote(g.Name), strconv.Quote(w.Name))
		}
		if g.TimeMinutes != w.TimeMinutes {
			t.Errorf("case %s (%s): %s.timeMinutes = %d, want %d (integers compare exactly)",
				c.ID, c.Source, path, g.TimeMinutes, w.TimeMinutes)
		}
		conformanceCompareStringSlice(t, c, path+".steps", g.Steps, w.Steps)
		conformanceCompareStringSlice(t, c, path+".missingItems", g.MissingItems, w.MissingItems)

		if len(g.Ingredients) != len(w.Ingredients) {
			t.Errorf("case %s (%s): %s.ingredients has %d entries, want %d",
				c.ID, c.Source, path, len(g.Ingredients), len(w.Ingredients))
			continue
		}
		for j := range w.Ingredients {
			gi, wi := g.Ingredients[j], w.Ingredients[j]
			if gi.Name != wi.Name {
				t.Errorf("case %s (%s): %s.ingredients[%d].name = %s, want %s",
					c.ID, c.Source, path, j, strconv.Quote(gi.Name), strconv.Quote(wi.Name))
			}
			if gi.HasIt != wi.HasIt {
				t.Errorf("case %s (%s): %s.ingredients[%d].hasIt = %v, want %v",
					c.ID, c.Source, path, j, gi.HasIt, wi.HasIt)
			}
		}
	}
}

// conformanceCompareStringSlice compares order- and length-sensitively.
func conformanceCompareStringSlice(t *testing.T, c conformanceCase, path string, got, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Errorf("case %s (%s): %s has %d entries, want %d", c.ID, c.Source, path, len(got), len(want))
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("case %s (%s): %s[%d] = %s, want %s",
				c.ID, c.Source, path, i, strconv.Quote(got[i]), strconv.Quote(want[i]))
		}
	}
}

// conformanceExpectedString decodes a fixture's `expected` as a JSON string.
func conformanceExpectedString(t *testing.T, c conformanceCase) string {
	t.Helper()

	var want string
	conformanceUnmarshalExpected(t, c, &want)
	return want
}

// conformanceUnmarshalExpected decodes a fixture's `expected` into target.
func conformanceUnmarshalExpected(t *testing.T, c conformanceCase, target any) {
	t.Helper()

	if c.Expected == nil {
		t.Fatalf("case %s (%s): no `expected` key with expectNull:false", c.ID, c.Source)
	}
	if err := json.Unmarshal(c.Expected, target); err != nil {
		t.Fatalf("case %s (%s): the fixture's `expected` does not fit the %s result shape: %v",
			c.ID, c.Source, c.Operation, err)
	}
}

// conformanceOptionalEqual treats "no value" on both sides as equal, and "no
// value" versus any string — INCLUDING the empty string — as unequal.
func conformanceOptionalEqual(got, want *string) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}

func conformanceOptionalString(p *string) string {
	if p == nil {
		return "null"
	}
	return strconv.Quote(*p)
}

// conformancePrefix renders an array index path as a field prefix.
func conformancePrefix(path string) string {
	if path == "" {
		return ""
	}
	return path + "."
}
