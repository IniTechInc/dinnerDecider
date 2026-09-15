# Porting Notes: verified spike results (Sept 14, 2026)

Toolchain and runtime facts VERIFIED by running them on this Mac, not assumed.
Android and Linux workers: read this before planning. It saves you the spikes.

## Staged model weights

Recovered from an old iOS simulator container into `models/` (gitignored):

| File | Size | sha256 |
|---|---|---|
| `gemma-4-E4B-it-UD-IQ3_XXS.gguf` | 3.5G | `dd0b5b2f3ba8b4cbeb35154484fc67729183c8f24f53e43319d806ec9fffc6a7` |
| `mmproj-gemma-4-E4B-it-Q8_0.gguf` | 534M | `197f49a93027f9843772bd24a6a9e0be2a32a788de5a3def330e9c585d86edd1` |

No 16GB re-download is required. Use these for every on-device/on-target test.

## Local toolchain (installed Sept 14, 2026)

- Go 1.27.1 (`/opt/homebrew/bin/go`)
- Android SDK at `~/Library/Android/sdk`; NDK `28.2.13676358` (r28c), cmake `3.31.6`,
  platforms android-34/36/37, build-tools 34/35/36. `ANDROID_HOME` is NOT exported in the
  shell profile: export it yourself.
- JDK 21 (JetBrains Runtime, from Android Studio)
- Docker: Desktop is NOT installed (CLI only). A colima VM provides the daemon instead
  (installed Sept 14, 2026). Server 29.5.2, buildx v0.37.1.
  Start it with `colima start` if `docker info` fails; stop with `colima stop`.
  VERIFIED buildx platforms: `linux/arm64`, `linux/amd64`, `linux/amd64/v2`, `linux/386`,
  so multi-arch images for Pi (arm64) and x86 boxes both build on this Mac.

## Android: llama.cpp cross-compile VERIFIED WORKING

