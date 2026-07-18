# dinnerDecider: Point Your Phone at the Fridge, Get Dinner

**Build with Gemma: JustBuild hackathon, On-Device AI with Gemma 4 track**

Team: [PLACEHOLDER: team name and member names]
Repo: [PLACEHOLDER: public GitHub repo URL]
Demo video: [PLACEHOLDER: video link, if required by submission form]

dinnerDecider is an iOS app that answers the nightly "what's for dinner?" question from a photo. You photograph your fridge and pantry, confirm what the app found, and get three kinds of output: recipes you can make right now, recipes you are one or two purchases away from, and the shopping list that closes the gap. Every Gemma 4 inference, vision and text, runs on the phone. The demo runs in airplane mode.

---

## 1. Value: the problem we are solving

**The problem is real and it is daily.** Two failure modes drive it:

- **Decision fatigue.** "What's for dinner" is asked in essentially every household, every day, usually at the worst moment: tired, hungry, staring into a fridge. The default escape hatches are takeout and delivery, which cost more and land right when willpower is lowest.
- **Food waste.** Households throw away a meaningful fraction of the groceries they buy, and much of that waste is simply food that was forgotten: the half bag of spinach behind the milk, the can of black beans bought for a recipe that never happened. Waste is a visibility problem before it is a behavior problem.

dinnerDecider attacks both at once. It makes the contents of your kitchen visible as a structured inventory, then turns that inventory into concrete, cookable answers. The "almost there" feature is the food-waste killer: instead of buying a whole new basket of ingredients for a new recipe, you buy 1 or 2 items that unlock what you already own.

**Who it helps:** anyone who cooks at home, with outsized value for busy parents, budget-conscious households, and anyone (like our ADHD teammates) for whom "remember what is in the fridge" is a genuinely hard executive-function task.

**Why on-device matters for this app specifically:**

1. **Privacy.** Fridge photos are surprisingly intimate: medications on the shelf, dietary conditions, alcohol, how much or how little food a family has. None of that should leave the phone. With dinnerDecider, it never does. There is no server, no account, no telemetry.
2. **Offline.** The kitchen is where connectivity is worst and where this app is used. After the one-time model download, dinnerDecider works with zero network: in a basement pantry, in a cabin, in airplane mode.
3. **Cost and latency.** No per-scan API bill means the user can rescan freely (scan after every grocery trip), and there is no round-trip latency stacked on top of inference.
4. **It is honest.** A kitchen inventory app that uploads your kitchen to a cloud API is a data-collection app wearing an apron. Ours is not.

---

## 2. Enablement and Ease of Use: three steps, no manual

The entire user flow is:

1. **Photograph.** Open the app, point the camera at the fridge or pantry, shoot. Multiple photos supported (fridge, freezer, shelves). Photo library import also works.
2. **Confirm.** The app streams recognized items onto a confirmation screen, grouped by category. Tap to rename, swipe to delete, add anything it missed. One tap saves the inventory.
3. **Cook.** Three tabs: "Make now" (recipes using only what you have), "Almost there" (recipes needing at most 2 more items, presented as "Buy 2 cans of black beans and you can make chili"), and "Shopping list" (the aggregated, de-duplicated missing items).

**The confirmation screen is a deliberate design decision, not an apology.** No vision model, cloud or local, reads a packed fridge perfectly. Rather than pretend otherwise, we designed the system so that imperfect recognition costs the user a two-second tap instead of a wrong recipe. The pipeline does the heavy lifting (our measurements are in Section 4), and the human does the last 10 percent at the moment they are most qualified to: looking at a list of their own groceries. This converts recognition errors from failures into micro-interactions, and it means the app feels reliable at accuracy levels far below 100 percent. Correcting "granola bars" to "Kirkland granola bars" is trivial; silently getting a recipe built on a misread is not.

Other enablement details:

- **First-run model download** happens once, in the background, with progress and resume (background URLSession). After that the app never needs a network again. For the judged demo the model files are bundled so the phone can stay in airplane mode from launch.
- **Scanning shows its work.** Per-item results stream in live during a scan, so the user sees progress instead of a spinner.
- **Preferences are one screen:** diet, allergies, cuisine likes. They are folded into every recipe prompt automatically.
- No account, no sign-up, no onboarding quiz. Camera permission is the only ask.

