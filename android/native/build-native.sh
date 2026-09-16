#!/usr/bin/env bash
#
# Cross-compile llama.cpp + the DinnerDecider JNI shim for arm64-v8a and stage
# the eight runtime .so files into android/app/src/main/jniLibs/arm64-v8a.
#
# Plan Step 34. The recipe below is VERBATIM from docs/PORTING_NOTES.md, section
# "Android: llama.cpp cross-compile VERIFIED WORKING". Do not "simplify" it.
#
# THE ONE FLAG THAT SILENTLY BREAKS VISION
# ----------------------------------------
# -DLLAMA_BUILD_TOOLS=OFF on its own produces NO libmtmd.so. At the pinned
# commit, CMakeLists.txt:253 guards add_subdirectory(tools) on
# (LLAMA_BUILD_COMMON AND LLAMA_BUILD_TOOLS), and add_library(mtmd ...) lives at
# tools/mtmd/CMakeLists.txt:15. The build goes to 100%, every other library is
# produced, and vision is simply gone at runtime: no mmproj, no scan, no error.
# Two flags together are the fix, and both are mandatory:
#   -DLLAMA_BUILD_MTMD=ON  the standalone escape hatch at CMakeLists.txt:266-269
#   -DLLAMA_BUILD_APP=OFF  the ACTUAL fix for the llama-app link failure that
#                          -DLLAMA_BUILD_TOOLS=OFF was wrongly credited with
# This script asserts libmtmd.so exists AND exports mtmd_* symbols before it
# will stage anything, because a static flag check is not a behavior check.
#
# Usage:
#   ./android/native/build-native.sh              # build everything, stage 8 .so
#   DD_CLEAN=1 ./android/native/build-native.sh   # wipe build dirs first
#   DD_LLAMA_CPP_SRC=/path/to/llama.cpp ./android/native/build-native.sh
#
set -euo pipefail

# --- Pinned toolchain and source -------------------------------------------
# Every one of these is load-bearing. minSdk 28 in app/build.gradle.kts is the
# other half of ANDROID_PLATFORM=android-28: they must move together.
NDK_VERSION="${DD_NDK_VERSION:-28.2.13676358}"
CMAKE_VERSION="${DD_CMAKE_VERSION:-3.31.6}"
LLAMA_CPP_REPO="${DD_LLAMA_CPP_REPO:-https://github.com/ggml-org/llama.cpp.git}"
# 2026-09-15, comfortably past the June 4 2026 vision-conversion fix (PR #24118).
LLAMA_CPP_COMMIT="${DD_LLAMA_CPP_COMMIT:-1bc7a5af0d14b1fb72f266abbd1237b394187115}"
ANDROID_ABI="arm64-v8a"
ANDROID_PLATFORM="android-28"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

LLAMA_SRC="${DD_LLAMA_CPP_SRC:-$SCRIPT_DIR/llama.cpp}"
LLAMA_BUILD="$SCRIPT_DIR/build-llama"
SHIM_BUILD="$SCRIPT_DIR/build-shim"
STAGE_DIR="$SCRIPT_DIR/build-stage"
JNI_LIBS_DIR="$REPO_ROOT/android/app/src/main/jniLibs/$ANDROID_ABI"

# The eight libraries the app loads at runtime, in dependency order. This list is
# duplicated in android/native/verify.sh and in LlamaNative.kt's load order; all
# three must agree. PORTING_NOTES says "seven" because it counts the build
# payload without the shim. Eight is the runtime set.
EXPECTED_LIBS=(
  libc++_shared.so
  libggml-base.so
  libggml-cpu.so
  libggml.so
  libllama.so
  libllama-common.so
  libmtmd.so
  libdinnerdecider_llama.so
)

log()  { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

# --- Preflight --------------------------------------------------------------
: "${ANDROID_HOME:=${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}}"
export ANDROID_HOME
[ -d "$ANDROID_HOME" ] || die "ANDROID_HOME does not exist: $ANDROID_HOME"

NDK_ROOT="$ANDROID_HOME/ndk/$NDK_VERSION"
[ -d "$NDK_ROOT" ] || die "NDK $NDK_VERSION not installed at $NDK_ROOT
Install it with: sdkmanager 'ndk;$NDK_VERSION'"

TOOLCHAIN_FILE="$NDK_ROOT/build/cmake/android.toolchain.cmake"
[ -f "$TOOLCHAIN_FILE" ] || die "Missing NDK cmake toolchain file: $TOOLCHAIN_FILE"

case "$(uname -s)" in
  Darwin) HOST_TAG="darwin-x86_64" ;;  # NDK r28 ships only the x86_64 build; it runs under Rosetta
  Linux)  HOST_TAG="linux-x86_64" ;;
  *)      die "Unsupported build host: $(uname -s)" ;;
esac
LLVM_BIN="$NDK_ROOT/toolchains/llvm/prebuilt/$HOST_TAG/bin"
[ -d "$LLVM_BIN" ] || die "Missing NDK llvm prebuilt for host tag $HOST_TAG: $LLVM_BIN"

