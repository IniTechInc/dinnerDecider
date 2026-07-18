#!/usr/bin/env bash
# Verify the finetuned GGUF loads and answers an image question in llama.cpp,
# using the stock Q8_0 mmproj (vision frozen in training) via llama-mtmd-cli.
#
# Usage: ./verify.sh [gguf] [image]
set -euo pipefail
cd "$(dirname "$0")"

GGUF="${1:-out/gemma-4-E4B-it-finetuned-Q3_K_S.gguf}"
IMAGE="${2:-/private/tmp/claude-501/-Users-philwoolley-Projects-gemma4hackathon/e7d67faa-807c-441f-af53-fba52792188d/scratchpad/test_box.png}"
MMPROJ=$(ls "$HOME"/.cache/huggingface/hub/models--ggml-org--gemma-4-E4B-it-GGUF/snapshots/*/mmproj-gemma-4-E4B-it-Q8_0.gguf | head -1)

PROMPT='You identify a single grocery item from the photo. Text found on the packaging: none. Respond ONLY with JSON, no other words: {"name": "...", "brand": "... or null", "category": "produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other", "confidence": 0-1}'

echo "model : $GGUF"
echo "mmproj: $MMPROJ"
echo "image : $IMAGE"
echo "---"
llama-mtmd-cli \
  -m "$GGUF" \
  --mmproj "$MMPROJ" \
  --image "$IMAGE" \
  -p "$PROMPT" \
  --jinja \
  -n 128 \
  --temp 0.2 --top-k 40 --top-p 0.95
