package contract

import (
	"encoding/json"
	"strings"
)

// This file is a port of DinnerDecider/Services/LLM/LLMResponseParser.swift,
// specified by shared/parser/response-parsing.md and pinned by the 27 golden
// fixtures in shared/conformance/cases.json.
//
// Every function here is TOTAL: no panics, no errors, no partial results.
// Failure is always the "no value" half of a (value, ok) pair, and every caller
// treats it as "this model call contributed nothing".
//
// The parser exists because the model is run GRAMMAR-FREE. That is not a style
// choice: see response-parsing.md § "PROHIBITION: no grammar / structured-output
// constraint on a resident model". The runtime's grammar sampler is never reset
// between calls, so call 1 returns clean JSON and calls 2-4 return nothing at
// all - which the scan loop swallows into "Nothing spotted". Prompt wording plus
// this salvage parser is the chosen path; do not replace it with a constrained
// decoder without a multi-call proof on a resident model.

// codeFenceLiterals are the markdown fences removed by StripCodeFences, IN THIS
// ORDER.
//
// The order is load-bearing. The language-tagged variants must go before the
// bare "```", otherwise removing the bare fence first would leave orphaned
// "json" / "JSON" / "Json" tokens sitting in the text.
//
// Only these three casings are handled. A hypothetical "```jSoN" leaves a stray
// "jSoN" behind, which is harmless: the balanced scanner ignores everything
// outside the brackets. Do not "improve" this into a case-insensitive regex  -
// the fixtures pin the literal behavior, and a regex would also start matching
// fences inside string values.
var codeFenceLiterals = [4]string{"```json", "```JSON", "```Json", "```"}

// channelCloseMarker is the Gemma 4 thinking-channel CLOSE marker.
//
// The July 2026 chat template wraps chain-of-thought as
// "<|channel>thought\n ...reasoning... \n<channel|>" immediately before the real
// answer. Only the close marker is ever searched for; the open marker is never
// required to be present.
const channelCloseMarker = "<channel|>"

// StripCodeFences removes every occurrence of the four fence literals, in
// codeFenceLiterals order, by plain substring replacement.
//
// This is unconditional replacement across the whole string, not a paired-fence
// parse. Prose before and after a fence stays exactly where it was; the scanner
// skips it.
func StripCodeFences(text string) string {
	result := text
	for _, fence := range codeFenceLiterals {
		result = strings.ReplaceAll(result, fence, "")
	}
	return result
}

// StripReasoning keeps only the text after the LAST channelCloseMarker.
//
// Rule, exactly:
//
//	find the LAST occurrence of the literal "<channel|>"
//	if found: return everything AFTER it
//	else:     return text unchanged
//
// Three details that the fixtures pin and that a reimplementation tends to lose:
//
//   - LAST, not first. Nested or repeated reasoning blocks are ALL discarded.
//     A decoy object inside the reasoning ({"name": "WRONG"}) would otherwise be
//     what a first-brace scanner returns.
//   - NO TRIMMING. "<channel|>\n  {...}" yields "\n  {...}" with the whitespace
//     intact. The bracket scanner skips leading junk anyway, so trimming buys
//     nothing and would break the byte-exact string fixtures.
//   - Text with no marker is returned BYTE-IDENTICAL, not normalized in any way.
func StripReasoning(text string) string {
	if idx := strings.LastIndex(text, channelCloseMarker); idx >= 0 {
		return text[idx+len(channelCloseMarker):]
	}
	return text
}

// clean applies the fixed order of operations: reasoning first, then fences.
//
//	cleaned = StripCodeFences(StripReasoning(text))
//
// The order is NOT arbitrary and must not be swapped. Fence markers may live
// INSIDE the answer that follows the channel close marker, and the reasoning
// text may itself contain fenced pseudo-JSON. Stripping reasoning first discards
// that decoy wholesale. Pinned by the fixture
// object-thinking-channel-inside-code-fence, where a thinking preamble is
// followed by a fenced object and the extractor must return {"name": "Omelet"}.
func clean(text string) string {
	return StripCodeFences(StripReasoning(text))
}

