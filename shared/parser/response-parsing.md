# Parser: model response -> structured JSON

Source of record: `DinnerDecider/Services/LLM/LLMResponseParser.swift` (141 lines,
pure, no I/O). Specification of record: `DinnerDeciderTests/LLMResponseParserTests.swift`
(20 tests). Golden fixtures for every platform: `shared/conformance/cases.json`.

Language models wrap their JSON in prose ("Sure, here you go:"), in markdown
fences, and — with this particular Gemma 4 build — in a thinking-channel
preamble. The parser's job is to salvage the payload without a schema-constrained
decoder, because **a schema-constrained decoder is not usable here** (see the
prohibition at the end; read it before you "simplify" any of this).

## Public surface

| Function | Returns |
| --- | --- |
| `stripCodeFences(text)` | `string` |
| `stripReasoning(text)` | `string` |
| `firstJSONObject(text)` | `string?` — the raw substring |
| `firstJSONArray(text)` | `string?` — the raw substring |
| `decode<T>(text)` | `T?` |
| `decodeArray<T>(text)` | `[T]?` |

Every one of them is **total**: no throws, no panics, no partial results. Failure
is always a null return, and every caller treats null as "this call contributed
nothing".

## Order of operations (fixed)

```text
cleaned = stripCodeFences(stripReasoning(text))
```

**Reasoning first, then fences.** The order is not arbitrary: the fence markers
may live *inside* the answer that follows the channel close marker, and the
reasoning text may itself contain fenced pseudo-JSON. Stripping reasoning first
discards that decoy wholesale. Pinned by
`testDecodesJSONWithThinkingChannelInsideCodeFence`, where a thinking preamble is
followed by a fenced object and the extractor must return `{"name": "Omelet"}`.


### KNOWN GAP: no fixture currently pins this order (found Sept 14 2026)

The Linux port mutation-tested its parser against the suite. Two mutations were
caught immediately -- using a nil slice to mean "parse failed" fails
`array-empty`, and reordering the fence literals fails the fence tests. **A third
was not: swapping to `StripReasoning(StripCodeFences(text))` passes all 27
fixtures.**

That is not a defective fixture, it is a property of the two functions.
`StripCodeFences` never touches the `<channel|>` literal and `StripReasoning`
never touches a fence, so for every input in the suite the two steps commute,
and the bracket scanner skips whatever leading junk either order leaves behind.
`object-thinking-channel-inside-code-fence` reaches the same object either way.

So: the order above is still correct and still required, but **do not describe
the fixtures as pinning it** -- they do not. A port can get the order wrong and
go green.

**Proposed 28th fixture** (not yet added -- see the sequencing note): an input
where a fence sits *inside* a would-be marker, so only the wrong order
assembles a real `<channel|>`:

    operation: firstJSONObject
    input:     {"name":"first"}<channel```|>{"name":"second"}

- Correct order: `StripReasoning` finds no literal `<channel|>` (the raw text has
  `<channel```|>`), so it passes through; fences are then stripped; the scanner
  returns **the first object**.
- Wrong order: fences are stripped first, assembling a real `<channel|>`;
  `StripReasoning` then discards everything before it; the scanner returns
  **the second object**.

Adding it means adding the matching Swift test in the same commit (the rule in
`shared/conformance/README.md`), and bumping the hardcoded `27` in every port's
runner. **Deliberately deferred**: ports were mid-implementation against a
suite of 27 when this was found, and changing the count underneath them would
have broken runners that were correct. Do it once the ports have landed.

Until then, each port should pin the order with its own local test, as the
Linux port does.

## Step 1 — channel stripping (`stripReasoning`)

The July 2026 Gemma 4 chat template wraps chain-of-thought as:

```text
<|channel>thought
 ...reasoning...
<channel|>
```

immediately before the real answer. The model emits this on messy real-world
photos **even with thinking nominally off**, and the reasoning text routinely
contains brace characters that would fool a naive extractor.

Rule, exactly:

```text
stripReasoning(text):
    find the LAST occurrence of the literal "<channel|>"
    if found: return everything AFTER it
    else:     return text unchanged
```

Details that matter:

- The marker searched for is the **close** marker `<channel|>` only. The open
  marker `<|channel>thought` is never searched for and never needs to be present.
- **LAST, not first** — nested or repeated reasoning blocks all get discarded.
- No trimming is applied. `"<channel|>{...}"` yields `"{...}"` with whatever
  whitespace followed the marker; the brace scanner skips leading junk anyway.
- Text with no marker is returned **byte-identical** (`testStripReasoningLeavesPlainTextUnchanged`).

Worked example (`testStripReasoningKeepsAnswerAfterLastChannelMarker`):

```text
in:  <|channel>thought\nThe box says OATMEAL so name is Oatmeal.\n<channel|>{"name": "Oatmeal"}
out: {"name": "Oatmeal"}
```

## Step 2 — fence stripping (`stripCodeFences`)

Remove **all** occurrences of these four literals, in this order, by plain
substring replacement:

```text
"```json"   then   "```JSON"   then   "```Json"   then   "```"
```

Order is load-bearing: the language-tagged variants must be removed before the
bare ``` ``` ``` , or you would be left with orphaned `json` tokens in the text.
Only those three casings are handled — a hypothetical ` ```jSoN ` leaves a stray
`jSoN` behind, which is harmless because the brace scanner ignores everything
outside the braces.

This is unconditional replacement across the whole string, not a paired-fence
parse. Nothing else is stripped: prose before and after stays, and the scanner
skips it.

## Step 3 — balanced extraction (`firstJSONObject` / `firstJSONArray`)

A single scanner, parameterised only by the bracket pair (`{`/`}` or `[`/`]`):

