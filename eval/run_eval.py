#!/usr/bin/env python3
"""
dinnerDecider evaluation harness.

Measures grocery-item recognition accuracy of Gemma 4 E4B on labeled photos of
real fridges/pantries, comparing:
  (a) --mode baseline : naive whole-scene prompt  (the ~40-50% baseline)
  (b) --mode pipeline : crop-then-ask + OCR fusion (Apple Vision saliency/rectangles
                        + VNRecognizeTextRequest, one Gemma call per crop)

It talks to a single persistent llama-server (see server.sh) over the OpenAI-compatible
/v1/chat/completions endpoint with base64 image_url parts. Raw model responses are
cached to eval/cache/<mode>/<photo>.json so reruns are free. Per-run scores are written
to eval/results/<run-label>__<mode>.json, and RESULTS.md is regenerated from every
result file found there (so a later fine-tuned GGUF just adds more rows/columns).

Requires: pillow, requests, and (for --mode pipeline) pyobjc Vision + Quartz on macOS.
Install:  python3 -m pip install pillow requests pillow-heif \
                  pyobjc-framework-Vision pyobjc-framework-Quartz

Examples:
  # after real photos land in eval/testset/photos + labels.csv:
  ./server.sh &                          # start model once
  python3 run_eval.py --mode both        # baseline + pipeline, writes RESULTS.md
  pkill -f 'llama-server.*8090'          # stop model (frees ~5GB)

  # later, score a fine-tuned model as a third column:
  MODEL=/path/to/ft.gguf MMPROJ=/path/to/mmproj.gguf ./server.sh &
  python3 run_eval.py --mode both --run-label finetuned-gguf
"""
from __future__ import annotations

import argparse
import base64
import csv
import difflib
import io
import json
import os
import re
import sys
import tempfile
import time
from pathlib import Path

import requests
from PIL import Image

# HEIC support (iPhone photos) if pillow-heif is present.
try:
    import pillow_heif  # type: ignore
    pillow_heif.register_heif_opener()
    _HEIF_OK = True
except Exception:
    _HEIF_OK = False

HERE = Path(__file__).resolve().parent
DEFAULT_TESTSET = HERE / "testset"
CACHE_DIR = HERE / "cache"
RESULTS_DIR = HERE / "results"
RESULTS_MD = HERE / "RESULTS.md"

CATEGORIES = ["produce", "dairy", "meat", "pantry", "snack",
              "beverage", "condiment", "frozen", "other"]
IMG_EXTS = {".jpg", ".jpeg", ".png", ".heic", ".heif", ".webp"}
MATCH_THRESHOLD = 0.75

# ---------------------------------------------------------------------------
# llama-server client
# ---------------------------------------------------------------------------

class LlamaClient:
    def __init__(self, base_url: str):
        self.base = base_url.rstrip("/")

    def wait_ready(self, timeout: float = 120.0) -> bool:
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                r = requests.get(self.base + "/health", timeout=5)
                if r.status_code == 200 and r.json().get("status") == "ok":
                    return True
            except Exception:
                pass
            time.sleep(1.5)
        return False

    def chat(self, content_parts, response_format, *, temperature=0.2,
             max_tokens=512, timeout=300) -> str:
        payload = {
            "model": "gemma",
            "messages": [{"role": "user", "content": content_parts}],
            "temperature": temperature,
            "max_tokens": max_tokens,
            "response_format": response_format,
        }
        r = requests.post(self.base + "/v1/chat/completions",
                          json=payload, timeout=timeout)
        r.raise_for_status()
        return r.json()["choices"][0]["message"]["content"]


def data_url(img: Image.Image, quality: int = 90) -> str:
    buf = io.BytesIO()
    img.convert("RGB").save(buf, format="JPEG", quality=quality)
    b64 = base64.b64encode(buf.getvalue()).decode("ascii")
    return f"data:image/jpeg;base64,{b64}"