// firstBalanced is THE scanner - one implementation, parameterised only by the
// bracket pair. FirstJSONObject and FirstJSONArray differ by two bytes.
//
// It returns the first balanced value INCLUSIVE of its brackets, or no value.
// Contractual behaviors, each with a fixture:
//
//  1. FIRST BALANCED VALUE ONLY. `{"a": {"nested": 1}} trailing {"b": 2}` yields
//     `{"a": {"nested": 1}}`. Trailing content is ignored, never an error.
//  2. Brackets inside STRING LITERALS do not count toward depth, and a
//     backslash-escaped quote does not end the string.
//  3. UNBALANCED INPUT RETURNS NO VALUE - never a truncated prefix. A model that
//     ran out of tokens mid-array must not yield the elements that did arrive.
//  4. depth only ever reaches 0 at the matching close bracket, because the scan
//     starts AT the first open bracket. A stray leading close bracket is
//     therefore impossible by construction.
//
// Scanning is byte-indexed where Swift scans a [Character] array. For the ASCII
// bracket, quote and backslash bytes this is identical: every byte of a
// multi-byte UTF-8 sequence has its high bit set, so none of them can collide
// with a delimiter. The returned substring is a byte slice of the cleaned input,
// so multi-byte content inside the value survives untouched.
func firstBalanced(cleaned string, open, closing byte) (string, bool) {
	start := strings.IndexByte(cleaned, open)
	if start < 0 {
		return "", false
	}

	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(cleaned); i++ {
		c := cleaned[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case open:
			depth++
		case closing:
			depth--
			if depth == 0 {
				return cleaned[start : i+1], true
			}
		}
	}
	return "", false
}

// FirstJSONObject returns the first balanced { ... } in text, after stripping
// reasoning and fences, or no value.
//
// The returned string is the RAW SUBSTRING, byte-exact, including any internal
// whitespace. It is not re-serialized and not validated: extraction succeeding
// does NOT mean the payload is legal JSON. `{ not: valid json }` is balanced, so
// this returns it, and DecodeIdentifiedItem then reports no value. The two
// stages are separate on purpose and two fixtures observe that same input.
func FirstJSONObject(text string) (string, bool) {
	return firstBalanced(clean(text), '{', '}')
}

// FirstJSONArray returns the first balanced [ ... ] in text, after stripping
// reasoning and fences, or no value.
//
// THE TWO EXTRACTORS ARE INDEPENDENT AND DO NOT FALL BACK TO ONE ANOTHER. Asking
// for an array and receiving a top-level object is a HARD MISS: there is no "["
// in the input, so this reports no value rather than quietly wrapping the object
// in a one-element array. Pinned by array-object-when-array-expected.
func FirstJSONArray(text string) (string, bool) {
	return firstBalanced(clean(text), '[', ']')
}

// DecodeIdentifiedItem extracts and decodes one IdentifiedItem from a raw model
// reply, or reports no value.
//
// Decode errors are swallowed into "no value" - a caller only ever learns that
// the call contributed nothing, never why. Required-key strictness (name,
// category and confidence must all be present and non-null) lives in
// UnmarshalIdentifiedItem, which this MUST go through: a bare json.Unmarshal
// would silently zero-fill a missing category and turn `{"name": "Milk"}` into a
// bogus item.
func DecodeIdentifiedItem(response string) (IdentifiedItem, bool) {
	raw, ok := FirstJSONObject(response)
	if !ok {
		return IdentifiedItem{}, false
	}
	return UnmarshalIdentifiedItem([]byte(raw))
}

// DecodeIdentifiedItemArray extracts and decodes a JSON array of IdentifiedItem
// from a raw model reply.
//
// THE RETURN SHAPE IS THE POINT. A nil slice MUST NEVER be used to mean "parse
// failed" - that is what ok is for:
//
//	"[]"  -> (non-nil empty slice, true)   the model answered: it saw nothing
//	""    -> (nil,                 false)  the model was unreadable
//
// Those are different outcomes with different consequences downstream: the first
// is an EMPTY scan, the second contributes to a FAILED one, and merging them
// reproduces the exact TestFlight bug where a fridge full of food reported
// "Nothing spotted". The fixtures array-empty and array-empty-output exist
// solely to keep them apart. Callers must branch on ok, never on len() or == nil.
//
// One bad element fails the WHOLE array, matching Swift's
// JSONDecoder().decode([IdentifiedItem].self); elements are not salvaged
// individually.
func DecodeIdentifiedItemArray(response string) ([]IdentifiedItem, bool) {
	raw, ok := FirstJSONArray(response)
	if !ok {
		return nil, false
	}
	return UnmarshalIdentifiedItems([]byte(raw))
}

// DecodeRecipeBundle extracts and decodes a RecipeBundleResponse from a raw
// model reply, or reports no value.
//
// On no value the caller leaves the existing recipes untouched and shows the
// user-facing error "We could not read the recipe ideas this time. Please try
// again." - it never renders a half-decoded bundle.
func DecodeRecipeBundle(response string) (RecipeBundleResponse, bool) {
	raw, ok := FirstJSONObject(response)
	if !ok {
		return RecipeBundleResponse{}, false
	}
	return unmarshalRecipeBundle([]byte(raw))
}

