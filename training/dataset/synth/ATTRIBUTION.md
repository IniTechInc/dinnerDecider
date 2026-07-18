# Attribution: Synthetic Training Data

The synthetic training set in this directory is derived from product data and
photographs published by **Open Food Facts**.

## Source

- **Open Food Facts** — https://world.openfoodfacts.org
- Product names, brands, categories, and front-of-pack photographs were fetched
  from the Open Food Facts API (`/api/v2/search`), filtered to US-market
  products with an English name and a front image.

## License

Open Food Facts data is made available under the **Open Database License (ODbL)
v1.0**, and the individual product images under the **Creative Commons
Attribution-ShareAlike (CC BY-SA)** license. Contents of the database are
licensed under the **Database Contents License (DbCL) v1.0**.

- ODbL: https://opendatacommons.org/licenses/odbl/1-0/
- Full terms: https://world.openfoodfacts.org/terms-of-use

Per the ODbL, this derived dataset:
- **credits** Open Food Facts as the source (this file);
- is itself shared under the same **ODbL** terms if redistributed;
- preserves provenance: every product record in `products.jsonl` retains its
  Open Food Facts product URL (`off_url`) and barcode (`code`).

## Contents

- **Source product images used:** 550 (all from Open Food Facts).
- **Categories** (mapped to the app's 9 buckets): produce, dairy, meat, pantry,
  snack, beverage, condiment, frozen, other.
- **Synthetic shelf scenes** and the **training crops** (`crops/`, `crops.jsonl`)
  are procedurally composited from these source images by
  `training/synth_tools/compose.py`. OCR text on each crop is computed on-device
  with Apple Vision (`VNRecognizeTextRequest`).
- A small procedural produce-image fallback exists in `fetch_off.py` for the
  case where Open Food Facts produce coverage is weak; it was **not** needed for
  this build (0 generated images).

## Regeneration

```bash
cd training/synth_tools
python3 fetch_off.py --target 400 --produce 40   # -> dataset/synth/products/
python3 compose.py  --scenes 230 --negatives 320 # -> dataset/synth/crops/ + crops.jsonl
```
