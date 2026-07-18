#!/usr/bin/env python
"""Local LoRA fine-tune of Gemma 4 E4B (text side only, vision+audio frozen).

Trains a language-side LoRA so the model emits the exact compact-JSON grocery
label that DinnerDecider expects, given the production prompt + the packaging
photo. The vision tower and audio tower run forward (frozen); only the text
attention/MLP projections get LoRA adapters.

Usage:
  python train.py --data smoke_data/train.jsonl --epochs 3 --lr 2e-4 \
      --rank 16 --max-steps 20 --output out/adapter

Prints per-step loss and tok/s. Saves a PEFT adapter (resumable: re-running
with an existing --output resumes from the saved adapter).
"""

import argparse
import json
import os
import time

import torch
from PIL import Image
from peft import LoraConfig, PeftModel, get_peft_model
from transformers import AutoProcessor, Gemma4ForConditionalGeneration

from prompt import build_target_json, build_user_prompt

DEFAULT_MODEL = os.path.expanduser(
    "~/.cache/huggingface/hub/models--unsloth--gemma-4-E4B-it/snapshots"
)
TARGET_LEAVES = {"q_proj", "k_proj", "v_proj", "o_proj", "gate_proj", "up_proj", "down_proj"}


def resolve_model_dir(path):
    if path and os.path.isdir(path) and os.path.exists(os.path.join(path, "config.json")):
        return path
    # snapshots parent -> pick the single snapshot dir
    snaps = [os.path.join(path, d) for d in os.listdir(path)] if os.path.isdir(path) else []
    snaps = [d for d in snaps if os.path.exists(os.path.join(d, "config.json"))]
    if not snaps:
        raise SystemExit(f"No model config.json found under {path}")
    return sorted(snaps)[-1]


