#!/usr/bin/env python3
"""Fetch US-market packaged products (+ some produce) from Open Food Facts.

Downloads product_name / brands / categories / front image for 300-500 products
that HAVE a front image and an English name, maps each to one of our 9
categories, and saves the image + a metadata row (products.jsonl). ODbL-licensed;
attribution written by the caller (see ATTRIBUTION.md).

Polite client: single search connection, modest concurrent image downloads,
proper User-Agent, retries with backoff.

Usage:
  python3 fetch_off.py --target 400 --produce 40
"""
from __future__ import annotations

import argparse
import concurrent.futures as cf
import json
import sys
import time
from io import BytesIO

import requests
from PIL import Image, ImageDraw

from off_common import (CATEGORIES, PRODUCTS_DIR, PRODUCTS_JSONL, SYNTH_DIR,
                        USER_AGENT, derive_generic_name, is_english,
                        map_category)

SEARCH_URL = "https://world.openfoodfacts.org/api/v2/search"
FIELDS = ("code,product_name,product_name_en,generic_name,generic_name_en,"
          "brands,categories,categories_tags,image_front_url,"
          "image_front_small_url,countries_tags,lang")

# Category seeds to guarantee spread across our 9 buckets. Each is an OFF
# category tag we page through; we still re-map every product ourselves.
SEEDS: list[tuple[str, str | None]] = [
    ("dairies", "dairy"),
    ("cheeses", "dairy"),
    ("yogurts", "dairy"),
    ("meats", "meat"),
    ("prepared-meats", "meat"),
    ("snacks", "snack"),
    ("chips-and-fries", "snack"),
    ("biscuits-and-cakes", "snack"),
    ("chocolates", "snack"),
    ("beverages", "beverage"),
    ("carbonated-drinks", "beverage"),
    ("juices", "beverage"),
    ("condiments", "condiment"),
    ("sauces", "condiment"),
    ("breakfast-cereals", "pantry"),
    ("pastas", "pantry"),
    ("canned-foods", "pantry"),
    ("spreads", "pantry"),
    ("frozen-foods", "frozen"),
    ("frozen-desserts", "frozen"),
]
PRODUCE_SEEDS = ["fruits", "vegetables", "fresh-vegetables", "fresh-fruits"]


def make_session() -> requests.Session:
    s = requests.Session()
    s.headers.update({"User-Agent": USER_AGENT, "Accept": "application/json"})
    return s


def search_page(session: requests.Session, category_tag: str, page: int,
                page_size: int = 100) -> list[dict]:
    params = {
        "categories_tags_en": category_tag,
        "countries_tags_en": "united-states",
        "fields": FIELDS,
        "page_size": page_size,
        "page": page,
        "sort_by": "unique_scans_n",  # popular first -> better images/names
    }
    for attempt in range(4):
        try:
            r = session.get(SEARCH_URL, params=params, timeout=30)
            if r.status_code == 429:
                time.sleep(3 * (attempt + 1))
                continue
            r.raise_for_status()
            return r.json().get("products", []) or []
        except Exception as e:  # noqa
            if attempt == 3:
                print(f"  [warn] search {category_tag} p{page} failed: {e}",
                      file=sys.stderr)
                return []
            time.sleep(1.5 * (attempt + 1))
    return []


def pick_name(p: dict) -> str:
    for k in ("product_name_en", "product_name"):
        v = (p.get(k) or "").strip()
        if v:
            return v
    return ""


def _build_rec(p: dict) -> dict | None:
    code = str(p.get("code") or "").strip()
    if not code:
        return None
    name = pick_name(p)
    img = p.get("image_front_url") or p.get("image_front_small_url")
    if not name or not img or not is_english(name):
        return None
    cats = p.get("categories_tags") or []
    category = map_category(cats, p.get("categories") or "")
    brand = (p.get("brands") or "").split(",")[0].strip() or None
    generic = (p.get("generic_name_en") or p.get("generic_name") or "")
    gname = derive_generic_name(name, brand, generic, category)
    return {
        "code": code, "name": gname, "raw_name": name, "brand": brand,
        "category": category, "image_url": img,
        "off_url": f"https://world.openfoodfacts.org/product/{code}",
    }


