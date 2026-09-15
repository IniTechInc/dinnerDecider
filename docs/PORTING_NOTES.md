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

Working configure line:

```sh
export ANDROID_HOME="$HOME/Library/Android/sdk"
export PATH="$ANDROID_HOME/cmake/3.31.6/bin:$PATH"
cmake -B build-android \
  -DCMAKE_TOOLCHAIN_FILE="$ANDROID_HOME/ndk/28.2.13676358/build/cmake/android.toolchain.cmake" \
  -DANDROID_ABI=arm64-v8a \
  -DANDROID_PLATFORM=android-28 \
  -DCMAKE_BUILD_TYPE=Release \
  -DGGML_OPENMP=OFF \
  -DLLAMA_CURL=OFF \
  -DLLAMA_BUILD_TESTS=OFF \
  -DLLAMA_BUILD_EXAMPLES=OFF \
  -DLLAMA_BUILD_SERVER=OFF \
  -DBUILD_SHARED_LIBS=ON
cmake --build build-android -j8
```

Produces valid `ELF 64-bit LSB shared object ARM aarch64`:

| Library | Size (unstripped) | Needed for |
|---|---|---|
| `libllama.so` | 38M | text inference |
| `libmtmd.so` | 19M | VISION (Gemma 4 mmproj). `mtmd_bitmap_*` C API confirmed exported. |
| `libggml.so` + `libggml-base.so` + `libggml-cpu.so` | 15M | compute backends |

GOTCHAS found:
- With `LLAMA_BUILD_TOOLS=ON` but SERVER/EXAMPLES OFF, the unified `llama` CLI target fails
  to link (`-lllama-server-impl`, `-lllama-cli-impl` missing). Harmless for JNI — the app
  loads the `.so` files, not a CLI. Set `-DLLAMA_BUILD_TOOLS=OFF` to silence it.
- The `.so` files ship `debug_info` and are 72M combined. STRIP THEM and use ABI splits or
  the APK will be enormous. Do not ship unstripped.
- `MTMD_VIDEO` auto-disables (needs `LLAMA_SUBPROCESS`) and HTTPS auto-disables (no OpenSSL
  in the NDK sysroot). Neither matters: still images only, and Kroger OAuth uses the Android
  HTTP stack, not llama.cpp's.
- x86_64 (emulator) build is UNVERIFIED. Either also build `-DANDROID_ABI=x86_64` for the
  emulator dev loop, or test inference on a physical arm64 device only.

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
