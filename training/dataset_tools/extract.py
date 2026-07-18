#!/usr/bin/env python3
"""
DinnerDecider real-photo dataset extraction kit.

Turns raw fridge/pantry photos dropped into `training/photos_inbox/` into
labeled-ready training crops for fine-tuning Gemma 4 E4B.

For every NEW photo it:
  1. Runs the SAME crop logic as the app / eval harness (Apple Vision
     objectness-saliency + rectangle detection, IoU de-dup, 15% padding, and a
     3x3 overlapping tile-grid fallback when <2 boxes are found).
  2. Downscales each crop to <=896px and saves a PNG to dataset/real/crops/.
  3. OCRs each crop with VNRecognizeTextRequest (accurate), keeping raw text.
  4. Emits 2-3 random LOW-saliency negative crops per photo (regions not covered
     by any detected box).
  5. Pre-labels every crop by calling a local llama-server (started once, on
     port 8091) with the EXACT production prompt from GemmaLLMService.swift
     (crop image + OCR text, json_schema response format).
  6. Appends one JSONL line per crop to dataset/real/crops.jsonl.

It is idempotent: photos already present in crops.jsonl are skipped, so you can
run it repeatedly as photos trickle in:  `python3 extract.py`

The llama-server is started once for the batch and stopped at the end (it holds
~4GB RAM), so the script is safely re-startable.

Requires: pillow, requests, pillow-heif, pyobjc Vision + Quartz (macOS).

Usage:
  python3 extract.py                 # process whatever is new in photos_inbox/
  python3 extract.py --no-prelabel   # skip the model (crops + OCR only)
  python3 extract.py --negatives 3   # negatives per photo (default 3)
"""
from __future__ import annotations

import argparse
import base64
import io
import json
import os
import random
import re
import signal
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import requests
from PIL import Image

# HEIC support (iPhone photos).
try:
    import pillow_heif  # type: ignore
    pillow_heif.register_heif_opener()
    _HEIF_OK = True
except Exception:
    _HEIF_OK = False

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------

HERE = Path(__file__).resolve().parent
TRAINING = HERE.parent                       # training/
INBOX = TRAINING / "photos_inbox"
OUT_DIR = TRAINING / "dataset" / "real"
CROPS_DIR = OUT_DIR / "crops"
JSONL = OUT_DIR / "crops.jsonl"

IMG_EXTS = {".jpg", ".jpeg", ".png", ".heic", ".heif", ".webp"}
CATEGORIES = ["produce", "dairy", "meat", "pantry", "snack",
              "beverage", "condiment", "frozen", "other"]

# ---------------------------------------------------------------------------
# Model / server config (port 8091, NOT the eval harness's 8090)
# ---------------------------------------------------------------------------

HF_HUB = Path.home() / ".cache" / "huggingface" / "hub"


def _find_one(pattern_root: Path, glob: str) -> Path | None:
    matches = sorted(pattern_root.glob(glob))
    return matches[0] if matches else None


MODEL = _find_one(
    HF_HUB / "models--unsloth--gemma-4-E4B-it-GGUF" / "snapshots",
    "*/gemma-4-E4B-it-UD-IQ3_XXS.gguf")
MMPROJ = _find_one(
    HF_HUB / "models--ggml-org--gemma-4-E4B-it-GGUF" / "snapshots",
    "*/mmproj-gemma-4-E4B-it-Q8_0.gguf")

SERVER_HOST = "127.0.0.1"
SERVER_PORT = 8091
SERVER_URL = f"http://{SERVER_HOST}:{SERVER_PORT}"

# ---------------------------------------------------------------------------
# JSON schema for the pre-label (constrains output -> no thinking preamble)
# ---------------------------------------------------------------------------

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


def production_prompt(ocr_text: str) -> str:
    """The EXACT prompt built by GemmaLLMService.identifyItem (Swift line
    continuations join the lines with a single space; empty OCR -> "none")."""
    trimmed = ocr_text.strip()
    ocr_line = trimmed if trimmed else "none"
    return (
        "You identify a single grocery item from the photo. "
        f"Text found on the packaging: {ocr_line}. "
        "Respond ONLY with JSON, no other words: "
        '{"name": "...", "brand": "... or null", '
        '"category": "produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other", '
        '"confidence": 0-1}'
    )


