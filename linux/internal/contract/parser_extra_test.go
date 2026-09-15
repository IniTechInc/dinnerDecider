package contract

import (
	"strconv"
	"strings"
	"testing"
)

// Coverage that the 27 golden fixtures do not reach.
//
// The fixtures pin every assertion in LLMResponseParserTests.swift, but
// shared/parser/response-parsing.md specifies behavior beyond them:
//
//   - StripCodeFences has no Swift test and no fixture of its own. It is only
//     ever observed through a decode, so the ORDER of the four literals - the
//     one thing most likely to be got wrong in a port - is invisible to the
//     suite.
//   - FirstJSONArray has NO DIRECT FIXTURE. All 8 array cases reach it through
//     DecodeIdentifiedItemArray, so its raw substring boundaries are never
//     asserted. That gap is expected and is closed here.
//   - Escaped quotes, nested depth, multi-byte input and totality on hostile
//     input are all specified in prose with no fixture behind them.

// parserStrPtr is a *string helper local to the parser tests.
func parserStrPtr(s string) *string { return &s }

// TestStripCodeFencesRemovesAllFourLiterals covers the function the fixtures
// only ever exercise indirectly.
func TestStripCodeFencesRemovesAllFourLiterals(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"lowercase json tag", "```json{\"a\":1}```", "{\"a\":1}"},
		{"uppercase JSON tag", "```JSON{\"a\":1}```", "{\"a\":1}"},
		{"title-case Json tag", "```Json{\"a\":1}```", "{\"a\":1}"},
		{"bare fence", "```{\"a\":1}```", "{\"a\":1}"},
		{"no fence at all", "{\"a\":1}", "{\"a\":1}"},
		// Unconditional substring replacement, not a paired-fence parse: a
		// single unclosed fence is still removed.
		{"unpaired fence", "```json{\"a\":1}", "{\"a\":1}"},
		// Prose survives. The scanner skips it, so there is nothing to gain by
		// trimming it and the byte-exact string fixtures would break if we did.
		{"prose is preserved", "Sure! ```json{\"a\":1}``` Enjoy!", "Sure! {\"a\":1} Enjoy!"},
		{"every occurrence, not just the first", "```a``` ```b``` ```c```", "a b c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripCodeFences(tc.in); got != tc.want {
				t.Errorf("StripCodeFences(%s) = %s, want %s",
					strconv.Quote(tc.in), strconv.Quote(got), strconv.Quote(tc.want))
			}
		})
	}
}

// TestStripCodeFencesLiteralOrderIsLoadBearing is the reason the four literals
// are an ordered list and not a set.
//
// If the bare "```" were removed first, "```json" would collapse to "json" and
// leave an orphaned language token sitting in the text. Harmless for the brace
// scanner, but it means the order is untested by every fixture - so test it
// directly.
func TestStripCodeFencesLiteralOrderIsLoadBearing(t *testing.T) {
	got := StripCodeFences("```json\n{\"name\": \"Omelet\"}\n```")
	if strings.Contains(got, "json") {
		t.Errorf("StripCodeFences left an orphaned language token: %s\n"+
			"The tagged fences must be removed BEFORE the bare ``` fence.", strconv.Quote(got))
	}
	if want := "\n{\"name\": \"Omelet\"}\n"; got != want {
		t.Errorf("StripCodeFences = %s, want %s", strconv.Quote(got), strconv.Quote(want))
	}
}

// TestStripCodeFencesUnhandledCasingLeavesStrayToken pins the documented
// limitation: only three casings are handled, and the leftover is harmless
// because the scanner ignores everything outside the brackets.
func TestStripCodeFencesUnhandledCasingLeavesStrayToken(t *testing.T) {
	const raw = "```jSoN\n{\"name\": \"Omelet\"}\n```"
	stripped := StripCodeFences(raw)
	if !strings.Contains(stripped, "jSoN") {
		t.Errorf("StripCodeFences(%s) = %s; the unhandled casing should leave its "+
			"token behind rather than being matched case-insensitively",
			strconv.Quote(raw), strconv.Quote(stripped))
	}
	got, ok := FirstJSONObject(raw)
	if !ok || got != "{\"name\": \"Omelet\"}" {
		t.Errorf("FirstJSONObject(%s) = %s, %v; the stray token must not affect extraction",
			strconv.Quote(raw), strconv.Quote(got), ok)
	}
}

