#!/usr/bin/env python3
"""Compose synthetic shelf scenes from OFF product cutouts, emit training crops
+ negatives, OCR every crop with Apple Vision, and write crops.jsonl.

Pipeline per scene:
  1. Procedural fridge/pantry background (gradient, shelf lines, warm/cool tint,
     vignette).
  2. Place 3-6 products across shelves with scale variance, small rotation,
     partial overlaps/occlusion, random crowding.
  3. For each placed product emit a training crop = its region + 15% padding
     PLUS jitter (offset + scale noise) so crops clip / include neighbors like
     the real app's crop stage would.
  4. Emit background-only / sliver NEGATIVE crops.
  5. OCR every crop for real (VNRecognizeTextRequest, accurate) - garbled and
     empty OCR is expected and wanted.
  6. Label crops from OFF metadata (trusted -> prelabel == label).

Output: crops.jsonl in the shared schema (see task / eval/run_eval.py).
Requires: pillow, pyobjc Vision + Quartz (macOS).
"""
from __future__ import annotations

import argparse
import json
import math
import random
import sys
import tempfile
from pathlib import Path

from PIL import Image, ImageDraw, ImageFilter

from off_common import (CROPS_DIR, CROPS_JSONL, DATASET_DIR, PRODUCTS_DIR,
                        PRODUCTS_JSONL, SCENES_DIR)

# ---------------------------------------------------------------------------
# Apple Vision OCR (reused pattern from eval/run_eval.py)
# ---------------------------------------------------------------------------
_VISION = None


def _vision():
    global _VISION
    if _VISION is None:
        import Vision
        from Foundation import NSURL
        _VISION = (Vision, NSURL)
    return _VISION


def ocr_image(img: Image.Image) -> str:
    """VNRecognizeTextRequest (accurate) on a PIL image via a temp PNG."""
    Vision, NSURL = _vision()
    with tempfile.NamedTemporaryFile(suffix=".png", delete=False) as tf:
        tmp = Path(tf.name)
    try:
        img.convert("RGB").save(tmp, format="PNG")
        url = NSURL.fileURLWithPath_(str(tmp))
        handler = Vision.VNImageRequestHandler.alloc().initWithURL_options_(url, {})
        req = Vision.VNRecognizeTextRequest.alloc().init()
        try:
            req.setRecognitionLevel_(1)  # accurate
            req.setUsesLanguageCorrection_(True)
        except Exception:
            pass
        ok, _err = handler.performRequests_error_([req], None)
        if not ok:
            return ""
        lines = []
        for obs in (req.results() or []):
            cand = obs.topCandidates_(1)
            if cand and len(cand):
                lines.append(str(cand[0].string()))
        return " ".join(lines).strip()
    finally:
        try:
            tmp.unlink()
        except Exception:
            pass


# ---------------------------------------------------------------------------
# Product loading + cutout
# ---------------------------------------------------------------------------

def load_products() -> list[dict]:
    if not PRODUCTS_JSONL.exists():
        raise SystemExit(f"{PRODUCTS_JSONL} missing - run fetch_off.py first")
    recs = []
    for line in PRODUCTS_JSONL.read_text().splitlines():
        line = line.strip()
        if line:
            recs.append(json.loads(line))
    return recs


def _cutout(img: Image.Image) -> Image.Image:
    """Rough white-background removal so pasted products don't look like boxes.
    Only strips near-white border-connected pixels; keeps colored backgrounds."""
    img = img.convert("RGBA")
    px = img.load()
    w, h = img.size
    # sample corners to decide if bg is white-ish
    corners = [px[0, 0], px[w - 1, 0], px[0, h - 1], px[w - 1, h - 1]]
    whiteish = sum(1 for c in corners if c[0] > 225 and c[1] > 225
                   and c[2] > 225) >= 3
    if whiteish:
        from PIL import ImageChops
        r, g, b, a = img.split()
        # per-pixel min channel; near-white where min channel is high
        mn = ImageChops.darker(ImageChops.darker(r, g), b)
        # alpha: 0 (transparent) where near-white, else keep original alpha
        white_mask = mn.point(lambda p: 0 if p > 232 else 255)
        new_alpha = ImageChops.multiply(a, white_mask.point(
            lambda p: 255 if p else 0))
        img.putalpha(new_alpha)
    return img