def text_part(s: str) -> dict:
    return {"type": "text", "text": s}


def image_part(img: Image.Image) -> dict:
    return {"type": "image_url", "image_url": {"url": data_url(img)}}


def load_image(path: Path) -> Image.Image:
    return Image.open(path).convert("RGB")


def downscale(img: Image.Image, longest: int = 896) -> Image.Image:
    w, h = img.size
    scale = longest / float(max(w, h))
    if scale < 1.0:
        img = img.resize((max(1, int(w * scale)), max(1, int(h * scale))),
                         Image.LANCZOS)
    return img


# ---------------------------------------------------------------------------
# JSON schemas (constrain output -> no thinking preamble)
# ---------------------------------------------------------------------------

BASELINE_SCHEMA = {
    "type": "json_schema",
    "json_schema": {
        "name": "grocery_items",
        "strict": True,
        "schema": {
            "type": "object",
            "properties": {
                "items": {"type": "array", "items": {"type": "string"}},
            },
            "required": ["items"],
            "additionalProperties": False,
        },
    },
}

ITEM_SCHEMA = {
    "type": "json_schema",
    "json_schema": {
        "name": "grocery_item",
        "strict": True,
        "schema": {
            "type": "object",
            "properties": {
                "name": {"type": "string"},
                "brand": {"type": ["string", "null"]},
                "category": {"type": "string", "enum": CATEGORIES},
                "confidence": {"type": "number"},
            },
            "required": ["name", "brand", "category", "confidence"],
            "additionalProperties": False,
        },
    },
}

BASELINE_PROMPT = (
    "This is a photo of a fridge, pantry, or grocery shelf. List every distinct "
    "food or grocery item you can see. Use the everyday name of each item (for "
    "example 'milk', 'oatmeal', 'ketchup'). Do not list the same item twice. "
    "Respond ONLY with JSON of the form {\"items\": [\"...\", \"...\"]}."
)


def identify_prompt(ocr_text: str) -> str:
    ocr = ocr_text.strip() or "(none)"
    return (
        "You identify a single grocery item in this cropped image. "
        f"Text found on the packaging: {ocr}. "
        "Respond ONLY with JSON: {\"name\": \"...\", \"brand\": \"... or null\", "
        "\"category\": \"produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other\", "
        "\"confidence\": 0-1}. Use the common item name for \"name\" "
        "(for example 'oatmeal', not the brand)."
    )


# ---------------------------------------------------------------------------
# Apple Vision: cropping + OCR (pipeline mode only)
# ---------------------------------------------------------------------------

def _vision_imports():
    import Vision  # noqa
    import Quartz  # noqa
    from Foundation import NSURL  # noqa
    return Vision, Quartz, NSURL


def _handler_for(path: Path):
    Vision, Quartz, NSURL = _vision_imports()
    url = NSURL.fileURLWithPath_(str(path))
    return Vision.VNImageRequestHandler.alloc().initWithURL_options_(url, {})


def _norm_boxes_from_saliency(path: Path, max_objects: int = 12):
    """VNGenerateObjectnessBasedSaliencyImageRequest -> list of normalized
    (x, y, w, h) boxes in Vision coords (origin bottom-left)."""
    Vision, Quartz, NSURL = _vision_imports()
    req = Vision.VNGenerateObjectnessBasedSaliencyImageRequest.alloc().init()
    handler = _handler_for(path)
    ok, err = handler.performRequests_error_([req], None)
    boxes = []
    if not ok:
        return boxes
    for obs in (req.results() or []):
        # obs is a VNSaliencyImageObservation; salientObjects are VNRectangleObservation
        objs = obs.salientObjects() or []
        for o in objs:
            bb = o.boundingBox()
            boxes.append((bb.origin.x, bb.origin.y, bb.size.width, bb.size.height))
    return boxes[:max_objects]


