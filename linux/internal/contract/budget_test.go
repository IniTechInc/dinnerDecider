package contract

import "testing"

// The scan-wide cap on heavy model calls (field fix: an 8GB phone jetsammed
// on the fifth heavy call of a scan, so a scan gets at most four).
//
// A 1:1 port of DinnerDeciderTests/ScanCallBudgetTests.swift, Swift names
// preserved.

func TestDefaultBudgetAllowsExactlyFourCalls(t *testing.T) {
	budget := NewDefaultScanCallBudget()
	if !budget.Consume() { // whole-image pass
		t.Error("call 1 (whole-image pass): got false want true")
	}
	if !budget.Consume() { // crop 1
		t.Error("call 2 (crop 1): got false want true")
	}
	if !budget.Consume() { // crop 2
		t.Error("call 3 (crop 2): got false want true")
	}
	if !budget.Consume() { // crop 3
		t.Error("call 4 (crop 3): got false want true")
	}
	if budget.Consume() { // fifth call: the one that crashed
		t.Error("call 5 (the one that crashed): got true want false")
	}
	if got := budget.Remaining(); got != 0 {
		t.Errorf("remaining: got %d want 0", got)
	}
}

func TestRemainingDrivesCropCapping(t *testing.T) {
	budget := NewDefaultScanCallBudget()
	_ = budget.Consume() // whole-image pass spends one
	if got := budget.Remaining(); got != 3 {
		t.Fatalf("remaining after the whole-image pass: got %d want 3", got)
	}

	nineCrops := make([]string, 9)
	for i := range nineCrops {
		nineCrops[i] = "crop"
	}
	capped := nineCrops[:budget.Remaining()]
	if got := len(capped); got != 3 {
		t.Errorf("capped crops: got %d want 3", got)
	}
}

func TestExhaustedBudgetStaysExhausted(t *testing.T) {
	budget := NewScanCallBudget(1)
	if !budget.Consume() {
		t.Error("call 1: got false want true")
	}
	if budget.Consume() {
		t.Error("call 2: got true want false")
	}
	if budget.Consume() {
		t.Error("call 3: got true want false (exhausted stays exhausted)")
	}
	if got := budget.Remaining(); got != 0 {
		t.Errorf("remaining: got %d want 0", got)
	}
}
