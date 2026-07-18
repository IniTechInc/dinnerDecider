# TestFlight Feedback Tracker — <APP NAME>

Single source of truth for every piece of TestFlight beta feedback: what came in, whether it's fixed, and which build fixed it. **Update this whenever feedback is pulled or a fix ships** so resolved items are never re-investigated.

App: `<APP_ID>` · Bundle: `<BUNDLE_ID>` · Team: `<TEAM_NAME / TEAM_ID>`

---

## The remote loop

1. User tests on TestFlight → submits feedback (comment + screenshot + device).
2. Claude pulls it: `asc testflight feedback list --app <APP_ID>` (confirm `asc auth status` default profile first).
3. Claude triages → fixes → ships the next build headlessly → records it here.

Status legend: ✅ Addressed · 🔧 In progress · ⏳ Open / not started · 🚫 Won't fix (with reason) · ❓ Needs user decision

---

## Feedback log

| # | Date (UTC) | Device | Reported build | Summary | Status | Fixed in |
|---|-----------|--------|----------------|---------|--------|----------|
| 1 | YYYY-MM-DD HH:MM | <device>, iOS <ver> | build <n> | <one-line summary> | ⏳ Open | — |

---

## Details

### #1 — <short title>
- **Feedback ID:** `<id>`
- **Reported:** <date> UTC, <device>, iOS <ver>, against build <n>
- **Comment:** "<verbatim user comment>"
- **Root cause:** <once known>
- **Fix:** <what changed + commit/build>
- **Status:** ⏳ Open

---

## Open follow-ups / decisions tied to feedback

- ❓ <any open product/design decision tied to a feedback item>
