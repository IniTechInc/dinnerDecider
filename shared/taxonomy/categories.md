# Taxonomy: `FoodCategory`

Source of record: `DinnerDecider/Models/InventoryItem.swift:7-48`
(`enum FoodCategory: String, Codable, CaseIterable, Identifiable, Sendable`).

Nine values. The **raw string is the wire and storage form**; the enum case name
happens to match it in every case, but ports must serialize the string, never an
ordinal or an index. Declaration order below is `CaseIterable` order, which is
also the order the UI iterates.

| # | Raw value (exact) | `displayName` | iOS SF Symbol |
| --- | --- | --- | --- |
| 1 | `produce` | Produce | `carrot` |
| 2 | `dairy` | Dairy | `drop` |
| 3 | `meat` | Meat | `fork.knife` |
| 4 | `pantry` | Pantry | `shippingbox` |
| 5 | `snack` | Snacks | `takeoutbag.and.cup.and.straw` |
| 6 | `beverage` | Beverages | `cup.and.saucer` |
| 7 | `condiment` | Condiments | `waterbottle` |
| 8 | `frozen` | Frozen | `snowflake` |
| 9 | `other` | Other | `questionmark.circle` |

Notes on the table:

- **Raw values are singular; three display names are plural** (`snack` ->
  "Snacks", `beverage` -> "Beverages", `condiment` -> "Condiments"). Do not
  "regularise" this: the raw value is persisted data and the display name is UI
  copy, and they are allowed to disagree.
- `id` is the raw value (`Identifiable` conformance).
- Symbol names are Apple SF Symbols and are **iOS-only**. A port picks its own
  icons; only the raw values and display names are contractual.

## Where the raw values appear

1. **In both vision prompts, as a literal alternation** — the model is shown the
   closed set inline rather than being asked to invent a taxonomy:

   ```text
   "category": "produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other"
   ```

   (`GemmaLLMService.swift:101` and `:122`.) If a value is ever added or renamed,
   both prompt strings must change in the same commit, or the model will keep
   emitting the old set.

2. **On the wire**, as `IdentifiedItem.category` — typed as a plain `String`, not
   as the enum (`LLMService.swift:10`). This is deliberate: an unexpected value
   must never fail the decode.

3. **In storage**, as `InventoryItem.categoryRaw` — a raw `String` column "per
   the spec ... so SwiftData keeps a stable value even if we reorder or rename
   cases later" (`InventoryItem.swift:53-56`).

## Fallback rule for unrecognized values (mandatory)

Mapping from string to enum happens in exactly two places and both use the same
rule:

```swift
// LLMService.swift:14-16  (model output -> enum)
FoodCategory(rawValue: category.lowercased()) ?? .other

// InventoryItem.swift:65-68  (storage -> enum)
FoodCategory(rawValue: categoryRaw) ?? .other
```

Normative behavior for a port:

1. **Never throw, never drop the item.** An unrecognized category is a display
   concern, not a validation failure. The item still enters the inventory.
2. **Lower-case the model's value before matching.** The model frequently
   returns `Dairy` or `PRODUCE`. `IdentifiedItem.foodCategory` lower-cases;
   matching is otherwise exact (no trimming, no fuzzy matching, no synonym
   table). Do not add any.
3. **Anything that still does not match maps to `other`.**
4. **Asymmetry to preserve:** the storage-side mapping
   (`InventoryItem.category`) does **not** lower-case, because the value it reads
   was written by the enum's own `rawValue` and is already canonical. Keep the
   normalization at the boundary where untrusted model text enters, not deeper.
5. **Write back the canonical raw value.** When an `IdentifiedItem` is confirmed
   into inventory, persist `foodCategory.rawValue`, not the model's original
   string. That is what keeps rule 4 safe — a mixed-case string never reaches the
   store.

### Consequence worth stating plainly

Because the fallback is silent, a port with a typo in one enum value (say
`beverages`) will not fail any test that checks parsing. It will simply file
every drink under **Other**, forever, and nobody will notice until someone looks
at the inventory screen. Copy the nine strings from this table literally.

## Related closed sets

Not part of `FoodCategory`, but the same "raw string is the contract" rule
applies (`DinnerDecider/Services/Preferences.swift`):

- `DietPreference`: `none`, `vegetarian`, `vegan`, `pescatarian`, `keto`,
  `glutenFree`. The value `none` is the sentinel that **omits** the `Diet:` line
  from the recipe prompt entirely (`AppModel.swift:498`). Note `glutenFree` is
  camelCase, unlike every `FoodCategory` value.
- `InventorySort`: `category`, `dateAdded`, `name`.