STRIP="$LLVM_BIN/llvm-strip"
NM="$LLVM_BIN/llvm-nm"
READELF="$LLVM_BIN/llvm-readelf"
for tool in "$STRIP" "$NM" "$READELF"; do
  [ -x "$tool" ] || die "Missing NDK tool: $tool"
done

SYSROOT_LIB="$NDK_ROOT/toolchains/llvm/prebuilt/$HOST_TAG/sysroot/usr/lib/aarch64-linux-android"
LIBCXX_SHARED="$SYSROOT_LIB/libc++_shared.so"
# ANDROID_STL=c++_shared is not optional once more than one .so ships: the NDK
# default c++_static links a private copy of the C++ runtime into every library,
# and multiple C++ runtimes in one process is undefined behavior. The price of
# the flag is packaging this file.
[ -f "$LIBCXX_SHARED" ] || die "Missing NDK libc++_shared.so: $LIBCXX_SHARED"

SDK_CMAKE="$ANDROID_HOME/cmake/$CMAKE_VERSION/bin"
if [ -x "$SDK_CMAKE/cmake" ]; then
  export PATH="$SDK_CMAKE:$PATH"
else
  warn "SDK cmake $CMAKE_VERSION not found at $SDK_CMAKE; falling back to cmake on PATH."
fi
command -v cmake >/dev/null 2>&1 || die "cmake not found on PATH"
command -v git >/dev/null 2>&1 || die "git not found on PATH"

JOBS="${DD_JOBS:-$( (getconf _NPROCESSORS_ONLN 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 4) )}"

log "ANDROID_HOME  $ANDROID_HOME"
log "NDK           $NDK_VERSION ($HOST_TAG)"
log "cmake         $(cmake --version | head -1)"
log "llama.cpp src $LLAMA_SRC @ $LLAMA_CPP_COMMIT"
log "jobs          $JOBS"

if [ "${DD_CLEAN:-0}" = "1" ]; then
  log "DD_CLEAN=1: removing build directories"
  rm -rf "$LLAMA_BUILD" "$SHIM_BUILD" "$STAGE_DIR"
fi

# --- 1. llama.cpp source at the pinned commit -------------------------------
if [ ! -d "$LLAMA_SRC/.git" ]; then
  log "Cloning llama.cpp into $LLAMA_SRC"
  git clone --filter=blob:none "$LLAMA_CPP_REPO" "$LLAMA_SRC"
fi
CURRENT_COMMIT="$(git -C "$LLAMA_SRC" rev-parse HEAD 2>/dev/null || echo none)"
if [ "$CURRENT_COMMIT" != "$LLAMA_CPP_COMMIT" ]; then
  log "Checking out $LLAMA_CPP_COMMIT (was $CURRENT_COMMIT)"
  git -C "$LLAMA_SRC" fetch --depth 1 origin "$LLAMA_CPP_COMMIT" 2>/dev/null || git -C "$LLAMA_SRC" fetch origin
  git -C "$LLAMA_SRC" checkout --detach "$LLAMA_CPP_COMMIT"
else
  log "llama.cpp already at the pinned commit"
fi

# --- 2. Configure and build llama.cpp ---------------------------------------
# VERBATIM from docs/PORTING_NOTES.md plus the one perf flag below.
#
# GGML_CPU_ARM_ARCH: the build otherwise emits no -march at all (GGML_NATIVE
# auto-defaults OFF when cross-compiling), so the dotprod int8 GEMM kernels are
# compiled out on a platform that is already CPU-only. armv8.2-a+dotprod+fp16
# matches the Snapdragon 778G exactly: FEAT_DotProd present, FEAT_I8MM absent
# (i8mm is ARMv8.6, first seen in Cortex-A710/X2). Caveat: dotprod activates the
# CPU_REPACK buffer type, which copies ELIGIBLE weights into non-evictable
# anonymous RAM. IQ3_XXS and Q3_K_S are not eligible so the shipped quant is
# unaffected; set use_extra_bufts = false if anyone ever pins Q4_0.
log "Configuring llama.cpp -> $LLAMA_BUILD"
cmake -B "$LLAMA_BUILD" -S "$LLAMA_SRC" \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN_FILE" \
  -DANDROID_ABI="$ANDROID_ABI" \
  -DANDROID_PLATFORM="$ANDROID_PLATFORM" \
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
  -DBUILD_SHARED_LIBS=ON \
  -DGGML_CPU_ARM_ARCH="armv8.2-a+dotprod+fp16"

log "Building llama.cpp"
cmake --build "$LLAMA_BUILD" -j"$JOBS"

# llama.cpp drops shared libraries in build/bin.
LLAMA_BIN="$LLAMA_BUILD/bin"
[ -d "$LLAMA_BIN" ] || die "llama.cpp build produced no bin directory at $LLAMA_BIN"

