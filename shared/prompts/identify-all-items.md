# Prompt: `identify-all-items`

Whole-image (or whole-tile) pass. Returns a **JSON array** of `IdentifiedItem`.

Source of record: `DinnerDecider/Services/LLM/GemmaLLMService.swift:116-124`
(`identifyAllItems(image:ocrText:)`).

Used in two places (`pipeline/scan-pipeline.md`):
1. The first call of every photo, on the full normalized image, before cropping.
2. Every crop, when the crop set came from the **tile-grid fallback** (a tile is
   a large region that legitimately contains several items).

## Placeholders

| Placeholder | Value |
| --- | --- |
| `{{OCR_TEXT}}` | OCR over the image being sent, trimmed. **Empty trimmed result substitutes the literal string `none`.** (`GemmaLLMService.swift:114-115`) |

## Template (verbatim, single line)

As with `identify-item`, the Swift literal's lines end in `\`, so the rendered
prompt is **one line, no embedded newlines**.

```text
List ALL food and grocery items visible in this photo. Common items include: milk, eggs, butter, cheese, yogurt, chicken, beef, lettuce, tomatoes, onions, carrots, bread, rice, pasta, cereal, juice, soda, ketchup, mustard, olive oil, salt, pepper, ice cream, frozen pizza. Text found in the image: {{OCR_TEXT}}. Respond ONLY with a JSON array, no other words: [{"name": "...", "brand": "... or null", "category": "produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other", "confidence": 0-1}]
```

Rendered length with `{{OCR_TEXT}}` = `none`: **491 characters**, no trailing
newline.

## Attachment, token budget, sampling

Identical to `identify-item`: one image attachment after the same preprocessing,
same 1024-token shared context, same `temperature 0.2 / topK 40 / topP 0.95`.
The reply here is longer than the single-item reply (an array of N objects), which
is another reason the context must not be spent on prompt padding.

- `TODO(unresolved): exact tokenizer token count, and any cap on N (the number of
  array elements). Neither the prompt nor the code bounds N; the pipeline bounds
  cost by call count, not by items per call.`
- `TODO(unresolved): maximum generated tokens / stop sequences - none configured,
  same as identify-item.`

## Why it is worded this way

- **`List ALL ... visible in this photo.`** with `ALL` capitalised - this prompt
  was added in `222b956` specifically to catch items that fall *between* crop
  boxes. Its whole job is recall, so the instruction leans hard on completeness.
- **Same 25-item "Common items include:" vocabulary anchor** as `identify-item`,
  for the same reason, and kept identical so the two passes agree on naming and
  their results dedupe cleanly (dedupe is a normalized name+brand string match  - 
  see `pipeline/scan-pipeline.md`; divergent vocabularies would defeat it).
- **`Text found in the image:`** (not "on the packaging") - a whole photo's OCR
  spans many labels, so the narrower packaging framing would be misleading.
- **No `unknown` escape hatch in this prompt.** The array form already lets the
  model answer "nothing here" with `[]`. The pipeline nevertheless applies the
  *same* `unknown` / `confidence < 0.15` filter to every element of the array
  (`AppModel.swift:329-331` and `370-373`), because models emit `unknown` rows
  anyway. Port the filter, not the omission.
- **`Respond ONLY with a JSON array, no other words:`** plus the literal
  `[{...}]` shape - the array brackets in the example are what make the model
  emit a top-level array rather than an object with an `items` key. The parser's
  array path requires a genuine top-level `[` (`firstJSONArray`), and an object
  reply is a hard miss: `decodeArray` on an object returns nil, which the pipeline
  treats as zero items (`GemmaLLMService.swift:126`).

## Output contract

A JSON array conforming to `items` of `schema/identified-item.schema.json`. Parse
with the array path of `parser/response-parsing.md` (`firstJSONArray` then
decode).

Failure handling differs from `identify-item`: a parse failure here is **not** an
error. `GemmaLLMService.identifyAllItems` returns `[]` on unparseable output
(`return LLMResponseParser.decodeArray(...) ?? []`, `GemmaLLMService.swift:126`).
Ports must match this: an unreadable whole-image pass silently contributes
nothing, and the crop passes still run.