def load_dataset(data_path):
    base = os.path.dirname(os.path.abspath(data_path))
    rows = []
    with open(data_path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            r = json.loads(line)
            img_path = r["image"]
            if not os.path.isabs(img_path):
                img_path = os.path.join(base, img_path)
            rows.append((img_path, r["ocr"], r["label"]))
    return rows


def find_language_targets(model):
    """Full module names of Linear projections inside the language model only."""
    # locate the language model submodule
    lm = None
    for name, mod in model.named_modules():
        if name.endswith("language_model") and hasattr(mod, "layers"):
            lm = name
            break
    targets = []
    for name, mod in model.named_modules():
        if lm and not name.startswith(lm):
            continue
        leaf = name.split(".")[-1]
        if leaf in TARGET_LEAVES and mod.__class__.__name__.endswith("Linear"):
            targets.append(name)
    return targets, lm


def build_example(processor, image, ocr, label, device, dtype):
    user_prompt = build_user_prompt(ocr)
    target = build_target_json(label)
    user_msgs = [{"role": "user", "content": [{"type": "image"}, {"type": "text", "text": user_prompt}]}]
    full_msgs = user_msgs + [{"role": "assistant", "content": [{"type": "text", "text": target}]}]

    prefix_text = processor.apply_chat_template(user_msgs, add_generation_prompt=True, tokenize=False)
    full_text = processor.apply_chat_template(full_msgs, add_generation_prompt=False, tokenize=False)

    full = processor(text=full_text, images=[image], return_tensors="pt", add_special_tokens=False)
    prefix = processor(text=prefix_text, images=[image], return_tensors="pt", add_special_tokens=False)
    prefix_len = prefix["input_ids"].shape[1]

    labels = full["input_ids"].clone()
    labels[:, :prefix_len] = -100
    full["labels"] = labels

    out = {}
    for k, v in full.items():
        if isinstance(v, torch.Tensor):
            if v.dtype.is_floating_point:
                v = v.to(dtype)
            out[k] = v.to(device)
        else:
            out[k] = v
    n_target = int((labels != -100).sum().item())
    return out, n_target


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--data", required=True)
    ap.add_argument("--model", default=DEFAULT_MODEL)
    ap.add_argument("--epochs", type=int, default=3)
    ap.add_argument("--lr", type=float, default=2e-4)
    ap.add_argument("--rank", type=int, default=16)
    ap.add_argument("--alpha", type=int, default=32)
    ap.add_argument("--dropout", type=float, default=0.05)
    ap.add_argument("--max-steps", type=int, default=0, help="0 = no cap")
    ap.add_argument("--output", required=True)
    ap.add_argument("--grad-accum", type=int, default=1)
    args = ap.parse_args()

    device = "mps" if torch.backends.mps.is_available() else "cpu"
    dtype = torch.bfloat16
    model_dir = resolve_model_dir(args.model)
    print(f"[init] device={device} dtype=bf16 model={model_dir}")

    processor = AutoProcessor.from_pretrained(model_dir)
    t0 = time.time()
    model = Gemma4ForConditionalGeneration.from_pretrained(model_dir, dtype=dtype)
    model.to(device)
    print(f"[init] base model loaded in {time.time()-t0:.1f}s")

    targets, lm_name = find_language_targets(model)
    print(f"[lora] language_model module = {lm_name}; {len(targets)} target linears")
    if not targets:
        raise SystemExit("No LoRA target modules found in language model.")

    resume = os.path.isdir(args.output) and os.path.exists(os.path.join(args.output, "adapter_config.json"))
    if resume:
        print(f"[lora] resuming adapter from {args.output}")
        model = PeftModel.from_pretrained(model, args.output, is_trainable=True)
    else:
        lora = LoraConfig(
            r=args.rank, lora_alpha=args.alpha, lora_dropout=args.dropout,
            target_modules=targets, task_type="CAUSAL_LM", bias="none",
        )
        model = get_peft_model(model, lora)
    model.train()

    trainable = sum(p.numel() for p in model.parameters() if p.requires_grad)
    total = sum(p.numel() for p in model.parameters())
    print(f"[lora] trainable params {trainable:,} / {total:,} ({100*trainable/total:.3f}%)")

    # sanity: vision/audio must be frozen
    frozen_vision = all(
        not p.requires_grad
        for n, p in model.named_parameters()
        if "vision" in n or "audio" in n or "multi_modal" in n or "mm_" in n
    )
    print(f"[lora] vision/audio/projector frozen = {frozen_vision}")

    data = load_dataset(args.data)
    print(f"[data] {len(data)} examples from {args.data}")

    opt = torch.optim.AdamW([p for p in model.parameters() if p.requires_grad], lr=args.lr)

    step = 0
    accum = 0
    opt.zero_grad()
    tok_accum = 0
    win_t0 = time.time()
    stop = False
    for epoch in range(args.epochs):
        if stop:
            break
        for (img_path, ocr, label) in data:
            image = Image.open(img_path).convert("RGB")
            inputs, n_target = build_example(processor, image, ocr, label, device, dtype)
            n_tokens = inputs["input_ids"].shape[1]

            out = model(**inputs)
            loss = out.loss
            (loss / args.grad_accum).backward()
            accum += 1
            tok_accum += n_tokens

            if accum >= args.grad_accum:
                opt.step()
                opt.zero_grad()
                accum = 0
                step += 1
                if device == "mps":
                    torch.mps.synchronize()
                dt = time.time() - win_t0
                tps = tok_accum / dt if dt > 0 else 0.0
                mem = torch.mps.current_allocated_memory() / 1e9 if device == "mps" else 0.0
                print(f"[step {step:04d}] loss={loss.item():.4f} "
                      f"sec/step={dt:.2f} tok/s={tps:.1f} "
                      f"tgt_tok={n_target} mps_alloc={mem:.2f}GB", flush=True)
                tok_accum = 0
                win_t0 = time.time()
                if args.max_steps and step >= args.max_steps:
                    stop = True
                    break

    os.makedirs(args.output, exist_ok=True)
    model.save_pretrained(args.output)
    print(f"[done] adapter saved to {args.output} after {step} steps")
    if device == "mps":
        print(f"[mem] peak mps allocated = {torch.mps.driver_allocated_memory()/1e9:.2f}GB")


if __name__ == "__main__":
    main()