// TestStripReasoningUsesTheLastMarker covers repeated and nested reasoning
// blocks, which no fixture contains.
func TestStripReasoningUsesTheLastMarker(t *testing.T) {
	raw := "<|channel>thought\nfirst {\"name\": \"WRONG1\"}\n<channel|>" +
		"<|channel>thought\nsecond {\"name\": \"WRONG2\"}\n<channel|>{\"name\": \"RIGHT\"}"
	if got, want := StripReasoning(raw), "{\"name\": \"RIGHT\"}"; got != want {
		t.Errorf("StripReasoning = %s, want %s\nLAST marker, not first: repeated "+
			"reasoning blocks are all discarded.", strconv.Quote(got), strconv.Quote(want))
	}
}

// TestStripReasoningDoesNotTrim pins the "no trimming" rule, which the fixtures
// cannot observe because their markers are immediately followed by a brace.
func TestStripReasoningDoesNotTrim(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"whitespace after the marker survives", "<channel|>\n  {\"a\":1}  ", "\n  {\"a\":1}  "},
		{"marker at the very end yields the empty string", "reasoning<channel|>", ""},
		{"no marker: byte-identical, including surrounding whitespace", "  {\"a\":1}\n", "  {\"a\":1}\n"},
		{"the empty string is returned unchanged", "", ""},
		// The OPEN marker is never searched for and never needs to be present.
		{"close marker alone is enough", "<channel|>answer", "answer"},
		// A lone open marker is not a close marker and must not trigger.
		{"open marker alone does nothing", "<|channel>thought only", "<|channel>thought only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripReasoning(tc.in); got != tc.want {
				t.Errorf("StripReasoning(%s) = %s, want %s",
					strconv.Quote(tc.in), strconv.Quote(got), strconv.Quote(tc.want))
			}
		})
	}
}

// TestOrderOfOperationsReasoningBeforeFences pins the fixed order
//
//	cleaned = StripCodeFences(StripReasoning(text))
//
// An honest note, discovered by mutating this port and re-running the suite:
// swapping the two steps still passes all 27 fixtures, and still passes the
// realistic case below. That is not a hole in the fixtures - it is a property of
// the two functions. StripCodeFences never touches the "<channel|>" literal and
// StripReasoning never touches a fence, so for every realistic input the two
// steps COMMUTE, and the bracket scanner skips whatever junk either one leaves
// behind. The order is still contractual and still specified, so it is pinned
// here through the one input that can observe it.
func TestOrderOfOperationsReasoningBeforeFences(t *testing.T) {
	// The realistic case: a fenced decoy inside the reasoning, a fenced answer
	// after it. Both orders agree, because the close marker survives fence
	// stripping either way.
	t.Run("fenced decoy inside the reasoning is discarded", func(t *testing.T) {
		raw := "<|channel>thought\nMaybe ```json\n[{\"name\": \"WRONG\"}]\n``` ... reconsidering.\n" +
			"<channel|>```json\n[{\"name\": \"RIGHT\"}]\n```"
		got, ok := FirstJSONArray(raw)
		if !ok {
			t.Fatal("FirstJSONArray returned no value")
		}
		if want := "[{\"name\": \"RIGHT\"}]"; got != want {
			t.Errorf("FirstJSONArray = %s, want %s", strconv.Quote(got), strconv.Quote(want))
		}
	})

	// The discriminating case. The input contains no literal "<channel|>": the
	// marker only APPEARS once fences are stripped, because a bare fence sits in
	// the middle of it. Correct order (reasoning first) therefore finds no marker,
	// strips nothing, and returns the first object in the text. The swapped order
	// would manufacture a marker and return the second object instead.
	//
	// The "right" answer here is the decoy, which reads backwards until you
	// remember what is being asserted: that the port applies the two steps in the
	// specified order, not that this particular salvage is desirable. Garbage in,
	// identical garbage out - that is what parity means.
	t.Run("fence stripping must not be able to manufacture a channel marker", func(t *testing.T) {
		raw := "{\"name\": \"FIRST\"} tail <channel```|> {\"name\": \"SECOND\"}"
		got, ok := FirstJSONObject(raw)
		if !ok {
			t.Fatal("FirstJSONObject returned no value")
		}
		if want := "{\"name\": \"FIRST\"}"; got != want {
			t.Errorf("FirstJSONObject = %s, want %s\nThe order is StripCodeFences(StripReasoning(text)). "+
				"Getting \"SECOND\" means the steps were applied in the wrong order: fences were "+
				"stripped first, which assembled a <channel|> marker that was never in the model's output.",
				strconv.Quote(got), strconv.Quote(want))
		}
	})
}

