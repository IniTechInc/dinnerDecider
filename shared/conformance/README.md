# Conformance suite

`cases.json` holds **27 golden fixtures** for the model-response parser,
extracted verbatim from the shipped Swift unit tests. A port (Kotlin, Go, or
anything else) is parser-conformant when it runs these exact fixtures and matches
every expected output.

This is the cheapest possible proof of parity. The parser is pure - no model, no
network, no filesystem - so the whole suite runs in milliseconds in any language.

## Why fixtures and not prose

`shared/parser/response-parsing.md` describes the rules; this file *pins* them.
Several of the rules are counter-intuitive enough that a reimplementation from
the prose alone tends to get them subtly wrong:

- extraction can succeed on input that then fails to decode (`{ not: valid json }`)
- `[]` must decode to an empty array, but `""` must decode to null
- a truncated array must yield null, not the elements that did arrive
- reasoning must be stripped **before** fences, not after
- the array path must **not** fall back to the object path

Each of those has a case, and each case names the Swift test it came from.

## File format

```jsonc
{
  "version": 1,
  "floatTolerance": 0.0001,
  "cases": [
    {
      "id": "object-fenced-happy-path",       // stable, unique
      "description": "...",                   // what the case proves
      "source": "DinnerDeciderTests/LLMResponseParserTests.swift:testDecodesJSONInsideCodeFences",
      "operation": "decodeIdentifiedItem",
      "input": "```json\n{\"name\": ...}\n```",  // the raw model output, verbatim
      "expectNull": false,
      "expected": { "name": "Whole Milk", "brand": "Horizon", "category": "dairy", "confidence": 0.92 }
    }
  ]
}
```

When `expectNull` is `true` the `expected` key is **absent**. Treat a missing
`expected` with `expectNull: false` as a malformed suite, not as "expect null".

## Operations

| `operation` | Input | Expected result type |
| --- | --- | --- |
| `stripReasoning` | raw model output | `string` (never null) |
| `firstJSONObject` | raw model output | `string` or null - the extracted substring, **byte-exact** |
| `firstJSONArray` | raw model output | `string` or null |
| `decodeIdentifiedItem` | raw model output | `IdentifiedItem` object or null |
| `decodeIdentifiedItemArray` | raw model output | array of `IdentifiedItem` or null |
| `decodeRecipeBundle` | raw model output | `RecipeBundleResponse` object or null |

Schemas for the decoded types: `shared/schema/identified-item.schema.json`,
`shared/schema/recipe.schema.json`.

## Comparison rules

- **Strings** (`stripReasoning`, `firstJSONObject`, `firstJSONArray`): exact
  equality, including whitespace. No trimming.
- **Numbers**: compare within `floatTolerance` (1e-4), matching the accuracy
  argument used in the Swift assertions. `timeMinutes` is an **integer** and must
  compare exactly.
- **Optionals**: `brand` absent and `brand: null` are the same result. The
  expected value uses JSON `null`; a port's "no value" representation
  (`nil`, `null`, `""` from a pointer, `None`) must map onto it - but an empty
  **string** brand is *not* null.
- **Arrays**: order-sensitive, length-sensitive.
- **Objects**: compare the fields named in the schema. Extra fields a port
  carries internally are ignored.
- **Null vs empty**: never conflate them. `decodeIdentifiedItemArray` returning
  `[]` and returning null are different outcomes and different cases
  (`array-empty` vs `array-empty-output`).

## Coverage

| Category | Cases |
| --- | --- |
| Happy path (object) | `object-fenced-happy-path`, `object-leading-and-trailing-prose`, `object-null-brand` |
| Happy path (array) | `array-fenced-happy-path`, `array-leading-prose`, `array-multi-item`, `array-empty` |
| Happy path (recipes) | `recipe-bundle-prose-and-fence`, `recipe-bundle-empty-buckets` |
| Channel-prefixed output | `strip-reasoning-keeps-answer-after-marker`, `strip-reasoning-no-marker-unchanged`, `object-thinking-channel-with-decoy-json`, `object-thinking-channel-inside-code-fence`, `array-after-thinking-channel` |
| Malformed JSON | `object-no-json-at-all`, `object-unquoted-keys-extracts`, `object-unquoted-keys-decodes-null`, `object-missing-required-fields`, `array-object-when-array-expected`, `recipe-bundle-unreadable` |
| Truncated output | `object-truncated-never-closes`, `array-truncated` |
| Empty output | `object-empty-output`, `array-empty-output` |
| String-literal edge cases | `object-braces-inside-string-extracts`, `object-braces-inside-string-decodes`, `object-first-balanced-only` |

Three fixtures are not from `LLMResponseParserTests` but are still real strings
from the shipped app, which is why they are worth keeping:

- `recipe-bundle-prose-and-fence` - the exact reply `MockLLMService.generateText`
  emits. The mock deliberately wraps its JSON in prose *and* a code fence
  "exactly like a real model tends to, so the parser is genuinely exercised".
- `recipe-bundle-unreadable` - the exact string the hidden
  `debugSimulateRecipeFailure` switch injects to demonstrate the retry UI.
- `recipe-bundle-empty-buckets` - the reply the test double in
  `AppModelMemoryTests` returns.

## Runner contract

```text
for case in cases:
    got = invoke(case.operation, case.input)
    if case.expectNull: assert got is null
    else:               assert matches(got, case.expected)
```

Requirements:

1. **Run every case.** Do not skip, filter, or tag cases as "known failing".
2. **Do not edit a case to make a port pass.** If a port disagrees with a
   fixture, either the port is wrong, or the Swift changed and both the Swift
   test and this file must be updated in the same commit.
3. Report failures by `id`, so a failure is traceable back to the named Swift
   test in `source`.
4. Wire it into CI. The suite is fast enough that there is no reason not to.

## Adding cases

Only add a case that is backed by real behavior - an existing Swift test, a
string the app actually produces, or a real model output captured from a device.
Set `source` accordingly. A hypothetical input is not a golden fixture; it is a
guess, and a guess that gets frozen into three implementations is worse than no
case at all.

## Not covered here

These are specified in prose and pinned by Swift tests, but have **no JSON
fixtures yet**:

- `InventoryLogic` - dedupe, confirm plans, cook plans, portion lines
  (`InventoryLogicTests.swift`)
- `ShoppingListLogic` - aggregation, new-item filtering, export text
  (`ShoppingListLogicTests.swift`)
- `ModelLifecycle` - the state machine (`ModelLifecycleTests.swift`, 16 tests)
- `ScanCallBudget` (`ScanCallBudgetTests.swift`)
- `ModelFileLocator` selection (`ModelFileLocatorTests.swift`)
- Prompt assembly (`RecipePromptTests.swift`, `TasteProfileTests.swift`)

`TODO(unresolved): whether these should also become JSON fixtures. They are all
pure and would mechanize just as well as the parser cases, but no fixture format
for a state machine or a plan-producing function exists in this repo yet, and
inventing one here would be a guess. For now a port should translate those Swift
test files directly - they are pure and synchronous, so the translation is
line-for-line.`
