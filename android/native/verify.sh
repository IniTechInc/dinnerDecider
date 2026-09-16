#!/bin/sh
#
# verify.sh - the native payload gate (plan Step 37).
#
# Usage:
#   ./android/native/verify.sh [JNI_LIBS_DIR]    # defaults to the arm64-v8a staging dir
#
# :app:verifyNativeLibs delegates here, invoking us as
# `/bin/sh verify.sh <jniLibsAbiDir>`. That is why this file is POSIX sh and not
# bash: the shebang is bypassed by that invocation, so a bashism would fail only
# on the machine where /bin/sh is dash, which is to say on CI and not on the
# laptop where it was written. No arrays, no [[ ]], no local.
#
# WHAT THIS GATE IS FOR
# ---------------------
# Every check below corresponds to a failure that is INVISIBLE at build time and
# expensive at runtime. A build can report 100% success and still produce a
# payload that cannot see, cannot load, or crashes the process on a 16 KB-page
# device. The compiler will not tell you. This script is where that gets caught.
#
# It deliberately re-checks things build-native.sh already checked. The staged
# directory is what actually ships, and it is not the directory the build wrote
# to; a gate that trusts the build step cannot catch a bad copy, a stale file
# left behind by an older revision, or a hand-edited jniLibs.

set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ABI=arm64-v8a
LIB_DIR=${1:-"$SCRIPT_DIR/../app/src/main/jniLibs/$ABI"}

# 26 MB, matching maxNativePayloadBytes in app/build.gradle.kts. The real
# stripped payload is ~19.2 MB. Unstripped it is ~140 MB, a 14x difference, so
# this budget's real job is catching a missing strip.
MAX_TOTAL_BYTES=$((26 * 1024 * 1024))

# Android 15 ships 16 KB pages. A library whose LOAD segments are aligned to the
# old 4 KB (0x1000) boundary will not load there AT ALL, and the failure arrives
# as an UnsatisfiedLinkError on a user's phone rather than on this machine. NDK
# r28 defaults to 0x4000; an older NDK or a stray -Wl,-z,max-page-size=4096 does
# not, and nothing else in the build would notice.
MIN_LOAD_ALIGN=16384

# THE EIGHT. Seven from the llama.cpp build, one shim.
#
# docs/PORTING_NOTES.md says "seven", counting the build payload from before the
# shim existed. Eight is the RUNTIME set, and the difference is the whole point
# of naming them explicitly: a gate that just counts *.so and expects seven is
# green with libdinnerdecider_llama.so missing, and the app then falls silently
# to mock mode with every build check passing.
EXPECTED_LIBS='libc++_shared.so
libggml-base.so
libggml-cpu.so
libggml.so
libllama.so
libllama-common.so
libmtmd.so
libdinnerdecider_llama.so'

# The C++ libraries, which must each link the SHARED C++ runtime. See the
# ANDROID_STL section below.
CXX_LIBS='libllama.so
libmtmd.so
libllama-common.so
libdinnerdecider_llama.so'

# The shim's complete exported surface. LlamaNative.kt declares exactly these as
# external funs; a JNI method missing here is an UnsatisfiedLinkError at the
# moment the user takes a photo, not at load time.
EXPECTED_JNI_SYMBOLS='Java_com_philwoolley_dinnerdecider_llm_LlamaNative_nativeInit
Java_com_philwoolley_dinnerdecider_llm_LlamaNative_nativeSystemInfo
Java_com_philwoolley_dinnerdecider_llm_LlamaNative_nativeLoad
Java_com_philwoolley_dinnerdecider_llm_LlamaNative_nativeFree
Java_com_philwoolley_dinnerdecider_llm_LlamaNative_nativeResetSession
Java_com_philwoolley_dinnerdecider_llm_LlamaNative_nativeCancel
Java_com_philwoolley_dinnerdecider_llm_LlamaNative_nativeSupportsVision
Java_com_philwoolley_dinnerdecider_llm_LlamaNative_nativeGenerate'

FAILURES=0

