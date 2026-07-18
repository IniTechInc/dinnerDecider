#!/usr/bin/env bash
# Finalize a curl-downloaded base model into the HF cache layout.
#
# The `hf` CLI stalled repeatedly in this environment (connects, 0 B/s), so the
# reliable path is a plain curl download of the single 15.99GB safetensors file
# straight into the blob store. This script hash-verifies it, strips the
# .incomplete suffix, and symlinks it into the snapshot dir so
# AutoProcessor / from_pretrained find it. Idempotent.
#
# If the blob is missing, it (re)starts the curl download first.
set -euo pipefail

CACHE="$HOME/.cache/huggingface/hub/models--unsloth--gemma-4-E4B-it"
SHA="cfbd3d2f1cd71bd471c37fe2bf8546d5028d41e5736f64e1ca6c6b8893125503"
BLOB="$CACHE/blobs/$SHA"
URL="https://huggingface.co/unsloth/gemma-4-E4B-it/resolve/main/model.safetensors"
SIZE=15992595884

if [ ! -f "$BLOB" ]; then
  echo "[dl] downloading base weights via curl (single writer) ..."
  curl -sS -L --retry 10 --retry-delay 5 --retry-all-errors -C - "$URL" -o "$BLOB.incomplete"
  mv "$BLOB.incomplete" "$BLOB"
fi

actual_size=$(stat -f%z "$BLOB")
if [ "$actual_size" != "$SIZE" ]; then
  echo "ERROR: size $actual_size != expected $SIZE. Re-download needed." >&2
  exit 1
fi

echo "[verify] sha256 (reads 16GB, ~1-2 min) ..."
got=$(shasum -a 256 "$BLOB" | awk '{print $1}')
if [ "$got" != "$SHA" ]; then
  echo "ERROR: sha256 mismatch. got=$got want=$SHA -> file corrupt, re-download." >&2
  exit 1
fi
echo "[verify] sha256 OK"

SNAP=$(ls -d "$CACHE"/snapshots/*/ | head -1)
ln -sf "../../blobs/$SHA" "$SNAP/model.safetensors"
echo "[done] linked $SNAP/model.safetensors -> blobs/$SHA"