def _norm_boxes_from_rectangles(path: Path, max_objects: int = 12):
    Vision, Quartz, NSURL = _vision_imports()
    req = Vision.VNDetectRectanglesRequest.alloc().init()
    try:
        req.setMaximumObservations_(max_objects)
        req.setMinimumConfidence_(0.3)
        req.setMinimumSize_(0.05)
    except Exception:
        pass
    handler = _handler_for(path)
    ok, err = handler.performRequests_error_([req], None)
    boxes = []
    if not ok:
        return boxes
    for obs in (req.results() or []):
        bb = obs.boundingBox()
        boxes.append((bb.origin.x, bb.origin.y, bb.size.width, bb.size.height))
    return boxes


def _tile_grid(rows: int = 3, cols: int = 3, overlap: float = 0.15):
    """Fallback: overlapping tile grid in normalized (x, y, w, h) bottom-left coords."""
    boxes = []
    tw, th = 1.0 / cols, 1.0 / rows
    for r in range(rows):
        for c in range(cols):
            x = max(0.0, c * tw - overlap * tw)
            y = max(0.0, r * th - overlap * th)
            w = min(1.0 - x, tw * (1 + 2 * overlap))
            h = min(1.0 - y, th * (1 + 2 * overlap))
            boxes.append((x, y, w, h))
    return boxes


def _pad_box(x, y, w, h, pad=0.15):
    nx = max(0.0, x - w * pad)
    ny = max(0.0, y - h * pad)
    nw = min(1.0 - nx, w * (1 + 2 * pad))
    nh = min(1.0 - ny, h * (1 + 2 * pad))
    return (nx, ny, nw, nh)


def _crop_pil(img: Image.Image, box_bl):
    """box_bl is normalized (x,y,w,h) with origin bottom-left (Vision).
    Convert to PIL pixel box (origin top-left)."""
    W, H = img.size
    x, y, w, h = box_bl
    left = int(x * W)
    right = int((x + w) * W)
    # Vision y is from bottom; PIL top = H - (y+h)*H
    top = int((1.0 - (y + h)) * H)
    bottom = int((1.0 - y) * H)
    left, right = max(0, left), min(W, right)
    top, bottom = max(0, top), min(H, bottom)
    if right - left < 8 or bottom - top < 8:
        return None
    return img.crop((left, top, right, bottom))


def get_crop_boxes(path: Path):
    """Return list of padded normalized (x,y,w,h) bottom-left boxes and the source
    of the boxes ('saliency+rect' or 'tile-grid')."""
    boxes = _norm_boxes_from_saliency(path)
    boxes += _norm_boxes_from_rectangles(path)
    # de-dup near-identical boxes
    uniq = []
    for b in boxes:
        if not any(_iou(b, u) > 0.7 for u in uniq):
            uniq.append(b)
    if len(uniq) < 2:
        return [_pad_box(*b) for b in _tile_grid()], "tile-grid"
    return [_pad_box(*b) for b in uniq], "saliency+rect"


def _iou(a, b):
    ax, ay, aw, ah = a
    bx, by, bw, bh = b
    ix = max(ax, bx); iy = max(ay, by)
    ix2 = min(ax + aw, bx + bw); iy2 = min(ay + ah, by + bh)
    iw = max(0.0, ix2 - ix); ih = max(0.0, iy2 - iy)
    inter = iw * ih
    union = aw * ah + bw * bh - inter
    return inter / union if union > 0 else 0.0


def ocr_image(img: Image.Image) -> str:
    """VNRecognizeTextRequest (accurate) on a PIL image via a temp file."""
    Vision, Quartz, NSURL = _vision_imports()
    with tempfile.NamedTemporaryFile(suffix=".png", delete=False) as tf:
        tmp = Path(tf.name)
    try:
        img.convert("RGB").save(tmp, format="PNG")
        req = Vision.VNRecognizeTextRequest.alloc().init()
        try:
            req.setRecognitionLevel_(1)  # 1 == VNRequestTextRecognitionLevelAccurate
            req.setUsesLanguageCorrection_(True)
        except Exception:
            pass
        handler = _handler_for(tmp)
        ok, err = handler.performRequests_error_([req], None)
        if not ok:
            return ""
        lines = []
        for obs in (req.results() or []):
            cand = obs.topCandidates_(1)
            if cand and len(cand):
                lines.append(str(cand[0].string()))
        return " ".join(lines)
    finally:
        try:
            tmp.unlink()
        except Exception:
            pass


