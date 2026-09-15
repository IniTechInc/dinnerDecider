# Pipeline: model lifecycle state machine

**This is the most important document in `shared/`.** It took three TestFlight
builds to get right. A port that skips it will reproduce two specific field bugs:

- the **"Nothing spotted" empty scan** — the scan counts N items, analyzes them
  all, and reports nothing recognized (TestFlight feedback #2, build 3), and
- the **mid-scan use-after-free crash** — the app dies the moment image analysis
  starts (TestFlight feedback #1, build 1).

Both have the same root cause: the multi-GB inference client was freed while
something was still using it.

Sources of record:
`DinnerDecider/Services/ModelLifecycle.swift` (the pure machine),
`DinnerDecider/Services/AppModel.swift:110-268` (the adapter that turns its
decisions into real load/unload calls), and
`DinnerDeciderTests/ModelLifecycleTests.swift` (**the specification** — 17 tests,
several written as explicit crash repros).

## Why a state machine at all

The client holds its weights, vision projector and KV cache in **wired** GPU
buffers — several gigabytes that the OS cannot page out. Two consequences:

- The system will ask you to free memory, often, and at the worst moments.
- Freeing the client while a load or an inference is in flight is a
  **use-after-free**; starting a reload before the old teardown drains is a
  **double allocation** of several GB, which is an OOM.

Ad-hoc booleans (`isLoading`, `isLoaded`, `shouldUnload`) cannot express "free it,
but not yet, and not until the *whole scan* is done". Keep this as a **pure value
type with no I/O**, exactly as the Swift does: it is then exhaustively unit
testable without the several-GB model, and its tests run in milliseconds.

## States

| State | Meaning |
| --- | --- |
| `UNLOADED` | No client resident |
| `LOADING` | A load is in flight; no client yet |
| `READY` | Client resident and idle |
| `INFERRING` | Client resident and serving an in-flight inference |
| `UNLOADING` | A teardown is in progress |

Derived:

```text
isBusy        = state in { LOADING, INFERRING, UNLOADING }
sessionActive = activeSessionCount > 0
```

## Extra state

**`pendingUnload: bool`** — an unload was requested while it was unsafe to honor
it. Honored the instant we become idle *and* no session is active. Set by an
explicit unload request while busy, or by a `CRITICAL` memory event; **never** by
a `WARNING`.

**`activeSessionCount: int`** — how many user-visible model sessions are running.
A session is a whole scan (over N crops, across N photos) or a whole recipe
generation. **This counter is the fix for the "Nothing spotted" bug.** See below.

## Decision enums

`LoadDecision`: `PROCEED` (caller performs the real load) · `COALESCED` (a load is
already in flight, fold in) · `ALREADY_LOADED` (resident, nothing to do).

`UnloadDecision`: `PROCEED` (caller performs the real unload now) · `DEFERRED`
(busy; recorded, runs once idle) · `NOTHING_TO_DO`.

The machine **never** touches the runtime. It returns decisions; the caller
translates them. Keep that separation — it is what makes the rules testable.

## Transitions

### `beginLoad() -> LoadDecision`

| From | Returns | To |
| --- | --- | --- |
| `UNLOADED` | `PROCEED` | `LOADING` |
| `LOADING` | `COALESCED` | `LOADING` |
| `READY` | `ALREADY_LOADED` | `READY` |
| `INFERRING` | `ALREADY_LOADED` | `INFERRING` |
| `UNLOADING` | `COALESCED` | `UNLOADING` |

*Why:* duplicate loads must never start a second multi-GB allocation. A load
requested during a teardown folds in and the caller retries once idle.

### `finishLoad(success) `

| From | success | To | Side effect |
| --- | --- | --- | --- |
| `LOADING` | true | `READY` | — |
| `LOADING` | false | `UNLOADED` | `pendingUnload = false` |
| anything else | any | unchanged (guard) | — |

*Why the `pendingUnload` reset:* if the load failed there is nothing resident to
free, and leaving the flag set would make the next successful load immediately
tear itself down. Pinned by `testFailedLoadDropsDeferredUnload`.

### `beginInference() -> bool`

Returns `true` and moves `READY -> INFERRING`. Returns `false` from any other
state and changes nothing.

*Why the bool:* the caller uses it to decide whether to run the matching
`finishInference()`. Without it a caller would "close" a bracket it never opened
and corrupt the state. On iOS this is a scoped bracket that runs
`finishInference()` + drain in a `defer`, so it is exception- and cancellation-safe.

### `finishInference()`

`INFERRING -> READY`. Guarded: a no-op from any other state.

### `requestUnload() -> UnloadDecision` (explicit, non-pressure)

| From | Returns | To | Side effect |
| --- | --- | --- | --- |
| `READY` | `PROCEED` | `UNLOADING` | — |
| `LOADING` | `DEFERRED` | `LOADING` | `pendingUnload = true` |
| `INFERRING` | `DEFERRED` | `INFERRING` | `pendingUnload = true` |
| `UNLOADED` | `NOTHING_TO_DO` | `UNLOADED` | — |
| `UNLOADING` | `NOTHING_TO_DO` | `UNLOADING` | — |

Note `requestUnload` at `READY` proceeds **even during an active session** — an
explicit request from the app is trusted; only *pressure* respects sessions.

### `handleMemoryPressure(level) -> UnloadDecision`

Levels: `WARNING`, `CRITICAL`.

| From | Level | Returns | To | Side effect |
| --- | --- | --- | --- | --- |
| `UNLOADED` | either | `NOTHING_TO_DO` | `UNLOADED` | — |
| `UNLOADING` | either | `NOTHING_TO_DO` | `UNLOADING` | — |
| `READY`, **no** session | either | `PROCEED` | `UNLOADING` | — |
| `READY`, session active | `WARNING` | `NOTHING_TO_DO` | `READY` | — |
| `READY`, session active | `CRITICAL` | `DEFERRED` | `READY` | `pendingUnload = true` |
| `LOADING` | `WARNING` | `NOTHING_TO_DO` | `LOADING` | — |
| `LOADING` | `CRITICAL` | `DEFERRED` | `LOADING` | `pendingUnload = true` |
| `INFERRING` | `WARNING` | `NOTHING_TO_DO` | `INFERRING` | — |
| `INFERRING` | `CRITICAL` | `DEFERRED` | `INFERRING` | `pendingUnload = true` |

Two rules, stated as rules:

- **`WARNING` is ignored entirely while busy or mid-session.** Loading and
  inferring legitimately spike memory, so a warning during them is *expected, not
  actionable*.
- **`CRITICAL` while busy or mid-session is recorded, never acted on.** The model
  is freed the moment the work drains, and never mid-operation.

### `finishUnload()`

Unconditional: `-> UNLOADED`, `pendingUnload = false`.

### `drainPendingUnload() -> UnloadDecision`

```text
if pendingUnload AND state == READY AND NOT sessionActive:
    pendingUnload = false
    state = UNLOADING
    return PROCEED
return NOTHING_TO_DO
```

All three conditions are load-bearing. The caller runs this **after every load
and after every inference**. The `NOT sessionActive` clause is what stops a
`CRITICAL` that arrived during a scan from freeing the model between crops — "a
mid-session drain would gut the remaining crops"
(`ModelLifecycle.swift:183-184`).

### `beginSession()` / `endSession() -> UnloadDecision`

```text
beginSession():  activeSessionCount += 1

endSession():
    activeSessionCount = max(0, activeSessionCount - 1)
    if sessionActive: return NOTHING_TO_DO        # other sessions still running
    return drainPendingUnload()                   # last one out honors the deferral
```

Sessions **nest**, counted not boolean — a scan and a recipe run can overlap, and
the deferred unload fires only when the last one ends
(`testNestedSessionsOnlyEndWhenAllEnd`). The clamp at 0 makes an unbalanced
`endSession` harmless.

## The session concept — why it exists

This is the part most likely to be dropped by a port, and the part that caused
the worst bug.

A scan is N model calls. **The moments between those calls are `READY`** — the
client is resident and no inference is in flight. Under the pre-`45b99c3` rules
that looked exactly like "idle and resident", so the memory-pressure handler
freed the model right there.

And it happened every single time, because — as the source records — after the
multi-GB load *the system reliably posts a memory warning*. So:

1. Scan starts, model loads (~3.5 GB wired).
2. System posts a memory warning moments later.
3. Handler sees `READY`, frees the client.
4. Crop 2..N each call into a freed client, throw "model not loaded", and the
   scan loop catches, logs, and continues — **silently**.
5. Scan ends with zero items: **"Nothing spotted"**, from a photo of clearly
   visible labeled milk jugs.

The fix (commit `45b99c3`): a scan or recipe run is **one session**. During a
session a `WARNING` at `READY` is ignored and a `CRITICAL` is deferred to the end
of the whole session, not merely to the end of the current inference.

Outside a session the original behavior is preserved: `WARNING` at `READY` frees
the model (`testWarningAtReadyOutsideSessionStillUnloads`). Sessions are the
narrow exception, not a blanket disabling of memory response.

Session bracketing rules for a port:

- Open the session **before** `loadModelIfNeeded()`, so pressure arriving during
  the load is already covered.
- Close it on **every** exit path including cancellation and thrown errors (iOS
  uses `defer`; use your language's equivalent — Kotlin `try/finally`, Go
  `defer`).
- Closing the session is what runs a deferred unload, so never skip it on the
  cancellation path or the model stays wired forever.

## The other bug: mid-operation free (use-after-free)

TestFlight feedback #1, build 1: "it crashed again at the part that it was
starting to analyze the image". The build-1 handler freed the client **during**
an inference. The vision projector was mid-encode against buffers that had just
been released.

Guarantees that prevent it, each with a named test:

| Guarantee | Test |
| --- | --- |
| `WARNING` during `LOADING` does nothing | `testWarningPressureDuringLoadDoesNotUnload` |
| `CRITICAL` during `LOADING` defers, never frees | `testCriticalPressureDuringLoadDefersUnloadNeverFrees` |
| `WARNING` during `INFERRING` does nothing | `testWarningPressureDuringInferenceDoesNotUnload` |
| Explicit unload during `INFERRING` defers | `testExplicitUnloadWhileInferringDefers` |
| Deferred unload runs after the load completes | `testPendingUnloadExecutesAfterLoadCompletes` |
| Deferred unload runs after the inference completes | `testPendingUnloadExecutesAfterInferenceCompletes` |
| Failed load drops the deferral | `testFailedLoadDropsDeferredUnload` |
| Duplicate loads coalesce | `testDoubleLoadCoalesces` |
| Load while `READY` is a no-op | `testLoadWhenAlreadyReadyIsNoOp` |
| Pressure while `UNLOADED` is a no-op | `testPressureWhenUnloadedIsNoOp` |
| Warning between inferences **during a session** does nothing | `testWarningBetweenInferencesDuringSessionDoesNotUnload` |
| Critical between inferences defers to **session end** | `testCriticalBetweenInferencesDuringSessionDefersToSessionEnd` |
| Warning at `READY` **outside** a session still unloads | `testWarningAtReadyOutsideSessionStillUnloads` |
| Nested sessions only end when all end | `testNestedSessionsOnlyEndWhenAllEnd` |
| No pressure during a session leaves the model resident | `testEndSessionWithoutPendingUnloadKeepsModelResident` |
| `WARNING` at `READY` while **idle** proceeds and unloads | `testWarningPressureWhenReadyIdleUnloads` |
| `finishUnload` returns to `UNLOADED` and clears `pendingUnload` | `testUnloadCycleReturnsToUnloaded` |

That is **17 tests**, matching `ModelLifecycleTests.swift` exactly. Port all of them.
They are pure and synchronous; there is no excuse not to.

The last two were missing from this table until Sept 14 2026. The first of them
is load-bearing and is easy to get wrong in a port whose "model" is a supervised
child process rather than an in-process client: it pins that `WARNING` is only
ignored while the machine is BUSY (`LOADING`/`INFERRING`) or inside a session. At
`READY` and idle, a plain `WARNING` really does unload. A port that treats
`WARNING` as always-ignorable keeps a multi-GB model resident through exactly the
pressure it was supposed to relieve.

Related trap, for the same reason: **every decision in this machine is the
function's RETURN VALUE, and is never inferred from the caller's reason for
calling.** `requestUnload()` returns `PROCEED` only at `READY`; at `LOADING` and
`INFERRING` it returns `DEFERRED` and sets `pendingUnload`
(`testExplicitUnloadWhileInferringDefers`). A supervisor that maps "the operator
asked for a restart" straight to "send SIGTERM now" will kill a child mid-encode,
which is the build-1 use-after-free class wearing a different hat: the in-flight
request fails with connection-refused, the scan loop swallows it, and the user
sees a failed scan. Always act on the returned decision.

There is a **second** layer of safety in the iOS runtime adapter worth copying:
an in-flight token stream holds its own strong reference to the client it started
with, so clearing the client handle cannot tear a running stream out from under
itself (`GemmaLLMService.swift:76-85`). Belt and braces. In Go, an equivalent is
for the inference call to capture the handle by value and for teardown to release
only after outstanding calls return; in Kotlin, a reference held by the
coroutine's scope.

## Adapter obligations (the impure half)

From `AppModel.swift`:

1. **Subscribe to every memory-pressure source the platform offers**, and funnel
   them all into one idempotent handler. iOS listens to *both* the UIKit
   memory-warning notification *and* a kernel-level pressure source, because
   "UIKit only posts a plain memory warning, never a 'critical' tier"
   (`AppModel.swift:158`). Map your platform's signals onto `WARNING`/`CRITICAL`
   and default to `WARNING` when the tier is unknown.
   - Android: `ComponentCallbacks2.onTrimMemory` levels are the nearest analogue.
   - Linux/Go: there is no equivalent signal. `TODO(unresolved): no
     memory-pressure source is specified for a Linux port anywhere in this repo.
     A Go port still needs the full machine — sessions, brackets, deferral — for
     explicit unloads and for lifecycle correctness; it simply may have no
     producer of pressure events, or may poll cgroup memory pressure. That is a
     port-level design decision with no shipped precedent to copy.`
2. **Perform the real unload only on `PROCEED`**, then call `finishUnload()` and
   record that the release was pressure-driven, so the next load can show a
   "warming up again" state rather than a bare spinner.
3. **Drain after every load and every inference.**
4. **Bracket every single model call** with `beginInference` / `finishInference`
   — including the recipe generation, not just the scan.
5. **Do not install pressure sources in headless/self-test processes.** iOS
   skips registration under the self-test flag so the process "behaves exactly
   like the last known-good build: no extra memory-pressure machinery firing a
   storm of no-op handlers while it loads the model" (`AppModel.swift:132-138`).

## Load-phase UI states (small, but part of the contract)

```text
modelLoadPhase = IDLE                    if not loading
               = RELOADING_AFTER_MEMORY  if loading and the model was previously released under pressure
               = FIRST_LOAD              otherwise
```

The "released under pressure" flag is set when an unload actually happens and
cleared on the next successful load. The first on-device load is ~20 s, so this
distinction is what the user sees; it is derived, never stored twice.