def load_product_image(rec: dict) -> Image.Image | None:
    path = DATASET_DIR / "synth" / rec["image"]
    if not path.exists():
        return None
    try:
        img = Image.open(path)
    except Exception:
        return None
    if rec.get("source") == "generated":
        return img.convert("RGBA")  # already transparent
    return _cutout(img)


# ---------------------------------------------------------------------------
# Procedural background
# ---------------------------------------------------------------------------

def make_background(w: int, h: int, rng: random.Random) -> Image.Image:
    # base vertical gradient between two cool/warm tints
    warm = rng.random() < 0.5
    if warm:
        top = (rng.randint(210, 240), rng.randint(195, 220), rng.randint(165, 195))
        bot = (rng.randint(170, 200), rng.randint(150, 180), rng.randint(120, 150))
    else:
        top = (rng.randint(200, 225), rng.randint(210, 235), rng.randint(220, 245))
        bot = (rng.randint(150, 180), rng.randint(165, 195), rng.randint(180, 210))
    bg = Image.new("RGB", (w, h))
    d = ImageDraw.Draw(bg)
    for y in range(h):
        t = y / h
        col = tuple(int(top[i] * (1 - t) + bot[i] * t) for i in range(3))
        d.line([(0, y), (w, y)], fill=col)
    # shelf lines
    n_shelves = rng.randint(2, 3)
    shelf_ys = [int(h * (i + 1) / (n_shelves + 1)) for i in range(n_shelves)]
    for sy in shelf_ys:
        thick = rng.randint(6, 12)
        shade = rng.randint(90, 140)
        d.rectangle([0, sy, w, sy + thick], fill=(shade, shade - 10, shade - 20))
        # subtle highlight lip
        d.line([(0, sy), (w, sy)], fill=(shade + 40, shade + 35, shade + 25),
               width=2)
    bg = bg.filter(ImageFilter.GaussianBlur(0.6))
    # vignette
    vig = Image.new("L", (w, h), 0)
    vd = ImageDraw.Draw(vig)
    vd.ellipse([-w * 0.25, -h * 0.25, w * 1.25, h * 1.25], fill=255)
    vig = vig.filter(ImageFilter.GaussianBlur(w * 0.15))
    dark = Image.new("RGB", (w, h), (0, 0, 0))
    bg = Image.composite(bg, dark, vig.point(lambda p: int(60 + p * 0.76)))
    return bg, shelf_ys


# ---------------------------------------------------------------------------
# Scene composition
# ---------------------------------------------------------------------------

def _rotate(img: Image.Image, deg: float) -> Image.Image:
    return img.rotate(deg, expand=True, resample=Image.BICUBIC)


def compose_scene(scene_idx: int, products: list[dict], rng: random.Random):
    """Return (canvas RGB, placements). Each placement:
       {rec, box(px x0,y0,x1,y1), occlusion(float 0-1)}"""
    W, H = 1024, 720
    bg, shelf_ys = make_background(W, H, rng)
    canvas = bg.convert("RGBA")
    placements = []

    n = rng.randint(3, 6)
    # choose a shelf baseline for this scene's row of products
    shelf_ys_sorted = sorted(shelf_ys)
    chosen = rng.sample(products, min(n, len(products)))

    x_cursor = rng.randint(10, 90)
    baseline = rng.choice(shelf_ys_sorted)
    for rec in chosen:
        pim = load_product_image(rec)
        if pim is None:
            continue
        # scale: target height 30-58% of canvas height
        target_h = int(H * rng.uniform(0.30, 0.58))
        scale = target_h / pim.height
        new_w = max(12, int(pim.width * scale))
        new_h = max(12, int(pim.height * scale))
        pim = pim.resize((new_w, new_h), Image.LANCZOS)
        # small rotation
        deg = rng.uniform(-8, 8)
        pim_r = _rotate(pim, deg)
        rw, rh = pim_r.size
        # place so bottom sits near the shelf baseline
        y = baseline - rh + rng.randint(-10, 8)
        y = max(0, min(H - rh, y))
        x = x_cursor
        if x + rw > W:
            break
        canvas.alpha_composite(pim_r, (x, y))
        # record TIGHT bbox from the rotated alpha (visible extent)
        bbox = pim_r.getbbox()  # in pim_r coords
        if bbox:
            bx0, by0, bx1, by1 = bbox
            box = (x + bx0, y + by0, x + bx1, y + by1)
        else:
            box = (x, y, x + rw, y + rh)
        placements.append({"rec": rec, "box": box, "occlusion": 0.0})
        # advance cursor with crowding / overlap
        step = int(rw * rng.uniform(0.55, 0.95))  # <1 => overlap
        x_cursor = x + step
        # occasionally jump to a different shelf
        if rng.random() < 0.25 and len(shelf_ys_sorted) > 1:
            baseline = rng.choice(shelf_ys_sorted)
            x_cursor = rng.randint(10, 120)

    # compute occlusion: fraction of each box covered by LATER-placed boxes
    for i, pl in enumerate(placements):
        x0, y0, x1, y1 = pl["box"]
        area = max(1, (x1 - x0) * (y1 - y0))
        covered = 0
        for j in range(i + 1, len(placements)):
            ox0, oy0, ox1, oy1 = placements[j]["box"]
            ix0, iy0 = max(x0, ox0), max(y0, oy0)
            ix1, iy1 = min(x1, ox1), min(y1, oy1)
            iw, ih = max(0, ix1 - ix0), max(0, iy1 - iy0)
            covered += iw * ih
        pl["occlusion"] = min(1.0, covered / area)

    return canvas.convert("RGB"), placements


