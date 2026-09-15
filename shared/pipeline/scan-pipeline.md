# Pipeline: scan (photo -> identified items)

Implementation-neutral spec for the recognition pipeline. Sources of record:

- `DinnerDecider/Services/AppModel.swift:276-400` - the orchestration (`startScan`, `runScan`)
- `DinnerDecider/Services/AppModel.swift:39-52` - `ScanCallBudget`
- `DinnerDecider/Services/Vision/ImageDownscaler.swift` - capture downscale
- `DinnerDecider/Services/Vision/CropService.swift` - crops, saliency, tile fallback
- `DinnerDecider/Services/Vision/OCRService.swift` - OCR
- `DinnerDecider/Services/LLM/GemmaLLMService.swift:154-190` - model-input preprocessing
- `DinnerDecider/Services/InventoryLogic.swift` - dedupe, confirm, cook
- `DinnerDeciderTests/ScanCallBudgetTests.swift`, `InventoryLogicTests.swift` - the specification of record

## Constants (all extracted, none invented)

| Constant | Value | Where |
| --- | --- | --- |
| Capture downscale, longest side | **2016 px** | `ImageDownscaler.captureMaxSide` |
| Model-input downscale, longest side | **896 px** | `GemmaLLMService.maxImageSide` |
| Crop padding | **0.15** (15% of each side, applied as an outset on both axes) | `CropService.paddingFraction` |
| Tile overlap | **0.20** (20%) | `CropService.tileOverlap` |
| Tile base size | width/3 x height/3 | `CropService.tileGrid` |
| Rectangle detector max observations | **8** | `CropService.detectBoxes` |
| Rectangle detector min confidence | **0.15** | `CropService.detectBoxes` |
| Rectangle detector min size | **0.05** (5% of the image) | `CropService.detectBoxes` |
| Box merge threshold | **IoU > 0.3** | `CropService.mergeOverlapping` |
| Tile-fallback trigger | fewer than **2** boxes after detection+padding | `CropService.crops` |
| Minimum usable crop | **8 x 8 px** (strictly `>= 8` for crops, `> 8` for tiles) | `CropService.crops:49`, `CropService.tileGrid:165` |
| Heavy model calls per scan | **4**, scan-wide across all photos | `ScanCallBudget(limit: 4)` |
| Low-confidence cutoff | drop if `confidence < 0.15` | `AppModel.swift:330, 371, 383` |
| Unknown cutoff | drop if `lowercase(name) == "unknown"` | same lines |
| Model context / batch | **1024 / 512** | `GemmaLLMService.swift:62,67` |
| Sampling | temperature **0.2**, topK **40**, topP **0.95** | `GemmaLLMService.swift:68-70` |

## Stage 0 - capture intake

Every photo entering the app (camera or picker) is immediately downscaled so its
longest side is at most **2016 px**, then flattened to an opaque, scale-1,
`.up`-oriented bitmap. Images already smaller are returned untouched.

Why: the runtime holds ~5 GB of **wired** Metal memory (weights + projector + KV
cache). The `vm-pageshortage` jetsam that killed the app was a *systemwide*
shortage of pageable memory, not an app-footprint limit. A 24 MP frame is ~90 MB
decoded and a batch can hold up to 8 of them. Crops get re-scaled to 896 px for
the model anyway, so retaining ~2000 px originals "keeps crop quality while
cutting resident pixel memory roughly 10x" (`ImageDownscaler.swift:5-13`).

Photos are staged in a batch keyed by **stable identity (UUID), not array index**
(`PhotoBatch.swift`). Removal by a stale index is a no-op rather than a trap.
That is a crash class, not a nicety - port it.

## Stage 1 - orchestration

```text
startScan(photos):
    cancel any in-flight scan
    clear scannedItems
    drop any photo that carries no usable pixel data
    if no photos remain: phase = EMPTY; return
    phase = SCANNING(photo 0 of N, stage = CROPPING)
    run runScan(photos) as a cancellable background task owned by the MODEL,
        not by a view      # navigation or backgrounding must not tear it down
```

