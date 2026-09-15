# Runtime: model files, quant preference, and memory levers

Sources of record: `DinnerDecider/Services/ModelFileLocator.swift`,
`DinnerDecider/Services/ModelDownloadService.swift:9-50`,
`DinnerDecider/Services/LLM/GemmaLLMService.swift:46-85`, `CLAUDE.md`
(Model files / Memory-OOM notes), `DinnerDeciderTests/ModelFileLocatorTests.swift`.

## The pair

The runtime needs **two** files: quantized weights **and** a vision projector
(mmproj). Either one missing means no vision-capable model, and the app falls
back to canned mock data with a visible badge rather than failing
(`ModelFileLocator.isModelPresent`, `AppModel.makeDefaultService`).

### Staged in `models/` right now

| File | Bytes | Size | sha256 |
| --- | --- | --- | --- |
| `gemma-4-E4B-it-UD-IQ3_XXS.gguf` | 3,717,115,008 | 3.4618 GiB (3544.9 MiB) | `dd0b5b2f3ba8b4cbeb35154484fc67729183c8f24f53e43319d806ec9fffc6a7` |
| `mmproj-gemma-4-E4B-it-Q8_0.gguf` | 559,874,816 | 0.5214 GiB (533.9 MiB) | `197f49a93027f9843772bd24a6a9e0be2a32a788de5a3def330e9c585d86edd1` |

Computed with `shasum -a 256 models/*.gguf` in this working tree. These are the
exact bytes the shipped configuration expects; verify against them after any
download or transfer.

Provenance (public, no-auth direct downloads, `ModelDownloadService.swift:26-32`):

- weights: `https://huggingface.co/unsloth/gemma-4-E4B-it-GGUF/resolve/main/gemma-4-E4B-it-UD-IQ3_XXS.gguf`
- mmproj: `https://huggingface.co/ggml-org/gemma-4-E4B-it-GGUF/resolve/main/mmproj-gemma-4-E4B-it-Q8_0.gguf`

The app's own progress UI uses `approxBytes` of **534,000,000** (mmproj) and
**3,460,000,000** (weights) as pre-HEAD estimates — deliberately approximate, not
a verification constant.

## Weights preference order

`ModelFileLocator.defaultModelPreference`, most preferred first:

1. `gemma-4-E4B-it-UD-IQ3_XXS.gguf` — **3.46 GiB**
2. `gemma-4-E4B-it-Q3_K_S.gguf` — **3.60 GiB**

Measured in the macOS harness against this GGUF (`ModelFileLocator.swift:23-28`,
corroborated by `CLAUDE.md`):

- **UD-IQ3_XXS** saves **~0.82 GiB** of wired memory vs Q4_0, emitted the
  cleanest and most compact JSON, and decoded **faster** than Q3_K_S on Apple
  Metal — **10.3 vs 8.8 tok/s**. The usual "IQ quants are slow on Metal" caveat
  did not hold on this hardware. Preferred.
- **Q3_K_S** saves **~0.68 GiB**, also correct. Kept as the fallback.

Smaller quants are preferred over Q4_0 / Q4_K_M *specifically* because the whole
model runs GPU-resident, so a smaller weights file is directly less wired Metal
memory — the thing that caused the on-device jetsam. This is a memory ranking,
not a quality ranking.

`TODO(unresolved): sha256 and exact byte size for gemma-4-E4B-it-Q3_K_S.gguf. It
is not staged in models/ in this working tree (only the two files above are), so
it cannot be hashed. The 3.60 GiB figure comes from ModelFileLocator.swift:28 and
CLAUDE.md, which give no byte count or hash.`

### Known-good larger pair (not preferred)

`gemma-4-E4B-it-Q4_0.gguf` from the post-June-4 `ggml-org` conversion
(snapshot `06f24bb...`) plus the same Q8_0 mmproj. Verified working, but it is
"the wired-memory hog that caused the crash". Only use if no smaller quant is
available. TestFlight follow-up in `testflight-feedback.md` recommends deleting
an orphaned 5.1 GB Q4_0 from the device entirely, because a **manual pin survives
app updates and would jetsam every load**.

