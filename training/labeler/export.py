#!/usr/bin/env python3
"""Merge crops + verified human labels into train/holdout splits.

train.jsonl    = verified real crops (using human label) + all synth crops.
holdout.jsonl  = a random 15% of VERIFIED REAL crops, stratified by source
                 photo so no photo appears in both splits. Eval only.

Rejected crops (action == "rejected", i.e. not food / unknown) are kept with
their rejected label so the model learns the negative too.

Python 3.13 stdlib only.
"""

import json
import os
import random

HERE = os.path.dirname(os.path.abspath(__file__))
DATASET = os.path.abspath(os.path.join(HERE, "..", "dataset"))
REAL_JSONL = os.path.join(DATASET, "real", "crops.jsonl")
SYNTH_JSONL = os.path.join(DATASET, "synth", "crops.jsonl")
VERIFIED_JSONL = os.path.join(DATASET, "labels_verified.jsonl")
TRAIN_OUT = os.path.join(DATASET, "train.jsonl")
HOLDOUT_OUT = os.path.join(DATASET, "holdout.jsonl")

HOLDOUT_FRAC = 0.15
SEED = 1337


def read_jsonl(path):
    rows = []
    if not os.path.exists(path):
        return rows
    with open(path, "r", encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if line:
                try:
                    rows.append(json.loads(line))
                except json.JSONDecodeError:
                    pass
    return rows


def photo_key(crop):
    """Group by source photo so no photo straddles train and holdout."""
    img = crop.get("image", "")
    base = os.path.basename(img)
    # crop ids often look like <photo>__<n>; fall back to image basename
    return base or crop.get("id", "")


def write_jsonl(path, rows):
    with open(path, "w", encoding="utf-8") as fh:
        for r in rows:
            fh.write(json.dumps(r, ensure_ascii=False) + "\n")


def main():
    real = read_jsonl(REAL_JSONL)
    synth = read_jsonl(SYNTH_JSONL)
    journal = read_jsonl(VERIFIED_JSONL)

    # latest verified entry wins per id
    verified = {}
    for e in journal:
        verified[e["id"]] = e

    real_by_id = {c["id"]: c for c in real}

    # build verified real examples (accepted/corrected/rejected all carry a label)
    verified_real = []
    for cid, entry in verified.items():
        crop = real_by_id.get(cid)
        if crop is None:
            continue
        label = entry.get("label")
        if label is None:
            continue
        example = {
            "id": cid,
            "image": crop.get("image"),
            "ocr": crop.get("ocr", ""),
            "label": label,
            "source": crop.get("source", "real"),
            "action": entry.get("action"),
        }
        verified_real.append(example)

    # stratified holdout by photo
    photos = sorted({photo_key(real_by_id[e["id"]]) for e in verified_real})
    rng = random.Random(SEED)
    rng.shuffle(photos)
    n_holdout = int(round(len(photos) * HOLDOUT_FRAC))
    holdout_photos = set(photos[:n_holdout])

    train_real, holdout_real = [], []
    for ex in verified_real:
        pk = photo_key(real_by_id[ex["id"]])
        if pk in holdout_photos:
            holdout_real.append(ex)
        else:
            train_real.append(ex)

    # synth carries its own trusted label; fall back to prelabel if label empty
    synth_examples = []
    for c in synth:
        label = c.get("label") or c.get("prelabel")
        synth_examples.append({
            "id": c["id"],
            "image": c.get("image"),
            "ocr": c.get("ocr", ""),
            "label": label,
            "source": "synth",
            "action": "synth",
        })

    train = train_real + synth_examples
    write_jsonl(TRAIN_OUT, train)
    write_jsonl(HOLDOUT_OUT, holdout_real)

    print("Export complete")
    print(f"  verified real crops : {len(verified_real)}")
    print(f"  photos (verified)   : {len(photos)}  -> holdout photos: {len(holdout_photos)}")
    print(f"  train.jsonl         : {len(train)}  ({len(train_real)} real + {len(synth_examples)} synth)")
    print(f"  holdout.jsonl       : {len(holdout_real)}  (real, eval only)")
    print(f"  -> {TRAIN_OUT}")
    print(f"  -> {HOLDOUT_OUT}")


if __name__ == "__main__":
    main()