---

## 3. Underlying Model: Gemma 4 E4B, 100 percent on-device

**Model:** Gemma 4 E4B (instruction-tuned), the family's on-device 4B-class model: 4.5B effective parameters (8B total), vision-capable, Apache 2.0 licensed. We use it for both jobs in the app: multimodal grocery identification (image + OCR text in, JSON out) and text-only recipe generation.

**Runtime:** llama.cpp, running the official ggml-org conversion as GGUF. Concretely:

- Language model quantized to **Q4_0**, vision projector (mmproj) at **Q8_0**. Both files are from post-June-4-2026 conversions (this matters; see the engineering notes in Section 7).
- **Context capped at 2048 tokens.** Every prompt in the app is engineered to fit comfortably under this: crops instead of whole scenes, inventory as a comma list instead of prose, chunking when the inventory is huge. This keeps memory bounded and steers well clear of the A18 Pro instability we observed above 4096 context.
- **Grammar-constrained JSON decoding.** We supply llama.cpp with a GBNF grammar derived from our response schemas, so the sampler literally cannot emit anything but valid JSON matching our shape. This eliminates the thinking-channel preamble and the "Sure, here you go:" wrapper problem at the decoder level, and guarantees every response parses. A defensive parser (fence-stripping plus balanced-brace extraction, unit tested) still sits behind it as a second line.

**Hardware:** iPhone 16 Pro Max (A18 Pro, 8GB RAM). Measured on device:

- Decode speed: [PLACEHOLDER: measured tok/s on the demo phone]
- Per-crop identification latency: [PLACEHOLDER: seconds per crop, image encode + decode]
- Peak app memory with model resident: [PLACEHOLDER: measured GB]

The model loads once and stays resident; crop queries run sequentially through a single queue, and camera buffers are released before inference to stay under the roughly 5 to 6GB per-app ceiling on an 8GB phone.

**Why E4B and not something bigger:** the 12B-and-up Gemma 4 sizes do not fit the phone's memory budget with a vision stack alongside them, and the point of this track is on-device. E4B is the largest Gemma 4 that runs comfortably resident on an 8GB iPhone while leaving room for the camera pipeline, and its text side is strong enough that recipe generation needed no tricks at all.

### LoRA fine-tune [PENDING TEAMMATE RESULTS]

In parallel with the app build, a teammate ran a LoRA fine-tune of Gemma 4 E4B on packaged-grocery data:

- **Tooling:** Unsloth FastVisionModel, which officially supports Gemma 4 E4B vision fine-tuning.
- **Recipe:** vision layers frozen (`finetune_vision_layers = False`), training only the text side against grocery identification targets. This is Unsloth's recommended first configuration and the safest export path.
- **Hardware:** NVIDIA DGX Spark (GB10, 128GB unified memory).
- **Deployment path:** merge LoRA, convert with llama.cpp's `convert_hf_to_gguf.py` plus mmproj extraction, quantize, and drop the resulting GGUF into the app. Because the app talks to the model through a single service seam, the fine-tuned GGUF is a file swap, not a code change.
- **Results:** [PLACEHOLDER: smoke-test outcome, training details (image count, steps, time), and before/after accuracy from the teammate. If the end-to-end chain did not complete in time, state that plainly and note the app ships on stock E4B.]

We gated this behind a 1-hour end-to-end smoke test because, to our knowledge, nobody had publicly demonstrated the full Gemma 4 vision LoRA-to-phone chain before this event.

---

## 4. Evidence and Evaluation: why the pipeline looks the way it does

**The naive approach fails, measurably.** Our first baseline was the obvious prompt: send the whole fridge photo and ask "what food items are in this image?" On our test set it scored roughly 40 to 50 percent. That is not a demo-day anecdote; it is consistent with the literature.

We fixed it with two zero-training techniques, both grounded in peer-reviewed work:

