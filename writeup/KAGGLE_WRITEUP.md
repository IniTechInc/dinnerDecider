# dinnerDecider: Point Your Phone at the Fridge, Get Dinner

**Build with Gemma: JustBuild hackathon, On-Device AI with Gemma 4 track**

Team: IniTech (Phil Woolley, Kurt Lehnardt)
Repo: https://github.com/IniTechInc/dinnerDecider (public)
Demo video: [PLACEHOLDER: demo video link, if required by submission form]

dinnerDecider is an iOS app that answers the nightly "what's for dinner?" question from a photo. You photograph your fridge and pantry, confirm what the app found, and get three kinds of output: recipes you can make right now, recipes you are one or two purchases away from, and the shopping list that closes the gap. Every Gemma 4 inference, vision and text, runs on the phone. The demo runs in airplane mode.

---

## 1. Value: the problem we are solving

**The problem is real and it is daily.** Two failure modes drive it:

- **Decision fatigue.** "What's for dinner" is asked in essentially every household, every day, usually at the worst moment: tired, hungry, staring into a fridge. The default escape hatches are takeout and delivery, which cost more and land right when willpower is lowest.
- **Food waste.** Households throw away a meaningful fraction of the groceries they buy, and much of that waste is simply food that was forgotten: the half bag of spinach behind the milk, the can of black beans bought for a recipe that never happened. Waste is a visibility problem before it is a behavior problem.

dinnerDecider attacks both at once. It makes the contents of your kitchen visible as a structured inventory, then turns that inventory into concrete, cookable answers. The "almost there" feature is the food-waste killer: instead of buying a whole new basket of ingredients for a new recipe, you buy 1 or 2 items that unlock what you already own.

**Who it helps:** anyone who cooks at home, with outsized value for busy parents, budget-conscious households, and anyone (like our ADHD teammates) for whom "remember what is in the fridge" is a genuinely hard executive-function task.

**Why on-device matters for this app specifically:**

1. **Privacy.** Fridge photos are surprisingly intimate. Our own test photos contain medications, supplements, syringes, and alcohol sitting next to the food. That is exactly the kind of data that should never leave the phone. With dinnerDecider, it never does. There is no server, no account, no telemetry.
2. **Offline.** The kitchen is where connectivity is worst and where this app is used. After the one-time model download, dinnerDecider works with zero network: in a basement pantry, in a cabin, in airplane mode.
3. **Cost and latency.** No per-scan API bill means the user can rescan freely (scan after every grocery trip), and there is no round-trip latency stacked on top of inference.
4. **It is honest.** A kitchen inventory app that uploads your kitchen to a cloud API is a data-collection app wearing an apron. Ours is not.

---

## 2. Enablement and Ease of Use: three steps, no manual

The entire user flow is:

1. **Photograph.** Open the app, point the camera at the fridge or pantry, shoot. Multiple photos supported (fridge, freezer, shelves). Photo library import also works.
2. **Confirm.** The app streams recognized items onto a confirmation screen, grouped by category. Tap to rename, swipe to delete, add anything it missed. One tap saves the inventory.
3. **Cook.** Three tabs: "Make now" (recipes using only what you have), "Almost there" (recipes needing at most 2 more items, presented as "Buy 2 cans of black beans and you can make chili"), and "Shopping list" (the aggregated, de-duplicated missing items).

**The confirmation screen is a deliberate design decision, not an apology.** No vision model, cloud or local, reads a packed fridge perfectly, and our own eval numbers (Section 4) prove ours does not. Rather than pretend otherwise, we designed the system so that imperfect recognition costs the user a two-second tap instead of a wrong recipe. The pipeline does the heavy lifting, and the human does the last mile at the moment they are most qualified to: looking at a list of their own groceries. This converts recognition errors from failures into micro-interactions, and it means the app feels reliable at accuracy levels far below 100 percent. Correcting "granola bars" to "Kirkland granola bars" is trivial; silently building a recipe on a misread is not.

Other enablement details:

