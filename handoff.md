# Session Handoff: DinnerDecider (Gemma 4 Hackathon)

Updated July 18, 2026, ~1:40 PM MDT. Previous overnight session built the entire app. Next session's PRIMARY MISSION: debug and fix the on-device crashes once and for all. Read CLAUDE.md first (coordinates, build commands, gotchas). Deadline: Kaggle writeup 3:00 PM MDT today (DONE, submitted content in writeup/KAGGLE_WRITEUP.md pending Phil's paste), live demo 3:00 to 4:30 PM.

## THE CRASH MISSION (read this whole section before touching anything)

Three distinct crashes were found. Two are proven fixed. The third is fixed in code with high confidence but NEVER VERIFIED ON DEVICE.

1. FIXED AND VERIFIED: vm-pageshortage jetsam from Q4_0 weights (5.1GB wired Metal on the 8GB phone; app footprint only 400MB; increased-memory-limit entitlement does not help wired memory). Fix: IQ3_XXS quant + context 1536. Proof: on-device self-test passed at 2:46 AM (status=OK, 824MB footprint, 21s load+identify).
2. FIXED AND VERIFIED (test-first): photo batch crash. Removing a photo then adding another trapped in Array.remove(at:) with a stale ForEach offset index. Fix: PhotoBatch value type with UUID identity, remove-by-identity. Repro test: PhotoBatchTests.
3. FIXED IN CODE, UNVERIFIED ON DEVICE: model freeze/crash when loading for a scan. Root cause (high confidence, from commit archaeology not a stack trace): the memory-pressure handler added in d2cfe76 called unloadModel() unconditionally; loading a 3.5GB model always raises system pressure; the handler freed the llama client mid-inference (use-after-free). Fix in f54856e: ModelLifecycle state machine (unloaded/loading/ready/inferring/unloading + pendingUnload) that DEFERS all unloads while loading/inferring, ignores .warning during busy phases, and skips pressure registration entirely in self-test mode. 104+ tests green including 14 lifecycle tests.

WHAT BLOCKS VERIFICATION: the Mac-to-phone developer bridge (developer disk image mount, error 12040 + InternalError -402636802) wedged and SURVIVED a phone restart, so neither cable install nor devicectl self-test could run after the fix. The wedge is likely Mac-side now.

VERIFICATION PATHS for the next session, in order of preference:
a. TestFlight (bypasses the cable entirely): build 3 contains all fixes. Phil installs from TestFlight, runs a scan with model files present. If the scan completes: crash 3 fixed, mission mostly over. Watch: Phil clicked "Team Internal group + build" attach in ASC? Unconfirmed. Check TestFlight tab in ASC.
b. Fix the Mac-side wedge for cable debugging: ask Phil to run `! sudo pkill -f remoted` (respawns via launchd), or open Xcode > Window > Devices and Simulators and click the phone (forces DDI mount, shows real error), or reboot the Mac (last resort, kills running downloads).
c. Once the bridge works: `xcrun devicectl device process launch --device 502D5057-2EAA-5CA3-9E0F-9ADDA99B891D --terminate-existing com.philwoolley.dinnerdecider --llm-selftest`, wait ~60s, pull Documents/selftest_result.txt via `devicectl device copy from --domain-type appDataContainer --domain-identifier com.philwoolley.dinnerdecider`. GOTCHA: a STALE result file from 08:46Z lives on the device; only trust a result whose timestamp is fresh. Success looks like: status=OK ... peakFootprint=~800MB.
d. Crash logs: `devicectl device copy from --domain-type systemCrashLogs` (worked once, then InternalError). Look for JetsamEvent-*.ips (reason vm-pageshortage = memory, process DinnerDecider with reason) or DinnerDecider-*.ips (code crash with stack). Phil can also read Settings > Privacy & Security > Analytics & Improvements > Analytics Data on the phone directly.

IF CRASH 3 PERSISTS after the state-machine fix, the remaining suspect is marginal wired-memory OOM (non-deterministic with system state). Escalation levers, in order: context 1536 -> 1024; fork LocalLLMClient to set mmproj use_gpu=false (saves ~534MB wired; Multimodal.swift hardcodes true) and/or partial n_gpu_layers (Model.swift hardcodes full offload); last resort UD-IQ2_M quant. All documented in CLAUDE.md Memory/OOM notes.

## Current state of everything else

