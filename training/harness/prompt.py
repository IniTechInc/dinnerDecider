"""Shared prompt construction.

The user prompt MUST match DinnerDecider/Services/LLM/GemmaLLMService.swift
(identifyItem) verbatim, so the fine-tuned model sees exactly what production
sends. The Swift string is a multi-line literal joined with `\\` continuations,
which collapses to a single line with single spaces. Reproduced below.
"""

import json

CATEGORIES = "produce|dairy|meat|pantry|snack|beverage|condiment|frozen|other"


def build_user_prompt(ocr_text: str) -> str:
    """Verbatim port of GemmaLLMService.identifyItem's prompt."""
    trimmed = (ocr_text or "").strip()
    ocr_line = trimmed if trimmed else "none"
    return (
        "You identify a single grocery item from the photo. "
        'If no grocery item is clearly visible, use name "unknown" with confidence 0. '
        f"Text found on the packaging: {ocr_line}. "
        "Respond ONLY with JSON, no other words: "
        '{"name": "...", "brand": "... or null", '
        f'"category": "{CATEGORIES}", '
        '"confidence": 0-1}'
    )


def build_target_json(label: dict) -> str:
    """Compact single-line JSON assistant response.

    Field order matches the production schema (name, brand, category,
    confidence). brand may be null.
    """
    obj = {
        "name": label["name"],
        "brand": label.get("brand"),
        "category": label["category"],
        "confidence": label["confidence"],
    }
    return json.dumps(obj, separators=(",", ":"), ensure_ascii=False)
