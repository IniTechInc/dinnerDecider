# dinnerDecider recognition accuracy

Grocery-item recognition of Gemma 4 E4B on labeled fridge/pantry photos.
Recall = fraction of ground-truth items found; Precision = fraction of predicted items that are correct; F1 = harmonic mean of the two means.
Match rule: normalized fuzzy match (lowercase, brand/filler words stripped, singular/plural folded, similarity >= 0.75).

| Run | Mode | Photos | Mean Recall | Mean Precision | F1 |
|---|---|---:|---:|---:|---:|
| stock-IQ3_XXS | Baseline (whole-scene prompt) | 7 | 16.1% | 26.3% | 20.0% |
| stock-IQ3_XXS | Pipeline (crop + OCR fusion) | 7 | 21.2% | 23.9% | 22.5% |

| stock-IQ3_XXS | Union (whole-image + crop passes, as shipped) | 7 | 33.5% | 27.2% | 30.0% |

_Generated 2026-07-18 13:23:29._
