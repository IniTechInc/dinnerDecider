#!/usr/bin/env bash
# Full-chain smoke test: finalize base model -> generate ~11 examples ->
# train 20 steps -> merge -> convert -> quantize Q3_K_S -> verify inference.
# Prints timing/memory along the way. Run after setup.sh.
set -euo pipefail
cd "$(dirname "$0")"
PY=./venv/bin/python

echo "########## 0. finalize base model download ##########"
./finalize_download.sh

echo "########## 1. generate smoke dataset ##########"
"$PY" gen_smoke_data.py

echo "########## 2. train 20 steps ##########"
time "$PY" train.py --data smoke_data/train.jsonl --epochs 3 --lr 2e-4 \
  --rank 16 --max-steps 20 --output out/adapter

echo "########## 3. merge -> convert -> quantize Q3_K_S ##########"
time ./merge_convert.sh out/adapter out

echo "########## 4. verify inference on test image ##########"
./verify.sh

echo "########## SMOKE TEST COMPLETE ##########"
