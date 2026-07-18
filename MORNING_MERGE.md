# Morning Merge: Getting the Fine-Tuned LoRA into the App

For the teammate training the LoRA on the DGX Spark. Follow this exactly and the merge takes minutes.

## What to produce on the Spark

1. Merge the LoRA into the base model (Unsloth merge step).
2. Convert with a **fresh llama.cpp checkout from July 2026**. Do NOT use a checkout older than June 4, 2026: `convert_hf_to_gguf.py` had a vision-metadata bug before then that silently breaks image input on the phone.
3. Quantize the main model to **Q3_K_S** (NOT Q4_0: we proved overnight that Q4-size weights crash the phone with an out-of-memory kill; Q3_K_S is the largest size that fits comfortably and needs no imatrix calibration). Command: `llama-quantize model-f16.gguf out.gguf Q3_K_S`.
4. Name the file: `gemma-4-E4B-it-finetuned-Q3_K_S.gguf`
5. You do NOT need to produce an mmproj file. Vision layers were frozen during training, so the app keeps using our verified stock vision projector (`mmproj-gemma-4-E4B-it-Q8_0.gguf`).

## How to deliver it

- Upload to a Hugging Face repo (private is fine) or AirDrop/USB the file to Phil's Mac.
- Do NOT commit the GGUF to this git repo. It is 4-5GB and will break the repo.

## What happens on our side

1. Quick sanity check on the Mac (2 minutes, catches a broken conversion before it wastes phone time):
   ```bash
   llama-mtmd-cli -m gemma-4-E4B-it-finetuned-Q4_0.gguf \
     --mmproj ~/.cache/huggingface/hub/models--ggml-org--gemma-4-E4B-it-GGUF/snapshots/06f24bb269339b2a19a5167199b81e89ef813c10/mmproj-gemma-4-E4B-it-Q8_0.gguf \
     --image <any grocery photo> --jinja -p "What grocery item is this?" -n 64
   ```
2. Run the eval harness against it for the writeup's before/after table:
   ```bash
   cd eval
   MODEL=/path/to/gemma-4-E4B-it-finetuned-Q4_0.gguf ./server.sh &
   python3 run_eval.py --mode both --run-label finetuned
   pkill -f 'llama-server.*8090'
   ```
3. Copy the file into the app's Documents folder on the iPhone (Finder file sharing) and select it in the app's Settings > Model picker. No code changes, no rebuild.

## Also send us for the writeup

- Training set: source, image count, license (Open Food Facts subset expected).
- Training config: base model, LoRA rank, epochs, which layers frozen.
- Wall-clock training time on the Spark.
