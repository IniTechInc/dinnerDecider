#!/usr/bin/env bash
#
# Starts one persistent llama-server instance hosting Gemma 4 E4B (vision) for the
# evaluation harness. Load the 4.3GB model ONCE and drive it over HTTP; never shell
# out to llama-mtmd-cli per image (that reloads the model every call).
#
# Usage:
#   ./server.sh                 # foreground (Ctrl-C to stop)
#   ./server.sh &               # background; note the PID it prints
#
# Override the model with env vars to evaluate a different GGUF (e.g. a fine-tune):
#   MODEL=/path/to/finetuned.gguf MMPROJ=/path/to/mmproj.gguf ./server.sh
#
# Stop it later with:  pkill -f 'llama-server.*8090'
set -euo pipefail

SNAP="${SNAP:-$HOME/.cache/huggingface/hub/models--ggml-org--gemma-4-E4B-it-GGUF/snapshots/06f24bb269339b2a19a5167199b81e89ef813c10}"
MODEL="${MODEL:-$SNAP/gemma-4-E4B-it-Q4_0.gguf}"
MMPROJ="${MMPROJ:-$SNAP/mmproj-gemma-4-E4B-it-Q8_0.gguf}"
PORT="${PORT:-8090}"
HOST="${HOST:-127.0.0.1}"
CTX="${CTX:-4096}"
NGL="${NGL:-99}"

if [[ ! -f "$MODEL" ]]; then echo "Model not found: $MODEL" >&2; exit 1; fi
if [[ ! -f "$MMPROJ" ]]; then echo "mmproj not found: $MMPROJ" >&2; exit 1; fi

echo "Starting llama-server on $HOST:$PORT"
echo "  model:  $MODEL"
echo "  mmproj: $MMPROJ"

exec llama-server \
  --model "$MODEL" \
  --mmproj "$MMPROJ" \
  --jinja \
  --ctx-size "$CTX" \
  -ngl "$NGL" \
  --host "$HOST" \
  --port "$PORT" \
  --temp 0.2
