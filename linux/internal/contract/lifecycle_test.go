package contract

import "testing"

// Unit tests for the pure model lifecycle state machine.
//
// A 1:1 port of DinnerDeciderTests/ModelLifecycleTests.swift — all 17 tests,
// with the Swift names preserved (only the leading `test` capitalized, as Go
// requires) so a failure here maps straight onto the iOS specification and onto
// the guarantee table in shared/pipeline/model-lifecycle.md § The other bug:
// mid-operation free.
//
// They encode the crash-fix guarantees directly: the client is never freed
// while a load or inference is in flight, deferred unloads run once idle, and
// duplicate loads coalesce. They are pure and synchronous (no runtime, no
// goroutines) so they are fast and deterministic.
//
// Plus one Go-only test at the bottom, for the port-added RequestDeferredUnload.

func assertState(t *testing.T, m ModelLifecycle, want ModelState) {
	t.Helper()
	if got := m.State(); got != want {
		t.Errorf("state: got %v want %v", got, want)
	}
}

func assertPendingUnload(t *testing.T, m ModelLifecycle, want bool) {
	t.Helper()
	if got := m.PendingUnload(); got != want {
		t.Errorf("pendingUnload: got %v want %v", got, want)
	}
}

func assertUnloadDecision(t *testing.T, got, want UnloadDecision) {
	t.Helper()
	if got != want {
		t.Errorf("unload decision: got %v want %v", got, want)
	}
}

func assertLoadDecision(t *testing.T, got, want LoadDecision) {
	t.Helper()
	if got != want {
		t.Errorf("load decision: got %v want %v", got, want)
	}
}

// --- Pressure during load --------------------------------------------------

func TestWarningPressureDuringLoadDoesNotUnload(t *testing.T) {
	var lifecycle ModelLifecycle
	assertLoadDecision(t, lifecycle.BeginLoad(), LoadProceed)
	assertState(t, lifecycle, Loading)

	// A warning while loading is expected pressure: ignore it entirely.
	decision := lifecycle.HandleMemoryPressure(PressureWarning)

	assertUnloadDecision(t, decision, UnloadNothingToDo)
	assertState(t, lifecycle, Loading)
	assertPendingUnload(t, lifecycle, false)
}

func TestCriticalPressureDuringLoadDefersUnloadNeverFrees(t *testing.T) {
	var lifecycle ModelLifecycle
	_ = lifecycle.BeginLoad()

	decision := lifecycle.HandleMemoryPressure(PressureCritical)

	// Recorded, but the client is NOT freed mid-load.
	assertUnloadDecision(t, decision, UnloadDeferred)
	assertState(t, lifecycle, Loading)
	assertPendingUnload(t, lifecycle, true)
}

// --- Pressure during inference ---------------------------------------------

func TestWarningPressureDuringInferenceDoesNotUnload(t *testing.T) {
	var lifecycle ModelLifecycle
	_ = lifecycle.BeginLoad()
	lifecycle.FinishLoad(true)
	if !lifecycle.BeginInference() {
		t.Fatal("BeginInference: got false want true from Ready")
	}
	assertState(t, lifecycle, Inferring)

	decision := lifecycle.HandleMemoryPressure(PressureWarning)

	assertUnloadDecision(t, decision, UnloadNothingToDo)
	assertState(t, lifecycle, Inferring)
	assertPendingUnload(t, lifecycle, false)
}

// --- Deferred unload drains once idle --------------------------------------

func TestPendingUnloadExecutesAfterLoadCompletes(t *testing.T) {
	var lifecycle ModelLifecycle
	_ = lifecycle.BeginLoad()
	_ = lifecycle.RequestUnload() // deferred while loading
	assertPendingUnload(t, lifecycle, true)

	lifecycle.FinishLoad(true)
	assertState(t, lifecycle, Ready)

	decision := lifecycle.DrainPendingUnload()

	assertUnloadDecision(t, decision, UnloadProceed)
	assertState(t, lifecycle, Unloading)
	assertPendingUnload(t, lifecycle, false)
}

func TestPendingUnloadExecutesAfterInferenceCompletes(t *testing.T) {
	var lifecycle ModelLifecycle
	_ = lifecycle.BeginLoad()
	lifecycle.FinishLoad(true)
	_ = lifecycle.BeginInference()

	// Critical pressure mid-inference records the unload but does not free.
	assertUnloadDecision(t, lifecycle.HandleMemoryPressure(PressureCritical), UnloadDeferred)
	assertState(t, lifecycle, Inferring)

	lifecycle.FinishInference()
	assertState(t, lifecycle, Ready)

	assertUnloadDecision(t, lifecycle.DrainPendingUnload(), UnloadProceed)
	assertState(t, lifecycle, Unloading)
}

