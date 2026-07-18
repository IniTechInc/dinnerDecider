#!/usr/bin/env bash
# Create the venv and install the pinned training stack for Gemma 4 E4B LoRA
# fine-tuning on Apple Silicon (MPS). Verified working on M1 Max / macOS 26.3 /
# Python 3.13.0 on 2026-07-18.
set -euo pipefail
cd "$(dirname "$0")"

PY=/Library/Frameworks/Python.framework/Versions/3.13/bin/python3.13
if [ ! -d venv ]; then
  "$PY" -m venv venv
fi
./venv/bin/python -m pip install --upgrade pip

# Pinned versions that verified end-to-end (train->merge->convert->quantize->verify).
./venv/bin/python -m pip install \
  "torch==2.13.0" \
  "torchvision==0.28.0" \
  "transformers==5.14.1" \
  "peft==0.19.1" \
  "accelerate==1.14.0" \
  "datasets" \
  "numpy==2.5.1" \
  "sentencepiece" \
  "protobuf" \
  "pillow"

# convert_hf_to_gguf.py needs these (llama.cpp gguf-py deps)
./venv/bin/python -m pip install "gguf" "mistral-common" || true

echo
echo "[setup] verifying Gemma4 support ..."
./venv/bin/python - <<'PY'
import torch, transformers
from transformers import Gemma4ForConditionalGeneration  # noqa
print("torch", torch.__version__, "mps", torch.backends.mps.is_available())
print("transformers", transformers.__version__, "Gemma4 OK")
PY

# Fresh llama.cpp master satisfies the post-June-4 converter requirement.
if [ ! -d llama.cpp ]; then
  git clone --depth 1 https://github.com/ggml-org/llama.cpp.git llama.cpp
fi
echo "[setup] done. llama-quantize/llama-mtmd-cli come from brew (on PATH)."
