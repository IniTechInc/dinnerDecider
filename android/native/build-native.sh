#!/usr/bin/env bash
#
# build-native.sh - cross-compile llama.cpp plus the DinnerDecider JNI shim for arm64-v8a
# and stage the eight runtime .so files into android/app/src/main/jniLibs/arm64-v8a/.
#
# The configure line below is the one recorded as VERIFIED WORKING in
# docs/PORTING_NOTES.md, "Android: llama.cpp cross-compile VERIFIED WORKING".
# Read that section before changing any flag. In particular:
#
#   LLAMA_BUILD_TOOLS=OFF ON ITS OWN DELETES libmtmd.so AND SILENTLY REMOVES VISION.
#   CMakeLists.txt:253 guards add_subdirectory(tools) on
#   LLAMA_BUILD_COMMON AND LLAMA_BUILD_TOOLS, and add_library(mtmd) lives at
#   tools/mtmd/CMakeLists.txt:15. The build succeeds; the scan then fails at runtime
#   with no diagnostic. LLAMA_BUILD_MTMD=ON is the standalone escape hatch at
#   CMakeLists.txt:266-269 and is what keeps libmtmd.so alive.
#
#   LLAMA_BUILD_APP=OFF is the actual fix for the llama-app link error, not TOOLS=OFF.
#
#   ANDROID_STL=c++_shared is mandatory once more than one .so ships, and it obliges
#   packaging the NDK sysroot's libc++_shared.so alongside our libraries. The shim's
#   own CMakeLists.txt sets the same flag; a c++_static shim next to c++_shared
#   libraries is the undefined behavior the flag exists to prevent.
#
# Usage:
#   ./android/native/build-native.sh              # llama.cpp, then the shim, then stage
#   ./android/native/build-native.sh --shim-only  # reuse an existing llama.cpp build
#   ./android/native/build-native.sh --clean      # wipe both build trees first
#
# Environment overrides:
#   ANDROID_HOME          default "$HOME/Library/Android/sdk"
#   DD_NDK_VERSION        default 28.2.13676358
#   DD_CMAKE_VERSION      default 3.31.6
#   DD_NATIVE_WORK_DIR    default "${TMPDIR:-/tmp}/dinnerdecider-native"
#                         checkout and build trees; deliberately outside the repo so
#                         nothing large is ever a candidate for commit
#   LLAMA_SRC             default "$DD_NATIVE_WORK_DIR/llama.cpp"

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
NATIVE_DIR="$REPO_ROOT/android/native"
JNILIBS_DIR="$REPO_ROOT/android/app/src/main/jniLibs/arm64-v8a"

ANDROID_HOME="${ANDROID_HOME:-$HOME/Library/Android/sdk}"
DD_NDK_VERSION="${DD_NDK_VERSION:-28.2.13676358}"
DD_CMAKE_VERSION="${DD_CMAKE_VERSION:-3.31.6}"
DD_NATIVE_WORK_DIR="${DD_NATIVE_WORK_DIR:-${TMPDIR:-/tmp}/dinnerdecider-native}"
LLAMA_SRC="${LLAMA_SRC:-$DD_NATIVE_WORK_DIR/llama.cpp}"

# Pinned upstream revision. Comfortably past the June 4 2026 vision-conversion fix
# (PR #24118) that CLAUDE.md warns about, and the revision the PORTING_NOTES recipe
# was verified against. Do not float this to master.
LLAMA_COMMIT="${LLAMA_COMMIT:-1bc7a5af0d14b1fb72f266abbd1237b394187115}"
LLAMA_REMOTE="${LLAMA_REMOTE:-https://github.com/ggml-org/llama.cpp.git}"

# Perf flag. armv8.2-a+dotprod+fp16 is exactly the feature set of the Snapdragon 778G
# in the available test device: ARMv8.2-A with FEAT_DotProd present and FEAT_I8MM
# absent (i8mm is ARMv8.6, first seen in Cortex-A710/X2). Without it the build emits
# no -march at all and the int8 GEMM kernels are compiled out on a platform that is
# already CPU-only. Enabling dotprod activates the CPU_REPACK buffer type, which copies
# eligible weights into non-evictable anonymous RAM; IQ3_XXS and Q3_K_S are not
# eligible so the shipped quant is unaffected. Revisit only with an ARMv8.6+ device in
# hand; the plan assigns that to Step 67.
GGML_CPU_ARM_ARCH="${GGML_CPU_ARM_ARCH:-armv8.2-a+dotprod+fp16}"