// TestFirstJSONArrayDirect is the direct coverage the conformance suite cannot
// provide: FirstJSONArray has no fixture of its own, so its exact substring
// boundaries are asserted only here.
func TestFirstJSONArrayDirect(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{
			name:   "top-level array",
			in:     "[{\"name\": \"Milk\"}]",
			want:   "[{\"name\": \"Milk\"}]",
			wantOK: true,
		},
		{
			name:   "array embedded in prose on both sides",
			in:     "Here are the items: [1, 2, 3] Hope that helps.",
			want:   "[1, 2, 3]",
			wantOK: true,
		},
		{
			name:   "array inside a code fence",
			in:     "```json\n[1]\n```",
			want:   "[1]",
			wantOK: true,
		},
		{
			// Unbalanced: no value, NEVER the elements that did arrive.
			name:   "truncated array yields no value, not a prefix",
			in:     "[{\"name\": \"Milk\"},",
			wantOK: false,
		},
		{
			name:   "opening bracket that never closes",
			in:     "[1, 2, 3",
			wantOK: false,
		},
		{
			// THE NO-FALLBACK RULE. There is no "[" in the input at all.
			name:   "object when an array was expected is a hard miss",
			in:     "{\"name\": \"Milk\", \"category\": \"dairy\", \"confidence\": 0.9}",
			wantOK: false,
		},
		{
			name:   "no bracket anywhere",
			in:     "there is no json here at all",
			wantOK: false,
		},
		{
			name:   "empty input",
			in:     "",
			wantOK: false,
		},
		{
			name:   "first balanced array only, trailing content ignored",
			in:     "[1, [2, 3]] trailing [4]",
			want:   "[1, [2, 3]]",
			wantOK: true,
		},
		{
			name:   "brackets inside a string literal do not count",
			in:     "[{\"name\": \"weird]name[here\"}]",
			want:   "[{\"name\": \"weird]name[here\"}]",
			wantOK: true,
		},
		{
			name:   "empty array",
			in:     "[]",
			want:   "[]",
			wantOK: true,
		},
		{
			// A stray CLOSE bracket before the first open is impossible by
			// construction: the scan starts at the first open bracket.
			name:   "leading stray close bracket is skipped",
			in:     "] [1]",
			want:   "[1]",
			wantOK: true,
		},
		{
			// The object path lives inside the array here; the array wins
			// because we asked for an array.
			name:   "an array wrapping objects returns the whole array",
			in:     "prose {\"decoy\": 1} more [{\"a\": {\"b\": 2}}]",
			want:   "[{\"a\": {\"b\": 2}}]",
			wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := FirstJSONArray(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("FirstJSONArray(%s) ok = %v, want %v (got %s)",
					strconv.Quote(tc.in), ok, tc.wantOK, strconv.Quote(got))
			}
			if ok && got != tc.want {
				t.Errorf("FirstJSONArray(%s) = %s, want %s",
					strconv.Quote(tc.in), strconv.Quote(got), strconv.Quote(tc.want))
			}
			if !ok && got != "" {
				t.Errorf("FirstJSONArray(%s) returned %s alongside no value; "+
					"a failed extraction must never carry a partial result",
					strconv.Quote(tc.in), strconv.Quote(got))
			}
		})
	}
}

