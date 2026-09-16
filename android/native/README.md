# `android/native` - the llama.cpp payload and the JNI shim

Everything the Android app needs to run Gemma 4 E4B on device, plus the gate that
stops a broken payload from shipping.

| File | What it is |
|---|---|
| `build-native.sh` | Cross-compiles llama.cpp and the shim for `arm64-v8a`, strips, stages the eight `.so` into `../app/src/main/jniLibs/arm64-v8a/`. |
| `jni/dd_llama_jni.cpp` | The JNI shim. One C++ translation unit; the only thing Kotlin calls. |
| `jni/CMakeLists.txt` | Builds `libdinnerdecider_llama.so`. Sets `ANDROID_STL=c++_shared`, same as the llama.cpp build. |
| `verify.sh` | The gate. `:app:verifyNativeLibs` delegates to it. |

## Quick start

```sh
export ANDROID_HOME="$HOME/Library/Android/sdk"
./android/native/build-native.sh      # roughly ten minutes from cold
./android/native/verify.sh            # defaults to ../app/src/main/jniLibs/arm64-v8a
```

`build-native.sh --shim-only` skips the llama.cpp configure and build and rebuilds
only the shim, which is the loop you want while editing `dd_llama_jni.cpp`.

## `jniLibs/` is gitignored, so the build is not optional

`.gitignore` excludes `android/app/src/main/jniLibs/`, so a fresh clone has **no**
native payload. That is deliberate: a 12 MB binary blob per revision does not belong
in git, and the libraries are reproducible from a pinned upstream commit. The
consequences to know about:

- A fresh clone runs in **mock mode**. `LlamaNative` reports `NATIVE_LIBS_MISSING`
  and the app shows the demo badge. It does not crash, and that is tested.
- `:app:verifyNativeLibs` **skips** on an empty `jniLibs` for debug builds and
  **fails** for release ones, so a release can never be cut without the payload.
- Run `build-native.sh` before any release build, and before the
  `NativeAbiSmokeTest` instrumented test, which needs the real libraries on device.

## The one flag that silently deletes vision

`-DLLAMA_BUILD_TOOLS=OFF` on its own **removes `libmtmd.so`**. At the pinned commit
`CMakeLists.txt:253` guards `add_subdirectory(tools)` on
`LLAMA_BUILD_COMMON AND LLAMA_BUILD_TOOLS`, and `add_library(mtmd)` lives at
`tools/mtmd/CMakeLists.txt:15`. The build reports 100% success, the app installs,
and then every scan comes back empty with no error anywhere. `-DLLAMA_BUILD_MTMD=ON`
is the standalone escape hatch (`CMakeLists.txt:266-269`) that keeps mtmd alive
without the CLI binaries, and `-DLLAMA_BUILD_APP=OFF` is the actual fix for the
`llama-app` link error that `TOOLS=OFF` was wrongly credited with.

`build-native.sh` hard-fails if `libmtmd.so` is absent or exports no `mtmd_*`
symbols, and `verify.sh` checks the staged set again. Both checks exist because the
failure is invisible at build time.

## `ANDROID_STL=c++_shared` is required in two places

The NDK default is `c++_static`, which links a private copy of the C++ runtime into
every library. More than one copy in a process is undefined behavior, and this app
ships eight libraries. So:

1. `build-native.sh` passes `-DANDROID_STL=c++_shared` to the llama.cpp configure.
2. `jni/CMakeLists.txt` sets the same for the shim. Passing only `ANDROID_ABI` to
   the shim's configure leaves the STL at the default and produces exactly the
   mismatch the flag exists to prevent.
3. The NDK sysroot's `libc++_shared.so` is packaged as one of the eight.

## The eight libraries

Seven come from the build, one is the shim. Sizes measured on this machine at the
pinned commit, after `llvm-strip --strip-unneeded`.

| Library | Stripped | Why it ships |
|---|---:|---|
| `libllama.so` | 3.15 M | text inference |
| `libmtmd.so` | 1.28 M | **VISION**. Gemma 4 mmproj. |
| `libllama-common.so` | 4.11 M | jinja chat templating |
| `libggml-base.so` | 0.78 M | compute |
| `libggml-cpu.so` | 0.93 M | compute |
| `libggml.so` | 0.06 M | compute |
| `libc++_shared.so` | 1.20 M | required by `ANDROID_STL=c++_shared` |
| `libdinnerdecider_llama.so` | ~0.08 M | the JNI shim |
| **total** | **~11.6 M** | budget is 26 M |

`docs/PORTING_NOTES.md` quotes 9.2 M for `libc++_shared.so`; that is the NDK's
unstripped copy. We strip it like the rest, which is what AGP would do anyway.

`docs/PORTING_NOTES.md` also says "seven `.so`", counting the build payload before
the shim existed. **Eight is the runtime set.** A gate that checks seven passes with
the shim missing, and the app then falls silently to mock mode with every build
check green. `verify.sh` names all eight explicitly for that reason.

## Pinned upstream revision

`1bc7a5af0d14b1fb72f266abbd1237b394187115`. Comfortably past the June 4 2026
vision-conversion fix (PR #24118). Do not float it to master: the model params API
changed shape recently (`use_mmap` became `load_mode`) and the shim is written
against this revision's headers.

## `GGML_CPU_ARM_ARCH`

Set to `armv8.2-a+dotprod+fp16`. Without it the cross-build emits no `-march` at all
and the int8 GEMM kernels are compiled out, on a platform that is already CPU-only.
The value matches the Snapdragon 778G in the available test device: ARMv8.2-A, with
FEAT_DotProd present and FEAT_I8MM absent. Enabling dotprod activates the CPU_REPACK
buffer type, which copies eligible weights into non-evictable anonymous RAM;
IQ3_XXS and Q3_K_S are not eligible, so the shipped quant is unaffected. Set
`use_extra_bufts = false` if anyone ever pins Q4_0.

An ARMv8.6+ device would want i8mm added. That belongs to the post-measurement
retune step, not here.

## Inference is CPU-only, and that inverts the iOS memory model

The libraries contain no Vulkan and no OpenCL backend. On iOS the weights were wired
Metal buffers and every jetsam came from that; here they are `MAP_SHARED|PROT_READ`
on a regular file, wrapped zero-copy by the CPU backend, and therefore evictable.
The shim uses `LLAMA_LOAD_MODE_MMAP` and **never** mlock, which would recreate the
iOS problem on a platform that does not otherwise have it.

The cost is latency. It has **not** been measured: no 8 GB-or-larger arm64 Android
device is available, and the 6 GB Galaxy XCover6 Pro on hand is refused by the
capability check. See `docs/PORTING_NOTES.md`, section *Available test hardware*.
Do not substitute an estimate.
