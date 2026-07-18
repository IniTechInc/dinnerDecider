# dinnerDecider evaluation harness

Measures grocery-item recognition accuracy of **Gemma 4 E4B** on labeled photos of real
fridges/pantries, comparing:

- **baseline** — naive whole-scene prompt ("here's my fridge, what's in it?"), the ~40-50% floor.
- **pipeline** — the crop-then-ask + OCR-fusion approach: Apple Vision saliency/rectangle
  detection crops each item, `VNRecognizeTextRequest` reads the packaging text, then one
  Gemma call per crop identifies it.

Output is an accuracy table (`RESULTS.md`) for the Kaggle writeup. The same harness scores a
future fine-tuned GGUF as an extra row — just point `server.sh` at the new model and pass a
different `--run-label`.

## One-time setup

```bash
python3 -m pip install pillow requests pillow-heif \
    pyobjc-framework-Vision pyobjc-framework-Quartz
```

(llama.cpp b10050 with `llama-server` must already be on PATH, and the Gemma 4 E4B GGUF +
mmproj must be in the Hugging Face cache — see `server.sh` for the exact paths.)

## Build the test set

1. Drop 20-30 photos of real pantry/fridge items into `testset/photos/`
   (`.jpg`, `.png`, `.heic`, `.heif`, `.webp` all work; HEIC is converted automatically).
2. Create `testset/labels.csv` (copy `labels.csv.example`) with two columns:
   - `filename` — the photo file name (bare stem also works, e.g. `fridge_01`).
   - `items` — the ground-truth item names, **semicolon-separated**.

Use everyday item names (`milk`, `oatmeal`, `granola bars`), not brand strings — scoring
strips brand/filler words and folds singular/plural before fuzzy-matching.

## Run it

```bash
# 1. start the model once (loads ~4.3GB; holds ~5GB RAM while running)
./server.sh &

# 2. run both modes; writes results/*.json and RESULTS.md
python3 run_eval.py --mode both

# 3. stop the model when done (frees the RAM)
pkill -f 'llama-server.*8090'
```

Other invocations:

```bash
python3 run_eval.py --mode baseline          # just the baseline
python3 run_eval.py --mode pipeline          # just the pipeline
python3 run_eval.py --mode both --no-cache   # ignore cache, re-query the model
python3 run_eval.py --mode report            # regenerate RESULTS.md from results/
python3 run_eval.py --mode both --limit 5    # first 5 photos (quick smoke test)
```

Score a **fine-tuned** model as a third set of rows:

```bash
MODEL=/path/to/finetuned.gguf MMPROJ=/path/to/mmproj.gguf ./server.sh &
python3 run_eval.py --mode both --run-label finetuned-gguf
pkill -f 'llama-server.*8090'
```

## Caching

Raw model responses are cached to `cache/<mode>/<photo>.json`, so reruns (e.g. to
regenerate the report or tweak scoring) are free and never re-hit the model. Delete a
cache file, or pass `--no-cache`, to force a fresh query.

## Scoring

- **Recall** (per photo): matched / expected ground-truth items.
- **Precision** (per photo): matched / predicted items.
- **Match rule**: normalize both names (lowercase, strip brand/filler words, fold
  singular/plural), then `difflib` similarity ≥ 0.75 (subset/token-overlap also counts,
  so "granola bar" matches "Kirkland granola bar").
- **Aggregate**: mean recall and mean precision across photos; F1 = harmonic mean of those.

## Files

- `run_eval.py` — the harness (baseline + pipeline, scoring, report).
- `server.sh` — starts `llama-server` with the right flags (jinja, ctx 4096, Metal, port 8090).
- `testset/` — `photos/` + `labels.csv`.
- `cache/` — cached raw model responses (safe to delete).
- `results/` — per-run metric JSON (one file per run-label + mode).
- `RESULTS.md` — the generated accuracy table.