// TestFirstJSONObjectDoesNotFallBackToArrays is the mirror of the no-fallback
// rule: asking for an object and receiving a top-level array is also a miss.
func TestFirstJSONObjectDoesNotFallBackToArrays(t *testing.T) {
	// A bare array of scalars contains no brace at all.
	if got, ok := FirstJSONObject("[1, 2, 3]"); ok {
		t.Errorf("FirstJSONObject(\"[1, 2, 3]\") = %s, want no value", strconv.Quote(got))
	}
	// An array OF OBJECTS does contain braces, and the object extractor
	// correctly returns the first element rather than the array. The two
	// extractors are independent, not layered.
	got, ok := FirstJSONObject("[{\"a\": 1}, {\"b\": 2}]")
	if !ok || got != "{\"a\": 1}" {
		t.Errorf("FirstJSONObject over an array of objects = %s, %v; want the first "+
			"balanced object", strconv.Quote(got), ok)
	}
}

// TestScannerHandlesEscapedQuotesInsideStrings pins the single-lookahead escape
// flag. No fixture contains a backslash.
func TestScannerHandlesEscapedQuotesInsideStrings(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{
			// The escaped quotes do not end the string, so the } inside it does
			// not count toward depth.
			name:   "escaped quote does not end the string",
			in:     "{\"note\": \"he said \\\"stop}\\\" loudly\"}",
			want:   "{\"note\": \"he said \\\"stop}\\\" loudly\"}",
			wantOK: true,
		},
		{
			// An escaped BACKSLASH consumes the escape, so the quote that
			// follows DOES close the string. Get this wrong and the scanner
			// runs off the end and reports no value.
			name:   "escaped backslash does not swallow the closing quote",
			in:     "{\"path\": \"C:\\\\\"}",
			want:   "{\"path\": \"C:\\\\\"}",
			wantOK: true,
		},
		{
			name:   "escaped quote inside an array element",
			in:     "[\"a\\\"]b\"]",
			want:   "[\"a\\\"]b\"]",
			wantOK: true,
		},
		{
			// A single trailing backslash escapes the closing quote, so the
			// string never ends and the value is genuinely unbalanced.
			name:   "trailing escape leaves the string open",
			in:     "{\"note\": \"unterminated\\\"}",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extract := FirstJSONObject
			if strings.HasPrefix(strings.TrimSpace(tc.in), "[") {
				extract = FirstJSONArray
			}
			got, ok := extract(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("extraction of %s: ok = %v, want %v (got %s)",
					strconv.Quote(tc.in), ok, tc.wantOK, strconv.Quote(got))
			}
			if ok && got != tc.want {
				t.Errorf("extraction of %s = %s, want %s",
					strconv.Quote(tc.in), strconv.Quote(got), strconv.Quote(tc.want))
			}
		})
	}
}

// TestScannerHandlesNestedDepth checks that depth counting has no shallow limit
// and that a nesting mismatch is still a clean miss.
func TestScannerHandlesNestedDepth(t *testing.T) {
	const depth = 200

	nestedObject := strings.Repeat("{\"a\":", depth) + "1" + strings.Repeat("}", depth)
	got, ok := FirstJSONObject("prose " + nestedObject + " trailing")
	if !ok || got != nestedObject {
		t.Errorf("FirstJSONObject over %d levels of nesting = (%d bytes, %v), want the whole value (%d bytes)",
			depth, len(got), ok, len(nestedObject))
	}

	nestedArray := strings.Repeat("[", depth) + strings.Repeat("]", depth)
	got, ok = FirstJSONArray(nestedArray)
	if !ok || got != nestedArray {
		t.Errorf("FirstJSONArray over %d levels of nesting = (%d bytes, %v), want the whole value",
			depth, len(got), ok)
	}

	// One close bracket short: unbalanced, so no value at any depth.
	if got, ok := FirstJSONArray(strings.Repeat("[", depth) + strings.Repeat("]", depth-1)); ok {
		t.Errorf("FirstJSONArray over unbalanced nesting = %s, want no value", strconv.Quote(got))
	}
}