# ---------------------------------------------------------------------------
# Prediction (with caching)
# ---------------------------------------------------------------------------

def _cache_path(mode: str, photo: Path) -> Path:
    d = CACHE_DIR / mode
    d.mkdir(parents=True, exist_ok=True)
    return d / (photo.stem + ".json")


def _parse_json(content: str):
    try:
        return json.loads(content)
    except Exception:
        m = re.search(r"\{.*\}|\[.*\]", content, re.DOTALL)
        if m:
            try:
                return json.loads(m.group(0))
            except Exception:
                return None
        return None


def predict_baseline(client, photo: Path, use_cache=True) -> list[str]:
    cp = _cache_path("baseline", photo)
    if use_cache and cp.exists():
        raw = json.loads(cp.read_text())
        content = raw["content"]
    else:
        img = downscale(load_image(photo), 896)
        content = client.chat(
            [image_part(img), text_part(BASELINE_PROMPT)],
            BASELINE_SCHEMA, max_tokens=1024)
        cp.write_text(json.dumps({"content": content}, indent=2))
    parsed = _parse_json(content) or {}
    items = parsed.get("items", []) if isinstance(parsed, dict) else parsed
    return [str(x) for x in items if str(x).strip()]


def predict_pipeline(client, photo: Path, use_cache=True) -> tuple[list[dict], str]:
    cp = _cache_path("pipeline", photo)
    if use_cache and cp.exists():
        raw = json.loads(cp.read_text())
        crops = raw["crops"]
        box_source = raw.get("box_source", "cached")
    else:
        img = load_image(photo)
        boxes, box_source = get_crop_boxes(photo)
        crops = []
        for i, box in enumerate(boxes):
            crop = _crop_pil(img, box)
            if crop is None:
                continue
            ocr = ocr_image(crop)
            content = client.chat(
                [image_part(downscale(crop, 896)), text_part(identify_prompt(ocr))],
                ITEM_SCHEMA, max_tokens=768)
            crops.append({"index": i, "box": list(box), "ocr": ocr,
                          "content": content})
        cp.write_text(json.dumps({"box_source": box_source, "crops": crops},
                                 indent=2))
    preds = []
    for c in crops:
        parsed = _parse_json(c["content"])
        if isinstance(parsed, dict) and parsed.get("name"):
            preds.append(parsed)
    # dedupe by normalized name
    seen = {}
    for p in preds:
        key = normalize(p["name"])
        if key and key not in seen:
            seen[key] = p
    return list(seen.values()), box_source


# ---------------------------------------------------------------------------
# Scoring
# ---------------------------------------------------------------------------

STOPWORDS = {
    "brand", "original", "flavor", "flavored", "the", "a", "of", "with",
    "fresh", "natural", "organic", "classic", "premium", "value",
}


def _singularize(tok: str) -> str:
    if len(tok) > 4 and tok.endswith("ies"):
        return tok[:-3] + "y"
    if len(tok) > 3 and tok.endswith("es"):
        return tok[:-2]
    if len(tok) > 3 and tok.endswith("s") and not tok.endswith("ss"):
        return tok[:-1]
    return tok


def normalize(name: str) -> str:
    s = name.lower()
    s = re.sub(r"[^a-z0-9\s]", " ", s)
    toks = [t for t in s.split() if t and t not in STOPWORDS]
    toks = [_singularize(t) for t in toks]
    return " ".join(toks).strip()


