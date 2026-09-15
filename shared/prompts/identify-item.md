# Prompt: `identify-item`

Single cropped grocery image + any OCR text read off that crop. Returns **one**
`IdentifiedItem` JSON object.

Source of record: `DinnerDecider/Services/LLM/GemmaLLMService.swift:94-103`
(`identifyItem(image:ocrText:)`).

## Placeholders

| Placeholder | Value |
| --- | --- |
| `{{OCR_TEXT}}` | The OCR result for this crop, trimmed of leading/trailing whitespace and newlines. **If the trimmed result is empty, substitute the literal string `none`.** (`GemmaLLMService.swift:92-93`) |

## Template (verbatim, single line)

The Swift source is a multi-line literal whose lines all end in `\`, so the
rendered prompt is **one line with no embedded newlines**. Reproduce it as one
line.

```text
You identify a single grocery item from the photo. Common items include: milk, eggs, butter, cheese, yogurt, chicken, beef, lettuce, tomatoes, onions, carrots, bread, rice, pasta, cereal, juice, soda, ketchup, mustard, olive oil, salt, pepper, ice cream, frozen pizza. If no grocery item is clearly visible, use name "unknown" with confidence 0. Text found on the packaging: {{OCR_TEXT}}. Respond ONLY with JSON, no other words: {"name": "...", "brand": "... or null", "category": "produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other", "confidence": 0-1}
```

Rendered length with `{{OCR_TEXT}}` = `none`: **558 characters**, no trailing
newline. Use this as a cheap regression check in a port.

## Attachment

Exactly one image attachment, the crop, after the model-input preprocessing in
`pipeline/scan-pipeline.md` (tone-curve normalization, then longest side capped
at 896px). One image is roughly **256 vision tokens** per the tuning note at
`GemmaLLMService.swift:50-61`.

## Token budget

- Runtime context is **1024 tokens total** - prompt + vision tokens + generated
  reply all share it (`GemmaLLMService.swift:62`). That is the only hard budget
  the code enforces.
- The context was chosen *because* this prompt is short: the comment reads "our
  prompts and replies are short (a single item JSON with ~256 image tokens ...),
  so 1024 is still comfortably enough". Lengthening this prompt eats KV cache,
  which is WIRED Metal memory, which is what jetsammed the app. Do not pad it.
- `TODO(unresolved): exact tokenizer token count for this prompt. No tokenizer
  count is recorded anywhere in the repo (searched GemmaLLMService.swift,
  SelfTest.swift, eval/, MacHarness/). Character count above is the only exact
  figure available.`
- `TODO(unresolved): maximum generated tokens / stop sequences. LocalLLMClient
  0.5.0's LlamaClient.Parameter exposes only context, batch and sampling (see
  CLAUDE.md "Memory / OOM notes"); the app sets no max-tokens cap and no stop
  strings, it simply drains the token stream to completion
  (GemmaLLMService.swift:145-148).`

## Sampling

`temperature: 0.2`, `topK: 40`, `topP: 0.95` (`GemmaLLMService.swift:68-70`).
Low temperature is deliberate: this is an extraction task, not a creative one.

## Why it is worded this way

- **"You identify a single grocery item from the photo."** - the crop is already
  one object; telling the model to pick one prevents it from listing neighbours
  that bleed in from the 15% padding.
- **The 25-item "Common items include:" list** - added in `222b956` ("Improve
  scan accuracy: whole-image pass, confidence filter, unknown escape hatch"). It
  anchors the model's vocabulary to everyday grocery nouns instead of hyper-
  specific product names it cannot actually read.
- **`If no grocery item is clearly visible, use name "unknown" with confidence 0.`**
  - the *escape hatch*. Without it the model hallucinates a plausible grocery
  item for a crop that is just fridge shelf. The pipeline relies on this exact
  behavior: it drops any result whose name lower-cases to `unknown` or whose
  confidence is below `0.15` (`AppModel.swift:382-384`). A port that omits this
  sentence will flood the inventory with hallucinated items, because the filter
  will have nothing to match on.
- **`Text found on the packaging: {{OCR_TEXT}}.`** - image+OCR fusion beats
  image-only on packaged goods (the "look-alike products" failure mode, see
  `OCRService.swift:5-8`). The literal `none` sentinel keeps the sentence
  grammatical when OCR found nothing, which is more reliable than emitting a
  dangling `Text found on the packaging: .`
- **`Respond ONLY with JSON, no other words:`** followed by the literal shape  - 
  the model still wraps output in prose and code fences often enough that the
  parser has to salvage it (see `parser/response-parsing.md`), but this line cuts
  the rate substantially. **Do not replace this with a grammar / structured-output
  constraint** - see the prohibition in `parser/response-parsing.md`.
- **The inline category alternation** `produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other`
  is the full `FoodCategory` enum (`taxonomy/categories.md`). It is spelled out
  in the prompt rather than post-mapped so the model picks from the closed set.

## Output contract

One JSON object conforming to `schema/identified-item.schema.json`. Parse it with
the object path of `parser/response-parsing.md` (`firstJSONObject` then decode).
A decode failure is an **error** for this call - on iOS it throws
`LLMServiceError.badResponse` (`GemmaLLMService.swift:105-107`), and the scan
loop catches it, logs, and moves to the next crop (`AppModel.swift:392-394`). It
does **not** abort the scan.
