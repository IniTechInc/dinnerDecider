#!/usr/bin/env bash
# Merge LoRA -> full checkpoint -> GGUF (q8_0) -> quantize Q3_K_S -> sha256.
#
# DISK-AWARE. This Mac has a tight free-space budget (~21GB free after the
# 16GB base model is cached). A full bf16/f16 GGUF intermediate (~16GB) does
# NOT fit alongside the 16GB merged HF save, so we convert straight to q8_0
# (~8GB), and we cannot keep base(16) + merged(16) + q8_0(8) = 40GB on disk at
# once. Sequence, with peak-disk annotations (free starts ~21GB):
#   1. merge -> save_pretrained bf16          (+16GB merged;  ~5GB free)
#   2. free the base HF cache                 (-16GB;         ~21GB free)   [--keep-base to skip]
#   3. convert merged -> q8_0 GGUF            (+8GB;          ~13GB free)
#   4. delete merged HF save                  (-16GB;         ~29GB free)
#   5. quantize q8_0 -> Q3_K_S                (+3.6GB;        ~25GB free)
#   6. delete q8_0 intermediate               (-8GB;          ~33GB free)
#
# Step 2 removes the base weights from the HF cache. Re-download before the
# next training run with:
#   hf download unsloth/gemma-4-E4B-it --exclude "*.gguf"
# Set KEEP_BASE=1 to skip step 2 (only safe if you have >=30GB free).
#
# Usage: ./merge_convert.sh <adapter_dir> [out_dir]
set -euo pipefail
cd "$(dirname "$0")"

ADAPTER="${1:?usage: merge_convert.sh <adapter_dir> [out_dir]}"
OUT="${2:-out}"
MERGED="$OUT/merged"
Q8="$OUT/gemma-4-E4B-it-finetuned-q8_0.gguf"
Q3="$OUT/gemma-4-E4B-it-finetuned-Q3_K_S.gguf"
PY=./venv/bin/python
KEEP_BASE="${KEEP_BASE:-0}"
BASE_CACHE="$HOME/.cache/huggingface/hub/models--unsloth--gemma-4-E4B-it"

mkdir -p "$OUT"

# --- fail fast if disk is too tight ---
FREE_GB=$(df -g "$OUT" | awk 'NR==2{print $4}')
echo "[disk] free on target volume: ${FREE_GB}GB"
if [ "$FREE_GB" -lt 20 ]; then
  echo "ERROR: need >=20GB free to run the merge/convert chain, have ${FREE_GB}GB." >&2
  echo "Free space (e.g. remove old GGUFs / DerivedData) and retry." >&2
  exit 1
fi

echo "=== [1/5] merge adapter into base -> $MERGED (bf16, ~16GB) ==="
"$PY" merge.py --adapter "$ADAPTER" --output "$MERGED"

if [ "$KEEP_BASE" != "1" ]; then
  echo "=== [2/5] freeing base HF cache to make room ($BASE_CACHE) ==="
  echo "         (re-download later: hf download unsloth/gemma-4-E4B-it --exclude \"*.gguf\")"
  rm -rf "$BASE_CACHE"
else
  echo "=== [2/5] KEEP_BASE=1 -> leaving base cache in place ==="
fi

echo "=== [3/5] convert merged HF checkpoint -> q8_0 GGUF (~8GB) ==="
"$PY" llama.cpp/convert_hf_to_gguf.py "$MERGED" --outfile "$Q8" --outtype q8_0

echo "=== [4/5] delete merged HF save to reclaim ~16GB ==="
rm -rf "$MERGED"

echo "=== [5/5] quantize q8_0 -> Q3_K_S, sha256, cleanup ==="
llama-quantize "$Q8" "$Q3" Q3_K_S
rm -f "$Q8"
shasum -a 256 "$Q3" | tee "$Q3.sha256"

echo
echo "DONE. Finetuned text weights: $Q3"
echo "Pair with the stock mmproj at:"
echo "  \$HOME/.cache/huggingface/hub/models--ggml-org--gemma-4-E4B-it-GGUF/snapshots/*/mmproj-gemma-4-E4B-it-Q8_0.gguf"
