"""Shared helpers for the DinnerDecider synthetic data generator.

Open Food Facts (ODbL) category mapping + generic-name derivation live here so
that fetch_off.py and compose.py agree on the label vocabulary used everywhere
else in the project (see eval/run_eval.py CATEGORIES).
"""
from __future__ import annotations

import re
from pathlib import Path

# The 9 categories the app + eval harness use.
CATEGORIES = ["produce", "dairy", "meat", "pantry", "snack",
              "beverage", "condiment", "frozen", "other"]

HERE = Path(__file__).resolve().parent
# training/synth_tools/ -> training/
TRAINING_DIR = HERE.parent
DATASET_DIR = TRAINING_DIR / "dataset"
SYNTH_DIR = DATASET_DIR / "synth"
PRODUCTS_DIR = SYNTH_DIR / "products"
CROPS_DIR = SYNTH_DIR / "crops"
SCENES_DIR = SYNTH_DIR / "scenes"
PRODUCTS_JSONL = SYNTH_DIR / "products.jsonl"
CROPS_JSONL = SYNTH_DIR / "crops.jsonl"

USER_AGENT = ("DinnerDecider-Hackathon/1.0 "
              "(Gemma4 hackathon; contact woolley.pj@gmail.com)")

# ---------------------------------------------------------------------------
# Category mapping: OFF categories_tags (en:*) -> our 9 buckets.
#
# OFF tags are hierarchical and include broad umbrella tags like
# "en:fruits-and-vegetables-based-foods" or "en:plant-based-foods" on processed
# products (ketchup, chips...). Naive substring matching on those wrongly buckets
# them as produce. So we match on whole hyphen-delimited *tokens* within each
# tag, skip umbrella tags (those containing "based"), and check specific
# product-type buckets (condiment/meat/beverage/snack) BEFORE the broad
# fresh-food buckets (dairy/produce). First bucket to match wins.
# ---------------------------------------------------------------------------
# (bucket, token-needles). A tag matches a needle if the needle equals the tag
# or is one of the tag's hyphen tokens.
_CATEGORY_RULES: list[tuple[str, set[str]]] = [
    ("frozen", {"frozen"}),
    ("condiment", {
        "condiments", "sauces", "sauce", "ketchup", "ketchups", "mustards",
        "mustard", "dressings", "dressing", "mayonnaises", "mayonnaise",
        "salsa", "salsas", "vinegars", "vinegar", "gravies", "gravy",
        "marinades", "relish", "relishes",
    }),
    ("meat", {
        "meats", "meat", "poultry", "fishes", "fish", "seafood", "sausages",
        "sausage", "hams", "ham", "bacon", "beef", "pork", "chicken",
        "turkey", "salamis", "salami", "charcuterie", "deli", "steaks",
        "steak", "hot-dogs",
    }),
    ("beverage", {
        "beverages", "beverage", "drinks", "drink", "waters", "water",
        "juices", "juice", "sodas", "soda", "coffees", "coffee", "teas",
        "tea", "energy-drinks", "sports-drinks", "smoothies", "smoothie",
        "sodas", "lemonades", "lemonade", "kombucha",
    }),
    ("snack", {
        "snacks", "snack", "chips", "crisps", "biscuits", "biscuit",
        "cookies", "candies", "candy", "chocolates", "chocolate",
        "confectioneries", "crackers", "popcorn", "pretzels", "bars",
        "nuts", "desserts", "dessert", "sweets", "granolas",
    }),
    ("dairy", {
        "dairies", "cheeses", "cheese", "yogurts", "yogurt", "yoghurts",
        "yoghurt", "milks", "milk", "creams", "butters", "butter", "eggs",
        "egg", "cheddar",
    }),
    ("pantry", {
        "cereals", "cereal", "pastas", "pasta", "rices", "rice", "flours",
        "flour", "canned", "sugars", "sugar", "oils", "oil", "baking",
        "spreads", "spread", "honeys", "honey", "jams", "jam", "soups",
        "soup", "noodles", "noodle", "beans", "grains", "grain", "spices",
        "spice", "salt", "syrups", "syrup", "broths", "broth", "stocks",
        "preserves", "jellies", "peanut-butter", "nut-butters", "legumes",
    }),
    ("produce", {
        "fruits", "fruit", "vegetables", "vegetable", "salads", "salad",
        "mushrooms", "berries", "citrus", "greens", "lettuce", "lettuces",
        "spinach", "tomatoes", "potatoes", "onions", "peppers", "carrots",
        "apples", "bananas", "avocados", "herbs",
    }),
]
_PRODUCE_REJECT = {"juice", "juices", "sauce", "dried", "canned", "puree",
                   "chips", "crisps", "snack", "snacks", "based"}


