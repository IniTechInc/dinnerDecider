# Session Handoff: dinnerDecider (Gemma 4 Hackathon)

Written July 17, 2026, ~11:30 PM MDT. For the next Claude session picking this up.

## The clock

Kaggle Writeup due **Saturday July 18, 3:00 PM MDT**. Live 3-min demo 3:00 to 4:30 PM. Judges disable network during the demo; all Gemma 4 inference must run on the demonstrated hardware or the project is ineligible.

## What is DONE

1. **Deep research completed and verified** (106-agent fact-checked run). Full verdict in `IMPLEMENTATION_SPEC.md` (repo root). Short version: build with stock Gemma 4 E4B on the iPhone via llama.cpp GGUF+mmproj; boost accuracy with crop-then-ask + Apple Vision OCR fed into the prompt; fine-tuning is gated behind a 1-hour end-to-end smoke test because nobody has publicly shipped a Gemma 4 vision LoRA to a phone.
2. **Model files downloaded** to this Mac's Hugging Face cache (symlinked snapshots, real sizes shown):
   - `~/.cache/huggingface/hub/models--unsloth--gemma-4-E4B-it-GGUF/snapshots/bfc15c382204943c3a8fff0c750b94ae2364d7a3/gemma-4-E4B-it-Q4_K_M.gguf` (4.6GB)
   - same dir, `mmproj-F16.gguf` (944MB, the vision projector; llama.cpp needs BOTH files)
   - `~/.cache/huggingface/hub/models--litert-community--gemma-4-E4B-it-litert-lm/snapshots/f7ad3343bd6ebc9607f4dc3bc4f2398bd5749bc5/gemma-4-E4B-it.litertlm` (3.4GB, alternate Google runtime)
3. **Memory files** written at `~/.claude/projects/-Users-philwoolley-Projects-gemma4hackathon/memory/` (project facts + research verdict).
4. Repo contains only: README.md, LICENSE, IMPLEMENTATION_SPEC.md, this file. No app code yet. Nothing committed beyond the initial commit.

## What is NOT done (the entire build)

Follow `IMPLEMENTATION_SPEC.md` for architecture, prompts, screens, and pitfalls. Build order that retires risk fastest:

1. **Prove Gemma 4 E4B answers an image question on Phil's iPhone 16 Pro Max from our own code.** Biggest unknown. Try the LocalLLMClient Swift package (wraps llama.cpp); confirm its llama.cpp is post-June-4-2026 or vision will be broken.
2. Crop + OCR + structured-output recognition pipeline.
3. Inventory UI with user confirmation, SwiftData persistence.
4. Recipes / almost-recipes / shopping list (text-only Gemma calls).
5. Eval: 20-30 labeled pantry photos, baseline vs pipeline accuracy table for the writeup (worth 20 rubric points).
6. TestFlight only if time permits; an Xcode-installed build demos identically.

## Open questions needing Phil

- **Which Apple Team for signing?** Two accounts exist: Personal (woolley.pj@gmail.com) vs Meora Studios. Hackathon project, so probably Personal, but CONFIRM before touching signing/TestFlight, and run `asc auth status` before any asc command (profile "Samplomatic" = personal team).
- **LoRA smoke test: go or skip?** Needs someone at the DGX Spark. Gate: 100-image Unsloth LoRA (freeze vision layers) → merge → convert_hf_to_gguf + mmproj → Q4_K_M → runs on phone, all within ~1 hour. If any link breaks, skip fine-tuning forever.
- **LAN fallback eligibility:** ask a hackathon organizer whether phone-to-DGX-Spark over local Wi-Fi counts as "network disabled" compliant. Do not build on this without an answer.

## Environment facts

- This Mac: M1 Max, 64GB RAM, macOS 26.3, Xcode 26.3, Python 3.13, `hf` CLI at /Library/Frameworks/Python.framework/Versions/3.13/bin/hf, Ollama installed. 58GB free disk.
- Phones: iPhone 16 Pro Max (Phil) + two iPhone 16s (teammates). A18 Pro constraint: keep prompts under ~2048 tokens (known crash above 4096 context).
- DGX Spark available for training/serving (Ollama has gemma4 in all 5 sizes if the Spark must become the demo box).

## Per Phil's instructions

Phil is a non-coder (vibe coding), has ADHD: orient him with done/in-progress/next at session start, keep chunks small and visible, plain language, and never use em dashes in anything written for this project.
