# Prompt: `recipes`

Text-only prompt. Takes the inventory (names only), the user's preferences, an
optional free-text mood, and an optional taste profile, and returns the
`makeNow` / `almostThere` recipe bundle.

Source of record: `DinnerDecider/Services/AppModel.swift:480-524`
(`AppModel.recipePrompt(itemNames:prefs:mood:tasteProfile:)`) plus
`DinnerDecider/Models/TasteProfile.swift:61-92` (`promptFragment()`) and
`DinnerDecider/Services/InventoryLogic.swift:147-153` (`portionLine`).

It is `nonisolated static` on iOS — a pure function of its arguments with no
global reads. Keep it pure in ports; its tests (`RecipePromptTests.swift`) depend
on that.

## Assembly

The prompt is a list of lines **joined with `\n`** (`AppModel.swift:523`). Lines
2 and 4-8 are conditional; lines 1, 9 and 10 are always present. Order matters —
`RecipePromptTests.testPromptIncludesTasteProfileAllergyLineBeforeSchema` asserts
the taste fragment sits **before** the JSON schema instruction "so it reads as
context, not an afterthought".

```text
You are a helpful home cooking assistant.
Inventory: {{INVENTORY_COMMA_LIST}}.
{{?MOOD}}The user says: "{{MOOD}}". Tailor your recipe suggestions to match this mood or craving.
{{?TASTE_PROFILE}}{{TASTE_PROFILE_FRAGMENT}}
{{?DIET}}Diet: {{DIET}}.
{{?ALLERGIES}}Avoid (allergies): {{ALLERGIES}}.
{{?CUISINES}}Preferred cuisines: {{CUISINES}}.
{{?PORTION}}{{PORTION_LINE}}
Suggest real, well-known dishes only, with their genuine standard ingredients. Never invent a dish or force a dish to use only the inventory. Assume salt, pepper, water and cooking oil are always on hand.
Respond ONLY with JSON of the form {"makeNow":[{"name":"","ingredients":[{"name":"","hasIt":true}],"steps":[""],"timeMinutes":0,"missingItems":[]}],"almostThere":[...]}. For every ingredient set hasIt true only if it is in the inventory. makeNow is for dishes whose ingredients are all in the inventory. almostThere is for dishes missing up to 3 ingredients; list each missing one in missingItems.
```

The last two lines are built by `+` string concatenation in Swift
(`AppModel.swift:510-522`); each is a **single line** with single spaces at the
join points. Do not wrap them.

### Placeholder rules

| Placeholder | Source | Included when |
| --- | --- | --- |
| `{{INVENTORY_COMMA_LIST}}` | `itemNames.joined(separator: ", ")` | Always (may be empty, producing `Inventory: .`) |
| `{{MOOD}}` | `AppModel.moodText`, trimmed of whitespace and newlines | Trimmed value is non-empty |
| `{{TASTE_PROFILE_FRAGMENT}}` | `TasteProfile.promptFragment()` | A profile exists **and** `taste.hasContent` is true |
| `{{DIET}}` | `prefs.diet` | `prefs.diet != "none"` (the raw value of `DietPreference.none`) |
| `{{ALLERGIES}}` | `prefs.allergies` | Trimmed value is non-empty |
| `{{CUISINES}}` | `prefs.cuisines` | Trimmed value is non-empty |
| `{{PORTION_LINE}}` | `InventoryLogic.portionLine(householdSize:)` | That function returns non-nil |

`DietPreference` raw values: `none`, `vegetarian`, `vegan`, `pescatarian`,
`keto`, `glutenFree` (`Preferences.swift:4-11`). `householdSize` defaults to `2`
when unset (`PrefKey.defaultHouseholdSize`, `Preferences.swift:69,86`).

### Portion line (verbatim)

`InventoryLogic.portionLine(householdSize:)` — `InventoryLogic.swift:147-153`:

- `householdSize < 1` -> **omit the line entirely** (returns nil).
- `householdSize == 1` -> `Cooking for 1 person; use single-serving portions.`
- otherwise -> `Cooking for {{N}} people; scale ingredient amounts to serve {{N}}.`

### Taste-profile fragment (verbatim)

`TasteProfile.promptFragment()` — `TasteProfile.swift:61-92`. Lines joined with
`\n`; each category line is emitted only when that category has content. A
category's content is its selected pills plus its trimmed free-text "other"
field, joined with `, ` (`combined()`, `TasteProfile.swift:53-58`).

```text
Favorite foods: {{FAVORITES}}.
Foods the user dislikes: {{DISLIKES}}. Avoid these.
Cuisines they enjoy: {{CUISINES_LOVED}}. Use these as flavor inspiration.
Cuisines they avoid: {{CUISINES_AVOIDED}}. Avoid those styles.
Allergies: {{ALLERGIES}}. Never include these.
Textures/flavors they avoid: {{TEXTURES_AVOIDED}}.
Spice preference: {{SPICE_DESC}}.
```

The spice line is **always emitted**, even for an otherwise-empty profile
(`TasteProfileTests.testPromptFragmentAlwaysIncludesSpiceLine`). Mapping from
`spiceLevel` (1-5, default 3):

