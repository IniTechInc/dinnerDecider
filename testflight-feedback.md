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
  - Build 5 (prepped, not built): vendored LocalLLMClient fork in `Vendor/LocalLLMClient` with mmproj `use_gpu=false`, saves ~534MB wired; slower image encode. Ship only if build 4 still jetsams at load.
- **Status:** 🔧 In progress (crash-at-load may be environmental low-memory jetsam; close apps / reboot before scans)

### #2: Scan runs all crops, finds nothing (ROOT CAUSE FOUND)
- **Feedback ID:** `AF7U_ZA007GV0K9VouADLYM` (screenshot: `scratchpad/tf_feedback_scan_empty.jpg`, milk jugs clearly visible, "Nothing spotted")
- **Reported:** 2026-07-18 20:05 UTC, iPhone 16 Pro Max, iOS 26.5.2, build 3
- **Comment:** "The image recognition didn't work at all. It counted 9 items that it was analyzing but then said nothing is recognized."
- **Root cause:** after the multi-GB model load the system reliably delivers a memory warning. The moments between per-crop inferences are lifecycle state `.ready`, where the pressure handler treated the model as idle and freed it. Every remaining identify then threw modelNotLoaded, silently caught, scan ended "Nothing spotted". Also explains build 1's crash-at-analyze (its handler freed the client mid-inference, use-after-free).
- **Fix:** commit 45b99c3. Scans and recipe runs are one lifecycle "session": `.warning` ignored during a session, `.critical` deferred to session end. Repro tests in ModelLifecycleTests.
- **Status:** 🔧 In progress, ships in build 4, verify on device
- **Follow-up (P2):** if identifies fail systemically mid-scan, the UI should say "something went wrong" instead of the misleading "Nothing spotted" empty state.

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

## Open follow-ups / decisions tied to feedback

- ❓ Settings > Model on Phil's phone: confirm "Weights file" is Auto or UD-IQ3_XXS, NOT the 5.1GB Q4_0 (a manual pin survives updates and would jetsam every load).
- Recommended: delete orphaned `gemma-4-E4B-it-Q4_0.gguf` in Files app (frees 4.3GB, removes pin risk).
- Demo hygiene: reboot phone / close other apps before scanning; the load itself can still jetsam on a memory-starved phone.
- ❓ Analytics Data crash log (JetsamEvent-* vs DinnerDecider-*) still wanted to confirm the ~1:55 PM load-crash was a memory kill.