ok()   { printf '  ok    %s\n' "$*"; }
bad()  { printf '  FAIL  %s\n' "$*"; FAILURES=$((FAILURES + 1)); }
info() { printf '%s\n' "$*"; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 2; }

# --- Locate the NDK binutils ------------------------------------------------
# readelf and nm here must be the NDK's LLVM copies. The host's binutils may not
# understand AArch64 at all, and macOS ships neither.
#
# Requiring them is not a portability problem in practice: this gate only runs
# when jniLibs is non-empty, and the only way it got that way is someone running
# build-native.sh on a machine that has the NDK.
: "${ANDROID_HOME:=${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}}"
NDK_VERSION=${DD_NDK_VERSION:-28.2.13676358}
NDK_ROOT=${DD_NDK_ROOT:-"$ANDROID_HOME/ndk/$NDK_VERSION"}

case $(uname -s) in
  Darwin) HOST_TAG=darwin-x86_64 ;;
  Linux)  HOST_TAG=linux-x86_64 ;;
  *)      die "unsupported host: $(uname -s)" ;;
esac

LLVM_BIN="$NDK_ROOT/toolchains/llvm/prebuilt/$HOST_TAG/bin"
READELF="$LLVM_BIN/llvm-readelf"
NM="$LLVM_BIN/llvm-nm"

[ -d "$LIB_DIR" ] || die "no such directory: $LIB_DIR"
[ -x "$READELF" ] || die "missing NDK llvm-readelf: $READELF
Set DD_NDK_ROOT, or install NDK $NDK_VERSION."
[ -x "$NM" ] || die "missing NDK llvm-nm: $NM"

info "verify.sh: $LIB_DIR"
info "NDK $NDK_VERSION ($HOST_TAG)"
info ""

# --- 1. Per-library structural checks ---------------------------------------
TOTAL_BYTES=0

for lib in $EXPECTED_LIBS; do
  path="$LIB_DIR/$lib"
  info "$lib"
  before=$FAILURES

  if [ ! -f "$path" ]; then
    bad "missing from the staged payload"
    continue
  fi

  size=$(wc -c < "$path" | tr -d ' ')
  if [ "$size" -eq 0 ]; then
    bad "is zero bytes"
    continue
  fi
  TOTAL_BYTES=$((TOTAL_BYTES + size))

  header=$("$READELF" -h "$path" 2>/dev/null) || {
    bad "is not readable as ELF"
    continue
  }

  # ELF64 / AArch64. A host-arch .so copied in by accident reaches the device
  # and fails at dlopen with a message that names no cause.
  case $header in
    *ELF64*) ;;
    *) bad "is not ELF64" ;;
  esac
  case $header in
    *AArch64*) ;;
    *) bad "is not AArch64 (wrong ABI staged into $ABI/)" ;;
  esac

  # Stripped. llvm-strip --strip-unneeded removes .symtab and leaves .dynsym.
  # An unstripped payload is ~14x larger and blows the size budget; catching it
  # here names the cause instead of just the symptom.
  if "$READELF" -S "$path" 2>/dev/null | grep -q '\.symtab'; then
    bad "still carries .symtab (not stripped)"
  fi

  # 16 KB LOAD alignment, checked on EVERY LOAD segment rather than the first.
  # The flags column is one field ("RW") or two ("R E"), so the alignment is
  # read as the last field and never by position.
  #
  # awk only selects the values; the hex is converted by POSIX shell arithmetic,
  # which understands a 0x literal. strtonum() would be the obvious awk way to
  # do it and is a GNU extension: absent from the BWK awk on macOS and from
  # mawk, where it yields 0 and would pass every library silently.
  for align in $("$READELF" -l "$path" 2>/dev/null | awk '$1 == "LOAD" { print $NF }'); do
    case $align in
      0x*|[0-9]*) ;;
      *) continue ;;
    esac
    if [ "$((align))" -lt "$MIN_LOAD_ALIGN" ]; then
      bad "has a LOAD segment aligned to $align, below 16 KB: will not load on Android 15"
    fi
  done

  if [ "$FAILURES" -eq "$before" ]; then
    ok "ELF64 AArch64, stripped, 16 KB aligned, $((size / 1024)) KB"
  fi