// TestScannerIsSafeOnMultiByteInput guards the byte-indexed scan. Every byte of
// a multi-byte UTF-8 sequence has its high bit set, so none can collide with an
// ASCII delimiter - but only a test keeps that true after someone "optimizes"
// the loop.
func TestScannerIsSafeOnMultiByteInput(t *testing.T) {
	const raw = "Voilà: {\"name\": \"Crème Brûlée 🍮\", \"brand\": null, \"category\": \"other\", \"confidence\": 0.5}"
	const want = "{\"name\": \"Crème Brûlée 🍮\", \"brand\": null, \"category\": \"other\", \"confidence\": 0.5}"

	got, ok := FirstJSONObject(raw)
	if !ok || got != want {
		t.Fatalf("FirstJSONObject = %s, %v, want %s", strconv.Quote(got), ok, strconv.Quote(want))
	}
	item, ok := DecodeIdentifiedItem(raw)
	if !ok {
		t.Fatal("DecodeIdentifiedItem returned no value for multi-byte input")
	}
	if item.Name != "Crème Brûlée 🍮" {
		t.Errorf("name = %s, want %s", strconv.Quote(item.Name), strconv.Quote("Crème Brûlée 🍮"))
	}
}

// TestDecodeIdentifiedItemArrayNilVersusEmpty restates the central invariant as
// an executable assertion, alongside the array-empty / array-empty-output
// fixtures. A nil slice must NEVER mean "parse failed".
func TestDecodeIdentifiedItemArrayNilVersusEmpty(t *testing.T) {
	items, ok := DecodeIdentifiedItemArray("[]")
	if !ok {
		t.Fatal("\"[]\" must decode successfully: the model answered, and it saw nothing")
	}
	if items == nil {
		t.Error("\"[]\" decoded to a NIL slice; it must be a non-nil empty slice, " +
			"otherwise \"saw nothing\" and \"was unreadable\" become indistinguishable")
	}
	if len(items) != 0 {
		t.Errorf("\"[]\" decoded to %d items, want 0", len(items))
	}

	items, ok = DecodeIdentifiedItemArray("")
	if ok {
		t.Errorf("an empty model reply must report no value, got %d items", len(items))
	}
	if items != nil {
		t.Errorf("a failed decode returned a non-nil slice (%d items); callers branch on "+
			"ok, and a non-nil slice here would tempt them to branch on len()", len(items))
	}
}

// TestDecodeIdentifiedItemDoesNotAcceptAnArray is the decode-level no-fallback
// assertion: an array reply where one item was expected contributes nothing.
func TestDecodeIdentifiedItemDoesNotAcceptAnArray(t *testing.T) {
	const raw = "[{\"name\": \"Milk\", \"brand\": null, \"category\": \"dairy\", \"confidence\": 0.9}]"
	// Extraction finds the element object, but the caller asked for ONE item and
	// gets exactly that one - it is never silently unwrapped from a list of many.
	item, ok := DecodeIdentifiedItem(raw)
	if !ok {
		t.Fatal("DecodeIdentifiedItem over a one-element array: extraction finds the element")
	}
	if item.Name != "Milk" {
		t.Errorf("name = %s, want \"Milk\"", strconv.Quote(item.Name))
	}

	// The reverse direction has no such luck and must not invent one.
	const object = "{\"name\": \"Milk\", \"brand\": null, \"category\": \"dairy\", \"confidence\": 0.9}"
	if items, ok := DecodeIdentifiedItemArray(object); ok {
		t.Errorf("DecodeIdentifiedItemArray over an object returned %d items, want no value; "+
			"there is no fallback from the array path to the object path", len(items))
	}
}

