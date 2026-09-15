package downloadlogic

import (
	"math"
	"testing"
)

// Unit tests for the pure, side-effect-free download logic: the disk-space
// gate, the size-verification decision, the metered-confirmation gate, the
// progress fraction and the state machine.
//
// A 1:1 port of the 20 logic tests in
// DinnerDeciderTests/ModelDownloadServiceTests.swift, with the Swift names
// preserved. The remaining 5 tests in that file cover the file definitions and
// the per-file progress fraction, which live in package model, and are ported
// in download_test.go.

// MARK: Disk-space gate

func TestDiskSpaceEnoughWithHeadroom(t *testing.T) {
	if !HasEnoughDiskSpace(5_000_000_000) {
		t.Error("5 GB is enough")
	}
}

func TestDiskSpaceExactBoundaryPasses(t *testing.T) {
	if !HasEnoughDiskSpace(RequiredFreeBytes) {
		t.Error("the requirement itself must pass; the comparison is >=")
	}
}

func TestDiskSpaceJustBelowBoundaryFails(t *testing.T) {
	if HasEnoughDiskSpace(RequiredFreeBytes - 1) {
		t.Error("one byte short must fail")
	}
}

func TestDiskSpaceInsufficient(t *testing.T) {
	if HasEnoughDiskSpace(1_000_000_000) {
		t.Error("1 GB is not enough for a 4 GB pair")
	}
}

// MARK: Size-verification decision

func TestSizeValidWhenExactMatch(t *testing.T) {
	if !SizeIsValid(3_460_000_000, 3_460_000_000) {
		t.Error("an exact match is valid")
	}
}

func TestSizeInvalidWhenShort(t *testing.T) {
	if SizeIsValid(3_000_000_000, 3_460_000_000) {
		t.Error("a short file is invalid")
	}
}

func TestSizeInvalidWhenLarger(t *testing.T) {
	if SizeIsValid(3_460_000_001, 3_460_000_000) {
		t.Error("one byte over is invalid too; this is equality, not a floor")
	}
}

func TestSizeInvalidWhenExpectedUnknown(t *testing.T) {
	// Never accept a file you cannot vouch for.
	if SizeIsValid(100, 0) {
		t.Error("a zero expected size must be invalid")
	}
}

// MARK: Metered-confirmation gate (iOS: cellular)

func TestMayStartOnWifi(t *testing.T) {
	if !MayStartDownload(false, false) {
		t.Error("an unmetered link needs no confirmation")
	}
}

func TestBlockedOnCellularWithoutConfirmation(t *testing.T) {
	if MayStartDownload(true, false) {
		t.Error("a metered link must be confirmed first")
	}
}

func TestAllowedOnCellularWithConfirmation(t *testing.T) {
	if !MayStartDownload(true, true) {
		t.Error("an explicit confirmation unblocks a metered link")
	}
}

// MARK: Overall fraction

func assertFraction(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.0001 {
		t.Errorf("fraction: got %v want %v", got, want)
	}
}

func TestOverallFractionHalf(t *testing.T) {
	assertFraction(t, OverallFraction(50, 100), 0.5)
}

func TestOverallFractionZeroTotalIsZero(t *testing.T) {
	assertFraction(t, OverallFraction(50, 0), 0)
}

func TestOverallFractionCapsAtOne(t *testing.T) {
	assertFraction(t, OverallFraction(150, 100), 1.0)
}

// MARK: State machine

func assertState(t *testing.T, got, want State) {
	t.Helper()
	if got != want {
		t.Errorf("state: got %v(%q) want %v(%q)", got, got.Reason, want, want.Reason)
	}
}

func TestHappyPathTransitions(t *testing.T) {
	state := NotStarted()
	state = Next(state, StartEvent())
	assertState(t, state, Downloading())
	state = Next(state, AllDownloadedEvent())
	assertState(t, state, Verifying())
	state = Next(state, VerifiedOKEvent())
	assertState(t, state, Done())
}

func TestPauseResume(t *testing.T) {
	assertState(t, Next(Downloading(), PauseEvent()), Paused())
	assertState(t, Next(Paused(), ResumeEvent()), Downloading())
	assertState(t, Next(Paused(), StartEvent()), Downloading())
}

func TestVerificationFailureGoesToFailed(t *testing.T) {
	assertState(t, Next(Verifying(), VerificationFailedEvent("bad size")), Failed("bad size"))
}

func TestFailedFromAnyStateOnError(t *testing.T) {
	assertState(t, Next(Downloading(), FailedEvent("no network")), Failed("no network"))
	assertState(t, Next(Verifying(), FailedEvent("io")), Failed("io"))
	assertState(t, Next(NotStarted(), FailedEvent("x")), Failed("x"))
}

func TestRetryFromFailedRestarts(t *testing.T) {
	assertState(t, Next(Failed("x"), RetryEvent()), Downloading())
	assertState(t, Next(Failed("x"), StartEvent()), Downloading())
}

func TestUnhandledEventsAreNoOps(t *testing.T) {
	assertState(t, Next(NotStarted(), PauseEvent()), NotStarted())
	assertState(t, Next(Done(), PauseEvent()), Done())
	assertState(t, Next(Downloading(), ResumeEvent()), Downloading())
	assertState(t, Next(Verifying(), PauseEvent()), Verifying())
}

// MARK: Go-only

// TestDuplicateStartWhileDownloadingIsANoOp is the pairing the Linux port
// depends on for single-writer safety: the service starts its transfer
// goroutine only on a transition INTO downloading, so this no-op is what stops
// a second writer opening the same .part file.
func TestDuplicateStartWhileDownloadingIsANoOp(t *testing.T) {
	state := Downloading()
	for i := 0; i < 5; i++ {
		state = Next(state, StartEvent())
		assertState(t, state, Downloading())
	}
}

// A failure reason must survive the transition verbatim; it is shown to the
// user and is the only explanation they get.
func TestFailureReasonIsCarriedThrough(t *testing.T) {
	const reason = "Gemma 4 weights failed its checksum and was removed."
	if got := Next(Downloading(), FailedEvent(reason)); got.Reason != reason {
		t.Errorf("reason: got %q want %q", got.Reason, reason)
	}
	if got := Next(Verifying(), VerificationFailedEvent(reason)); got.Reason != reason {
		t.Errorf("reason: got %q want %q", got.Reason, reason)
	}
}

func TestStateStrings(t *testing.T) {
	cases := []struct {
		state State
		want  string
	}{
		{NotStarted(), "notStarted"},
		{Downloading(), "downloading"},
		{Paused(), "paused"},
		{Verifying(), "verifying"},
		{Done(), "done"},
		{Failed("x"), "failed"},
	}
	for _, tc := range cases {
		if got := tc.state.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}
