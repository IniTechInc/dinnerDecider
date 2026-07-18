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
| 1 | 2026-07-18 13:40 | iPhone 16 Pro Max | unknown (1 or 2, pre-build-3) | Crash right as image analysis starts during a scan | 🔧 In progress | - |

---

## Details

### #1: Crash at start of image analysis (crash 3 of the crash mission)
- **Source:** Phil, direct chat report 1:40 PM MDT (no TestFlight crash submission came through; ASC shows zero crash and zero feedback submissions, typical for jetsam memory kills)
- **Reported:** against an unknown build. Build 3 only became installable ~1:38 PM, so this was build 1 or 2. Build number pending Phil's answer.
- **Comment:** "it crashed again at the part that it was starting to analyze the image"
- **Root cause:** suspected marginal wired-memory OOM (vm-pageshortage jetsam) at the moment the vision projector encodes the image, per handoff escalation plan. If Phil was on build 1, the ModelLifecycle use-after-free (fixed in build 2) is still a candidate. Awaiting: build number, crash style (vanish vs freeze), Analytics Data log.
- **Fix, staged ladder:**
  - Build 3 (already live, attached to Team Internal): ModelLifecycle state machine + IQ3_XXS + context 1536. Phil retesting on it now = the verdict test.
  - Build 4 (uploading): context 1536 -> 1024, less wired KV cache. Held unattached as a spare.
  - Build 5 (prepped, not built): vendored LocalLLMClient fork in `Vendor/LocalLLMClient` with mmproj `use_gpu=false`, saves ~534MB wired; slower image encode. Ship only if 3 and 4 both crash.
- **Status:** 🔧 In progress

---

## Open follow-ups / decisions tied to feedback

- ❓ Which build was Phil on when it crashed (TestFlight showed 1, 2, or 3)?
- ❓ Crash style: app vanished to home screen (jetsam) or froze first?
- ❓ Analytics Data screenshot (Settings > Privacy & Security > Analytics & Improvements > Analytics Data, JetsamEvent-* or DinnerDecider-* around 1:35-1:40 PM).