# --- 3. THE VISION GATE -----------------------------------------------------
# Assert the artifact, then assert its symbols. A flag that was present in the
# configure line proves nothing about what came out of the compiler.
[ -f "$LLAMA_BIN/libmtmd.so" ] || die "libmtmd.so was NOT produced.
This is the failure mode LLAMA_BUILD_TOOLS=OFF causes on its own. Confirm that
BOTH -DLLAMA_BUILD_MTMD=ON and -DLLAMA_BUILD_APP=OFF are in the configure line
above, then rebuild with DD_CLEAN=1."

MTMD_SYMBOLS="$("$NM" --dynamic --defined-only "$LLAMA_BIN/libmtmd.so" 2>/dev/null | grep -c ' mtmd_' || true)"
[ "${MTMD_SYMBOLS:-0}" -gt 0 ] || die "libmtmd.so exports no mtmd_* symbols. Vision would fail at runtime."
log "libmtmd.so exports $MTMD_SYMBOLS mtmd_* symbols"

# --- 4. Configure and build the JNI shim ------------------------------------
# The shim links llama-common (NOT common), whose headers are not installed, so
# the llama.cpp SOURCE tree goes on the include path. Its public surface is C++
# with no extern "C", which is why the shim is C++ and ABI-matched to this NDK.
log "Configuring JNI shim -> $SHIM_BUILD"
cmake -B "$SHIM_BUILD" -S "$SCRIPT_DIR/jni" \
  -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN_FILE" \
  -DANDROID_ABI="$ANDROID_ABI" \
  -DANDROID_PLATFORM="$ANDROID_PLATFORM" \
  -DANDROID_STL=c++_shared \
  -DCMAKE_BUILD_TYPE=Release \
  -DDD_LLAMA_SRC_DIR="$LLAMA_SRC" \
  -DDD_LLAMA_BIN_DIR="$LLAMA_BIN"

log "Building JNI shim"
cmake --build "$SHIM_BUILD" -j"$JOBS"

SHIM_SO="$(find "$SHIM_BUILD" -name 'libdinnerdecider_llama.so' -type f | head -1)"
[ -n "$SHIM_SO" ] || die "Shim build produced no libdinnerdecider_llama.so"

# --- 5. Strip and stage -----------------------------------------------------
# 140 MB unstripped vs 10 MB stripped is a 14x difference. Never ship unstripped.
rm -rf "$STAGE_DIR"
mkdir -p "$STAGE_DIR"

stage() {
  local src="$1" name
  name="$(basename "$src")"
  cp "$src" "$STAGE_DIR/$name"
  chmod u+w "$STAGE_DIR/$name"
  "$STRIP" --strip-unneeded "$STAGE_DIR/$name"
}

for name in libllama.so libmtmd.so libllama-common.so libggml-base.so libggml-cpu.so libggml.so; do
  [ -f "$LLAMA_BIN/$name" ] || die "llama.cpp build is missing $name"
  stage "$LLAMA_BIN/$name"
done
stage "$SHIM_SO"
# libc++_shared.so goes through stage() like everything else. An earlier version
# of this script copied it as-is, on the stated grounds that the NDK sysroot copy
# "comes already stripped". It does not: it carries .symtab and measures 8.81 MB,
# against 1.2 MB stripped. That one file was 46% of a 19.2 MB payload, and the
# size budget is generous enough that nothing failed and nobody noticed.
# Found by verify.sh on its first run, which is the entire argument for the gate.
stage "$LIBCXX_SHARED"

# --- 6. Install into jniLibs and assert the full set ------------------------
mkdir -p "$JNI_LIBS_DIR"
missing=0
for name in "${EXPECTED_LIBS[@]}"; do
  if [ ! -f "$STAGE_DIR/$name" ]; then
    warn "staged payload is missing $name"
    missing=1
  fi
done
[ "$missing" -eq 0 ] || die "Refusing to install an incomplete native payload."

rm -f "$JNI_LIBS_DIR"/*.so
for name in "${EXPECTED_LIBS[@]}"; do
  cp "$STAGE_DIR/$name" "$JNI_LIBS_DIR/$name"
done

log "Installed ${#EXPECTED_LIBS[@]} libraries into $JNI_LIBS_DIR"
total=0
for name in "${EXPECTED_LIBS[@]}"; do
  bytes="$(wc -c < "$JNI_LIBS_DIR/$name" | tr -d ' ')"
  total=$((total + bytes))
  printf '    %-32s %8.2f MB\n' "$name" "$(echo "$bytes" | awk '{printf "%.2f", $1/1048576}')"
done
printf '    %-32s %8.2f MB\n' "TOTAL" "$(echo "$total" | awk '{printf "%.2f", $1/1048576}')"

# --- 7. Hand off to the gate ------------------------------------------------
if [ -x "$SCRIPT_DIR/verify.sh" ]; then
  log "Running verify.sh"
  "$SCRIPT_DIR/verify.sh" "$JNI_LIBS_DIR"
else
  warn "verify.sh not found or not executable; skipping the verification gate."
fi

log "Done."