Checkout `1bc7a5af0d14b1fb72f266abbd1237b394187115` (2026-09-15) — comfortably past the
June 4 2026 vision-conversion fix (PR #24118) that CLAUDE.md warns about.

### CORRECTION (Sept 14, 2026): an earlier version of this file was WRONG

It said "set `-DLLAMA_BUILD_TOOLS=OFF` to silence" the `llama-app` link error. **That
advice would have silently removed vision support entirely.** At this commit
`CMakeLists.txt:253` guards `add_subdirectory(tools)` on
`LLAMA_BUILD_COMMON AND LLAMA_BUILD_TOOLS`, and `add_library(mtmd ...)` lives at
`tools/mtmd/CMakeLists.txt:15`. `TOOLS=OFF` therefore produces **no `libmtmd.so`**:
no vision, no Gemma mmproj, no scan. It would have built clean and failed at runtime.

Two flags fix it, both verified by building:
- `-DLLAMA_BUILD_MTMD=ON` — the standalone escape hatch at `CMakeLists.txt:266-269`,
  which exists for exactly this case: the mtmd library without the CLI binaries.
- `-DLLAMA_BUILD_APP=OFF` — `llama-app` (the unified CLI) is added at
  `CMakeLists.txt:257` and defaults ON for a standalone build. It is what failed in the
  original spike (`-lllama-server-impl`, `-lllama-cli-impl`) and what failed again with
  only `MTMD=ON` (`arg.h` / `build-info.h` not found). Turning it off is the actual fix;
  nothing in the app needs it.

### Working configure line (VERIFIED: clean 100% build, all six libraries)

```sh
export ANDROID_HOME="$HOME/Library/Android/sdk"
export PATH="$ANDROID_HOME/cmake/3.31.6/bin:$PATH"
cmake -B build-android \
  -DCMAKE_TOOLCHAIN_FILE="$ANDROID_HOME/ndk/28.2.13676358/build/cmake/android.toolchain.cmake" \
  -DANDROID_ABI=arm64-v8a \
  -DANDROID_PLATFORM=android-28 \
  -DANDROID_STL=c++_shared \
  -DCMAKE_BUILD_TYPE=Release \
  -DGGML_OPENMP=OFF \
  -DLLAMA_CURL=OFF \
  -DLLAMA_BUILD_TESTS=OFF \
  -DLLAMA_BUILD_EXAMPLES=OFF \
  -DLLAMA_BUILD_SERVER=OFF \
  -DLLAMA_BUILD_APP=OFF \
  -DLLAMA_BUILD_TOOLS=OFF \
  -DLLAMA_BUILD_MTMD=ON \
  -DLLAMA_BUILD_COMMON=ON \
  -DBUILD_SHARED_LIBS=ON
cmake --build build-android -j8
```

`ANDROID_STL=c++_shared` is not optional once you ship more than one `.so`. The NDK
default `c++_static` links a private copy of the C++ runtime into each library, and
multiple copies in one process is undefined behavior. Shipping `c++_shared` means you
must also package the NDK's `libc++_shared.so` (see the size table).

### Artifacts (VERIFIED, `ELF 64-bit LSB shared object ARM aarch64`)

| Library | Unstripped | **Stripped** | Needed for |
|---|---:|---:|---|
| `libllama.so` | 38M | **3.3M** | text inference |
| `libmtmd.so` | 19M | **1.3M** | **VISION** (Gemma 4 mmproj). 172 exported `mtmd_*` symbols. |
| `libllama-common.so` | 68M | **4.3M** | jinja chat templating + sampling (see below) |
| `libggml-base.so` | | **0.8M** | compute |
| `libggml-cpu.so` | | **0.9M** | compute |
| `libggml.so` | | **0.06M** | compute |
| **subtotal** | **140M** | **10M** | |
| `libc++_shared.so` (from the NDK sysroot) | | **9.2M** | required by `ANDROID_STL=c++_shared` |
| **total shipped native payload** | | **~19.2M** | |

Strip with
`$ANDROID_HOME/ndk/28.2.13676358/toolchains/llvm/prebuilt/darwin-x86_64/bin/llvm-strip --strip-unneeded`.
140M unstripped to 10M stripped is a 14x difference — do not ship unstripped.

Note the target is named **`llama-common`** (producing `libllama-common.so`), not
`common`. It is built, installed and fully exported under `BUILD_SHARED_LIBS=ON`, so
linking a JNI shim against it for `common_chat_templates_apply` (jinja) and
`common_sampler_*` is viable. Its headers are NOT installed; add the source tree to
your include path. Its public surface is C++ with no `extern "C"`, so the JNI shim must
be C++ and ABI-matched to the NDK that built it.

If you skip `llama-common` and use `llama_chat_apply_template` instead, understand the
cost: `include/llama.h:1215` states outright that it does **not** use a jinja parser.
Gemma 4 falls through to the `gemma-7b-it` scaffolding. Mechanically it works and the
`<__media__>` marker survives, but expect degraded instruction-following. That is a
fallback, not an equivalent.

### 16 KB page alignment: satisfied by default, but assert it

`targetSdk >= 35` requires 16 KB-aligned LOAD segments. NDK r28 already emits
`0x4000` alignment (verified on `libllama.so`). Assert it in a build task rather than
trusting it, since a toolchain change would break it silently:
`llvm-readelf -l <lib>.so | grep LOAD` must show `0x4000`.

### Other gotchas found

- `llama_model_params.use_mmap` and `.use_mlock` **no longer exist** at this commit.
  They were replaced by `enum llama_load_mode load_mode` (`include/llama.h:205-212, 322`).
  `mparams.use_mmap = true` is a compile error. Use `LLAMA_LOAD_MODE_MMAP` (AUTO already
  resolves to mmap, and mlock is never engaged unless asked for). Do not enable mlock: it
  would recreate the iOS wired-memory problem on a platform that does not otherwise have it.
- `mtmd_bitmap_init(nx, ny, data)` takes **tightly-packed 3-channel RGB** and memcpys
  `nx*ny*3` with **no length parameter and no validation** (`mtmd.h:159-161`,
  `mtmd.cpp:161-168`). An Android `Bitmap.getPixels()` gives 4-channel ARGB_8888, which is
  BGRA byte order in memory on little-endian. Passing it straight through is both garbage
  input and an out-of-bounds read. Convert explicitly and assert the buffer length.
- `MTMD_VIDEO` auto-disables (needs `LLAMA_SUBPROCESS`) and HTTPS auto-disables (no OpenSSL
  in the NDK sysroot). Neither matters: still images only, and Kroger OAuth uses the Android
  HTTP stack.
- The build emits no `-march` flag. `GGML_NATIVE` auto-defaults OFF when cross-compiling and
  `GGML_CPU_ARM_ARCH` is empty, so dotprod and i8mm int8 GEMM kernels are compiled out — a
  real decode/prefill slowdown on a platform that is already CPU-only. Consider
  `-DGGML_CPU_ARM_ARCH=armv8.2-a+dotprod+fp16`. Caveat: enabling dotprod activates the
  CPU_REPACK buffer type, which copies eligible weights into non-evictable anonymous RAM.
  IQ3_XXS and Q3_K_S are NOT in the eligible list, so the shipped quant is unaffected — but
  set `use_extra_bufts = false` if anyone pins Q4_0.
- x86_64 (emulator) is UNVERIFIED. On an Apple Silicon host the arm64-v8a emulator image is
  the native one, so this does not block the dev loop. Ship `arm64-v8a` only and let the
  capability check fall back to mock mode on any device without it.

### Android inference is CPU-only, and that inverts the memory model

The libraries above contain no Vulkan and no OpenCL backend. Gemma 4 E4B runs on the
phone's CPU. Consequences:
- On iOS the weights were **wired** Metal buffers (unpageable) and that caused every jetsam.
  On Android the weights are `MAP_SHARED|PROT_READ` on a regular file and the CPU backend
  wraps them zero-copy, so they are clean and **evictable**. The iOS jetsam profile does not
  transfer. Caveat: `MAP_POPULATE` pre-faults all 3.5GB at load, and Android's LMK and
  `Debug.getMemoryInfo()` both count clean file-backed pages in PSS — so the killer sees
  more than "just KV cache plus compute buffers".
- Latency will be materially worse than iOS's ~20 s/call. **Measure it before building any
  scan-UI timing assumptions. Do not ship an estimate.**
- A `-DGGML_VULKAN=ON` backend is the obvious follow-up and is UNVERIFIED.

## Linux/IoT: DO NOT COMPILE llama.cpp

Official prebuilt artifacts cover every target we care about. Release tags are `bNNNNN`
build numbers (note: the GitHub "latest release" API returns a stray `v0.4.1` tag — ignore
it and query the releases list instead).

Per-release Linux assets confirmed present (e.g. `b10970`):

| Target | Asset |
|---|---|
| generic arm64 (Raspberry Pi 5) | `llama-bNNNNN-bin-ubuntu-arm64.tar.gz` |
| Jetson / arm64 + CUDA | `llama-bNNNNN-bin-ubuntu-cuda-13.3-arm64.tar.gz` |
| x86_64 + CUDA | `llama-bNNNNN-bin-ubuntu-cuda-12.8-x64.tar.gz` |
| x86_64 + AMD | `llama-bNNNNN-bin-ubuntu-rocm-10.0-x64.tar.gz` |
| x86_64 + Intel | `llama-bNNNNN-bin-ubuntu-sycl-fp16-x64.tar.gz`, `...-openvino-...` |
| s390x | `llama-bNNNNN-bin-ubuntu-s390x.tar.gz` |

Official multi-arch Docker image `ghcr.io/ggml-org/llama.cpp:server` publishes
`linux/amd64`, `linux/arm64`, `linux/s390x` (verified via `docker manifest inspect`).

Design implication: the Go service should detect CPU arch + accelerator and fetch/pin the
matching official `llama-server` build, then talk to it over localhost HTTP. No cgo, so Go
cross-compilation stays trivial.

## Known design constraint: browser camera access

`getUserMedia` is gated to secure contexts. A Pi serving plain HTTP on a LAN IP
(`http://192.168.x.x:PORT`) CANNOT open the camera. Plan for: a generated self-signed TLS
cert, plus a file-upload fallback so the app is still usable without it.

## Performance expectation (set honestly, then measure)

E4B at IQ3_XXS on a Pi 5 is CPU-only: expect minutes per scan, not the ~20s an A18 Pro
does. Same install on a Jetson or a GPU x86 box is far faster. BENCHMARK and publish real
numbers; do not ship a guess.