```text
runScan(photos):
    lifecycle.beginSession()                  # see model-lifecycle.md - MANDATORY
    defer { lifecycle.endSession() }          # on EVERY exit path, cancellation included
    loadModelIfNeeded()

    results = []                              # raw, pre-dedupe, scan-wide
    budget  = ScanCallBudget(limit = 4)       # scan-wide, NOT per photo

    for (photoIndex, photo) in photos:
        if cancelled: return
        phase = SCANNING(photoIndex, N, itemsDone=0, itemsTotal=0, stage=CROPPING)

        # --- Pass A: whole image ---
        scope {                               # scoped so the full-res bitmap is
                                              # released before the crop loop allocates
            if budget.consume():
                full = normalizedImage(photo)             # orientation-baked, pixel-backed
                if full != null:
                    ocr = recognizeText(full)
                    items = try? identifyAllItems(full, ocr)   # nil on failure, no throw
                    if items != null:
                        for item in items:
                            if isNoise(item): continue
                            results.append(item)
                        scannedItems = dedupe(results)         # stream into the UI
        }

        if cancelled: return

        # --- Pass B: crops ---
        cropResult = crops(from: photo)
        cappedCrops = cropResult.images.prefix(budget.remaining)   # NOTE: remaining, before consuming
        total = cappedCrops.count

        for (index, crop) in cappedCrops:
            if not budget.consume(): break
            if cancelled: return
            phase = SCANNING(photoIndex, N, itemsDone=index, itemsTotal=total, stage=IDENTIFYING)
            ocr = recognizeText(crop)
            try:
                if cropResult.isTileFallback:
                    items = identifyAllItems(crop, ocr)    # a tile may hold several items
                    for item in items:
                        if isNoise(item): continue
                        results.append(item)
                else:
                    item = identifyItem(crop, ocr)         # a detected box is one object
                    if isNoise(item): continue
                    results.append(item)
                scannedItems = dedupe(results)
            catch error:
                log("[Scan] Crop {index+1}/{total} failed: {error}")
                inferenceFailures += 1                     # counted, not silent
                continue                                   # NEVER aborts the scan

    if cancelled: return
    phase = terminalPhase(scannedItems.count, inferenceFailures)   # see Stage 7
```

`inferenceFailures` is incremented at BOTH swallow sites: the whole-image pass
above and the per-crop loop here. Only a THROWN error counts. `identifyAllItems`
returning `[]` from an unreadable response is not a failure -- it answered, we
just could not read it. Getting that backwards turns an unreadable-but-answered
call into a spurious `FAILED`.

`isNoise(item)` is the single filter, applied identically at all three call sites:

```text
isNoise(item) = lowercase(item.name) == "unknown"  OR  item.confidence < 0.15
```

Strictly less-than: an item at exactly `0.15` is **kept**. The `unknown` half
only works if the identify-item prompt keeps its escape-hatch sentence - see
`shared/prompts/identify-item.md`.

Every model call must be bracketed by the lifecycle's inference bracket
(`beginInference` / `finishInference` / drain). See `model-lifecycle.md`.

## Stage 2 - crop / saliency strategy

```text
crops(from photo) -> (images, rects, isTileFallback):
    image = normalizedImage(photo)       # bake EXIF rotation, force pixel-backed
    if image == null: return ([], [], false)

    boxes = detectBoxes(image)           # saliency + rectangles, merged
    boxes = boxes.map { pad(box, 0.15) } # padding applied BEFORE the count check

    isTileFallback = (boxes.count < 2)
    if isTileFallback:
        boxes = tileGrid(image.width, image.height)   # tiles are NOT padded

    for box in boxes:
        r = integral(box)                # round out to whole pixels
        if r.width >= 8 and r.height >= 8 and crop succeeds:
            emit (croppedImage, r)
```