# ---------------------------------------------------------------------------
# llama-server lifecycle
# ---------------------------------------------------------------------------

class LlamaServer:
    def __init__(self):
        self.proc: subprocess.Popen | None = None

    def start(self) -> None:
        if MODEL is None or not MODEL.exists():
            raise SystemExit("Model GGUF not found (unsloth UD-IQ3_XXS).")
        if MMPROJ is None or not MMPROJ.exists():
            raise SystemExit("mmproj GGUF not found (ggml-org Q8_0).")
        # Reuse an already-running server on 8091 if healthy.
        if self._healthy():
            print(f"Reusing llama-server already running on :{SERVER_PORT}")
            return
        print(f"Starting llama-server on {SERVER_HOST}:{SERVER_PORT}")
        print(f"  model:  {MODEL}")
        print(f"  mmproj: {MMPROJ}")
        cmd = [
            "llama-server",
            "--model", str(MODEL),
            "--mmproj", str(MMPROJ),
            "--jinja",
            "--ctx-size", "4096",
            "-ngl", "99",
            "--host", SERVER_HOST,
            "--port", str(SERVER_PORT),
            "--temp", "0.2",
        ]
        self.proc = subprocess.Popen(
            cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if not self._wait_ready(timeout=180):
            self.stop()
            raise SystemExit("llama-server failed to become ready.")
        print("llama-server ready.")

    def _healthy(self) -> bool:
        try:
            r = requests.get(SERVER_URL + "/health", timeout=3)
            return r.status_code == 200 and r.json().get("status") == "ok"
        except Exception:
            return False

    def _wait_ready(self, timeout: float = 180.0) -> bool:
        deadline = time.time() + timeout
        while time.time() < deadline:
            if self._healthy():
                return True
            if self.proc and self.proc.poll() is not None:
                return False  # process died
            time.sleep(1.5)
        return False

    def stop(self) -> None:
        if self.proc and self.proc.poll() is None:
            print("Stopping llama-server (freeing ~4GB)...")
            self.proc.send_signal(signal.SIGTERM)
            try:
                self.proc.wait(timeout=15)
            except subprocess.TimeoutExpired:
                self.proc.kill()
        self.proc = None


def chat(content_parts, response_format, *, temperature=0.2,
         max_tokens=768, timeout=300) -> str:
    payload = {
        "model": "gemma",
        "messages": [{"role": "user", "content": content_parts}],
        "temperature": temperature,
        "max_tokens": max_tokens,
        "response_format": response_format,
    }
    r = requests.post(SERVER_URL + "/v1/chat/completions",
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


# ---------------------------------------------------------------------------
# Image helpers
# ---------------------------------------------------------------------------

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
# Apple Vision: cropping + OCR (reused from eval/run_eval.py)
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
    Vision, Quartz, NSURL = _vision_imports()
    req = Vision.VNGenerateObjectnessBasedSaliencyImageRequest.alloc().init()
    handler = _handler_for(path)
    ok, err = handler.performRequests_error_([req], None)
    boxes = []
    if not ok:
        return boxes
    for obs in (req.results() or []):
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


def _iou(a, b):
    ax, ay, aw, ah = a
    bx, by, bw, bh = b
    ix = max(ax, bx); iy = max(ay, by)
    ix2 = min(ax + aw, bx + bw); iy2 = min(ay + ah, by + bh)
    iw = max(0.0, ix2 - ix); ih = max(0.0, iy2 - iy)
    inter = iw * ih
    union = aw * ah + bw * bh - inter
    return inter / union if union > 0 else 0.0


def _overlap_frac(a, b):
    """Fraction of box `a` covered by box `b`."""
    ax, ay, aw, ah = a
    bx, by, bw, bh = b
    ix = max(ax, bx); iy = max(ay, by)
    ix2 = min(ax + aw, bx + bw); iy2 = min(ay + ah, by + bh)
    iw = max(0.0, ix2 - ix); ih = max(0.0, iy2 - iy)
    inter = iw * ih
    area_a = aw * ah
    return inter / area_a if area_a > 0 else 0.0


def _crop_pil(img: Image.Image, box_bl):
    """box_bl is normalized (x,y,w,h) with origin bottom-left (Vision)."""
    W, H = img.size
    x, y, w, h = box_bl
    left = int(x * W)
    right = int((x + w) * W)
    top = int((1.0 - (y + h)) * H)
    bottom = int((1.0 - y) * H)
    left, right = max(0, left), min(W, right)
    top, bottom = max(0, top), min(H, bottom)
    if right - left < 8 or bottom - top < 8:
        return None
    return img.crop((left, top, right, bottom))


def get_crop_boxes(path: Path):
    """Return (padded normalized boxes, source, raw_detected_boxes)."""
    raw = _norm_boxes_from_saliency(path)
    raw += _norm_boxes_from_rectangles(path)
    uniq = []
    for b in raw:
        if not any(_iou(b, u) > 0.7 for u in uniq):
            uniq.append(b)
    if len(uniq) < 2:
        return [_pad_box(*b) for b in _tile_grid()], "tile-grid", uniq
    return [_pad_box(*b) for b in uniq], "saliency+rect", uniq


def negative_boxes(detected, n=3, seed=0):
    """Random low-saliency crops in regions NOT covered by any detected box.
    `detected` are raw (unpadded) normalized (x,y,w,h) bottom-left boxes."""
    rng = random.Random(seed)
    out = []
    attempts = 0
    while len(out) < n and attempts < 400:
        attempts += 1
        w = rng.uniform(0.15, 0.30)
        h = rng.uniform(0.15, 0.30)
        x = rng.uniform(0.0, 1.0 - w)
        y = rng.uniform(0.0, 1.0 - h)
        cand = (x, y, w, h)
        # reject if it meaningfully overlaps any detected object...
        if any(_overlap_frac(cand, d) > 0.10 for d in detected):
            continue
        # ...or a previously chosen negative (keep them spread out).
        if any(_iou(cand, o) > 0.25 for o in out):
            continue
        out.append(cand)
    # If detections blanket the frame, relax and just take spread-out corners.
    if not out:
        for c in [(0.0, 0.0, 0.22, 0.22), (0.78, 0.0, 0.22, 0.22),
                  (0.0, 0.78, 0.22, 0.22), (0.78, 0.78, 0.22, 0.22)][:n]:
            out.append(c)
    return out


def ocr_image(img: Image.Image) -> str:
    Vision, Quartz, NSURL = _vision_imports()
    with tempfile.NamedTemporaryFile(suffix=".png", delete=False) as tf:
        tmp = Path(tf.name)
    try:
        img.convert("RGB").save(tmp, format="PNG")
        req = Vision.VNRecognizeTextRequest.alloc().init()
        try:
            req.setRecognitionLevel_(1)  # accurate
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
# Idempotency
# ---------------------------------------------------------------------------

def already_processed() -> set[str]:
    done = set()
    if JSONL.exists():
        for line in JSONL.read_text().splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                done.add(json.loads(line)["photo"])
            except Exception:
                pass
    return done


def append_jsonl(records: list[dict]) -> None:
    with JSONL.open("a") as f:
        for rec in records:
            f.write(json.dumps(rec, ensure_ascii=False) + "\n")


# ---------------------------------------------------------------------------
# Per-photo processing
# ---------------------------------------------------------------------------

def prelabel_crop(crop: Image.Image, ocr: str) -> dict:
    content = chat(
        [image_part(downscale(crop, 896)), text_part(production_prompt(ocr))],
        ITEM_SCHEMA, max_tokens=768)
    parsed = _parse_json(content)
    if isinstance(parsed, dict) and parsed.get("name"):
        cat = parsed.get("category")
        if cat not in CATEGORIES:
            cat = "other"
        try:
            conf = float(parsed.get("confidence", 0.0))
        except Exception:
            conf = 0.0
        return {
            "name": str(parsed["name"]),
            "brand": parsed.get("brand") if parsed.get("brand") else None,
            "category": cat,
            "confidence": max(0.0, min(1.0, conf)),
        }
    # Model gave nothing usable.
    return {"name": "unknown", "brand": None, "category": "other",
            "confidence": 0.0}


NEG_PRELABEL = {"name": "unknown", "brand": None, "category": "other",
                "confidence": 0.1}


def process_photo(photo: Path, n_negatives: int, do_prelabel: bool) -> dict:
    stem = photo.stem
    img = load_image(photo)
    boxes, box_source, detected = get_crop_boxes(photo)
    records = []
    idx = 0

    # Positive crops.
    n_pos = 0
    for box in boxes:
        crop = _crop_pil(img, box)
        if crop is None:
            continue
        crop = downscale(crop, 896)
        fname = f"{stem}_{idx}.png"
        crop.save(CROPS_DIR / fname, format="PNG")
        ocr = ocr_image(crop)
        prelabel = (prelabel_crop(crop, ocr) if do_prelabel
                    else {"name": "unknown", "brand": None,
                          "category": "other", "confidence": 0.0})
        records.append({
            "id": f"{stem}_{idx}",
            "image": f"crops/{fname}",
            "ocr": ocr,
            "prelabel": prelabel,
            "label": None,
            "source": "real",
            "photo": photo.name,
        })
        idx += 1
        n_pos += 1

    # Negative crops (regions not covered by any detected box).
    n_neg = 0
    negs = negative_boxes(detected, n=n_negatives, seed=hash(stem) & 0xFFFF)
    for nb in negs:
        crop = _crop_pil(img, nb)
        if crop is None:
            continue
        crop = downscale(crop, 896)
        fname = f"{stem}_{idx}.png"
        crop.save(CROPS_DIR / fname, format="PNG")
        ocr = ocr_image(crop)
        records.append({
            "id": f"{stem}_{idx}",
            "image": f"crops/{fname}",
            "ocr": ocr,
            "prelabel": dict(NEG_PRELABEL),
            "label": None,
            "source": "negative",
            "photo": photo.name,
        })
        idx += 1
        n_neg += 1

    append_jsonl(records)
    return {"positives": n_pos, "negatives": n_neg,
            "box_source": box_source, "records": len(records)}


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def find_new_photos(done: set[str]) -> list[Path]:
    if not INBOX.exists():
        return []
    photos = [p for p in sorted(INBOX.iterdir())
              if p.is_file() and p.suffix.lower() in IMG_EXTS]
    return [p for p in photos if p.name not in done]


def main():
    ap = argparse.ArgumentParser(description="DinnerDecider dataset extractor")
    ap.add_argument("--negatives", type=int, default=3,
                    help="negative crops per photo (default 3)")
    ap.add_argument("--no-prelabel", action="store_true",
                    help="skip the model; crops + OCR only")
    args = ap.parse_args()

    CROPS_DIR.mkdir(parents=True, exist_ok=True)

    done = already_processed()
    new_photos = find_new_photos(done)

    if not new_photos:
        print(f"No new photos in {INBOX} "
              f"({len(done)} already processed). Nothing to do.")
        return

    print(f"Found {len(new_photos)} new photo(s) "
          f"({len(done)} already done).")

    server = None
    do_prelabel = not args.no_prelabel
    if do_prelabel:
        server = LlamaServer()
        server.start()

    tot_pos = tot_neg = 0
    processed = 0
    try:
        for photo in new_photos:
            print(f"\n-> {photo.name}")
            try:
                r = process_photo(photo, args.negatives, do_prelabel)
            except Exception as e:
                print(f"   [error] {photo.name}: {e}", file=sys.stderr)
                continue
            processed += 1
            tot_pos += r["positives"]
            tot_neg += r["negatives"]
            print(f"   boxes={r['box_source']} "
                  f"positives={r['positives']} negatives={r['negatives']}")
    finally:
        if server is not None:
            server.stop()

    print("\n=== Summary ===")
    print(f"Photos processed this run : {processed}")
    print(f"Positive crops            : {tot_pos}")
    print(f"Negative crops            : {tot_neg}")
    print(f"Total crops this run      : {tot_pos + tot_neg}")
    total_lines = sum(1 for _ in JSONL.open()) if JSONL.exists() else 0
    print(f"Total lines in crops.jsonl: {total_lines}")
    print(f"Output: {JSONL}")


if __name__ == "__main__":
    main()
