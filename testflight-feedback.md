# TestFlight Feedback Tracker: DinnerDecider

Single source of truth for every piece of TestFlight beta feedback: what came in, whether it's fixed, and which build fixed it. **Update this whenever feedback is pulled or a fix ships** so resolved items are never re-investigated.

App: `6792239464` · Bundle: `com.philwoolley.dinnerdecider` · Team: Personal / `AXW4GKUTKZ`

---

## The remote loop

1. Phil tests on TestFlight, sends feedback (comment + screenshot + device) or reports directly in chat.
2. Claude pulls it: `asc testflight feedback list --app 6792239464` and `asc testflight crashes list --app 6792239464` (confirm `asc auth status` default profile is `Samplomatic` first).
3. Claude triages, fixes, ships the next build headlessly, records it here.

Status legend: ✅ Addressed · 🔧 In progress · ⏳ Open / not started · 🚫 Won't fix (with reason) · ❓ Needs user decision

---

## Feedback log

| # | Date (MDT) | Device | Reported build | Summary | Status | Fixed in |
|---|-----------|--------|----------------|---------|--------|----------|
| 1 | 2026-07-18 13:40 | iPhone 16 Pro Max | unknown (1 or 2, pre-build-3) | Crash right as image analysis starts during a scan | 🔧 In progress | build 5 (memory levers) |
| 2 | 2026-07-18 14:05 | iPhone 16 Pro Max, iOS 26.5.2 | 3 | Scan counted 9 items analyzing, then "Nothing spotted" (screenshot: clearly visible labeled milk jugs) | ✅ Session fix verified (build 4 did real inference); superseded by #4 | build 4 |
| 4 | 2026-07-18 14:40 | iPhone 16 Pro Max | 4 | Crash while identifying item 4 of 9 (real inference confirmed working through item 3, then memory ceiling) | 🔧 In progress | build 5 (memory levers) |
| 3 | 2026-07-18 ~13:55 | iPhone 16 Pro Max | 3 | Recipe quality: invented "cheesy Italian dip" from ketchup + cheddar; wants real recipes + missing-item marking + shopping list flow | 🔧 In progress | build 4 (prompt fix; UI existed) |

---

## Details