Order matters and is easy to get wrong: **pad first, then count**. Padded boxes
can merge visually but are not re-merged, and the `< 2` test sees the padded set.

### Normalization (`normalizedCGImage`)

Return a bitmap that is guaranteed `.up`-oriented and pixel-backed. Fast path:
if the image is already `.up` and has pixel data, return it as-is. Otherwise
redraw it at scale 1, non-opaque.

Two real failure modes this exists for (`CropService.swift:134-139`):
1. HDR/HEIC photos from the picker can decode to a deferred, non-pixel-backed
   image whose bitmap accessor is null - which would silently yield **zero crops**.
2. Portrait phone photos are stored landscape with an EXIF rotation tag. Without
   baking it, the detector returns boxes in the wrong coordinate frame.

A port on a platform without EXIF-aware decoding must still normalize explicitly.

### Detection

Two detectors run over the same normalized image and their outputs are pooled:

1. **Objectness-based saliency** - every salient object's bounding box.
2. **Rectangle detection** - max 8 observations, min confidence 0.15, min size
   0.05 of the image.

Both report **normalized rects with a bottom-left origin**; convert to pixel
coordinates with a **top-left origin**:

```text
x = minX * width
y = (1 - maxY) * height
w = normalizedWidth  * width
h = normalizedHeight * height
```

**This flip is Apple-Vision-specific. Check your detector before reusing it.**
The bottom-left origin is a property of `VNGenerateObjectnessBasedSaliencyImageRequest`
and `VNDetectRectanglesRequest`, not of detectors in general. ML Kit's
`ObjectDetector.getBoundingBox()`, for instance, returns an Android `Rect` that is
already in **pixel space with a top-left origin**: running it through the formula
above applies a flip it does not need and puts every crop in the wrong half of the
image. Nothing crashes, and recognition simply gets worse -- the same silent
wrong-answer shape as the other traps in this document.

Getting this flip wrong produces crops of the wrong half of the photo, and the
model dutifully describes whatever is there - a silent wrong-answer bug, not a
crash.

Then **greedy merge**: walk the pooled boxes in order; for each, merge it into
the first already-kept box whose IoU with it exceeds **0.3** (union the two
rects) and stop; otherwise keep it as a new box. Zero-area boxes are skipped.
Greedy and order-dependent - not a clustering algorithm. Reproduce the order.

IoU is `intersectionArea / (areaA + areaB - intersectionArea)`, and `0` when the
rects do not intersect or the union area is not positive.

### Padding

```text
dx = box.width  * 0.15
dy = box.height * 0.15
padded = box outset by (dx, dy) on each axis, then intersected with image bounds
```

The outset is **proportional per axis**, so a wide flat box gains more horizontal
than vertical padding. It grows each dimension by 30% total (15% per side) before
clamping. Clamping to bounds is required; an unclamped crop rect is a crash on
most imaging APIs.

### Tile-grid fallback

Triggered when detection yields **fewer than 2** padded boxes - meaning a packed
fridge shelf where nothing stood out, exactly the case where missing items is
worst.

```text
tileWidth  = width  / 3
tileHeight = height / 3
stepX = tileWidth  * (1 - 0.20)
stepY = tileHeight * (1 - 0.20)

y = 0
while y < height:
    x = 0
    while x < width:
        w = min(tileWidth,  width  - x)
        h = min(tileHeight, height - y)
        if w > 8 and h > 8: emit rect(x, y, w, h)
        x += stepX
    y += stepY
```

**Read the loop, not the comment.** The source calls this "a 3x3 grid", but with
a step of `0.8 * size/3` the loop emits **4 columns x 4 rows = 16 tiles** for any
image of ordinary size (verified for 2016x1512, 1512x2016 and 900x900). The last
column and row are narrow remainder tiles (20% of a tile wide). A port that
literally emits 9 tiles is not a parity implementation. In practice the 4-call
budget means at most 3 tiles are ever analyzed, but the *first three tiles in
emission order* are then what the scan sees, so the geometry still matters.