# ---------------------------------------------------------------------------
# Crop extraction
# ---------------------------------------------------------------------------

def jittered_crop(canvas: Image.Image, box, rng: random.Random):
    W, H = canvas.size
    x0, y0, x1, y1 = box
    bw, bh = x1 - x0, y1 - y0
    if bw < 8 or bh < 8:
        return None
    pad_x = bw * 0.15
    pad_y = bh * 0.15
    # jitter: offset up to +-12% of size, independent scale noise per side
    jx = rng.uniform(-0.12, 0.12) * bw
    jy = rng.uniform(-0.12, 0.12) * bh
    sx0 = rng.uniform(0.85, 1.2)
    sy0 = rng.uniform(0.85, 1.2)
    sx1 = rng.uniform(0.85, 1.2)
    sy1 = rng.uniform(0.85, 1.2)
    cx0 = int(x0 - pad_x * sx0 + jx)
    cy0 = int(y0 - pad_y * sy0 + jy)
    cx1 = int(x1 + pad_x * sx1 + jx)
    cy1 = int(y1 + pad_y * sy1 + jy)
    cx0, cy0 = max(0, cx0), max(0, cy0)
    cx1, cy1 = min(W, cx1), min(H, cy1)
    if cx1 - cx0 < 12 or cy1 - cy0 < 12:
        return None
    return canvas.crop((cx0, cy0, cx1, cy1))


def negative_crop(canvas: Image.Image, placements, rng: random.Random):
    """Background region with little/no product coverage, or a thin sliver."""
    W, H = canvas.size
    for _ in range(12):
        sliver = rng.random() < 0.35
        if sliver:
            cw = rng.randint(16, 46)
            ch = rng.randint(120, 300)
        else:
            cw = rng.randint(120, 260)
            ch = rng.randint(90, 220)
        cx0 = rng.randint(0, max(1, W - cw))
        cy0 = rng.randint(0, max(1, H - ch))
        cx1, cy1 = cx0 + cw, cy0 + ch
        area = cw * ch
        covered = 0
        for pl in placements:
            x0, y0, x1, y1 = pl["box"]
            ix0, iy0 = max(cx0, x0), max(cy0, y0)
            ix1, iy1 = min(cx1, x1), min(cy1, y1)
            iw, ih = max(0, ix1 - ix0), max(0, iy1 - iy0)
            covered += iw * ih
        cov = covered / area
        # accept if mostly background, OR a heavily-covered sliver (occluded)
        if cov < 0.15 or (sliver and cov > 0.7):
            return canvas.crop((cx0, cy0, cx1, cy1))
    return None


def label_for(rec: dict, occlusion: float) -> dict:
    conf = 0.6 if occlusion >= 0.30 else 0.9
    return {
        "name": rec["name"],
        "brand": rec.get("brand"),
        "category": rec["category"],
        "confidence": conf,
    }