`TODO(unresolved): byte size and sha256 for the Q4_0 weights. CLAUDE.md and
testflight-feedback.md quote "5.1GB" and "frees 4.3GB" for it in different places
without reconciling them, and the file is not present here to measure.`

## Selection algorithm (`chooseModelFile`)

```text
eligible = candidates where
    name starts with "gemma-4-E4B-it"
    AND lowercase(name) ends with ".gguf"
    AND lowercase(name) does NOT contain "mmproj"

1. if a user-pinned `preferred` name is in eligible -> return it
2. else return the first entry of defaultModelPreference that is in eligible
3. else return the first eligible candidate (unchanged legacy fallback)
4. else null
```

Details pinned by `ModelFileLocatorTests`:

- The prefix match is **case-sensitive** (`hasPrefix("gemma-4-E4B-it")`), while
  the `.gguf` suffix and the `mmproj` exclusion are **case-insensitive**. Keep
  that asymmetry.
- A user pin always wins, even when it selects the memory-hungry Q4_0
  (`testUserPinnedFileStillWinsOverDefaultPreference`). This is how a fine-tuned
  build is selected from Settings > Model.
- A pin naming a file that is not present is ignored, falling through to steps
  2-3 (`testFallsBackToFirstMatchWhenPreferredMissing`).
- With none of the preferred quants present, the first eligible candidate wins in
  **candidate-list order** — which is directory-enumeration order, so it is not
  deterministic across platforms. Do not depend on it.
- The prefix match means fine-tuned builds are auto-eligible as long as they are
  named `gemma-4-E4B-it-finetuned-*.gguf` (`CLAUDE.md`: keep the stock mmproj,
  since vision layers are frozen in training).

The pin is stored under the key **`selectedModelFileName`**.

## Projector selection (`chooseMmprojFile`)

```text
eligible = candidates where lowercase(name) starts with "mmproj"
                        AND lowercase(name) ends with ".gguf"
1. if "mmproj-gemma-4-E4B-it-Q8_0.gguf" is in eligible -> return it
2. else return the first eligible candidate
3. else null
```

Both the prefix and suffix checks are case-insensitive here (unlike the weights
prefix). There is no user pin for the projector.

## Search order

Per file, in priority order:

1. The app's **documents directory** — so files transferred by the user win.
2. The **app bundle** — for a bundled demo build.

The first directory containing the chosen filename supplies it. `TODO(unresolved):
no search path is specified for a Linux or Android port. The Swift resolves
platform-specific locations only (Documents, Bundle.main.resourceURL); a port must
choose its own equivalents, keeping the "user-supplied location wins over
shipped location" ordering.`

## Runtime parameters (tuned, not defaults)

| Parameter | Value | Why |
| --- | --- | --- |
| `context` | **1024** | KV cache is wired memory. Was 2048 (library default), then 1536, then dropped to 1024 after a field crash at scan start on the IQ3_XXS build — "escalation lever 1". Prompts and replies are short by design. |
| `batch` | **512** | Must be **>= one image's vision-token chunk**. At 256 (builds 5/7) *every image chunk failed to evaluate and scans found nothing*. 512 is the proven value. |
| `temperature` | 0.2 | Extraction task, not a creative one |
| `topK` | 40 | |
| `topP` | 0.95 | |

**Do not lower `batch` to save memory.** It looks like a memory lever and is not:
below the per-image chunk size it silently breaks vision entirely. Commit
`af11e8c` is titled "Fix empty scans on build 5: batch back to 512, image vision
chunks must fit one batch".

## Memory behavior: the wired-memory ceiling

The crash is a **systemwide `vm-pageshortage` jetsam, not an app-footprint
limit**. llama.cpp uploads all weights + mmproj + KV cache into GPU buffers,
which count as **WIRED kernel memory**. The app was killed at roughly **5.1 GB
systemwide wired** while its own footprint was only ~400 MB.