Tiles are **not** padded - they already overlap by 20%.

`isTileFallback` is carried alongside the crops because it changes which prompt
each crop gets: tiles are large regions -> `identify-all-items`; detected boxes
are single objects -> `identify-item`.

### Ports without a saliency detector

Android and Linux have no equivalent of the platform saliency + rectangle
detectors. A port with no detector available must go **straight to the tile grid**
with `isTileFallback = true`. That is not a degraded special case - it is a path
iOS already takes routinely, and the rest of the pipeline is identical. Do **not**
substitute a different object detector without re-running the eval harness
(`eval/`): crop geometry changes recall, and the confidence/dedupe constants were
tuned against these crops.

## Stage 3 - OCR

Run text recognition over each image just before it is sent to the model - once
over the whole normalized photo for Pass A, once per crop for Pass B.

- Accurate (not fast) recognition level; language correction **on**.
- Take the **top candidate only** for each recognized line.
- Join the lines with a **single space** into one flat string. No newlines, no
  confidence filtering, no ordering guarantees beyond the detector's own.
- Failures are swallowed: an OCR error yields an empty string, never an
  exception. The scan continues image-only.
- At the prompt boundary the string is trimmed, and an **empty result becomes the
  literal `none`** (`GemmaLLMService.swift:92-93, 114-115`).

Why OCR at all: image+OCR beats image-only on packaged goods, the "look-alike
products" failure mode (`OCRService.swift:5-8`).

## Stage 4 - model-input preprocessing

Applied inside the LLM service to every image, *after* cropping and immediately
before inference (`GemmaLLMService.downscaled`, `:160-190`):

1. **Tone-curve exposure/contrast normalization**, for dim fridge and pantry
   photos. Control points, verbatim:

   | Input | Output |
   | --- | --- |
   | 0.00 | 0.05 |
   | 0.25 | 0.28 |
   | 0.50 | 0.55 |
   | 0.75 | 0.78 |
   | 1.00 | 1.00 |

   A gentle lift of the shadows and midtones with the white point pinned. If the
   filter fails, fall back to the original image rather than erroring.

2. **Downscale so the longest side is at most 896 px**, only if it exceeds it.
   Target dimensions are rounded. Result is scale 1 and opaque.

Two preprocessing rules that are memory decisions, not style:

- **Reuse one image-processing context for the whole app lifetime.** Creating one
  per crop allocates a fresh GPU context each time, "exhausting wired memory on
  top of the already ~3.5GB GPU-resident model and triggering jetsam"
  (`GemmaLLMService.swift:152-157`).
- **Use the GPU renderer** for this pass. Build 5/7 moved it to software and
  regressed; build 8 restored GPU "to match the build-4 configuration, the last
  one proven to recognize items on device".

## Stage 5 - the two-pass union and the call budget

The scan is a **union**, not a choice: Pass A (whole image) and Pass B (crops)
both append into one `results` list, and dedupe reconciles them. Pass A exists
because cropping loses items that fall between boxes; Pass B exists because
"vision models collapse on small subjects" (`CropService.swift:16-20`).

### `ScanCallBudget`

```text
ScanCallBudget(limit = 4):
    remaining = limit
    consume():
        if remaining <= 0: return false     # stays exhausted forever
        remaining -= 1
        return true
```

Rules, all pinned by `ScanCallBudgetTests`:

- Exactly **4** heavy calls per scan, **scan-wide across every photo**, not per
  photo. A 3-photo batch still gets 4 calls total - photos after the budget runs
  out contribute nothing.
- The whole-image pass spends one, leaving **3** for crops.
- Crops are capped by `prefix(budget.remaining)` **before** the loop consumes
  anything, so `itemsTotal` shown to the user is what will actually be analyzed,
  not the raw crop count. With a 9-crop photo this yields exactly 3.
- An exhausted budget stays exhausted.