```text
extract(cleaned, open, close):
    start = index of first `open` in cleaned;  if none: return null
    depth = 0; inString = false; escaped = false
    for i from start to end:
        c = cleaned[i]
        if inString:
            if escaped:        escaped = false
            elif c == '\\':    escaped = true
            elif c == '"':     inString = false
        else:
            if c == '"':       inString = true
            elif c == open:    depth += 1
            elif c == close:
                depth -= 1
                if depth == 0: return cleaned[start...i]      # inclusive
    return null                                               # never balanced
```

Contractual behaviors:

1. **First balanced value only.** `{"a": {"nested": 1}} trailing {"b": 2}` yields
   `{"a": {"nested": 1}}`. Trailing content is ignored, not an error.
2. **Braces and brackets inside string literals do not count.**
   `{"name": "weird}name{here", ...}` extracts whole. Escape handling is a single
   lookahead flag, so `\"` inside a string does not end it.
3. **Unbalanced input returns null**, it does not return a truncated prefix.
   `{ this never closes` -> null. `[{"name": "Milk"},` -> null.
4. **The two extractors are independent and do not fall back to one another.**
   `firstJSONArray` on a top-level object returns null — it finds no `[` at all.
   An object reply where an array was expected is a hard miss
   (`testReturnsNilForObjectWhenArrayExpected`).
5. The scanner is **index-based over characters**. Swift iterates a `[Character]`
   array (grapheme clusters). For ASCII JSON this is identical to bytes or code
   points; only the returned substring's exact boundaries could differ on exotic
   input, and every fixture is ASCII. Byte-based scanning in Go or char-based in
   Kotlin is fine.
6. `depth` only ever reaches 0 at the matching close, so a stray leading close
   bracket before the first open is impossible by construction (the scan starts at
   the first open).

## Step 4 — decode

```text
decode<T>(text):      j = firstJSONObject(text); if null -> null; return jsonDecode<T>(j)  ?? null
decodeArray<T>(text): j = firstJSONArray(text);  if null -> null; return jsonDecode<[T]>(j) ?? null
```

Decode errors are swallowed into null — extraction succeeding does not mean the
payload is valid. `{ not: valid json }` is *balanced*, so `firstJSONObject`
returns it, and `decode` then returns null because it is not legal JSON.
Conformance cases cover both observations of that same input.

Type strictness inherited from Swift `Codable`, which a port must match:

- **Missing non-optional key -> null.** `{"name": "Milk"}` fails to decode as an
  `IdentifiedItem` (no `category`, no `confidence`).
- **Optional key may be absent OR explicitly null** — both yield nil for `brand`.
- **`timeMinutes` is an integer.** A float there fails the whole recipe bundle.
- **Unknown extra keys are ignored**, not an error.
- **Category is never validated at decode time** — any string decodes, and the
  enum mapping happens later with an `other` fallback
  (`shared/taxonomy/categories.md`).
- `[]` decodes to an **empty array, not null** — "the model saw nothing" and "the
  model was unreadable" are different outcomes and must stay different.

## Failure semantics per call site

| Call | On parse failure | Source |
| --- | --- | --- |
| `identifyItem` | **throws** `badResponse`; the scan loop catches it, logs `[Scan] Crop i/N failed: ...`, and continues to the next crop | `GemmaLLMService.swift:105-107`, `AppModel.swift:392-394` |
| `identifyAllItems` | returns **`[]`**, no error at all | `GemmaLLMService.swift:126` |
| recipe bundle | leaves the existing recipes untouched and sets the user-facing error `We could not read the recipe ideas this time. Please try again.` | `AppModel.swift:467-474` |

A scan is **never** aborted by a parse failure. That is deliberate — but it is
also why a systemic failure looks like an empty scan, which is the trap described
in `shared/pipeline/model-lifecycle.md`.

## PROHIBITION: no grammar / structured-output constraint on a resident model

**Do not use the runtime's grammar, GBNF, JSON-schema or `responseFormat`
feature.** This is not a style preference; it is a defect in the stack.

From `GemmaLLMService.swift:11-19`, verified in the macOS harness against this
exact GGUF:

> LocalLLMClient 0.5.0 does expose `responseFormat` grammar and it applies from
> token zero (killing Gemma's thinking preamble), but its grammar sampler is
> never reset between calls, so it produces output only on the FIRST call and
> then goes terminal.

And from `CLAUDE.md` (Known gotchas):

> Do not use LocalLLMClient grammar/responseFormat for repeated calls on a
> resident model: the grammar sampler never resets, **second call returns 0
> tokens**. Grammar-free + parser channel-stripping is the chosen path.

Why this is fatal here specifically: the model is loaded **once and kept
resident**, and a single scan makes up to 4 calls against it. Call 1 returns
clean JSON, calls 2-4 return **nothing**. The scan loop swallows the resulting
failures, and the user gets "Nothing spotted" — the same visible symptom as the
lifecycle bug, from a completely different cause. It would pass any single-call
smoke test.

Rules for a port:

1. Run **grammar-free**. Get JSON from prompt wording (`Respond ONLY with JSON,
   no other words:` plus a literal shape) plus this parser.
2. If your runtime's grammar sampler *does* reset correctly per call, prove it
   with a **multi-call** test on a resident model — at least 4 sequential calls,
   asserting non-empty output on every one — before considering it. A first-call
   test proves nothing.
3. Even then, keep this parser. Channel stripping and fence salvage cost nothing
   and the conformance suite depends on them.

## Conformance

`shared/conformance/cases.json` holds the golden fixtures, extracted from the
Swift tests. A port is conformant when it passes all of them unmodified. See
`shared/conformance/README.md` for the runner contract.