def map_category(categories_tags: list[str], categories_text: str = "") -> str:
    """Map OFF categories to one of our 9 buckets via per-token tag matching."""
    tags = []
    for t in (categories_tags or []):
        t = t.lower()
        if ":" in t:
            t = t.split(":", 1)[1]
        tags.append(t)
    # fold in free-text categories as pseudo-tags
    if categories_text:
        for part in categories_text.lower().replace(" ", "-").split(","):
            part = part.strip()
            if part:
                tags.append(part)

    tokenized = [(t, set(t.split("-"))) for t in tags]
    for bucket, needles in _CATEGORY_RULES:
        for tag, toks in tokenized:
            if "based" in toks:  # skip umbrella tags
                continue
            if bucket == "produce" and (toks & _PRODUCE_REJECT):
                continue
            if tag in needles or (toks & needles):
                return bucket
    return "other"


# ---------------------------------------------------------------------------
# Generic-name derivation: turn a marketing product name into a concise generic
# name, e.g. "Skippy Creamy Peanut Butter, 16.3 oz" -> "peanut butter".
# ---------------------------------------------------------------------------
_FILLER = {
    "original", "classic", "premium", "value", "natural", "organic", "fresh",
    "creamy", "crunchy", "smooth", "extra", "new", "improved", "family",
    "size", "pack", "count", "ct", "the", "a", "of", "with", "and", "for",
    "flavor", "flavored", "flavour", "style", "brand", "great", "real",
    "less", "lite", "light", "reduced", "low", "fat", "free", "no", "added",
    "sugar", "gluten", "made", "our", "your", "all", "pure", "whole",
    "select", "signature", "kirkland", "double", "big", "mini", "jumbo",
    "delicious", "tasty", "gourmet", "artisan", "homestyle", "home", "style",
}
_UNIT_RE = re.compile(
    r"\b\d+([.,]\d+)?\s?(oz|g|kg|mg|ml|l|lb|lbs|ct|count|pack|pk|fl|floz|"
    r"gal|qt|pt|inch|in|cm|mm|x)\b", re.I)
_NUM_RE = re.compile(r"\b\d+([.,]\d+)?\b")
_PAREN_RE = re.compile(r"\([^)]*\)")


def _strip_brand(name: str, brand: str | None) -> str:
    if not brand:
        return name
    for b in re.split(r"[,;/]", brand):
        b = b.strip()
        if not b:
            continue
        # remove leading brand occurrence, case-insensitive
        name = re.sub(r"^\s*" + re.escape(b) + r"\b[\s,'\-:]*", "", name,
                      flags=re.I)
        # also remove brand anywhere as a whole word
        name = re.sub(r"\b" + re.escape(b) + r"\b", " ", name, flags=re.I)
    return name


def derive_generic_name(product_name: str, brand: str | None = None,
                        generic_name: str | None = None,
                        category: str = "other") -> str:
    """Produce a short generic name. Prefers OFF generic_name if it is short and
    sane; otherwise cleans the marketing product_name."""
    # 1) OFF generic_name is often exactly what we want.
    for cand in (generic_name, product_name):
        if not cand:
            continue
        s = cand.strip()
        s = _PAREN_RE.sub(" ", s)
        s = _strip_brand(s, brand)
        s = s.replace("&", " and ")
        s = _UNIT_RE.sub(" ", s)
        s = _NUM_RE.sub(" ", s)
        s = re.sub(r"[^a-zA-Z\s\-]", " ", s)
        toks = [t for t in re.split(r"[\s\-]+", s.lower()) if t]
        toks = [t for t in toks if t not in _FILLER and len(t) > 1]
        if not toks:
            continue
        # cap to the last 3 tokens (the head noun usually trails the modifiers)
        name = " ".join(toks[-3:]) if len(toks) > 3 else " ".join(toks)
        name = name.strip()
        if 2 <= len(name) <= 40:
            return name
    # 2) Fall back to the category label.
    return category if category != "other" else "food item"


def is_english(text: str) -> bool:
    """Cheap ASCII-ratio heuristic for English product names."""
    if not text:
        return False
    letters = [c for c in text if c.isalpha()]
    if len(letters) < 2:
        return False
    ascii_letters = [c for c in letters if ord(c) < 128]
    return len(ascii_letters) / len(letters) >= 0.9