func TestFailedLoadDropsDeferredUnload(t *testing.T) {
	var lifecycle ModelLifecycle
	_ = lifecycle.BeginLoad()
	_ = lifecycle.RequestUnload()
	assertPendingUnload(t, lifecycle, true)

	lifecycle.FinishLoad(false)

	assertState(t, lifecycle, Unloaded)
	assertPendingUnload(t, lifecycle, false)
	assertUnloadDecision(t, lifecycle.DrainPendingUnload(), UnloadNothingToDo)
}

// --- Load coalescing -------------------------------------------------------

func TestDoubleLoadCoalesces(t *testing.T) {
	var lifecycle ModelLifecycle
	assertLoadDecision(t, lifecycle.BeginLoad(), LoadProceed)

	// A second load while the first is in flight must not start another.
	assertLoadDecision(t, lifecycle.BeginLoad(), LoadCoalesced)
	assertState(t, lifecycle, Loading)
}

func TestLoadWhenAlreadyReadyIsNoOp(t *testing.T) {
	var lifecycle ModelLifecycle
	_ = lifecycle.BeginLoad()
	lifecycle.FinishLoad(true)

	assertLoadDecision(t, lifecycle.BeginLoad(), LoadAlreadyLoaded)
	assertState(t, lifecycle, Ready)
}

// --- Unload deferral while inferring ---------------------------------------

func TestExplicitUnloadWhileInferringDefers(t *testing.T) {
	var lifecycle ModelLifecycle
	_ = lifecycle.BeginLoad()
	lifecycle.FinishLoad(true)
	_ = lifecycle.BeginInference()

	decision := lifecycle.RequestUnload()

	assertUnloadDecision(t, decision, UnloadDeferred)
	assertState(t, lifecycle, Inferring) // still serving
	assertPendingUnload(t, lifecycle, true)
}

// --- Idle behavior ---------------------------------------------------------

func TestWarningPressureWhenReadyIdleUnloads(t *testing.T) {
	var lifecycle ModelLifecycle
	_ = lifecycle.BeginLoad()
	lifecycle.FinishLoad(true)

	decision := lifecycle.HandleMemoryPressure(PressureWarning)

	assertUnloadDecision(t, decision, UnloadProceed)
	assertState(t, lifecycle, Unloading)
}

func TestPressureWhenUnloadedIsNoOp(t *testing.T) {
	var lifecycle ModelLifecycle

	assertUnloadDecision(t, lifecycle.HandleMemoryPressure(PressureWarning), UnloadNothingToDo)
	assertUnloadDecision(t, lifecycle.HandleMemoryPressure(PressureCritical), UnloadNothingToDo)
	assertState(t, lifecycle, Unloaded)
}

func TestUnloadCycleReturnsToUnloaded(t *testing.T) {
	var lifecycle ModelLifecycle
	_ = lifecycle.BeginLoad()
	lifecycle.FinishLoad(true)
	assertUnloadDecision(t, lifecycle.RequestUnload(), UnloadProceed)
	assertState(t, lifecycle, Unloading)

	lifecycle.FinishUnload()

	assertState(t, lifecycle, Unloaded)
	assertPendingUnload(t, lifecycle, false)
}

// --- Scan/recipe sessions (field bug: model freed between crops) ------------

// TestWarningBetweenInferencesDuringSessionDoesNotUnload is the repro for the
// "9 items analyzed, nothing recognized" TestFlight report: after the 3.5GB
// load the system reliably posts a memory warning, and the moments BETWEEN
// per-crop inferences are Ready, so the old rules freed the model mid-scan and
// every remaining identify failed silently. During an active session a warning
// at Ready must be ignored.
func TestWarningBetweenInferencesDuringSessionDoesNotUnload(t *testing.T) {
	var lifecycle ModelLifecycle
	lifecycle.BeginSession()
	_ = lifecycle.BeginLoad()
	lifecycle.FinishLoad(true)
	if !lifecycle.BeginInference() { // crop 1
		t.Fatal("BeginInference: got false want true from Ready")
	}
	lifecycle.FinishInference() // between crops: Ready

	decision := lifecycle.HandleMemoryPressure(PressureWarning)

	assertUnloadDecision(t, decision, UnloadNothingToDo)
	assertState(t, lifecycle, Ready)
	assertPendingUnload(t, lifecycle, false)
}

func TestCriticalBetweenInferencesDuringSessionDefersToSessionEnd(t *testing.T) {
	var lifecycle ModelLifecycle
	lifecycle.BeginSession()
	_ = lifecycle.BeginLoad()
	lifecycle.FinishLoad(true)

	assertUnloadDecision(t, lifecycle.HandleMemoryPressure(PressureCritical), UnloadDeferred)
	assertState(t, lifecycle, Ready)
	assertPendingUnload(t, lifecycle, true)

	// Mid-session drain (called after every inference) must NOT free it.
	assertUnloadDecision(t, lifecycle.DrainPendingUnload(), UnloadNothingToDo)
	assertState(t, lifecycle, Ready)

	// Session over: the deferred critical unload finally runs.
	assertUnloadDecision(t, lifecycle.EndSession(), UnloadProceed)
	assertState(t, lifecycle, Unloading)
}

