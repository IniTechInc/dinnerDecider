package contract

import "testing"

// TestFlight feedback P2: a scan whose model calls all FAILED was reported with
// the same "Nothing spotted" empty state as a scan of a genuinely empty fridge.
// Users were told to add light and move closer when the real problem was that
// the model never answered. The two cases must be distinguishable.
//
// A 1:1 port of DinnerDeciderTests/ScanOutcomeTests.swift, Swift names
// preserved.

func TestItemsFoundFinishes(t *testing.T) {
	if got := TerminalPhase(3, 0); got != Finished {
		t.Errorf("TerminalPhase(3, 0): got %v want %v", got, Finished)
	}
}

// TestItemsFoundWinsOverFailures: partial failure still counts as a success —
// the user gets what was found rather than an error that throws away real
// results.
func TestItemsFoundWinsOverFailures(t *testing.T) {
	if got := TerminalPhase(2, 5); got != Finished {
		t.Errorf("TerminalPhase(2, 5): got %v want %v", got, Finished)
	}
}

// TestNothingFoundAndNoFailuresIsEmpty: the genuinely-empty case — every call
// answered, none of them saw food. This is the only case where "Nothing
// spotted" is honest.
func TestNothingFoundAndNoFailuresIsEmpty(t *testing.T) {
	if got := TerminalPhase(0, 0); got != Empty {
		t.Errorf("TerminalPhase(0, 0): got %v want %v", got, Empty)
	}
}

// TestNothingFoundWithFailuresIsFailedNotEmpty is the regression this function
// exists to prevent. Before the fix this returned Empty, which told the user
// their fridge looked empty when in fact every single inference had thrown.
func TestNothingFoundWithFailuresIsFailedNotEmpty(t *testing.T) {
	phase := TerminalPhase(0, 4)
	if phase != Failed {
		t.Errorf("TerminalPhase(0, 4): got %v want %v", phase, Failed)
	}
	if phase == Empty {
		t.Error("A scan where every model call failed must not claim the fridge was empty")
	}
}

// TestSingleFailureIsEnoughToReportFailure: one swallowed failure is enough to
// make "Nothing spotted" a lie.
func TestSingleFailureIsEnoughToReportFailure(t *testing.T) {
	if got := TerminalPhase(0, 1); got != Failed {
		t.Errorf("TerminalPhase(0, 1): got %v want %v", got, Failed)
	}
}

// TestExhaustedBudgetAllThrowingReportsFailure mirrors the real budget: four
// calls, all throwing (the build-4 field report where the model unloaded
// mid-scan and every identify threw "model not loaded").
func TestExhaustedBudgetAllThrowingReportsFailure(t *testing.T) {
	budget := NewDefaultScanCallBudget()
	failures := 0
	for budget.Consume() {
		failures++
	}

	if failures != 4 {
		t.Fatalf("failures: got %d want 4", failures)
	}
	if got := TerminalPhase(0, failures); got != Failed {
		t.Errorf("TerminalPhase(0, %d): got %v want %v", failures, got, Failed)
	}
}
