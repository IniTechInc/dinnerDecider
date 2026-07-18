"""Generate a tiny self-contained smoke dataset (~10 examples).

Produces PIL grocery-ish label images + a train.jsonl matching the dataset
contract that the real labeler agent emits:
  {"id","image","ocr","label":{name,brand,category,confidence},"source"}
image path is relative to the dataset dir.

This exists ONLY to prove the train->merge->convert->quantize->verify chain.
The real dataset lands in training/dataset/train.jsonl.
"""

import json
import os
import shutil
from PIL import Image, ImageDraw, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, "smoke_data")
IMG_DIR = os.path.join(OUT, "images")
TEST_BOX = "/private/tmp/claude-501/-Users-philwoolley-Projects-gemma4hackathon/e7d67faa-807c-441f-af53-fba52792188d/scratchpad/test_box.png"

# (filename, big label text, ocr, name, brand, category, confidence)
ITEMS = [
    ("milk.png",      "MILK\n2% Reduced Fat\nHORIZON",        "MILK 2% Reduced Fat HORIZON",   "Milk",            "Horizon",       "dairy",     0.95),
    ("bananas.png",   "BANANAS\nOrganic",                     "BANANAS Organic",               "Bananas",         None,            "produce",   0.9),
    ("cheddar.png",   "SHARP\nCHEDDAR\nTILLAMOOK",            "SHARP CHEDDAR TILLAMOOK",       "Cheddar Cheese",  "Tillamook",     "dairy",     0.92),
    ("chips.png",     "TORTILLA\nCHIPS\nTOSTITOS",            "TORTILLA CHIPS TOSTITOS",       "Tortilla Chips",  "Tostitos",      "snack",     0.93),
    ("cola.png",      "COCA-COLA\nClassic\n12 fl oz",         "COCA-COLA Classic 12 fl oz",    "Cola",            "Coca-Cola",     "beverage",  0.96),
    ("ketchup.png",   "TOMATO\nKETCHUP\nHEINZ",               "TOMATO KETCHUP HEINZ",          "Ketchup",         "Heinz",         "condiment", 0.94),
    ("chicken.png",   "CHICKEN\nBREAST\nFRESH",               "CHICKEN BREAST FRESH",          "Chicken Breast",  None,            "meat",      0.88),
    ("peas.png",      "FROZEN\nSWEET PEAS\nBIRDS EYE",        "FROZEN SWEET PEAS BIRDS EYE",   "Sweet Peas",      "Birds Eye",     "frozen",    0.9),
    ("rice.png",      "JASMINE\nRICE\n5 LB",                  "JASMINE RICE 5 LB",             "Jasmine Rice",    None,            "pantry",    0.87),
    ("yogurt.png",    "GREEK\nYOGURT\nCHOBANI\nVanilla",      "GREEK YOGURT CHOBANI Vanilla",  "Greek Yogurt",    "Chobani",       "dairy",     0.94),
]


def _font(size):
    for path in [
        "/System/Library/Fonts/Supplemental/Arial Bold.ttf",
        "/System/Library/Fonts/Helvetica.ttc",
        "/Library/Fonts/Arial.ttf",
    ]:
        if os.path.exists(path):
            try:
                return ImageFont.truetype(path, size)
            except Exception:
                pass
    return ImageFont.load_default()


def make_label_image(path, text):
    img = Image.new("RGB", (512, 512), (245, 240, 230))
    d = ImageDraw.Draw(img)
    d.rectangle([16, 16, 496, 496], outline=(60, 60, 60), width=6)
    f = _font(48)
    lines = text.split("\n")
    y = 120
    for ln in lines:
        bbox = d.textbbox((0, 0), ln, font=f)
        w = bbox[2] - bbox[0]
        d.text(((512 - w) / 2, y), ln, fill=(20, 20, 20), font=f)
        y += 70
    img.save(path)


def main():
    os.makedirs(IMG_DIR, exist_ok=True)
    rows = []
    for i, (fn, text, ocr, name, brand, cat, conf) in enumerate(ITEMS):
        make_label_image(os.path.join(IMG_DIR, fn), text)
        rows.append({
            "id": f"smoke-{i:03d}",
            "image": f"images/{fn}",
            "ocr": ocr,
            "label": {"name": name, "brand": brand, "category": cat, "confidence": conf},
            "source": "smoke-synthetic",
        })
    # include the real verification image as an 11th example
    if os.path.exists(TEST_BOX):
        shutil.copy(TEST_BOX, os.path.join(IMG_DIR, "test_box.png"))
        rows.append({
            "id": "smoke-testbox",
            "image": "images/test_box.png",
            "ocr": "",
            "label": {"name": "Cereal", "brand": None, "category": "pantry", "confidence": 0.7},
            "source": "smoke-testbox",
        })
    with open(os.path.join(OUT, "train.jsonl"), "w") as f:
        for r in rows:
            f.write(json.dumps(r) + "\n")
    print(f"wrote {len(rows)} examples to {OUT}/train.jsonl")


if __name__ == "__main__":
    main()
