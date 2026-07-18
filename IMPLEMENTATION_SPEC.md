# dinnerDecider: Implementation Spec

Self-contained build brief. Everything an LLM needs to implement this app is in this document. Facts below marked "verified" come from a fact-checked research pass on July 17, 2026 (sources listed at the end).

## What we are building

An iOS app for the "Build with Gemma: JustBuild" hackathon (On-Device AI with Gemma 4 track). The user photographs their fridge and pantry. The app recognizes the food items 100% on-device with Gemma 4, builds an inventory, then generates recipes from what they have, plus "you only need 2 more items to make X" suggestions. Stretch goal: a shopping list that fills those gaps.

## Hard constraints (hackathon rules)

- Deadline: Kaggle Writeup due Saturday July 18, 2026, 3:00 PM MDT. Live 3-minute demo 3:00 to 4:30 PM.
- Gemma 4 inference must run entirely on the hardware demonstrated to judges. Any cloud inference for core AI = ineligible. Judges verify with network disabled (demo in airplane mode).
- New public GitHub repo, post-kickoff code only, clean commit history.
- Judging rubric (100 pts): Value 25, Enablement/Ease of Use 20, Underlying Model 20, Evidence & Evaluation 20, Inputs & Data 15. Note: a measured before/after accuracy table earns Evidence points; data provenance earns Inputs & Data points.

## Target hardware

- Demo device: iPhone 16 Pro Max (8GB RAM, A18 Pro). Also two iPhone 16s.
- Dev machine: MacBook Pro M1 Max, 64GB RAM, macOS 26.3, Xcode 26.3.
- Available for training only: NVIDIA DGX Spark (GB10, 128GB unified memory).

## Model and runtime (verified)