| Level | `{{SPICE_DESC}}` |
| --- | --- |
| 1 | `very mild, no spice at all` |
| 2 | `mild, just a hint of spice` |
| 3 | `medium spice` |
| 4 | `spicy` |
| 5 | `very spicy, the hotter the better` |
| any other | `medium spice` |

`hasContent` — the gate on whether the fragment is included at all — is true if
**any** of the six pill lists is non-empty **or any** of the six "other" text
fields is non-blank (`TasteProfile.swift:29-34`). It must cover all six
categories: an earlier version only checked favorites/dislikes, which silently
dropped allergies-only profiles. That was logged in the source as a **safety
bug**; `TasteProfileTests.testHasContentTrueForAllergiesOnly` locks it down. A
port that checks fewer categories reintroduces a safety bug, not a cosmetic one.

## Token budget

- Same 1024-token runtime context as the vision prompts
  (`GemmaLLMService.swift:62`) — this call carries **no image**, so all 1024
  tokens are text: prompt plus the full recipe bundle reply.
- The tuning comment justifies 1024 partly on this call being "a compact recipe
  list". The inventory is passed as a **comma list, not prose** ("per spec",
  `AppModel.swift:478`) precisely to keep it compact — `Inventory: Eggs, Milk,
  Cheese.` is the asserted shape
  (`RecipePromptTests.testPromptListsInventoryAsCommaList`).
- `TODO(unresolved): the maximum safe inventory size before the reply is
  truncated by the 1024-token context. Nothing in the source caps itemNames, and
  no measurement of this limit exists in the repo (searched AppModel.swift,
  eval/, MacHarness/). A port targeting a larger context should still emit the
  comma-list form for parity.`
- `TODO(unresolved): maximum generated tokens / stop sequences — none configured.`

## Why it is worded this way

### The real-dishes clause — a deliberate bug fix (commit `45b99c3`)

> `Suggest real, well-known dishes only, with their genuine standard ingredients.`
> `Never invent a dish or force a dish to use only the inventory.`
> `Assume salt, pepper, water and cooking oil are always on hand.`

TestFlight feedback #3 (build 3, `testflight-feedback.md`): the generator
invented a *"cheesy Italian dip"* from ketchup + cheddar. The cause was the
prior wording, which told the model that `makeNow` "uses only inventory items" —
so the model worked backwards from the inventory and fabricated a plausible
dish name to fit it. Commit `45b99c3` removed the inventory-only rule and
inverted the framing: pick a **real** dish first, then mark honestly which of its
genuine ingredients you have.

`RecipePromptTests.testPromptDemandsRealDishesWithHonestInventoryMarking` pins
all four halves of this fix, including a **negative** assertion:

- must contain `real, well-known dishes`
- must contain `Never invent a dish`
- must contain `hasIt true only if it is in the inventory`
- must **NOT** contain `uses only inventory items`

Port that negative assertion too. It is the guard against someone
"helpfully" restoring the old constraint.

The salt/pepper/water/oil line stops the model from listing pantry staples as
missing items, which made every dish look unmakeable.

### The JSON schema clause

> `Respond ONLY with JSON of the form {...}. For every ingredient set hasIt true`
> `only if it is in the inventory. makeNow is for dishes whose ingredients are all`
> `in the inventory. almostThere is for dishes missing up to 3 ingredients; list`
> `each missing one in missingItems.`

- The literal skeleton (with empty-string and `0` placeholder values) is more
  reliable than describing the fields in prose.
- **`hasIt true only if it is in the inventory`** is the honesty rule: the UI
  shows have/missing per ingredient and the "add missing to shopping list" button
  is built from `missingItems`, so a model that marks everything `hasIt: true`
  breaks the shopping flow, not just the display.
- **`missing up to 3 ingredients`** bounds `almostThere` to things actually worth
  a grocery trip.
- `almostThere":[...]` is deliberately abbreviated with `...` in the skeleton
  rather than repeating the whole object — it keeps the prompt short (context is
  wired memory) and the model infers the repeat correctly.

### House style

`RecipePromptTests.testPromptNeverContainsEmDash` asserts the assembled prompt
contains no U+2014 em dash, for any combination of preferences. The same
assertion exists for the taste fragment
(`TasteProfileTests.testPromptFragmentNeverContainsEmDash`). Keep semicolons and
periods; never introduce em dashes into prompt text in a port.

## Output contract

One JSON object conforming to `schema/recipe.schema.json` (`RecipeBundleResponse`).
Parse with the **object** path of `parser/response-parsing.md`.

Failure handling (`AppModel.swift:467-475`): if the bundle does not decode, the
existing recipe lists are left untouched and a user-facing error is set:

```text
We could not read the recipe ideas this time. Please try again.
```

On success, `makeNow` and `almostThere` are replaced wholesale,
`hasGeneratedRecipes` is set true, and the error is cleared. `hasGeneratedRecipes`
exists so the UI can distinguish "never generated" from "generated, nothing
matched" — port that distinction.