def collect(session: requests.Session, target: int) -> dict[str, dict]:
    """Gather products evenly across all seeds so every bucket is represented.
    Per-seed cap keeps one popular category from crowding out the rest."""
    out: dict[str, dict] = {}
    per_seed = max(15, target // max(1, len(SEEDS)) + 8)
    for tag, _hint in SEEDS:
        got = 0
        for page in (1, 2, 3):
            if got >= per_seed:
                break
            prods = search_page(session, tag, page)
            if not prods:
                break
            for p in prods:
                if got >= per_seed:
                    break
                rec = _build_rec(p)
                if rec is None or rec["code"] in out:
                    continue
                out[rec["code"]] = rec
                got += 1
            time.sleep(0.3)  # be polite between category queries
        print(f"  {tag:22s}: +{got}  (total {len(out)})")
    return out


def collect_produce(session: requests.Session, want: int) -> dict[str, dict]:
    out: dict[str, dict] = {}
    for tag in PRODUCE_SEEDS:
        if len(out) >= want:
            break
        for page in (1, 2):
            prods = search_page(session, tag, page, page_size=100)
            for p in prods:
                code = str(p.get("code") or "").strip()
                if not code or code in out:
                    continue
                name = pick_name(p)
                img = p.get("image_front_url") or p.get("image_front_small_url")
                if not name or not img or not is_english(name):
                    continue
                brand = (p.get("brands") or "").split(",")[0].strip() or None
                gname = derive_generic_name(
                    name, brand, p.get("generic_name_en"), "produce")
                out[code] = {
                    "code": code, "name": gname, "raw_name": name,
                    "brand": brand, "category": "produce", "image_url": img,
                    "off_url": f"https://world.openfoodfacts.org/product/{code}",
                }
            time.sleep(0.3)
    return out


PRODUCE_COLORS = {
    "apple": (200, 40, 40), "banana": (230, 200, 60), "orange": (240, 140, 30),
    "tomato": (210, 55, 45), "lemon": (240, 220, 70), "lime": (120, 190, 70),
    "carrot": (230, 130, 40), "broccoli": (70, 130, 60), "grape": (110, 70, 150),
    "pepper": (60, 160, 70), "eggplant": (90, 60, 120), "peach": (240, 170, 120),
    "pear": (170, 200, 90), "cucumber": (80, 150, 70), "onion": (200, 170, 120),
    "strawberry": (215, 50, 60), "blueberry": (70, 90, 160), "potato": (190, 160, 110),
}


def generate_produce(n: int, start_idx: int) -> list[dict]:
    """Procedural fallback produce images (simple colored fruit/veg shapes on a
    transparent PNG) so we always have produce coverage even when OFF is weak."""
    import random
    rows = []
    names = list(PRODUCE_COLORS.items())
    for i in range(n):
        name, color = names[i % len(names)]
        code = f"synthproduce_{start_idx + i:04d}"
        size = 320
        img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
        d = ImageDraw.Draw(img)
        cx, cy = size // 2, size // 2
        r = random.randint(90, 130)
        # slight organic wobble via overlapping ellipses
        for _ in range(3):
            ox, oy = random.randint(-15, 15), random.randint(-15, 15)
            rr = r + random.randint(-15, 10)
            shade = tuple(min(255, max(0, c + random.randint(-20, 20)))
                          for c in color)
            d.ellipse([cx - rr + ox, cy - rr + oy, cx + rr + ox, cy + rr + oy],
                      fill=shade + (255,))
        # highlight
        d.ellipse([cx - r // 2, cy - r, cx, cy - r // 3],
                  fill=(255, 255, 255, 70))
        path = PRODUCTS_DIR / f"{code}.png"
        img.save(path)
        rows.append({
            "code": code, "name": name, "raw_name": name, "brand": None,
            "category": "produce", "image_url": None,
            "off_url": None, "image": f"products/{code}.png",
            "generated": True,
        })
    return rows


def download_one(session: requests.Session, rec: dict) -> dict | None:
    url = rec["image_url"]
    code = rec["code"]
    # cache: skip re-download if already fetched
    existing = PRODUCTS_DIR / f"{code}.jpg"
    if existing.exists():
        rec = dict(rec)
        rec["image"] = f"products/{code}.jpg"
        return rec
    for attempt in range(3):
        try:
            r = session.get(url, timeout=30)
            if r.status_code == 429:
                time.sleep(2 * (attempt + 1))
                continue
            r.raise_for_status()
            img = Image.open(BytesIO(r.content)).convert("RGB")
            # cap size to keep dataset small
            img.thumbnail((512, 512), Image.LANCZOS)
            path = PRODUCTS_DIR / f"{code}.jpg"
            img.save(path, "JPEG", quality=88)
            rec = dict(rec)
            rec["image"] = f"products/{code}.jpg"
            return rec
        except Exception:  # noqa
            if attempt == 2:
                return None
            time.sleep(1.0 * (attempt + 1))
    return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--target", type=int, default=400,
                    help="packaged products to fetch (300-500)")
    ap.add_argument("--produce", type=int, default=40,
                    help="produce images to secure (30-50)")
    ap.add_argument("--workers", type=int, default=8)
    args = ap.parse_args()

    PRODUCTS_DIR.mkdir(parents=True, exist_ok=True)
    SYNTH_DIR.mkdir(parents=True, exist_ok=True)
    session = make_session()

    t0 = time.time()
    print(f"Fetching ~{args.target} packaged products from Open Food Facts...")
    packaged = collect(session, args.target)
    print(f"Fetching produce (want {args.produce})...")
    produce = collect_produce(session, args.produce)

    # Merge (produce codes are numeric OFF codes, no clash with packaged unless
    # same product; dict dedups by code).
    all_recs = {**packaged, **produce}
    to_download = list(all_recs.values())
    print(f"Downloading {len(to_download)} images with {args.workers} workers...")

    saved: list[dict] = []
    with cf.ThreadPoolExecutor(max_workers=args.workers) as ex:
        futs = {ex.submit(download_one, session, r): r for r in to_download}
        for fut in cf.as_completed(futs):
            rec = fut.result()
            if rec:
                saved.append(rec)

    # Ensure produce floor with procedural fallback.
    produce_saved = [r for r in saved if r["category"] == "produce"]
    if len(produce_saved) < args.produce:
        need = args.produce - len(produce_saved)
        print(f"OFF produce weak ({len(produce_saved)}); "
              f"generating {need} procedural produce images...")
        saved.extend(generate_produce(need, start_idx=0))

    # Write products.jsonl (drop bulky image_url from disk record? keep off_url
    # for attribution/provenance).
    with PRODUCTS_JSONL.open("w") as f:
        for r in saved:
            out = {
                "code": r["code"], "name": r["name"], "raw_name": r["raw_name"],
                "brand": r["brand"], "category": r["category"],
                "image": r["image"], "off_url": r.get("off_url"),
                "source": "generated" if r.get("generated") else "openfoodfacts",
            }
            f.write(json.dumps(out) + "\n")

    # Report distribution.
    from collections import Counter
    dist = Counter(r["category"] for r in saved)
    dt = time.time() - t0
    print(f"\nSaved {len(saved)} product images in {dt:.1f}s -> {PRODUCTS_JSONL}")
    for c in CATEGORIES:
        print(f"  {c:10s}: {dist.get(c, 0)}")


if __name__ == "__main__":
    main()