Consequences a port on any memory-constrained device should internalize:

- **The Increased Memory Limit entitlement does not help.** It raises the
  app-footprint ceiling; the kill is about wired memory. (It is enabled anyway,
  and is portal-UI-only — not settable via API.)
- **The only lever is shrinking wired memory**: a smaller quant, a smaller
  context, and not leaking per-request GPU state.
- **Wired memory is why the scan is capped at 4 heavy calls** — see
  `shared/pipeline/scan-pipeline.md`.

### What each lever costs

| Lever | Wired saved | Cost | Status |
| --- | --- | --- | --- |
| Q4_0 -> UD-IQ3_XXS weights | ~0.82 GiB | Lower-precision weights; in practice output got *cleaner* and decode got *faster* (10.3 vs 8.8 tok/s) | **Shipped** |
| Q4_0 -> Q3_K_S weights | ~0.68 GiB | Slower decode than IQ3_XXS on Metal | Fallback |
| context 2048 -> 1536 -> 1024 | Proportional KV cache | Less room for prompt + reply; already tight, do not raise without re-reading the memory notes | **Shipped** |
| mmproj on CPU instead of GPU | ~534 MB | **Vision fails instantly on device.** Tried in builds 5/7, reverted in build 8 | **Reverted — do not retry** |
| batch 512 -> 256 | Smaller compute buffer | **Every image chunk fails to evaluate; scans find nothing** | **Reverted — do not retry** |
| Per-request session reset | Removes per-call creep of cached image chunks | None measured | **Shipped (mandatory)** |
| 4-call scan budget | Bounds worst case | At most 3 crops analyzed per scan | **Shipped** |
| Capture downscale to 2016 px | ~10x less resident pixel memory | Slightly smaller crops | **Shipped** |
| One shared image-processing context | Avoids a new GPU context per crop | None | **Shipped** |
| UD-IQ2_M quant | Not measured | Not measured | Listed as the remaining escalation if all else fails; **never shipped or evaluated** |

The two "do not retry" rows are the most valuable lines in this table. Both look
like obvious memory wins, both were shipped to TestFlight, and both broke
recognition completely while *appearing* to work (empty results, not crashes).

### Levers the iOS wrapper could not reach

LocalLLMClient 0.5.0 exposes only `context`, `batch` and sampling. It does **not**
expose `n_gpu_layers` (partial offload), KV-cache type (q8 KV), flash attention,
or the mmproj `use_gpu` toggle; `use_mmap=true` and full GPU offload are
hardcoded. Partial offload or a CPU-side KV cache would each save more wired
memory than anything above, but required forking the package.

The repo **does** vendor a fork (`Vendor/LocalLLMClient`) — its one surviving
change is adding `resetSession()` (KV clear **plus** prompt-cache wipe; clearing
the KV cache alone desyncs the prompt cache). A Kotlin or Go port binding
llama.cpp directly has all of these knobs available natively and is not bound by
this limitation — but must still implement the per-request reset.

## Fallback when files are absent

If either file is missing, run the mock service and **surface it in the UI** (iOS
shows an orange demo badge). Never silently present canned data as real
recognition. The simulator always forces the mock: its Metal device cannot
allocate the XPC shared-memory buffers the projector needs, so the real model
always crashes at load there.

## Verification

- Headless memory self-test: launch with `--llm-selftest`, which writes peak
  footprint and available memory to `Documents/selftest_result.txt`
  (`SelfTest.swift`; bundled test image `DinnerDecider/Resources/selftest.png`).
  A port should keep an equivalent headless probe — it is how the memory levers
  were measured without a UI in the loop.
- Accuracy harness: `eval/` (see `eval/README.md`), which needs photos in
  `eval/testset/photos/` plus `labels.csv`.
- CLI note: this GGUF's July 2026 chat template needs `--jinja` on llama.cpp CLI
  tools. A port that drives llama.cpp directly must ensure the chat template is
  applied.