1. **Crop, then ask.** Multimodal LLMs collapse on small subjects in large scenes. "MLLMs Know Where to Look" (arXiv 2502.17422, ICLR 2025) documents a 23 to 24 point accuracy drop on small visual subjects, and shows that cropping to the region of interest recovers most of it. A jar of capers occupies maybe 1 percent of a fridge photo's pixels; asking the model about the whole scene throws that jar away at the vision encoder. So we never send the whole scene for identification. We detect candidate items first (Apple Vision saliency plus rectangle detection, with a 3x3 overlapping tile grid as fallback), pad each box by 15 percent, and query Gemma once per crop.
2. **OCR fusion.** For packaged goods, the discriminating signal is usually text: brand, product name, variant. Image-plus-text beats image-only for fine-grained packaged-grocery recognition (Machine Vision and Applications, 2024, doi 10.1007/s00138-024-01549-9), with the largest gains exactly where vision alone fails: look-alike products (the "which granola bar is this" failure mode). We run Apple's on-device `VNRecognizeTextRequest` (accurate mode, roughly 100 to 300ms per crop on A18 Pro) on every crop and inject the recognized text directly into the identification prompt.

Both techniques cost zero training and compose: the crop gives the vision encoder enough pixels to see the item, and the OCR text disambiguates what the pixels cannot.

**Measured results** on our test set of [PLACEHOLDER: N] labeled photos of real home kitchens (methodology in Section 5):

[EVAL TABLE PLACEHOLDER: fill from eval run]

| Configuration | Accuracy on [PLACEHOLDER: N]-photo test set | Notes |
|---|---|---|
| Naive whole-scene prompt (baseline) | [PLACEHOLDER: %] | Single prompt per photo, no cropping, no OCR |
| Crop + OCR fusion pipeline (ours) | [PLACEHOLDER: %] | Per-crop Gemma calls with OCR text in prompt, grammar-constrained JSON |
| Fine-tuned E4B + pipeline | [PLACEHOLDER: % or "pending teammate results"] | LoRA per Section 3, same pipeline |

**Failure cases we observed:** [PLACEHOLDER: 2-3 concrete failure examples from the eval run, e.g. occluded items at the back of the fridge, unpackaged leftovers in containers, produce varieties]. Every one of these lands on the confirmation screen, where fixing it costs the user one tap. That is the point of the design: the eval numbers tell us how often the user has to tap, not whether the app works.

---

## 5. Inputs and Data: provenance

- **Evaluation test set:** original photos of real home kitchens (fridges, freezers, pantry shelves), taken by the team on our own devices during the hackathon, with ground-truth item labels recorded in a CSV. No scraped, licensed-ambiguous, or third-party imagery. [PLACEHOLDER: final photo count and total labeled item count]
- **Fine-tuning data (if the fine-tuned model is reported in Section 3):** Open Food Facts, an openly licensed database of packaged food products with images, covering US retail products. [PLACEHOLDER: confirm exact subset and image count used by the teammate]
- **Model weights:** Gemma 4 E4B is Apache 2.0. The GGUF and mmproj files we run are from ggml-org's official conversions.
- **Everything else on the phone is Apple's own frameworks** (Vision for saliency, rectangles, and OCR; SwiftData for persistence; AVFoundation for capture): no third-party data touches the pipeline.
- **Code:** new public GitHub repo, all code written after kickoff. [PLACEHOLDER: repo URL, same as header]

---

## 6. Architecture overview

```
 Camera / photo library
        |
        v
 Apple Vision saliency + rectangle detection      (fallback: 3x3 overlapping tiles)
        |            crops padded 15%, overlaps merged (IoU > 0.3)
        v
 Per-crop on-device OCR (VNRecognizeTextRequest, accurate mode)
        |
        v
 One Gemma 4 E4B call per crop  <== crop image + OCR text in prompt
        |            grammar-constrained JSON: {name, brand, category, confidence}
        v
 Dedupe (same name + brand across overlapping crops)
        |
        v
 USER CONFIRMATION SCREEN  (rename / delete / add)
        |
        v
 SwiftData inventory  (name, brand, category, quantity, dateAdded, source photo)
        |
        v
 Text-only Gemma 4 E4B calls
   - "Make now" recipes (inventory + diet/allergy/cuisine prefs)
   - "Almost there" recipes (at most 2 missing items each)
   - Shopping list (aggregate + dedupe missing items)
```

The app is SwiftUI, iOS 18+. All model access goes through a single `LLMService` protocol seam, which is what makes the fine-tuned GGUF a drop-in swap and let us build and test the full UI against a mock before the runtime was wired.