ANDROID_ABI="arm64-v8a"
ANDROID_PLATFORM="android-28"
ANDROID_STL="c++_shared"

NDK_DIR="$ANDROID_HOME/ndk/$DD_NDK_VERSION"
TOOLCHAIN_FILE="$NDK_DIR/build/cmake/android.toolchain.cmake"
LLVM_BIN="$NDK_DIR/toolchains/llvm/prebuilt/darwin-x86_64/bin"
SYSROOT_LIB="$NDK_DIR/toolchains/llvm/prebuilt/darwin-x86_64/sysroot/usr/lib/aarch64-linux-android"
CMAKE_BIN="$ANDROID_HOME/cmake/$DD_CMAKE_VERSION/bin/cmake"
STRIP_BIN="$LLVM_BIN/llvm-strip"
NM_BIN="$LLVM_BIN/llvm-nm"

LLAMA_BUILD_DIR="$DD_NATIVE_WORK_DIR/build-llama"
SHIM_BUILD_DIR="$DD_NATIVE_WORK_DIR/build-shim"

SHIM_ONLY=0
CLEAN=0
for arg in "$@"; do
  case "$arg" in
    --shim-only) SHIM_ONLY=1 ;;
    --clean)     CLEAN=1 ;;
    -h|--help)   sed -n '1,40p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

log() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------- preflight ---

[ -d "$NDK_DIR" ]          || die "NDK $DD_NDK_VERSION not found at $NDK_DIR"
[ -f "$TOOLCHAIN_FILE" ]   || die "NDK toolchain file missing: $TOOLCHAIN_FILE"
[ -x "$CMAKE_BIN" ]        || die "cmake $DD_CMAKE_VERSION not found at $CMAKE_BIN"
[ -x "$STRIP_BIN" ]        || die "llvm-strip not found at $STRIP_BIN"
[ -x "$NM_BIN" ]           || die "llvm-nm not found at $NM_BIN"
[ -f "$SYSROOT_LIB/libc++_shared.so" ] || die "libc++_shared.so missing from the NDK sysroot: $SYSROOT_LIB"

# The NDK toolchain file wants ninja; the SDK cmake package ships one next to cmake.
export PATH="$ANDROID_HOME/cmake/$DD_CMAKE_VERSION/bin:$PATH"

mkdir -p "$DD_NATIVE_WORK_DIR" "$JNILIBS_DIR"

if [ "$CLEAN" = "1" ]; then
  log "cleaning $LLAMA_BUILD_DIR and $SHIM_BUILD_DIR"
  rm -rf "$LLAMA_BUILD_DIR" "$SHIM_BUILD_DIR"
fi

# ----------------------------------------------------------------- sources ---

if [ ! -d "$LLAMA_SRC/.git" ]; then
  log "cloning llama.cpp into $LLAMA_SRC"
  git clone "$LLAMA_REMOTE" "$LLAMA_SRC"
fi

HEAD_SHA="$(git -C "$LLAMA_SRC" rev-parse HEAD)"
if [ "$HEAD_SHA" != "$LLAMA_COMMIT" ]; then
  log "checking out pinned revision $LLAMA_COMMIT (was $HEAD_SHA)"
  git -C "$LLAMA_SRC" fetch --depth 1 origin "$LLAMA_COMMIT" 2>/dev/null || git -C "$LLAMA_SRC" fetch origin
  git -C "$LLAMA_SRC" checkout --detach "$LLAMA_COMMIT"
fi
log "llama.cpp at $(git -C "$LLAMA_SRC" rev-parse --short HEAD)"

# -------------------------------------------------------------- llama.cpp ---

if [ "$SHIM_ONLY" = "0" ]; then
  log "configuring llama.cpp for $ANDROID_ABI / $ANDROID_PLATFORM / STL=$ANDROID_STL"
  "$CMAKE_BIN" -S "$LLAMA_SRC" -B "$LLAMA_BUILD_DIR" -G Ninja \
    -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN_FILE" \
    -DANDROID_ABI="$ANDROID_ABI" \
    -DANDROID_PLATFORM="$ANDROID_PLATFORM" \
    -DANDROID_STL="$ANDROID_STL" \
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
    -DGGML_CPU_ARM_ARCH="$GGML_CPU_ARM_ARCH"

  log "building llama.cpp (this takes roughly ten minutes from cold)"
  "$CMAKE_BIN" --build "$LLAMA_BUILD_DIR" -j "$(sysctl -n hw.ncpu 2>/dev/null || echo 8)"
