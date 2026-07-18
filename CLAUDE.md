# DinnerDecider (Gemma 4 Hackathon)

iOS app: photograph your fridge, Gemma 4 E4B identifies items 100% on-device, builds inventory, suggests recipes. Build plan: IMPLEMENTATION_SPEC.md. Fine-tune handoff: MORNING_MERGE.md.

## App coordinates (personal team, NOT Meora Studios)

- Apple Team: Personal, `AXW4GKUTKZ` (woolley.pj@gmail.com)
- asc CLI profile: `Samplomatic` (run `asc auth status` first; switch with `asc auth switch --name Samplomatic`)
- Bundle ID: `com.philwoolley.dinnerdecider` (ASC resource `LY45SDH5J3`)
- App Store Connect app: `6792239464`, name "DinnerDecider - Scan & Cook", SKU `dinnerdecider2026`
- Dev provisioning profile: "DinnerDecider Development" (ASC `68R88K5FQH`, includes Increased Memory Limit)
- Dev cert: "Apple Development: woolley.pj@gmail.com (38P6V2YYLU)" = ASC cert `2AV3MGS2PM`
- Phil's iPhone 16 Pro Max: UDID `00008140-000C218C34D8801C`, ASC device `8226C97VCD`, devicectl id `502D5057-2EAA-5CA3-9E0F-9ADDA99B891D`

## Workflow

- Phil's standing instruction (July 18): commit and push to main along the way as each work chunk lands. No need to ask per-commit.

## Design language (approved by Phil, July 18)

- Palette: terracotta #E4573D (primary), sage #7FA27A (secondary), amber #F2B23C (accent), cream #FFF6E8 / charcoal #1F1D1B surfaces, greige #CFC7BD neutral. Destructive actions stay system red. Tokens live in DinnerDecider/DesignSystem/Theme.swift.
- Typography: DM Serif Display (large headings + recipe names only, never below title2) + DM Sans (body/UI). Bundled OFL fonts in Resources/Fonts.
- App icon: heart-tomato in sage viewfinder brackets on cream (chosen concept 13), dark + tinted variants in the appiconset.

## Build system

- `project.yml` is the source of truth; `.xcodeproj` is generated (git-ignored). After adding/removing files or settings: `xcodegen generate`.
- Headless builds MUST pass `-skipMacroValidation` (LocalLLMClient ships a Swift macro).
- App target uses Manual signing (profile above); do not pass signing overrides on the xcodebuild command line, they leak onto SPM package targets and fail the build.
- Simulator to use: iPhone 17 Pro (no 16-series sims in Xcode 26.3).
- Test command: `xcodebuild -project DinnerDecider.xcodeproj -scheme DinnerDecider -destination 'platform=iOS Simulator,name=iPhone 17 Pro' -skipMacroValidation build test`

## Model files (never commit; git-ignored)

- Memory-optimized weights (preferred on device): `unsloth/gemma-4-E4B-it-GGUF/gemma-4-E4B-it-UD-IQ3_XXS.gguf` (3.46 GiB) with the ggml-org `mmproj-gemma-4-E4B-it-Q8_0.gguf`. Chosen to fix an on-device `vm-pageshortage` OOM: the whole model is GPU-resident (wired Metal), so IQ3_XXS saves ~0.82 GiB wired vs Q4_0, decoded faster than Q3_K_S on Apple Metal (10.3 vs 8.8 tok/s in MacHarness), and emitted the cleanest JSON. `Q3_K_S.gguf` (3.60 GiB, ~0.68 GiB saved) is the fallback. `ModelFileLocator.defaultModelPreference` encodes this order.
- Verified-good larger pair (post-June-4 conversion): `models--ggml-org--gemma-4-E4B-it-GGUF/snapshots/06f24bb.../gemma-4-E4B-it-Q4_0.gguf` + `mmproj-gemma-4-E4B-it-Q8_0.gguf`. Still works but is the wired-memory hog that caused the crash; only use if a smaller quant is unavailable.
- Deliver to device: copy weights + mmproj into the app's Documents (Finder file sharing or `xcrun devicectl`). App auto-detects; falls back to mock with an orange demo badge when absent.
- Fine-tuned model: name it `gemma-4-E4B-it-finetuned-*.gguf`, select in Settings > Model. Keep stock mmproj (vision layers frozen in training).

## Memory / OOM notes

- The crash is a systemwide `vm-pageshortage` jetsam (not app-footprint), driven by WIRED Metal memory: llama.cpp uploads all weights + mmproj + KV cache into GPU buffers. The Increased Memory Limit entitlement does NOT help this. Lever = shrink wired: smaller quant + smaller `context` (1536, in GemmaLLMService).
- LocalLLMClient 0.5.0 does NOT expose `n_gpu_layers`, KV-cache type (q8 KV), flash-attention, or the mmproj `use_gpu` toggle. `LlamaClient.Parameter` only exposes `context` and `batch` (and sampling). `Model.swift` hardcodes full GPU offload + `use_mmap=true`; `Multimodal.swift` hardcodes `use_gpu=true`. Reducing GPU-resident weights (partial offload) or running the mmproj on CPU would each save more wired memory but require forking the package.
- Headless memory self-test: launch with `--llm-selftest` (see SelfTest.swift). Writes `Documents/selftest_result.txt` with peak footprint + available memory. Bundled test image is `DinnerDecider/Resources/selftest.png`.

## Known gotchas

- The GGUF's July 2026 chat template needs jinja on CLI tools (`--jinja`); LocalLLMClient handles it natively.
- Do not use LocalLLMClient grammar/responseFormat for repeated calls on a resident model: the grammar sampler never resets, second call returns 0 tokens. Grammar-free + parser channel-stripping is the chosen path.
- Increased Memory Limit capability: NOT settable via API, portal UI only (enabled July 18). Entitlement is in DinnerDecider.entitlements and in profile 68R88K5FQH.
- Eval harness: `eval/` (see eval/README.md). Needs photos in `eval/testset/photos/` + `labels.csv`.