NEG_LABEL = {"name": "unknown", "brand": None, "category": "other",
             "confidence": 0.1}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--scenes", type=int, default=200)
    ap.add_argument("--negatives", type=int, default=300)
    ap.add_argument("--seed", type=int, default=1234)
    ap.add_argument("--save-scenes", type=int, default=12,
                    help="how many full scene images to keep (for inspection)")
    args = ap.parse_args()

    CROPS_DIR.mkdir(parents=True, exist_ok=True)
    SCENES_DIR.mkdir(parents=True, exist_ok=True)
    rng = random.Random(args.seed)

    products = load_products()
    products = [p for p in products if (DATASET_DIR / "synth" / p["image"]).exists()]
    if len(products) < 4:
        raise SystemExit(f"Too few product images ({len(products)}).")
    print(f"Loaded {len(products)} product images.")

    import time
    t0 = time.time()
    rows = []
    crop_i = 0
    neg_i = 0
    neg_budget = args.negatives
    negs_per_scene = max(1, math.ceil(args.negatives / args.scenes))

    with CROPS_JSONL.open("w") as out:
        for s in range(args.scenes):
            try:
                canvas, placements = compose_scene(s, products, rng)
            except Exception as e:  # noqa
                print(f"  [skip scene {s}] {e}", flush=True)
                continue
            if args.save_scenes and s < args.save_scenes:
                canvas.save(SCENES_DIR / f"scene_{s:04d}.jpg", "JPEG", quality=80)

            # positive crops
            for pl in placements:
                try:
                    crop = jittered_crop(canvas, pl["box"], rng)
                    if crop is None:
                        continue
                    fn = f"crop_{crop_i:05d}.png"
                    crop.save(CROPS_DIR / fn, "PNG")
                    ocr = ocr_image(crop)
                except Exception as e:  # noqa
                    print(f"  [skip crop] {e}", flush=True)
                    continue
                lbl = label_for(pl["rec"], pl["occlusion"])
                rows.append({
                    "id": f"synth_{crop_i:05d}",
                    "image": f"synth/crops/{fn}",
                    "ocr": ocr,
                    "prelabel": lbl,
                    "label": lbl,
                    "source": "synth",
                })
                out.write(json.dumps(rows[-1]) + "\n")
                crop_i += 1

            # negative crops
            for _ in range(negs_per_scene):
                if neg_budget <= 0:
                    break
                try:
                    ncrop = negative_crop(canvas, placements, rng)
                    if ncrop is None:
                        continue
                    fn = f"neg_{neg_i:05d}.png"
                    ncrop.save(CROPS_DIR / fn, "PNG")
                    ocr = ocr_image(ncrop)
                except Exception as e:  # noqa
                    print(f"  [skip neg] {e}", flush=True)
                    continue
                rows.append({
                    "id": f"neg_{neg_i:05d}",
                    "image": f"synth/crops/{fn}",
                    "ocr": ocr,
                    "prelabel": dict(NEG_LABEL),
                    "label": dict(NEG_LABEL),
                    "source": "negative",
                })
                out.write(json.dumps(rows[-1]) + "\n")
                neg_i += 1
                neg_budget -= 1

            if (s + 1) % 25 == 0:
                print(f"  scene {s+1}/{args.scenes}: "
                      f"{crop_i} positives, {neg_i} negatives "
                      f"({time.time()-t0:.0f}s)")

    dt = time.time() - t0
    print(f"\nDone in {dt:.0f}s: {crop_i} positive crops, {neg_i} negatives")
    print(f"Wrote {CROPS_JSONL}")

    # quick quality report
    from collections import Counter
    cat = Counter(r["prelabel"]["category"] for r in rows if r["source"] == "synth")
    empt = sum(1 for r in rows if r["source"] == "synth" and not r["ocr"])
    pos = [r for r in rows if r["source"] == "synth"]
    print("Category distribution (positives):")
    for c, n in cat.most_common():
        print(f"  {c:10s}: {n}")
    print(f"Positives with empty OCR: {empt}/{len(pos)}")
    # 5 sample (name, ocr)
    print("\nSample (name | ocr):")
    for r in random.Random(7).sample(pos, min(5, len(pos))):
        o = (r["ocr"][:60] + "...") if len(r["ocr"]) > 60 else r["ocr"]
        print(f"  {r['prelabel']['name']!r} | {o!r}")


if __name__ == "__main__":
    main()