fi

# The six libraries land in build/bin under BUILD_SHARED_LIBS=ON.
LLAMA_OUT="$LLAMA_BUILD_DIR/bin"
[ -d "$LLAMA_OUT" ] || die "llama.cpp output directory missing: $LLAMA_OUT"

LLAMA_LIBS=(libllama.so libmtmd.so libllama-common.so libggml-base.so libggml-cpu.so libggml.so)
for lib in "${LLAMA_LIBS[@]}"; do
  [ -f "$LLAMA_OUT/$lib" ] || die "$lib was not produced. If it is libmtmd.so, re-read the LLAMA_BUILD_TOOLS warning at the top of this script."
done

# ------------------------------------------------------------------- shim ---

if [ -f "$NATIVE_DIR/jni/CMakeLists.txt" ]; then
  log "configuring the JNI shim"
  "$CMAKE_BIN" -S "$NATIVE_DIR/jni" -B "$SHIM_BUILD_DIR" -G Ninja \
    -DCMAKE_TOOLCHAIN_FILE="$TOOLCHAIN_FILE" \
    -DANDROID_ABI="$ANDROID_ABI" \
    -DANDROID_PLATFORM="$ANDROID_PLATFORM" \
    -DANDROID_STL="$ANDROID_STL" \
    -DCMAKE_BUILD_TYPE=Release \
    -DDD_LLAMA_SRC_DIR="$LLAMA_SRC" \
    -DDD_LLAMA_LIB_DIR="$LLAMA_OUT"

  log "building the JNI shim"
  "$CMAKE_BIN" --build "$SHIM_BUILD_DIR" -j "$(sysctl -n hw.ncpu 2>/dev/null || echo 8)"
  [ -f "$SHIM_BUILD_DIR/libdinnerdecider_llama.so" ] || die "libdinnerdecider_llama.so was not produced"
else
  log "no jni/CMakeLists.txt yet, skipping the shim (Step 35 adds it)"
fi

# ------------------------------------------------------------------ stage ---

stage() { # stage <source path> <basename>
  local src="$1" name="$2"
  cp -f "$src" "$JNILIBS_DIR/$name"
  chmod u+w "$JNILIBS_DIR/$name"
  # 140M unstripped down to 10M stripped is a 14x difference; never ship unstripped.
  "$STRIP_BIN" --strip-unneeded "$JNILIBS_DIR/$name"
}

log "staging into $JNILIBS_DIR"
for lib in "${LLAMA_LIBS[@]}"; do
  stage "$LLAMA_OUT/$lib" "$lib"
done
stage "$SYSROOT_LIB/libc++_shared.so" "libc++_shared.so"
if [ -f "$SHIM_BUILD_DIR/libdinnerdecider_llama.so" ]; then
  stage "$SHIM_BUILD_DIR/libdinnerdecider_llama.so" "libdinnerdecider_llama.so"
fi

# ----------------------------------------------------------------- report ---

echo
log "staged libraries"
printf '  %-32s %10s\n' "LIBRARY" "SIZE"
total=0
for f in "$JNILIBS_DIR"/*.so; do
  sz=$(stat -f%z "$f" 2>/dev/null || stat -c%s "$f")
  total=$((total + sz))
  printf '  %-32s %10s\n' "$(basename "$f")" "$(echo "$sz" | awk '{printf "%.2f M", $1/1048576}')"
done
printf '  %-32s %10s\n' "TOTAL" "$(echo "$total" | awk '{printf "%.2f M", $1/1048576}')"

echo
MTMD_SYMBOLS=$("$NM_BIN" -D --defined-only "$JNILIBS_DIR/libmtmd.so" 2>/dev/null | grep -c ' mtmd_' || true)
log "libmtmd.so exports $MTMD_SYMBOLS mtmd_* symbols"
[ "$MTMD_SYMBOLS" -gt 0 ] || die "libmtmd.so exports no mtmd_* symbols: vision would be silently dead at runtime"

echo
log "done. Run ./android/native/verify.sh to gate the staged set."