done

# --- 2. No strays -----------------------------------------------------------
# An extra .so is not harmless: it is packaged, it counts against the budget,
# and if it is a stale copy from an older revision it can be the one the loader
# resolves a symbol against.
for found in "$LIB_DIR"/*.so; do
  [ -e "$found" ] || continue
  name=$(basename "$found")
  case "
$EXPECTED_LIBS
" in
    *"
$name
"*) ;;
    *) bad "unexpected library staged: $name" ;;
  esac
done

# --- 3. ANDROID_STL=c++_shared ----------------------------------------------
# The NDK default is c++_static, which links a PRIVATE copy of the C++ runtime
# into each library. Two runtimes in one process means two sets of type_info,
# two operator new implementations and two exception tables: undefined behavior
# that presents as a crash far from its cause. A c++_shared library records
# libc++_shared.so in DT_NEEDED; a c++_static one does not, which makes this a
# direct check of the flag rather than a proxy for it.
info ""
info "ANDROID_STL"
for lib in $CXX_LIBS; do
  path="$LIB_DIR/$lib"
  [ -f "$path" ] || continue
  if "$READELF" -d "$path" 2>/dev/null | grep -q 'libc++_shared\.so'; then
    ok "$lib links the shared C++ runtime"
  else
    bad "$lib does NOT link libc++_shared.so (built with c++_static?)"
  fi
done

# --- 4. Vision actually survived the build ----------------------------------
# -DLLAMA_BUILD_TOOLS=OFF without -DLLAMA_BUILD_MTMD=ON produces no libmtmd.so
# and no error. The build goes to 100%, the app installs, and every scan comes
# back empty with nothing logged anywhere. Presence is checked above; this
# checks the library is not an empty shell.
info ""
info "vision"
MTMD_PATH="$LIB_DIR/libmtmd.so"
if [ -f "$MTMD_PATH" ]; then
  mtmd_count=$("$NM" -D --defined-only "$MTMD_PATH" 2>/dev/null | grep -c ' mtmd_' || true)
  if [ "$mtmd_count" -lt 1 ]; then
    bad "libmtmd.so exports no mtmd_* symbols: vision is gone"
  else
    ok "libmtmd.so exports $mtmd_count mtmd_* symbols"
  fi
fi

# --- 5. The shim's JNI surface ----------------------------------------------
# -fvisibility=hidden is on, so a JNIEXPORT that got dropped or renamed is
# simply absent here. The failure it prevents is an UnsatisfiedLinkError raised
# the moment the user takes a photo, which is both the worst time to find out
# and the hardest to reproduce.
info ""
info "JNI surface"
SHIM_PATH="$LIB_DIR/libdinnerdecider_llama.so"
if [ -f "$SHIM_PATH" ]; then
  exported=$("$NM" -D --defined-only "$SHIM_PATH" 2>/dev/null | awk '{ print $NF }')
  missing=0
  for sym in $EXPECTED_JNI_SYMBOLS; do
    case "
$exported
" in
      *"
$sym
"*) ;;
      *) bad "shim does not export $sym"; missing=$((missing + 1)) ;;
    esac
  done
  [ "$missing" -eq 0 ] && ok "all 8 JNI entry points exported"
fi

# --- 6. Size budget ---------------------------------------------------------
info ""
info "size"
if [ "$TOTAL_BYTES" -gt "$MAX_TOTAL_BYTES" ]; then
  bad "payload is $((TOTAL_BYTES / 1024 / 1024)) MB, budget is $((MAX_TOTAL_BYTES / 1024 / 1024)) MB"
else
  ok "payload is $((TOTAL_BYTES / 1024 / 1024)) MB of a $((MAX_TOTAL_BYTES / 1024 / 1024)) MB budget"
fi

info ""
if [ "$FAILURES" -gt 0 ]; then
  info "verify.sh: FAILED ($FAILURES problem(s))"
  exit 1
fi
info "verify.sh: OK - 8 libraries, $((TOTAL_BYTES / 1024 / 1024)) MB"