- **In-app model download, no computer needed.** On first run the app downloads the GGUF weights and the vision projector directly to the phone over a background URLSession, with a progress bar, resume after interruption, and post-download integrity verification. This is the same "download the model inside the app" experience as Google's AI Edge Gallery: the user never plugs into a Mac. After that download the app never needs a network again.
- **Scanning shows its work.** Per-item results stream in live during a scan, so the user sees progress instead of a spinner.
- **Preferences are one screen:** diet, allergies, cuisine likes. They are folded into every recipe prompt automatically.
- No account, no sign-up, no onboarding quiz. Camera permission is the only ask.
- **On TestFlight now.** Build 2 is live for internal testers; external testers are pending Apple beta review. The build ships with the in-app downloader, so a tester installs and is running fully on-device with no side-loading.

---

## 3. Underlying Model: Gemma 4 E4B, 100 percent on-device

**Model:** Gemma 4 E4B (instruction-tuned), the family's on-device 4B-class model: 4.5B effective parameters (8B total), vision-capable, Apache 2.0 licensed. We use it for both jobs in the app: multimodal grocery identification (image plus OCR text in, JSON out) and text-only recipe generation.

**Runtime:** llama.cpp, accessed through the LocalLLMClient Swift package (MIT, version 0.5.0), running GGUF artifacts. The shipping configuration on the phone is:

- Language model quantized to **IQ3_XXS** (`gemma-4-E4B-it-UD-IQ3_XXS.gguf`, 3.46GB), vision projector (mmproj) at **Q8_0** (534MB). Both are from post-June-4-2026 conversions (this matters; see the engineering notes in Section 7). We started on Q4_0 and moved to IQ3_XXS deliberately, for memory and speed reasons documented below.
- **Context capped at 1536 tokens.** Every prompt in the app is engineered to fit under this: crops instead of whole scenes, inventory as a comma list instead of prose, chunking when the inventory is huge. This keeps wired GPU memory bounded (again, see Section 7).
- **Prompt-guided JSON plus a defensive parser, not grammar-constrained decoding.** We tried GBNF grammar constraint first. On a resident model doing dozens of calls per scan it fails: LocalLLMClient's grammar sampler does not reset between calls, so the second call returns zero tokens. The shipping path is grammar-free decoding into a hardened parser that strips the model's thinking-channel preamble and markdown fences, then extracts the balanced-brace JSON. That parser is unit-tested (LLMResponseParserTests) so a stray "Sure, here you go:" never breaks a scan.

**Hardware:** iPhone 16 Pro Max (A18 Pro, 8GB RAM). Measured on device with a headless self-test:

- Model load plus one full image identification: **21 seconds total**, cold.
- App footprint with the model resident and running: **824MB** (as reported by the phone's memory API; the GPU-wired cost is discussed in Section 7).

The model loads once and stays resident; crop queries run sequentially through a single queue, and camera buffers are released before inference to stay under the memory ceiling on an 8GB phone.

**Why E4B and not something bigger:** the 12B-and-up Gemma 4 sizes do not fit the phone's memory budget with a vision stack alongside them, and the point of this track is on-device. E4B is the largest Gemma 4 that runs resident on an 8GB iPhone while leaving room for the camera pipeline, and its text side is strong enough that recipe generation needed no tricks at all.

### Fine-tune: in progress at submission

The recognition numbers in Section 4 show a clear ceiling: even the shipped two-pass pipeline recovers only about a third of the items in our hardest real-fridge photos, because a stock 3-bit E4B is weak at fine-grained grocery identification on cluttered scenes. So the night before submission we built a full data-and-training pipeline to close that gap. It is not finished at submission time; if the trained weights are ready we will show them live at the demo. Here is exactly what exists.

**A Claude-orchestrated, human-in-the-loop labeling pipeline.** We turned 7 team-shot kitchen photos into 88 item crops using the app's own cropper and real on-device OCR, then labeled them in stages:

1. The stock model prelabeled every crop.
2. A second model cross-labeled independently. Where the two agreed, the label was auto-verified: **51 of 88** crops.
3. The **37** disagreements were adjudicated by a human. A human then verified the remaining set as well.
4. Final correction rate: **13.6%** (12 of 88 crops corrected). We logged the prelabeler's failure modes (from `labeling_report.json`): confidently naming a single item when handed a whole-scene crop, and inventing a brand from a bare lid (for example, calling a pink cap "Hellmann's Mayonnaise" when only the cap is visible). Those two failure modes are exactly what the training data below is built to punish.

**The training set: 1,288 examples.** 75 verified real crops from the photos above, plus 1,213 synthetic crops composited from Open Food Facts product images (550 US-market source products; ODbL, attribution recorded, provenance preserved per record). Critically, **320 of these are explicit negatives that teach the model to answer "unknown"** rather than hallucinate, which directly targets the bare-lid failure mode. A 13-crop held-out set is reserved for evaluation.

**The training harness.** A local LoRA fine-tune on Apple Silicon: transformers 5.14.1 (Gemma 4 support verified), PEFT LoRA at rank 16 on the text side only, with the vision and audio towers frozen (the harness asserts this at startup). The output converts to a Q3_K_S GGUF that drops into the app as a file swap, keeping the verified stock mmproj since vision is frozen. The full chain is architecture-verified end to end; the actual training run is gated on a 16GB base-model download that was still in flight at submission.

Because the app talks to the model through a single service seam, the fine-tuned GGUF is a drop-in swap when it lands, not a code change.

---

## 4. Evidence and Evaluation: what we actually measured

We built a reproducible eval harness (committed in `eval/`) and ran it on 7 original photos of real home kitchens with 68 hand-labeled ground-truth items. The ground truth is deliberately exhaustive: every identifiable item in genuinely cluttered kitchens, including items half-hidden in door drawers and tucked behind other items. Metrics are recall (fraction of ground-truth items found), precision (fraction of predictions that are correct), and F1. Matching is one-to-one greedy fuzzy matching (lowercase, brand and filler words stripped, singular/plural folded, similarity threshold 0.75) so a lucky over-prediction cannot inflate recall. Per-photo detail is committed in `eval/results/*.json`. Everything below is measured on the shipping IQ3_XXS model with zero fine-tuning.

**The headline: the shipped two-pass pipeline more than doubles recall over naive prompting, on-device, with no training.**

| Configuration | Mean recall | Mean precision | F1 | Notes |
|---|---:|---:|---:|---|
| Naive whole-scene prompt (baseline) | 16.1% | 26.3% | 20.0% | One prompt per photo, no cropping, no OCR |
| Crop + OCR fusion pass | 21.2% | 23.9% | 22.5% | Per-crop Gemma calls with OCR text in the prompt |
| Both passes unioned (as shipped) | **33.5%** | **27.2%** | **30.0%** | Crop pass + whole-image recall pass, confidence-filtered |
| Fine-tuned E4B + pipeline | pending | pending | pending | LoRA per Section 3, same pipeline; shown live at the demo if ready |

Recall goes from 16.1% to 33.5%, a **2.1x improvement**, and precision goes up as well (26.3% to 27.2%) even while finding twice as many items. F1 rises from 20.0% to 30.0%. All of it is zero-training and 100 percent on-device.

Two zero-training techniques do the work, both grounded in peer-reviewed research:

1. **Crop, then ask.** "MLLMs Know Where to Look" (arXiv 2502.17422, ICLR 2025) documents a 23 to 24 point accuracy drop on small visual subjects and shows that cropping to the region of interest recovers most of it. A jar of capers occupies maybe 1 percent of a fridge photo's pixels; asking the model about the whole scene throws that jar away at the vision encoder. So we detect candidate items first (Apple Vision saliency plus rectangle detection, with a tile-grid fallback), pad each box by 15 percent, and query Gemma once per crop.
2. **OCR fusion.** For packaged goods the discriminating signal is usually text: brand, product name, variant. Image-plus-text beats image-only for fine-grained packaged-grocery recognition (Machine Vision and Applications, 2024, doi 10.1007/s00138-024-01549-9), with the largest gains exactly where vision alone fails: look-alike products. We run Apple's on-device `VNRecognizeTextRequest` (accurate mode) on every crop and inject the recognized text directly into the identification prompt.

On top of those, the as-shipped system runs a **second, whole-image recall pass** and unions its results with the crop pass, then applies a confidence filter (drop predictions below 0.15, keep an explicit "unknown") to suppress hallucinations. The two passes are complementary: the crop pass sees small items the whole-scene view throws away, and the whole-image pass catches items that fall between crop boundaries. Unioned, they roughly double what either does alone, which is why the shipped configuration is both passes together.

**Absolute numbers need context, or they read as failure when they are not.** On dense, real-world kitchen scenes a small on-device model misses most items in a single pass. That is a fact about the benchmark's difficulty as much as the model: our ground truth counts every identifiable item in genuinely cluttered home kitchens, including items partially hidden in drawers and behind other food, roughly 68 items across 7 photos. Benchmarks built from staged or synthetic scenes produce far higher absolute numbers for the same model. We chose the harder benchmark on purpose, because it is what a real fridge looks like. This is exactly why the product is built around three things: (a) multi-pass recognition, which more than doubles single-pass recall; (b) a user confirm screen, where fixing a miss costs one tap; and (c) the fine-tune pipeline in Section 3, which targets these failure modes directly.

**Honest limitations, not cherry-picked:**

- **phil_03 scored 0% in every configuration.** It is a dense fridge interior shot through heavy glare, and neither pass recovered a single item. We are reporting it rather than dropping it.
- **Whole-scene give-up.** In baseline mode, 2 of 7 photos returned zero items; the crop and whole-image passes recover most of them.
- **Bare packaging.** A pink cap with no visible label got confidently named as a branded mayonnaise. Our negatives-and-"unknown" training data is built to fix this.
- **Look-alikes.** Butter read as milk, chocolate milk read as pantry chocolate, an electrolyte mix read as moisturizer. These are the fine-grained cases OCR fusion and fine-tuning are meant to disambiguate.

Every one of these lands on the confirmation screen, where fixing it costs the user one tap.

---

## 5. Inputs and Data: provenance

- **Evaluation test set:** 7 original photos of real home kitchens (fridges, pantry shelves), taken by the team on our own devices during the hackathon, with 68 ground-truth item labels recorded in `eval/testset/labels.csv`. No scraped, licensed-ambiguous, or third-party imagery. The photos themselves are kept out of the public repo for privacy; the labels are committed so the eval is reproducible.
- **Fine-tuning data:** 1,213 synthetic training crops composited from **Open Food Facts** product data and images (550 US-market source products), plus 75 verified real crops from the team photos, plus 320 explicit "unknown" negatives. Open Food Facts data is ODbL v1.0 and its images are CC BY-SA; attribution is recorded in `training/dataset/synth/ATTRIBUTION.md`, and every synthetic record preserves its source product URL and barcode.
- **Model weights:** Gemma 4 E4B is Apache 2.0. The GGUF and mmproj files we run are from the unsloth and ggml-org public conversions.
- **Everything else on the phone is Apple's own frameworks** (Vision for saliency, rectangles, and OCR; SwiftData for persistence; AVFoundation for capture): no third-party data touches the pipeline.
- **Code:** public GitHub repo (github.com/IniTechInc/dinnerDecider), all code written after kickoff.

---

## 6. Architecture overview

```
 Camera / photo library
        |
        v
 Apple Vision saliency + rectangle detection      (fallback: overlapping tile grid)
        |            crops padded 15%, overlaps merged
        v
 Per-crop on-device OCR (VNRecognizeTextRequest, accurate mode)
        |
        v
 One Gemma 4 E4B call per crop  <== crop image + OCR text in prompt
        |            prompt-guided JSON, channel-stripping parser: {name, brand, category, confidence}
        v
 Whole-image recall pass (catches items between crops)
        |
        v
 Confidence filter (drop < 0.15, keep "unknown") + dedupe across overlapping crops
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

The app is SwiftUI, iOS 18+. All model access goes through a single `LLMService` protocol seam, which is what makes the fine-tuned GGUF a drop-in swap and let us build and test the full UI against a mock before the runtime was wired. The repo carries 104 unit tests across nine XCTest suites, including dedicated coverage for the memory lifecycle, the response parser, the download service, and the photo-batch and inventory logic.

---

## 7. Hard-won engineering notes

Things we learned the expensive way, recorded so the next team does not:

- **The 8GB memory wall is wired GPU memory, and the entitlement does not save you.** Our first on-device crash was a systemwide `vm-pageshortage` jetsam, not an app-footprint kill. With the Q4_0 weights plus mmproj, llama.cpp uploaded roughly 5.1GB of *wired* Metal memory into GPU buffers on an 8GB phone, while the app's own footprint was only about 400MB. Because it is wired GPU memory rather than app footprint, the `com.apple.developer.kernel.increased-memory-limit` entitlement cannot help. The only lever is shrinking wired memory: we moved to the IQ3_XXS quant (saves about 0.82GB wired versus Q4_0) and dropped context to 1536.
- **IQ3_XXS is also faster than Q3_K_S on Apple Metal.** In our Mac harness IQ3_XXS decoded at 10.3 tok/s versus 8.8 for Q3_K_S, and it emitted cleaner JSON. So the smaller quant was a win on memory, speed, and output quality at once.
- **A second crash taught us to serialize the model lifecycle.** After the memory fix, the app still crashed when the OS fired a memory-pressure warning while an inference was in flight: the pressure handler tried to unload the model out from under the running call. We fixed it with a model lifecycle state machine that defers unloads until the current load or inference completes, covered by its own test suite.
- **Use post-June-4-2026 llama.cpp builds and GGUF artifacts for Gemma 4 vision.** Day-0 Gemma 4 vision support landed in llama.cpp on April 2, 2026 (PR #21309), but a vision conversion bug meant artifacts converted before the June 4 fix (PR #24118) silently break image input: the model loads, runs, and confidently describes an image it cannot actually see. If your Gemma 4 vision outputs feel like hallucinations, check your artifact dates first.
- **Do not use grammar-constrained decoding for repeated calls on a resident model.** Under LocalLLMClient 0.5.0 the grammar sampler does not reset between calls on a resident model, so the second call returns zero tokens. We use grammar-free decoding plus a defensive channel-stripping parser instead. LocalLLMClient applies Gemma 4's jinja chat template natively; if you drive the model with llama.cpp CLI tools directly, you must pass `--jinja` or instruction following quietly degrades.
- **Keep context small and the queue serial.** Context is 1536, every prompt is designed to fit with room to spare, the model loads exactly once, camera and image buffers are released before inference, and crop queries run sequentially, never in parallel.

---

## 8. Demo script (3 minutes, airplane mode)

The model is already downloaded to the phone (via the in-app downloader described in Section 2), so the whole demo runs with the network off.

0:00 - **Prove it is offline.** Show Control Center: airplane mode on, Wi-Fi off. It stays visible on the screen edge for the whole demo.

0:15 - **The problem, one sentence.** "It is 6 PM, you are hungry, and you have no idea what is in your fridge. Watch."

0:25 - **Scan.** Open dinnerDecider, photograph the demo fridge shelf ([PLACEHOLDER: exact staged shelf contents, 8-12 items including one look-alike packaged pair to show off OCR fusion]). Items stream onto the screen live as each crop is identified. Narrate one interesting hit: "it read the label, that is on-device OCR feeding Gemma."

1:15 - **Confirm.** Show the confirmation screen. Deliberately point at one imperfect result, fix it with a tap: "no vision model is perfect in a packed fridge, and ours is not either, so we made errors cost two seconds instead of a bad dinner." Save to inventory.

1:45 - **Recipes.** Open "Make now": real recipes from exactly what was scanned. Then the money shot, "Almost there": "buy 2 cans of black beans and you can make chili." Then the shopping list it generated.

2:30 - **The stack, fast.** "Gemma 4 E4B, 4.5 billion effective parameters, running in llama.cpp on this phone, 100 percent on-device. Two-pass crop-then-ask plus OCR fusion more than doubled recall over the naive whole-scene prompt on our test set, 16 to 34 percent with no training, and the confirm screen absorbs the rest. Every token you saw was computed on this device."

2:50 - **Close.** "Private, offline, free to run. Dinner, decided." Hand the (still airplane-moded) phone to a judge to scan whatever they want.

Contingency: if live capture misbehaves under demo-room lighting, the photo-library import path uses the identical pipeline on pre-shot photos of the same shelf.

---

## Appendix: key references

- Gemma 4 E4B model card and edge documentation: developers.google.com/edge/litert-lm/models/gemma-4
- LocalLLMClient (Swift wrapper over llama.cpp, MIT): github.com/tattn/LocalLLMClient (version 0.5.0)
- llama.cpp Gemma 4 vision support: github.com/ggml-org/llama.cpp PR #21309; vision conversion fix (June 4, 2026): PR #24118
- Crop sensitivity: "MLLMs Know Where to Look: Training-free Perception of Small Visual Details with Multimodal LLMs," arXiv 2502.17422, ICLR 2025
- OCR fusion for fine-grained grocery recognition: Machine Vision and Applications, 2024, doi 10.1007/s00138-024-01549-9
- Fine-tune harness: Hugging Face transformers 5.14.1 + PEFT (LoRA rank 16, text side only, vision frozen)
- Open Food Facts: openfoodfacts.org (Open Database License, ODbL v1.0)