- Model: **Gemma 4 E4B** (instruction-tuned). 4.5B effective / 8B total params, vision-capable, Apache 2.0 licensed. This is the family's on-device 4B-class model; there is no dense 4B.
- Primary runtime: **llama.cpp** with GGUF + mmproj vision. Day-0 Gemma 4 vision support merged April 2, 2026 (PR #21309). CRITICAL: use a llama.cpp build and GGUF artifacts from after June 4, 2026 (PR #24118 fixed a vision conversion bug; older artifacts have broken image input).
- Model files (already downloaded to the dev Mac's Hugging Face cache):
  - `unsloth/gemma-4-E4B-it-GGUF` → `gemma-4-E4B-it-Q4_K_M.gguf` + `mmproj-F16.gguf`
  - `litert-community/gemma-4-E4B-it-litert-lm` → `gemma-4-E4B-it.litertlm` (alternate runtime, see below)
- Integration shortcut: the **LocalLLMClient** Swift package (MIT, github.com/tattn/LocalLLMClient) wraps llama.cpp behind a Swift API and supports multimodal input. Verify it vendors a post-June-2026 llama.cpp; if not, pin/update the llama.cpp dependency or embed llama.cpp directly.
- Alternate runtime: Google **LiteRT-LM** (first-party; Google's AI Edge Gallery app proves E4B vision runs on iPhone 16 Pro Max with roughly 3.4GB peak memory and about 25 tok/s decode). Caveat: no third-party example of the iOS SDK's image-input API was found. Timebox any attempt to 1 hour, then fall back to llama.cpp.
- Known constraints on A18 Pro: LiteRT-LM crashes above 4096-token context, so keep every prompt short regardless of runtime. Expect roughly 3.5 to 4.5GB app memory in use while the model is loaded; the per-app ceiling on an 8GB iPhone is roughly 5 to 6GB, so do not load anything else heavy while inferring.

## The recognition pipeline (this is the core insight, follow it exactly)

A naive "here is my whole fridge, what's in it?" prompt scored 40 to 50% in our baseline testing. Two zero-training techniques fix most of this (both verified in peer-reviewed literature):

1. **Crop, then ask.** Vision models collapse on small subjects (23 to 24 point accuracy drop documented). Never send the whole-scene photo for identification. Detect and crop each item first, then query the model once per crop.
2. **OCR fusion.** Feed the model the package's text alongside the image. Image+text beats image-only for fine-grained packaged-grocery recognition, with the biggest gains on look-alike products (the "Kirkland granola bars" failure mode).

Pipeline per photo:

1. Capture photo (AVFoundation camera, plus photo library import).
2. Segment items: Apple Vision framework `VNGenerateObjectnessBasedSaliencyImageRequest` and/or `VNDetectRectanglesRequest` to get bounding boxes; fall back to a simple 2x2 or 3x3 tile grid with overlap if detection is weak. Pad crops by ~15%.
3. OCR each crop with `VNRecognizeTextRequest` (accurate mode, on-device, roughly 100 to 300ms per crop on A18 Pro).
4. For each crop, one Gemma 4 E4B call: crop image + OCR text + a short prompt demanding structured JSON:
   - System-style instruction: "You identify grocery items. Text found on the packaging: <OCR text>. Respond ONLY with JSON: {\"name\": \"...\", \"brand\": \"... or null\", \"category\": \"produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other\", \"confidence\": 0-1}"
   - Keep total context well under 2048 tokens.
5. Deduplicate (same name+brand across overlapping crops), then show the inventory list for user confirmation. The user can rename, delete, or add items with taps. This review step is essential: it makes imperfect recognition feel fine (and scores Enablement points).
6. Persist inventory with SwiftData (item name, brand, category, quantity, dateAdded, source photo).

## Recipe features (text-only Gemma calls, its strong suit)

- **Recipes from inventory:** prompt E4B with the inventory list + user preferences (diet, cuisine likes, allergies from a simple settings screen). Ask for 3 recipes as JSON: name, ingredients (marking which the user has), steps, time.
- **Almost-recipes:** ask for 2 recipes the user could make by buying at most 2 more items; present as "Buy 2 cans of black beans and you can make chili."
- **Stretch, shopping list:** aggregate missing ingredients across accepted almost-recipes into a checklist. Optional standing rules ("always keep 2 gallons of milk").
- Keep each call's prompt compact (inventory as a comma list, not prose). Chunk if inventory is huge.

## App structure (SwiftUI, iOS 18+)

Screens:
1. **First-run model download:** downloads the GGUF + mmproj from Hugging Face direct URLs via background URLSession with progress bar and resume support. After this, the app is fully offline. (For the demo build, also support bundling the files locally to skip the download.)
2. **Capture:** camera view, shoot multiple photos (fridge, pantry, shelves).
3. **Scanning:** progress UI while crops are OCR'd and identified (show per-item results streaming in; this looks great in a demo).
4. **Inventory:** confirm/edit list, grouped by category.
5. **Recipes:** the three tabs of value: "Make now", "Almost there (buy 2)", "Shopping list".
6. **Settings:** preferences, allergies, model management.

Engineering notes:
- Load the model once, keep it resident; queue crop queries sequentially.
- Release camera/image buffers before inference to stay under the memory ceiling.
- All processing must work in airplane mode after the model download.
- TestFlight: keep the .ipa small (models are downloaded post-install). If time is short, demoing via an Xcode-installed build on the phone is equally valid to judges; do not burn the final morning on TestFlight processing.

## Evaluation (worth 20 rubric points, do not skip)

- Build a test set tonight: 20 to 30 photos of real pantry/fridge items, with ground-truth labels in a CSV.
- Measure: (a) naive whole-scene prompt accuracy (the baseline, roughly 40 to 50%), (b) the crop+OCR pipeline accuracy.
- Report the table in the Kaggle writeup, plus failure cases and how the confirm-screen UX absorbs them.

## Conditional branch: LoRA fine-tune on the DGX Spark

Only pursue if the smoke test passes; otherwise skip entirely.

- Tooling: Unsloth officially supports Gemma 4 E4B vision fine-tuning (FastVisionModel). Their guide recommends `finetune_vision_layers = False` first (train text side, freeze vision tower), which is also the safer export.
- Smoke test gate (about 1 hour total): train a throwaway LoRA on ~100 grocery images, merge, convert with llama.cpp's `convert_hf_to_gguf.py` (plus mmproj extraction), quantize to Q4_K_M, and run it on the phone. No one has publicly demonstrated this full chain for Gemma 4 vision, so prove it before spending the night on it. If the chain breaks, abandon fine-tuning.
- If the gate passes: train overnight on grocery product images (Open Food Facts is open-licensed and covers US retail products; good provenance story for the rubric), then report before/after accuracy.

## Explicitly rejected / risky paths

- MLX vision on iPhone: unproven for Gemma 4, skip.
- 12B/26B/31B models on phone: memory, no.
- Phone calling a Gemma server on the DGX Spark over local Wi-Fi: eligibility gamble under "network disabled" judging; ask organizers before considering. The Spark can instead BE the demo hardware outright if the phone path fails (Ollama has all five Gemma 4 sizes: `ollama pull gemma4:e4b` etc.).
- Whole-scene identification prompts: known to fail, always crop.

## Key sources

- Gemma 4 for LiteRT-LM (sizes, benchmarks, license): developers.google.com/edge/litert-lm/models/gemma-4
- Gemma 4 launch details: huggingface.co/blog/gemma4 and developers.googleblog.com (Gemma 4 edge post)
- llama.cpp Gemma 4 vision support: github.com/ggml-org/llama.cpp PR #21309 (and the June 4, 2026 fix, PR #24118)
- Unsloth Gemma 4 training guide: unsloth.ai/docs/models/gemma-4/train
- Crop sensitivity: "MLLMs Know Where to Look" (arxiv.org/abs/2502.17422, ICLR 2025)
- OCR fusion for groceries: Machine Vision and Applications 2024 (doi 10.1007/s00138-024-01549-9)