// ingredientLineShadow is the decode shadow for IngredientLine: both keys are
// non-optional in Swift, so both are pointers here.
type ingredientLineShadow struct {
	Name  *string `json:"name"`
	HasIt *bool   `json:"hasIt"`
}

// recipeSuggestionShadow is the decode shadow for RecipeSuggestion. All five
// keys are non-optional in Swift.
//
// The list fields are pointers-to-slices-of-pointers so that three separate
// failure modes are all caught: an ABSENT key (nil outer pointer), an explicit
// null for the list itself (also nil outer pointer - encoding/json nils a
// pointer on null), and a null ELEMENT inside the list (nil inner pointer),
// which Swift rejects but which plain encoding/json would silently decode as "".
type recipeSuggestionShadow struct {
	Name         *string                 `json:"name"`
	Ingredients  *[]ingredientLineShadow `json:"ingredients"`
	Steps        *[]*string              `json:"steps"`
	TimeMinutes  *int                    `json:"timeMinutes"`
	MissingItems *[]*string              `json:"missingItems"`
}

// recipeBundleShadow is the decode shadow for RecipeBundleResponse. Both buckets
// are non-optional in Swift: a bundle missing "almostThere" entirely fails, it
// does not decode to an empty bucket.
type recipeBundleShadow struct {
	MakeNow     *[]recipeSuggestionShadow `json:"makeNow"`
	AlmostThere *[]recipeSuggestionShadow `json:"almostThere"`
}

// unmarshalRecipeBundle decodes a RecipeBundleResponse with Swift Codable's
// required-key strictness, which encoding/json does not give for free.
//
// TimeMinutes is an int on purpose: a JSON float there fails the WHOLE bundle in
// Swift, and must fail the whole bundle here. Both decoders agree, and that
// agreement is deliberate rather than incidental - do not relax it to a float
// with a rounding step.
//
// Empty buckets decode to NON-NIL empty slices, mirroring the nil-versus-empty
// discipline in DecodeIdentifiedItemArray.
func unmarshalRecipeBundle(data []byte) (RecipeBundleResponse, bool) {
	var shadow recipeBundleShadow
	if err := json.Unmarshal(data, &shadow); err != nil {
		return RecipeBundleResponse{}, false
	}
	if shadow.MakeNow == nil || shadow.AlmostThere == nil {
		return RecipeBundleResponse{}, false
	}
	makeNow, ok := recipeSuggestionsFromShadow(*shadow.MakeNow)
	if !ok {
		return RecipeBundleResponse{}, false
	}
	almostThere, ok := recipeSuggestionsFromShadow(*shadow.AlmostThere)
	if !ok {
		return RecipeBundleResponse{}, false
	}
	return RecipeBundleResponse{MakeNow: makeNow, AlmostThere: almostThere}, true
}

// recipeSuggestionsFromShadow validates one bucket. A single bad suggestion
// fails the bucket, and therefore the whole bundle - suggestions are not
// salvaged individually, exactly as Swift's array decode does not salvage them.
func recipeSuggestionsFromShadow(shadows []recipeSuggestionShadow) ([]RecipeSuggestion, bool) {
	out := make([]RecipeSuggestion, 0, len(shadows))
	for _, s := range shadows {
		if s.Name == nil || s.Ingredients == nil || s.Steps == nil || s.TimeMinutes == nil || s.MissingItems == nil {
			return nil, false
		}
		ingredients := make([]IngredientLine, 0, len(*s.Ingredients))
		for _, line := range *s.Ingredients {
			if line.Name == nil || line.HasIt == nil {
				return nil, false
			}
			ingredients = append(ingredients, IngredientLine{Name: *line.Name, HasIt: *line.HasIt})
		}
		steps, ok := stringsFromShadow(*s.Steps)
		if !ok {
			return nil, false
		}
		missing, ok := stringsFromShadow(*s.MissingItems)
		if !ok {
			return nil, false
		}
		out = append(out, RecipeSuggestion{
			Name:         *s.Name,
			Ingredients:  ingredients,
			Steps:        steps,
			TimeMinutes:  *s.TimeMinutes,
			MissingItems: missing,
		})
	}
	return out, true
}

// stringsFromShadow flattens a []*string, rejecting any null element. Returns a
// non-nil empty slice for an empty list.
func stringsFromShadow(shadows []*string) ([]string, bool) {
	out := make([]string, 0, len(shadows))
	for _, s := range shadows {
		if s == nil {
			return nil, false
		}
		out = append(out, *s)
	}
	return out, true
}
