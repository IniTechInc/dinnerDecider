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

## Build system

- `project.yml` is the source of truth; `.xcodeproj` is generated (git-ignored). After adding/removing files or settings: `xcodegen generate`.
- Headless builds MUST pass `-skipMacroValidation` (LocalLLMClient ships a Swift macro).
- App target uses Manual signing (profile above); do not pass signing overrides on the xcodebuild command line, they leak onto SPM package targets and fail the build.
- Simulator to use: iPhone 17 Pro (no 16-series sims in Xcode 26.3).
- Test command: `xcodebuild -project DinnerDecider.xcodeproj -scheme DinnerDecider -destination 'platform=iOS Simulator,name=iPhone 17 Pro' -skipMacroValidation build test`

## Model files (never commit; git-ignored)

- Verified-good pair (post-June-4 conversion) in HF cache: `~/.cache/huggingface/hub/models--ggml-org--gemma-4-E4B-it-GGUF/snapshots/06f24bb.../gemma-4-E4B-it-Q4_0.gguf` + `mmproj-gemma-4-E4B-it-Q8_0.gguf`
- Deliver to device: copy both into the app's Documents (Finder file sharing or `xcrun devicectl`). App auto-detects; falls back to mock with an orange demo badge when absent.
- Fine-tuned model: name it `gemma-4-E4B-it-finetuned-*.gguf`, select in Settings > Model. Keep stock mmproj (vision layers frozen in training).

## Known gotchas

- The GGUF's July 2026 chat template needs jinja on CLI tools (`--jinja`); LocalLLMClient handles it natively.
- Do not use LocalLLMClient grammar/responseFormat for repeated calls on a resident model: the grammar sampler never resets, second call returns 0 tokens. Grammar-free + parser channel-stripping is the chosen path.
- Increased Memory Limit capability: NOT settable via API, portal UI only (enabled July 18). Entitlement is in DinnerDecider.entitlements and in profile 68R88K5FQH.
- Eval harness: `eval/` (see eval/README.md). Needs photos in `eval/testset/photos/` + `labels.csv`.