def _ratio(a: str, b: str) -> float:
    if not a or not b:
        return 0.0
    if a == b:
        return 1.0
    # token overlap boosts partial matches like "granola bar" vs "kirkland granola bar"
    r = difflib.SequenceMatcher(None, a, b).ratio()
    ta, tb = set(a.split()), set(b.split())
    if ta and tb:
        jacc = len(ta & tb) / len(ta | tb)
        r = max(r, jacc)
        if ta <= tb or tb <= ta:  # one is subset of the other
            r = max(r, 0.9)
    return r


def score_photo(expected: list[str], predicted: list[str]):
    exp = [normalize(e) for e in expected if normalize(e)]
    pred = [normalize(p) for p in predicted if normalize(p)]
    # greedy one-to-one matching on ratio >= threshold
    pairs = []
    for i, e in enumerate(exp):
        for j, p in enumerate(pred):
            r = _ratio(e, p)
            if r >= MATCH_THRESHOLD:
                pairs.append((r, i, j))
    pairs.sort(reverse=True)
    used_e, used_p = set(), set()
    matched = 0
    for r, i, j in pairs:
        if i in used_e or j in used_p:
            continue
        used_e.add(i); used_p.add(j); matched += 1
    recall = matched / len(exp) if exp else 0.0
    precision = matched / len(pred) if pred else 0.0
    return {"expected": len(exp), "predicted": len(pred), "matched": matched,
            "recall": recall, "precision": precision}


# ---------------------------------------------------------------------------
# Test set loading
# ---------------------------------------------------------------------------

def load_labels(testset: Path) -> dict[str, list[str]]:
    csv_path = testset / "labels.csv"
    if not csv_path.exists():
        raise SystemExit(f"labels.csv not found at {csv_path}")
    labels = {}
    with csv_path.open(newline="") as f:
        reader = csv.DictReader(f)
        for row in reader:
            fn = (row.get("filename") or "").strip()
            if not fn:
                continue
            items = [x.strip() for x in (row.get("items") or "").split(";") if x.strip()]
            labels[fn] = items
    return labels


def find_photo(testset: Path, filename: str) -> Path | None:
    p = testset / "photos" / filename
    if p.exists():
        return p
    # allow bare stem in labels.csv
    for ext in IMG_EXTS:
        cand = testset / "photos" / (Path(filename).stem + ext)
        if cand.exists():
            return cand
    return None


# ---------------------------------------------------------------------------
# Run + report
# ---------------------------------------------------------------------------

def run_mode(client, mode: str, testset: Path, labels: dict, run_label: str,
             use_cache=True, limit=None):
    per_photo = []
    processed = 0
    for filename, expected in labels.items():
        if limit and processed >= limit:
            break
        photo = find_photo(testset, filename)
        if photo is None:
            print(f"  [skip] {filename}: file not found in photos/", file=sys.stderr)
            continue
        processed += 1
        if mode == "baseline":
            predicted = predict_baseline(client, photo, use_cache)
            pred_names = predicted
            extra = {}
        else:
            preds, box_source = predict_pipeline(client, photo, use_cache)
            pred_names = [p["name"] for p in preds]
            extra = {"box_source": box_source}
        sc = score_photo(expected, pred_names)
        sc.update({"filename": filename, "predicted_names": pred_names, **extra})
        per_photo.append(sc)
        print(f"  {filename}: R={sc['recall']:.2f} P={sc['precision']:.2f} "
              f"({sc['matched']}/{sc['expected']} found, {sc['predicted']} predicted)")

    n = len(per_photo)
    mean_recall = sum(x["recall"] for x in per_photo) / n if n else 0.0
    mean_prec = sum(x["precision"] for x in per_photo) / n if n else 0.0
    f1 = (2 * mean_prec * mean_recall / (mean_prec + mean_recall)
          if (mean_prec + mean_recall) else 0.0)
    result = {
        "run_label": run_label, "mode": mode, "photos": n,
        "mean_recall": mean_recall, "mean_precision": mean_prec, "f1": f1,
        "per_photo": per_photo, "timestamp": time.strftime("%Y-%m-%d %H:%M:%S"),
    }
    RESULTS_DIR.mkdir(parents=True, exist_ok=True)
    out = RESULTS_DIR / f"{_slug(run_label)}__{mode}.json"
    out.write_text(json.dumps(result, indent=2))
    print(f"  -> {mode}: mean recall {mean_recall:.3f}, mean precision "
          f"{mean_prec:.3f}, F1 {f1:.3f}  ({out.name})")
    return result