---

## 7. Hard-won engineering notes

Things we learned the expensive way, recorded so the next team does not:

- **Use post-June-4-2026 llama.cpp builds and GGUF artifacts for Gemma 4 vision.** Day-0 Gemma 4 vision support landed in llama.cpp on April 2, 2026 (PR #21309), but a vision conversion bug meant artifacts converted before the June 4 fix (PR #24118) have silently broken image input: the model loads, runs, and confidently describes an image it cannot actually see. If your Gemma 4 vision outputs feel like hallucinations, check your artifact dates first.
- **Enable the jinja chat template.** Gemma 4's chat format must be applied via the model's embedded jinja template; llama.cpp's legacy template guessing does not produce the right turn structure, and a wrong template quietly degrades instruction following rather than erroring.
- **Grammar-constrained decoding is the difference between a demo and a product.** Prompt-begging for "ONLY JSON" still leaks thinking-channel preamble and markdown fences often enough to break a pipeline that parses dozens of responses per scan. A GBNF grammar makes invalid output unsamplable. We kept the defensive parser anyway; belt and suspenders.
- **You need the increased-memory-limit entitlement on an 8GB phone.** With the Q4_0 weights plus Q8_0 mmproj resident, the app sits near the default per-app memory ceiling. The `com.apple.developer.kernel.increased-memory-limit` entitlement is what makes the resident-model design viable on the iPhone 16 Pro Max. Also: release camera and image buffers before inference, load the model exactly once, and run crop queries sequentially, never in parallel.
- **Keep context at 2048.** We saw instability on A18 Pro above 4096-token context; every prompt in the app is designed to fit in 2048 with room to spare, which also keeps prefill fast.

---

## 8. Demo script (3 minutes, airplane mode)

0:00 - **Prove it is offline.** Show Control Center: airplane mode on, Wi-Fi off. It stays visible on screen edge for the whole demo.

0:15 - **The problem, one sentence.** "It is 6 PM, you are hungry, and you have no idea what is in your fridge. Watch."

0:25 - **Scan.** Open dinnerDecider, photograph the demo fridge shelf ([PLACEHOLDER: exact staged shelf contents, 8-12 items including one look-alike packaged pair to show off OCR fusion]). Items stream onto the screen live as each crop is identified. Narrate one interesting hit: "it read the label, that is on-device OCR feeding Gemma."

1:15 - **Confirm.** Show the confirmation screen. Deliberately point at one imperfect result, fix it with a tap: "no vision model is perfect in a packed fridge, so we made errors cost two seconds instead of a bad dinner." Save to inventory.

1:45 - **Recipes.** Open "Make now": real recipes from exactly what was scanned. Then the money shot, "Almost there": "buy 2 cans of black beans and you can make chili." Then the shopping list it generated.

2:30 - **The stack, fast.** "Gemma 4 E4B, 4 billion effective parameters, running in llama.cpp on this phone. Crop-then-ask plus OCR fusion took us from [PLACEHOLDER: baseline %] to [PLACEHOLDER: pipeline %] accuracy. Every token you saw generated was computed on this device."

2:50 - **Close.** "Private, offline, free to run. Dinner, decided." Hand the (still airplane-moded) phone to a judge to scan whatever they want.

Contingency: if live capture misbehaves under demo-room lighting, the photo-library import path uses the identical pipeline on pre-shot photos of the same shelf.

---

## Appendix: key references

- Gemma 4 E4B model card and edge documentation: developers.google.com/edge/litert-lm/models/gemma-4
- llama.cpp Gemma 4 vision support: github.com/ggml-org/llama.cpp PR #21309; vision conversion fix (June 4, 2026): PR #24118
- Crop sensitivity: "MLLMs Know Where to Look: Training-free Perception of Small Visual Details with Multimodal LLMs," arXiv 2502.17422, ICLR 2025
- OCR fusion for fine-grained grocery recognition: Machine Vision and Applications, 2024, doi 10.1007/s00138-024-01549-9
- Unsloth Gemma 4 fine-tuning guide: unsloth.ai/docs/models/gemma-4/train
- Open Food Facts: openfoodfacts.org (open database license)