Why 4: "the wired-memory ceiling on an 8GB phone is real: build 4 jetsammed on
the fifth heavy call of a scan" (`AppModel.swift:35-38`). TestFlight feedback #4:
crash while identifying item 4 of 9, after items 1-3 got real inference. The
per-request context reset removed the per-call creep; the budget is the belt to
that braces. The test comments the fifth call as "the one that crashed".

A port on a machine without an 8 GB wired ceiling (a Linux box with 64 GB) may
legitimately want a larger budget - make it a **named, documented parameter with
a default of 4**, keep the semantics (scan-wide, prefix-capping, stays
exhausted), and never remove it.

### Per-request context reset (mandatory)

Before **every** request - each crop, each whole-image pass, each recipe
generation - reset the inference session: clear the KV cache **and** wipe the
prompt cache. Every request is independent, and without the reset the runtime
"keeps KV state and cached image chunks from every previous request, and that
creep is what jetsammed multi-crop scans around item 4"
(`GemmaLLMService.swift:138-142`).

Note from the build-8 commit: clearing the KV cache alone **desyncs the prompt
cache**. Both must be cleared together.

## Stage 6 - dedupe

Applied to the raw `results` list every time it grows, so the UI streams in
deduplicated (`InventoryLogic.dedupe`, `InventoryLogic.swift:35-61`).

```text
mergeKey(name, brand) = trim(lowercase(name)) + "|" + trim(lowercase(brand ?? ""))
```

The `|` separator and the empty-brand-becomes-empty-string rule are both pinned
by test literals (`"milk|horizon"`, `"eggs|"`). Trim whitespace **and** newlines.

```text
dedupeWithCounts(items):
    for item in items (in order):
        key = mergeKey(item.name, item.brand)
        if key already seen:
            counts[key] += 1
            if item.confidence > chosen[key].confidence: chosen[key] = item
        else:
            append key to order
            chosen[key] = item
            counts[key] = 1
    return order.map { (item: chosen[key], count: counts[key]) }
```

Guarantees a port must reproduce:

1. **First-seen order is preserved**, even when a later, higher-confidence
   sighting replaces the stored item. Position comes from first sighting; content
   comes from the best sighting.
2. **Highest confidence wins**, strictly greater-than - ties keep the earlier item.
3. **Brand is part of identity.** `Milk/Horizon` and `Milk/Fairlife` are two rows.
   `Milk/nil` and `Milk/""` are the same row.
4. The **count** (how many crops resolved to this item) is retained separately and
   used when merging quantities on confirm.

`dedupe()` is exactly `dedupeWithCounts()` with the counts dropped.

## Stage 7 - terminal states

| Phase | Meaning |
| --- | --- |
| `IDLE` | Nothing running |
| `SCANNING(progress)` | Running; carries photoIndex, photoCount, itemsDone, itemsTotal, stage (`CROPPING` \| `IDENTIFYING`) |
| `FINISHED` | Done, at least one item survived the filters |
| `EMPTY` | Done, every model call answered, none of them saw food |
| `FAILED` | Done, nothing found because the model calls themselves failed |
| `CANCELLED` | User cancelled part way |

`EMPTY` is the state that rendered as **"Nothing spotted"** in TestFlight
feedback #2 while the real cause was the model being freed mid-scan. The scan
loop swallows per-crop errors by design, so for a long time systemic failure and
genuine emptiness looked identical from here. Two obligations for a port:

1. Implement the lifecycle session rules so the systemic case cannot happen
   (`model-lifecycle.md`).