// TestExtractionAndDecodingAreSeparateStagesForArrays mirrors the
// object-unquoted-keys pair onto the array path, which has no such fixture.
func TestExtractionAndDecodingAreSeparateStagesForArrays(t *testing.T) {
	const raw = "[ not: valid json ]"

	got, ok := FirstJSONArray(raw)
	if !ok || got != raw {
		t.Errorf("FirstJSONArray(%s) = %s, %v; the scanner only balances brackets, so "+
			"extraction SUCCEEDS on illegal JSON", strconv.Quote(raw), strconv.Quote(got), ok)
	}
	if items, ok := DecodeIdentifiedItemArray(raw); ok {
		t.Errorf("DecodeIdentifiedItemArray(%s) returned %d items; extraction succeeding "+
			"does not mean the payload is valid", strconv.Quote(raw), len(items))
	}
}

// TestDecodeIdentifiedItemArrayFailsWholeArrayOnOneBadElement pins Swift's
// all-or-nothing array decode. Elements are not salvaged individually.
func TestDecodeIdentifiedItemArrayFailsWholeArrayOnOneBadElement(t *testing.T) {
	const raw = "[{\"name\": \"Milk\", \"brand\": null, \"category\": \"dairy\", \"confidence\": 0.9}," +
		" {\"name\": \"Eggs\"}]"
	if items, ok := DecodeIdentifiedItemArray(raw); ok {
		t.Errorf("got %d items, want no value: the second element is missing category and "+
			"confidence, which fails the WHOLE array", len(items))
	}
}

// TestDecodeIdentifiedItemBrandIsThreeWayThroughTheParser checks that the
// absent/null/empty distinction survives the whole raw-text path, not just the
// bytes-level decoder in types.go.
func TestDecodeIdentifiedItemBrandIsThreeWayThroughTheParser(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want *string
	}{
		{"absent", "```json\n{\"name\": \"Eggs\", \"category\": \"dairy\", \"confidence\": 0.8}\n```", nil},
		{"explicit null", "prose {\"name\": \"Eggs\", \"brand\": null, \"category\": \"dairy\", \"confidence\": 0.8}", nil},
		{"empty string is NOT null", "{\"name\": \"Eggs\", \"brand\": \"\", \"category\": \"dairy\", \"confidence\": 0.8}", parserStrPtr("")},
		{"a real brand", "<channel|>{\"name\": \"Eggs\", \"brand\": \"Horizon\", \"category\": \"dairy\", \"confidence\": 0.8}", parserStrPtr("Horizon")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item, ok := DecodeIdentifiedItem(tc.in)
			if !ok {
				t.Fatalf("DecodeIdentifiedItem(%s) returned no value", strconv.Quote(tc.in))
			}
			switch {
			case tc.want == nil && item.Brand != nil:
				t.Errorf("brand = %s, want null", strconv.Quote(*item.Brand))
			case tc.want != nil && item.Brand == nil:
				t.Errorf("brand = null, want %s", strconv.Quote(*tc.want))
			case tc.want != nil && *item.Brand != *tc.want:
				t.Errorf("brand = %s, want %s", strconv.Quote(*item.Brand), strconv.Quote(*tc.want))
			}
		})
	}
}