- App: feature-complete on main, all tests green (104+). Design system (terracotta/sage/amber + DM Serif Display/DM Sans), icon, onboarding, in-app model downloader (background URLSession, resume, integrity), voice mood input (on-device speech forced for airplane mode), two-pass scan (whole-image + per-crop), unknown/low-confidence filtering.
- TestFlight: build 1 (WAITING_FOR_REVIEW for external, contains crash 3), build 2 VALID (crash fix, internal-ready), build 3 (everything incl. voice) was uploading + polling when this handoff was written; verify with `asc builds list --app 6792239464`. Groups: "DinnerDecider Beta" external (96f19d6b-57f9-4762-bdde-cf1c8a7cbfdb, testers Jordon/Blain/Monica queued pending beta review), "Team Internal" internal (d5d6594c-0a80-4c07-8406-8c64566cece6, Phil added). Build-to-group attach is UI-only; Phil clicks in ASC TestFlight tab. asc CLI profile must be "Samplomatic" (check `asc auth status`).
- Writeup: writeup/KAGGLE_WRITEUP.md finalized, committed, pushed. OPEN ITEMS: team roster line (git authors are Phil/Kurt Lehnardt/olsonjb; Blain trained the LoRA in github.com/BlainThomas/gemma-4-trained; Phil must confirm final names) and the demo video link placeholder.
- Eval (eval/RESULTS.md, committed): stock model on 7 real kitchen photos, exhaustive ~68-item ground truth: baseline 16.1% recall, crop+OCR 21.2%, as-shipped union 33.5% (2.1x baseline). Rerun: `cd eval && MODEL=<path> MMPROJ=<path> ./server.sh &` then `python3 run_eval.py --mode both --run-label <label>`; add fine-tuned row the same way.
- Fine-tune sprint (bonus, post-deadline): dataset READY at training/dataset/train.jsonl (1,288 examples: 75 verified real + 1,213 Open Food Facts synth + negatives) and holdout.jsonl (13 real). Harness READY at training/harness/ (transformers 5.14.1 Gemma4 verified, PEFT r16 text-only, merge -> convert -> Q3_K_S chain architecture-verified). BLOCKED ON: 16GB base model download (unsloth/gemma-4-E4B-it). The hf CLI is BROKEN in this environment (0 B/s, corrupts blobs with concurrent writers); a single-writer curl into the blob path was running (check `du -sh ~/.cache/huggingface/hub/models--unsloth--gemma-4-E4B-it` and ps for curl); finish with training/harness/finalize_download.sh, then ./run_smoke.sh (prints sec/step; decide epochs/subsample), then train.py / merge_convert.sh / verify.sh per training/harness/. Disk: ~20-25GB free; merge_convert.sh is disk-aware (q8_0 intermediate, cleans up).
- Team change (July 18, ~1:45 PM): BLAIN IS NO LONGER ON THE TEAM per Phil. All references to his DGX Spark LoRA (42.1 -> 60.3 synthetic result, repo github.com/BlainThomas/gemma-4-trained, "PantryLens" Android framing) were REMOVED from the Kaggle writeup and his artifacts must NOT be used in the submission or app. MORNING_MERGE.md's premise (merging his LoRA) is dead; the only fine-tune path is OUR local harness. Roster in writeup is now "IniTech (Phil Woolley, olsonjb)"; Kurt Lehnardt is a git author and remains unlisted, CONFIRM with Phil. olsonjb (Jordon?) pushes directly to main; review incoming commits (two features landed mid-session, one needed a test-conformance fix and an airplane-mode speech fix).
- Phone: iPhone 16 Pro Max, model files in app Documents (IQ3_XXS + mmproj Q8_0 + orphaned Q4_0 that can be deleted for 4.3GB space). Auto-Lock was the recurring saboteur of cable operations; ask Phil to set it to Never during debug.
- Model files on the Mac (HF cache): ONLY IQ3_XXS + mmproj Q8_0 remain (disk crisis purged the rest; all re-downloadable). Eval server and prelabeler use these.

## Phil's pending personal actions

1. ASC clicks: attach newest VALID build to "Team Internal" (and build 1 or approved build to "DinnerDecider Beta" external).
2. Kaggle: confirm team roster, paste writeup, submit before 3:00 PM.
3. Install from TestFlight, airplane-mode scan test = crash 3 verdict.
4. Talk to Blain: platform question + LoRA conversion per MORNING_MERGE.md.

## Working agreements in force (see CLAUDE.md)

Commit AND push after every landed change. Never use em dashes anywhere. Fable orchestrates, Opus subagents do bulk work, never Haiku. Bug reports get a failing repro test before the fix. Ship headlessly via asc; -skipMacroValidation always; manual signing (profiles in CLAUDE.md).
