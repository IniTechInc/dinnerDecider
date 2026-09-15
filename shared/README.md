# `shared/` - the cross-platform behavior contract

This directory is the **source of truth** for everything about DinnerDecider that
is not iOS-specific. It was extracted verbatim from the shipped Swift
implementation (`DinnerDecider/`) and its unit tests (`DinnerDeciderTests/`),
which remain the *implementation of record* for iOS.

The reason it exists: the recognition pipeline, the prompts, the model lifecycle
rules and the JSON salvage rules are not obvious. Several of them are the direct
scar tissue of TestFlight builds 1 through 8 - a use-after-free crash, a
systemwide `vm-pageshortage` jetsam, a silent "Nothing spotted" empty scan, and
a recipe generator that invented dishes. A port that re-derives these from first
principles will re-derive the bugs too.

**Rules for any port (Android/Kotlin, Linux/Go, or a future platform):**

1. Prompts are **byte-identical**. Do not "improve" the wording. Every clause in
   `prompts/` exists because a different wording produced a field bug.
2. The parser contract in `parser/response-parsing.md` is mandatory, and the
   golden fixtures in `conformance/cases.json` must pass **unmodified**.
3. The lifecycle state machine in `pipeline/model-lifecycle.md` is mandatory.
   It is a pure value type on iOS on purpose - port it as a pure value type too,
   and port its tests.
4. Constants in `pipeline/scan-pipeline.md` and `runtime/model-files.md` are
   tuned numbers, not defaults. Changing one is a memory decision, not a taste
   decision.
5. Unknown enum values never throw. See `taxonomy/categories.md`.

## Provenance

Every document here cites the Swift file and line it came from. When the Swift
changes, update the document in the same commit. If you cannot find a value in
the Swift, the document says `TODO(unresolved): ...` rather than guessing - do
the same.

Extracted at commit `658a685` ("Build 8: GPU vision restored, context reset
between requests, 4-call scan budget"), app build 8, `MARKETING_VERSION 0.1.0`.

## What lives where

| Document | Covers |
| --- | --- |
| `prompts/identify-item.md` | Single-crop identification prompt (verbatim) |
| `prompts/identify-all-items.md` | Whole-image / tile multi-item prompt (verbatim) |
| `prompts/recipes.md` | Recipe prompt assembly, taste-profile fragment, portion line |
| `schema/*.schema.json` | JSON Schema for every wire/persistence type |
| `taxonomy/categories.md` | `FoodCategory` values + the unrecognized-value fallback |
| `pipeline/scan-pipeline.md` | Downscale, crop, saliency, tile fallback, OCR, two-pass union, call budget, dedupe, filters |
| `pipeline/model-lifecycle.md` | Load/infer/unload state machine, sessions, memory-pressure deferral |
| `parser/response-parsing.md` | Channel stripping, fence stripping, balanced-JSON extraction, the grammar prohibition |
| `runtime/model-files.md` | GGUF quant preference, mmproj pairing, sizes, sha256, memory levers |
| `conformance/` | Golden parser fixtures every platform must pass |

## Platform conformance matrix

Status legend: **Shipped** = implemented and verified on device · **Planned** =
not started · **N/A** = does not apply to that platform.

| Contract area | iOS (Swift) | Android (Kotlin) | Linux (Go) |
| --- | --- | --- | --- |
| `identify-item` prompt | Shipped - `GemmaLLMService.swift:94-103` | Planned | Planned |
| `identify-all-items` prompt | Shipped - `GemmaLLMService.swift:116-124` | Planned | Planned |
| Recipe prompt assembly | Shipped - `AppModel.swift:480-524` | Planned | Planned |
| Taste-profile prompt fragment | Shipped - `TasteProfile.swift:61-92` | Planned | Planned |
| Response parser (object) | Shipped - `LLMResponseParser.swift:39-88` | Planned | Planned |
| Response parser (array) | Shipped - `LLMResponseParser.swift:91-140` | Planned | Planned |
| Thinking-channel stripping | Shipped - `LLMResponseParser.swift:28-33` | Planned | Planned |
| Model lifecycle state machine | Shipped - `ModelLifecycle.swift` | Planned | Planned |
| Memory-pressure sources | Shipped - UIKit notification + `DispatchSourceMemoryPressure` (`AppModel.swift:152-173`) | Planned - `ComponentCallbacks2.onTrimMemory` is the nearest analogue | Planned - no OS equivalent; see `pipeline/model-lifecycle.md` |
| Scan call budget (4) | Shipped - `AppModel.swift:39-52` | Planned | Planned |
| Capture downscale (2016px) | Shipped - `ImageDownscaler.swift` | Planned | Planned |
| Model-input downscale (896px) | Shipped - `GemmaLLMService.swift:30,160-190` | Planned | Planned |
| Tone-curve exposure normalization | Shipped - Core Image `CIToneCurve` (`GemmaLLMService.swift:166-176`) | Planned - needs an equivalent curve, not a library-specific filter | Planned |
| Saliency + rectangle detection | Shipped - Apple Vision (`CropService.swift:59-83`) | Planned - no Vision equivalent; tile fallback is the floor | Planned - same |
| Tile-grid fallback (3x3, 20% overlap) | Shipped - `CropService.swift:152-173` | Planned | Planned |
| OCR | Shipped - Apple Vision accurate mode (`OCRService.swift`) | Planned - ML Kit / Tesseract | Planned - Tesseract or none |
| Dedupe / confirm / cook plans | Shipped - `InventoryLogic.swift` | Planned | Planned |
| Shopping list aggregation + export | Shipped - `ShoppingListLogic.swift` | Planned | Planned |
| Model file locator + quant preference | Shipped - `ModelFileLocator.swift` | Planned | Planned |
| Persistence | Shipped - SwiftData + `UserDefaults` | Planned - Room / DataStore | Planned - any store matching `schema/` |
| Calendar meal planning | Shipped - EventKit (`CalendarService.swift`) | Planned | N/A |
| Kroger cart integration | Shipped - `KrogerService.swift` | Planned | Planned |

Note: the Vision-backed crop step is the only part of the pipeline with no
portable equivalent. `pipeline/scan-pipeline.md` specifies exactly what a port
must do when saliency detection is unavailable - it degrades to the same tile
grid iOS already uses as a fallback, which keeps behavior inside the contract.