func TestWarningAtReadyOutsideSessionStillUnloads(t *testing.T) {
	var lifecycle ModelLifecycle
	_ = lifecycle.BeginLoad()
	lifecycle.FinishLoad(true)

	// Idle with no scan/recipe running: freeing on a warning is correct.
	assertUnloadDecision(t, lifecycle.HandleMemoryPressure(PressureWarning), UnloadProceed)
	assertState(t, lifecycle, Unloading)
}

func TestNestedSessionsOnlyEndWhenAllEnd(t *testing.T) {
	var lifecycle ModelLifecycle
	lifecycle.BeginSession() // scan
	lifecycle.BeginSession() // recipes overlap
	_ = lifecycle.BeginLoad()
	lifecycle.FinishLoad(true)

	assertUnloadDecision(t, lifecycle.EndSession(), UnloadNothingToDo)
	// One session still active: warnings stay ignored.
	assertUnloadDecision(t, lifecycle.HandleMemoryPressure(PressureWarning), UnloadNothingToDo)
	assertState(t, lifecycle, Ready)

	_ = lifecycle.EndSession()
	assertUnloadDecision(t, lifecycle.HandleMemoryPressure(PressureWarning), UnloadProceed)
}

func TestEndSessionWithoutPendingUnloadKeepsModelResident(t *testing.T) {
	var lifecycle ModelLifecycle
	lifecycle.BeginSession()
	_ = lifecycle.BeginLoad()
	lifecycle.FinishLoad(true)

	// No pressure during the scan: model stays warm for the next scan.
	assertUnloadDecision(t, lifecycle.EndSession(), UnloadNothingToDo)
	assertState(t, lifecycle, Ready)
}

// --- Go-only: the port-added RequestDeferredUnload --------------------------

// TestRequestDeferredUnloadMatchesCriticalPressureInAllStates pins the Linux
// port's single addition as a behavior-preserving alias. RequestDeferredUnload
// exists so a health-check failure, a config change or an idle timeout no longer
// has to pretend to be a memory event; it must remain a thin alias for the
// critical branch, never a second implementation that can drift.
//
// All six rows of the plan's Step 35a decision table, i.e. the five states with
// Ready split by whether a session is active.
func TestRequestDeferredUnloadMatchesCriticalPressureInAllStates(t *testing.T) {
	cases := []struct {
		name  string
		build func() ModelLifecycle
		want  UnloadDecision
	}{
		{
			name:  "unloaded",
			build: func() ModelLifecycle { return ModelLifecycle{} },
			want:  UnloadNothingToDo,
		},
		{
			name: "loading",
			build: func() ModelLifecycle {
				var m ModelLifecycle
				_ = m.BeginLoad()
				return m
			},
			want: UnloadDeferred,
		},
		{
			name: "ready no session",
			build: func() ModelLifecycle {
				var m ModelLifecycle
				_ = m.BeginLoad()
				m.FinishLoad(true)
				return m
			},
			want: UnloadProceed,
		},
		{
			name: "ready in session",
			build: func() ModelLifecycle {
				var m ModelLifecycle
				m.BeginSession()
				_ = m.BeginLoad()
				m.FinishLoad(true)
				return m
			},
			want: UnloadDeferred,
		},
		{
			name: "inferring",
			build: func() ModelLifecycle {
				var m ModelLifecycle
				_ = m.BeginLoad()
				m.FinishLoad(true)
				_ = m.BeginInference()
				return m
			},
			want: UnloadDeferred,
		},
		{
			name: "unloading",
			build: func() ModelLifecycle {
				var m ModelLifecycle
				_ = m.BeginLoad()
				m.FinishLoad(true)
				_ = m.RequestUnload()
				return m
			},
			want: UnloadNothingToDo,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			viaPressure := tc.build()
			viaReason := tc.build()

			gotPressure := viaPressure.HandleMemoryPressure(PressureCritical)
			gotReason := viaReason.RequestDeferredUnload(UnloadReasonHealthFailure)

			if gotPressure != tc.want {
				t.Errorf("HandleMemoryPressure(critical): got %v want %v", gotPressure, tc.want)
			}
			if gotReason != gotPressure {
				t.Errorf("RequestDeferredUnload: got %v want %v (must be a thin alias)", gotReason, gotPressure)
			}
			if viaReason.State() != viaPressure.State() {
				t.Errorf("resulting state: got %v want %v", viaReason.State(), viaPressure.State())
			}
			if viaReason.PendingUnload() != viaPressure.PendingUnload() {
				t.Errorf("resulting pendingUnload: got %v want %v",
					viaReason.PendingUnload(), viaPressure.PendingUnload())
			}
			if viaReason.ActiveSessionCount() != viaPressure.ActiveSessionCount() {
				t.Errorf("resulting activeSessionCount: got %d want %d",
					viaReason.ActiveSessionCount(), viaPressure.ActiveSessionCount())
			}
		})
	}
}