### #1: Crash at start of image analysis (crash 3 of the crash mission)
- **Source:** Phil, direct chat report 1:40 PM MDT (no TestFlight crash submission came through; ASC shows zero crash and zero feedback submissions, typical for jetsam memory kills)
- **Reported:** against an unknown build. Build 3 only became installable ~1:38 PM, so this was build 1 or 2. Build number pending Phil's answer.
- **Comment:** "it crashed again at the part that it was starting to analyze the image"
- **Root cause:** suspected marginal wired-memory OOM (vm-pageshortage jetsam) at the moment the vision projector encodes the image, per handoff escalation plan. If Phil was on build 1, the ModelLifecycle use-after-free (fixed in build 2) is still a candidate. Awaiting: build number, crash style (vanish vs freeze), Analytics Data log.
- **Fix, staged ladder:**
  - Build 3 (live, attached to Team Internal): ModelLifecycle state machine + IQ3_XXS + context 1536. Result: no crash but scan produced zero items (see #2); load-crash still reported once at ~1:55 PM.
  - Build 5 (VALID 14:55 MDT, attached to Team Internal): CPU-mmproj fork + batch 256 + taste wizard redesign + real recipes. Build 4 (VALID 14:19): lifecycle sessions fix (see #2), context 1024, accuracy improvements, real-recipes prompt. Manual-signing export path (dist cert A25BKU2237 + profile 48Q9M7QM6X) replaced the broken cloud-signing export.
- **Status:** 🔧 In progress (crash-at-load may be environmental low-memory jetsam; close apps / reboot before scans)

### #2: Scan runs all crops, finds nothing (ROOT CAUSE FOUND)
- **Feedback ID:** `AF7U_ZA007GV0K9VouADLYM` (screenshot: `scratchpad/tf_feedback_scan_empty.jpg`, milk jugs clearly visible, "Nothing spotted")
- **Reported:** 2026-07-18 20:05 UTC, iPhone 16 Pro Max, iOS 26.5.2, build 3
- **Comment:** "The image recognition didn't work at all. It counted 9 items that it was analyzing but then said nothing is recognized."
- **Root cause:** after the multi-GB model load the system reliably delivers a memory warning. The moments between per-crop inferences are lifecycle state `.ready`, where the pressure handler treated the model as idle and freed it. Every remaining identify then threw modelNotLoaded, silently caught, scan ended "Nothing spotted". Also explains build 1's crash-at-analyze (its handler freed the client mid-inference, use-after-free).
- **Fix:** commit 45b99c3. Scans and recipe runs are one lifecycle "session": `.warning` ignored during a session, `.critical` deferred to session end. Repro tests in ModelLifecycleTests.
- **Status:** 🔧 In progress, ships in build 4, verify on device
- **Follow-up (P2): ADDRESSED Sept 14, 2026 (commit b9268ef).** Inference failures are now
  counted at both swallow sites, `ScanPhase.failed` is distinct from `.empty`, and the
  decision lives in a pure `ScanOutcome.terminalPhase` covered by six tests. A scan where
  every model call throws now says the scan could not finish instead of blaming the photo.
  NOT yet verified on a physical device.

### #4: Build 4 crash at item 4 of 9 (memory ceiling during real inference)
- **Source:** Phil, chat, ~2:40 PM MDT, build 4
- **What it proves:** the #2 session fix works (model stayed resident, items 1-3 got real inference). The crash moved to the true wired-memory ceiling mid-scan.
- **Fix (build 5):** vendored LocalLLMClient fork (Vendor/LocalLLMClient) with mmproj use_gpu=false (~534MB wired saved, slower image encode); batch 512 -> 256 (smaller compute buffer); CIContext exposure pass moved to software renderer. Escalation remaining if this fails: UD-IQ2_M quant.
- **Awaiting:** crash log screenshot from Analytics Data to confirm JetsamEvent (asked 3x).
- **Status:** 🔧 In progress

### #3: Real recipes + shopping list flow (chat request)
- **Request:** real recognizable recipes (not inventory-invented fakes), mark have/missing per ingredient, add-missing-to-shopping-list button, persistent shopping list.
- **Already existed:** per-ingredient hasIt marking, missingItems, "Add to shopping list" button, persistent list (SwiftData).
- **Fix:** prompt rewritten (45b99c3): demand real well-known dishes with genuine ingredients, honest hasIt against inventory, up to 3 missing items in almostThere. makeNow inventory-only rule removed.
- **Status:** 🔧 In progress, ships in build 4; recipe quality needs on-device judgment (P1 for demo)

---

## Build status since this tracker was last updated

Builds 6, 7 and 8 shipped after the last entry below and were never logged here.
Build 8 (`658a685`) is the current head of main: GPU vision restored, context reset
between requests, 4-call scan budget. **None of builds 6 to 8 have been verified on a
physical device**, so items #1 and #4 remain open on evidence, not on code.

Sept 14, 2026 session (branch `fix/kroger-store-picker-flow`), all simulator-verified
at 138 tests green, none device-verified:

- `b9268ef` P2 failed-vs-empty scan state (see #2 follow-up)
- `8d12822` Kroger sign-in and store-picker flow reachable from the shopping list
- `c1a8897` fixed the documented test command, which named an ambiguous simulator and
  exited 0 without building anything

### Recall is capped by the call budget, not the model

`CropService.tileGrid` is commented as a 3x3 grid but actually emits 4x4 = 16 tiles
(`stepX` is `tileWidth * 0.8`). The scan budget allows 4 model calls scan-wide, so at
most 4 of those 16 tiles are ever examined. That is the most plausible cause of the
33.5% recall in `eval/RESULTS.md`: the model is barely being asked. Raising the budget
is a one-line change but reintroduces jetsam risk on an 8GB phone, so it needs an eval
run and a device test together. Left unchanged for now.

## Open follow-ups / decisions tied to feedback

- ❓ Settings > Model on Phil's phone: confirm "Weights file" is Auto or UD-IQ3_XXS, NOT the 5.1GB Q4_0 (a manual pin survives updates and would jetsam every load).
- Recommended: delete orphaned `gemma-4-E4B-it-Q4_0.gguf` in Files app (frees 4.3GB, removes pin risk).
- Demo hygiene: reboot phone / close other apps before scanning; the load itself can still jetsam on a memory-starved phone.
- ❓ Analytics Data crash log (JetsamEvent-* vs DinnerDecider-*) still wanted to confirm the ~1:55 PM load-crash was a memory kill.