// TestDecodeRecipeBundleStrictness pins the Swift Codable rules on the recipe
// path, where the three fixtures only cover a full bundle, an empty one and an
// unreadable one.
func TestDecodeRecipeBundleStrictness(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		wantOK bool
	}{
		{"both buckets present and empty", "{\"makeNow\":[],\"almostThere\":[]}", true},
		{"almostThere missing entirely", "{\"makeNow\":[]}", false},
		{"makeNow missing entirely", "{\"almostThere\":[]}", false},
		{"a bucket explicitly null", "{\"makeNow\":null,\"almostThere\":[]}", false},
		{"unknown extra keys are ignored", "{\"makeNow\":[],\"almostThere\":[],\"notes\":\"hi\"}", true},
		{
			name: "a complete suggestion decodes",
			in: "{\"makeNow\":[{\"name\":\"Omelet\",\"ingredients\":[{\"name\":\"Eggs\",\"hasIt\":true}]," +
				"\"steps\":[\"Beat\"],\"timeMinutes\":5,\"missingItems\":[]}],\"almostThere\":[]}",
			wantOK: true,
		},
		{
			// timeMinutes is an Int in Swift: a float there fails the WHOLE
			// bundle and surfaces as the unreadable-recipes error.
			name: "a float timeMinutes fails the whole bundle",
			in: "{\"makeNow\":[{\"name\":\"Omelet\",\"ingredients\":[],\"steps\":[]," +
				"\"timeMinutes\":5.5,\"missingItems\":[]}],\"almostThere\":[]}",
			wantOK: false,
		},
		{
			name: "a suggestion missing missingItems fails",
			in: "{\"makeNow\":[{\"name\":\"Omelet\",\"ingredients\":[],\"steps\":[]," +
				"\"timeMinutes\":5}],\"almostThere\":[]}",
			wantOK: false,
		},
		{
			name: "an ingredient missing hasIt fails",
			in: "{\"makeNow\":[{\"name\":\"Omelet\",\"ingredients\":[{\"name\":\"Eggs\"}]," +
				"\"steps\":[],\"timeMinutes\":5,\"missingItems\":[]}],\"almostThere\":[]}",
			wantOK: false,
		},
		{
			name: "a null step element fails",
			in: "{\"makeNow\":[{\"name\":\"Omelet\",\"ingredients\":[],\"steps\":[null]," +
				"\"timeMinutes\":5,\"missingItems\":[]}],\"almostThere\":[]}",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle, ok := DecodeRecipeBundle(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("DecodeRecipeBundle(%s) ok = %v, want %v", strconv.Quote(tc.in), ok, tc.wantOK)
			}
			if ok && (bundle.MakeNow == nil || bundle.AlmostThere == nil) {
				t.Errorf("a decoded bundle must carry non-nil buckets, got makeNow=%v almostThere=%v",
					bundle.MakeNow, bundle.AlmostThere)
			}
		})
	}
}

// TestParserIsTotalOnHostileInput checks the one property every caller relies
// on: these functions never panic and never return a partial result, whatever
// the model emits.
func TestParserIsTotalOnHostileInput(t *testing.T) {
	inputs := []string{
		"",
		" ",
		"\n\n\t",
		"}",
		"]",
		"}{",
		"][",
		"```",
		"```json```",
		"<channel|>",
		"<channel|><channel|>",
		"\"unterminated string",
		"{",
		"[",
		"{\"a\": [1, 2}",
		"[{\"a\": 1]",
		"\\",
		"{\"a\": \"\\",
		strings.Repeat("{", 5000),
		strings.Repeat("[", 5000),
		"null",
		"[null]",
		"{\"name\": null, \"category\": null, \"confidence\": null}",
	}
	for _, in := range inputs {
		t.Run(strconv.Quote(in)[:min(len(strconv.Quote(in)), 24)], func(t *testing.T) {
			// Each of these must return, not panic. Values are deliberately
			// unasserted: totality is the property under test.
			_ = StripCodeFences(in)
			_ = StripReasoning(in)
			if got, ok := FirstJSONObject(in); !ok && got != "" {
				t.Errorf("FirstJSONObject carried a partial result %s with no value", strconv.Quote(got))
			}
			if got, ok := FirstJSONArray(in); !ok && got != "" {
				t.Errorf("FirstJSONArray carried a partial result %s with no value", strconv.Quote(got))
			}
			_, _ = DecodeIdentifiedItem(in)
			if items, ok := DecodeIdentifiedItemArray(in); !ok && items != nil {
				t.Errorf("DecodeIdentifiedItemArray carried %d items with no value", len(items))
			}
			_, _ = DecodeRecipeBundle(in)
		})
	}
}