def _slug(s: str) -> str:
    return re.sub(r"[^a-z0-9]+", "-", s.lower()).strip("-")


MODE_LABELS = {
    "baseline": "Baseline (whole-scene prompt)",
    "pipeline": "Pipeline (crop + OCR fusion)",
}


def write_report():
    results = []
    for f in sorted(RESULTS_DIR.glob("*.json")):
        try:
            results.append(json.loads(f.read_text()))
        except Exception:
            pass
    lines = ["# dinnerDecider recognition accuracy", "",
             "Grocery-item recognition of Gemma 4 E4B on labeled fridge/pantry photos.",
             "Recall = fraction of ground-truth items found; Precision = fraction of "
             "predicted items that are correct; F1 = harmonic mean of the two means.",
             "Match rule: normalized fuzzy match (lowercase, brand/filler words stripped, "
             "singular/plural folded, similarity >= 0.75).", ""]
    if not results:
        lines.append("_No results yet. Run `python3 run_eval.py --mode both`._")
        RESULTS_MD.write_text("\n".join(lines) + "\n")
        return
    lines += ["| Run | Mode | Photos | Mean Recall | Mean Precision | F1 |",
              "|---|---|---:|---:|---:|---:|"]
    # order: group by run label, baseline before pipeline
    def sort_key(r):
        return (r["run_label"], 0 if r["mode"] == "baseline" else 1)
    for r in sorted(results, key=sort_key):
        lines.append(
            f"| {r['run_label']} | {MODE_LABELS.get(r['mode'], r['mode'])} "
            f"| {r['photos']} | {r['mean_recall']:.1%} "
            f"| {r['mean_precision']:.1%} | {r['f1']:.1%} |")
    lines += ["", "_Generated " + time.strftime("%Y-%m-%d %H:%M:%S") + "._"]
    RESULTS_MD.write_text("\n".join(lines) + "\n")
    print(f"Wrote {RESULTS_MD}")


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def main():
    ap = argparse.ArgumentParser(description="dinnerDecider eval harness")
    ap.add_argument("--mode", choices=["baseline", "pipeline", "both", "report"],
                    default="both")
    ap.add_argument("--server", default=os.environ.get("EVAL_SERVER",
                                                        "http://127.0.0.1:8090"))
    ap.add_argument("--testset", default=str(DEFAULT_TESTSET),
                    help="dir containing photos/ and labels.csv")
    ap.add_argument("--run-label", default="gemma-e4b-q4",
                    help="row/column label (use a different one for a fine-tuned model)")
    ap.add_argument("--limit", type=int, default=None)
    ap.add_argument("--no-cache", action="store_true",
                    help="ignore cached responses and re-query the model")
    args = ap.parse_args()

    if args.mode == "report":
        write_report()
        return

    testset = Path(args.testset)
    labels = load_labels(testset)
    print(f"Loaded {len(labels)} labeled photos from {testset}")

    client = LlamaClient(args.server)
    if not client.wait_ready(timeout=90):
        raise SystemExit(f"llama-server not reachable at {args.server} "
                         f"(start it with ./server.sh)")

    modes = ["baseline", "pipeline"] if args.mode == "both" else [args.mode]
    for mode in modes:
        print(f"\n=== {mode} ===")
        run_mode(client, mode, testset, labels, args.run_label,
                 use_cache=not args.no_cache, limit=args.limit)

    write_report()


if __name__ == "__main__":
    main()
