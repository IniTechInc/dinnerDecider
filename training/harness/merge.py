#!/usr/bin/env python
"""Merge a LoRA adapter into the base Gemma 4 E4B and save a full checkpoint.

peft merge_and_unload -> save_pretrained. The saved dir is a standard
Gemma4ForConditionalGeneration checkpoint that convert_hf_to_gguf.py accepts.
"""

import argparse
import os
import shutil

import torch
from peft import PeftModel
from transformers import AutoProcessor, Gemma4ForConditionalGeneration

DEFAULT_MODEL = os.path.expanduser(
    "~/.cache/huggingface/hub/models--unsloth--gemma-4-E4B-it/snapshots"
)


def resolve_model_dir(path):
    if path and os.path.isdir(path) and os.path.exists(os.path.join(path, "config.json")):
        return path
    snaps = [os.path.join(path, d) for d in os.listdir(path)] if os.path.isdir(path) else []
    snaps = [d for d in snaps if os.path.exists(os.path.join(d, "config.json"))]
    if not snaps:
        raise SystemExit(f"No model config.json under {path}")
    return sorted(snaps)[-1]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--adapter", required=True)
    ap.add_argument("--model", default=DEFAULT_MODEL)
    ap.add_argument("--output", required=True)
    args = ap.parse_args()

    model_dir = resolve_model_dir(args.model)
    print(f"[merge] base={model_dir} adapter={args.adapter}")
    # merge on CPU to keep it simple and memory-safe
    model = Gemma4ForConditionalGeneration.from_pretrained(model_dir, dtype=torch.bfloat16)
    model = PeftModel.from_pretrained(model, args.adapter)
    print("[merge] merge_and_unload ...")
    model = model.merge_and_unload()
    os.makedirs(args.output, exist_ok=True)
    model.save_pretrained(args.output, safe_serialization=True)

    # copy processor / tokenizer / chat template so the converter has everything
    processor = AutoProcessor.from_pretrained(model_dir)
    processor.save_pretrained(args.output)
    for fn in ("chat_template.jinja", "generation_config.json"):
        src = os.path.join(model_dir, fn)
        if os.path.exists(src):
            shutil.copy(src, os.path.join(args.output, fn))
    print(f"[merge] merged checkpoint saved to {args.output}")


if __name__ == "__main__":
    main()