2. Distinguish "every identify threw" from "nothing recognized". **This is now
   shipped iOS behavior, not an open follow-up** (commit `b9268ef`,
   Sept 14 2026). There IS behavior to copy, and a port must copy it:

   - Count every inference that throws. Both swallow sites count: the
     whole-image pass and the per-crop loop. They still log; they are no
     longer silent.
   - The terminal phase is chosen by a pure function,
     `ScanOutcome.terminalPhase(itemsFound:inferenceFailures:)`
     (`AppModel.swift`), so it is testable without standing up a scan:
     - `itemsFound > 0` -> `FINISHED`, regardless of how many calls failed.
       A partial success shows what was found rather than discarding it.
     - otherwise `inferenceFailures > 0` -> `FAILED`
     - otherwise -> `EMPTY`
   - `FAILED` gets its own user-facing state. `EMPTY` keeps the photo advice
     ("get closer, add more light") because that advice is only honest when
     the model actually answered and saw nothing. `FAILED` says the scan could
     not finish and suggests closing other apps. Reusing the `EMPTY` copy for
     `FAILED` blames the user's photo for our failure, which is the whole
     substance of the P2 report.

   Covered by `ScanOutcomeTests.swift` (6 tests). A port should mirror those
   cases, including the boundary one: a single failure is enough to make
   "Nothing spotted" a lie.

## Stage 8 - confirm into inventory

`InventoryLogic.confirmPlan` (`InventoryLogic.swift:81-106`). Input: the
confirmed `(mergeKey, quantity)` pairs plus a snapshot of existing rows as
`(key, quantity)`. Output: an ordered action list.

```text
confirmPlan(confirming, existing):
    quantities = { ref.key: ref.quantity for ref in existing }
    known      = set(ref.key for ref in existing)
    actions    = []
    for entry in confirming:                   # input order preserved
        updated = (quantities[entry.key] ?? 0) + entry.quantity
        quantities[entry.key] = updated
        if entry.key in known:
            actions.append(INCREMENT(entry.key, newQuantity = updated))
        else:
            known.insert(entry.key)
            actions.append(INSERT(entry.key, quantity = entry.quantity))
    return actions
```

Subtlety pinned by `testConfirmStacksRepeatedKeysAcrossTheBatch`: two confirmed
entries with the same key produce `INSERT(key, 1)` then `INCREMENT(key, 2)`  - 
the running tally spans the batch, and `INSERT` carries the entry's own quantity
while `INCREMENT` carries the running total. Emit both actions in order; do not
collapse them.

## Stage 9 - cooking (quantity decrement)

`InventoryLogic.cookPlan` (`InventoryLogic.swift:122-141`). Ingredients have no
brand, so matching uses the **name-only key** `trim(lowercase(name))`.

```text
cookPlan(usedKeys, stock):
    quantities = { ref.key: ref.quantity }, order = [ref.key] in stock order
    touched = {}
    for key in usedKeys where key exists in quantities:
        quantities[key] -= 1                    # exactly 1 per use
        touched.insert(key)
    return, in STOCK order, for each touched key:
        CookChange(key, newQuantity = max(quantities[key], 0), removed = newQuantity <= 0)
```

- Rows not used are untouched and **not returned**.
- Ingredients not in stock are ignored silently (no error, no shopping-list side
  effect here).
- Output is ordered by **stock order**, not by `usedKeys` order.
- Quantity is clamped at 0; a row reaching 0 is flagged `removed`.
- A repeated key in `usedKeys` decrements more than once - there is no
  de-duplication of `usedKeys`.

## Stage 10 - shopping list

`ShoppingListLogic` (`ShoppingListLogic.swift`).

```text
aggregate(names):
    for raw in names:
        name = trim(raw)
        if name is empty: skip
        if lowercase(name) not seen: mark seen; emit name      # ORIGINAL casing

newItems(adding, existing):
    existingKeys = { lowercase(trim(e)) for e in existing }
    return aggregate(adding).filter { lowercase(it) not in existingKeys }
```

Case-insensitive identity, **first-seen casing preserved** in the output. Note
the asymmetry pinned by the tests: `aggregate` trims each candidate before
comparing, and `newItems` trims the existing entries too, but the final filter
compares `lowercase(it)` on the already-trimmed aggregate output.

Export format (`exportText`) is contractual - see the `$comment` in
`shared/schema/shopping-list-item.schema.json`.
