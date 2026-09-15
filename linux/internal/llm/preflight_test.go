package llm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// meminfo renders a /proc/meminfo body from kibibyte values. Real kernels print
// a lot more keys; the parser has to pick these out of the noise, so the fixture
// includes some of the noise.
func meminfo(totalKiB, availableKiB int64) string {
	return fmt.Sprintf(`MemTotal:       %8d kB
MemFree:         1234567 kB
MemAvailable:   %8d kB
Buffers:          123456 kB
Cached:          2345678 kB
SwapCached:            0 kB
Active:          1111111 kB
`, totalKiB, availableKiB)
}

func writeMeminfo(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write meminfo: %v", err)
	}
	return path
}

// TestMemoryTiers walks the table from the plan, in the units real kernels
// report. The Pi 5 rows are the point of the carveout allowance: a nominal 8 GB
// Pi reports 7.88 GiB, and a literal 8 GiB comparison would demote the port's
// primary target device to marginal.
func TestMemoryTiers(t *testing.T) {
	tests := []struct {
		name            string
		totalKiB        int64
		availableKiB    int64
		wantTier        MemoryTier
		wantCanLoad     bool
		wantDegraded    bool
		wantForceUnload bool
	}{
		{
			name: "raspberry pi 5 8gb idle", totalKiB: 8261548, availableKiB: 7_800_000,
			wantTier: TierSupported, wantCanLoad: true,
		},
		{
			name: "raspberry pi 5 16gb idle", totalKiB: 16_316_056, availableKiB: 15_500_000,
			wantTier: TierSupported, wantCanLoad: true,
		},
		{
			name: "x86 32gb", totalKiB: 32_768_000, availableKiB: 30_000_000,
			wantTier: TierSupported, wantCanLoad: true,
		},
		{
			name: "nominal 6gb board", totalKiB: 5_980_000, availableKiB: 5_300_000,
			wantTier: TierMarginal, wantCanLoad: true, wantDegraded: true, wantForceUnload: true,
		},
		{
			name: "jetson orin nano 8gb", totalKiB: 7_800_000, availableKiB: 6_900_000,
			wantTier: TierSupported, wantCanLoad: true,
		},
		{
			name: "raspberry pi 4 4gb", totalKiB: 3_970_000, availableKiB: 3_600_000,
			wantTier: TierRefused, wantCanLoad: false,
		},
		{
			// The 6 GB single-configuration Android device from
			// docs/PORTING_NOTES.md, which lands at 5.4 to 5.7 GiB after
			// carveouts. The bottom of the band stays strict: this is refused.
			name: "6gb device after large carveouts", totalKiB: 5_660_000, availableKiB: 5_000_000,
			wantTier: TierRefused, wantCanLoad: false,
		},
		{
			name: "2gb board", totalKiB: 2_000_000, availableKiB: 1_900_000,
			wantTier: TierRefused, wantCanLoad: false,
		},
		{
			name: "8gb box already running three services", totalKiB: 8_261_548, availableKiB: 3_100_000,
			wantTier: TierSupported, wantCanLoad: false,
		},
		{
			name: "8gb box with just enough headroom", totalKiB: 8_261_548, availableKiB: 4_900_000,
			wantTier: TierSupported, wantCanLoad: true, wantDegraded: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeMeminfo(t, meminfo(tc.totalKiB, tc.availableKiB))
			got, err := CheckMemoryAt(path)
			if err != nil {
				t.Fatalf("CheckMemoryAt: %v", err)
			}
			if got.Tier != tc.wantTier {
				t.Errorf("tier = %v, want %v (%s)", got.Tier, tc.wantTier, got.Message)
			}
			if got.CanLoad != tc.wantCanLoad {
				t.Errorf("canLoad = %v, want %v (%s)", got.CanLoad, tc.wantCanLoad, got.Message)
			}
			if got.Degraded != tc.wantDegraded {
				t.Errorf("degraded = %v, want %v", got.Degraded, tc.wantDegraded)
			}
			if got.ForceIdleUnload != tc.wantForceUnload {
				t.Errorf("forceIdleUnload = %v, want %v", got.ForceIdleUnload, tc.wantForceUnload)
			}
			if got.TotalBytes != uint64(tc.totalKiB)*KiB {
				t.Errorf("total = %d, want %d", got.TotalBytes, uint64(tc.totalKiB)*KiB)
			}
			if got.AvailableBytes != uint64(tc.availableKiB)*KiB {
				t.Errorf("available = %d, want %d", got.AvailableBytes, uint64(tc.availableKiB)*KiB)
			}
		})
	}
}

// TestMemAvailableGatesTheLoadNotJustMemTotal is the rule stated on its own: the
// tier is about the machine, the gate is about right now.
func TestMemAvailableGatesTheLoadNotJustMemTotal(t *testing.T) {
	plenty := Classify(16*GiB, 12*GiB, false)
	if !plenty.CanLoad || plenty.Tier != TierSupported {
		t.Fatalf("16 GiB idle should load: %+v", plenty)
	}
	squeezed := Classify(16*GiB, 2*GiB, false)
	if squeezed.Tier != TierSupported {
		t.Fatalf("tier should still be supported, got %v", squeezed.Tier)
	}
	if squeezed.CanLoad {
		t.Fatal("a supported box with 2 GiB available must not load a 4.6 GiB working set")
	}
	if !strings.Contains(squeezed.Message, "Not enough free memory") {
		t.Fatalf("message should say why: %q", squeezed.Message)
	}
}

// TestRefusalCarriesTheRealNumbers backs the contract line that refusing is not
// failing. The service starts, the UI is navigable, the mock backs the API and
// the demo badge shows - but only if the status message can explain itself.
func TestRefusalCarriesTheRealNumbers(t *testing.T) {
	s := Classify(4*GiB, 3*GiB, false)
	if s.CanLoad {
		t.Fatal("4 GiB must be refused")
	}
	for _, want := range []string{"4.0 GiB total", "3.0 GiB available", "4.6 GiB", "demo badge"} {
		if !strings.Contains(s.Message, want) {
			t.Errorf("message %q should contain %q", s.Message, want)
		}
	}
	err := s.Err()
	if !errors.Is(err, ErrInsufficientMemory) {
		t.Fatalf("Err() = %v, want it to wrap ErrInsufficientMemory", err)
	}
	if !strings.Contains(err.Error(), "4.0 GiB total") {
		t.Fatalf("the wrapped error should carry the numbers: %v", err)
	}
	if ok := Classify(16*GiB, 12*GiB, false); ok.Err() != nil {
		t.Fatalf("a loadable status must have no error, got %v", ok.Err())
	}
}

// TestZeroValueStatusRefuses pins the fail-closed zero value. A MemoryStatus
// nobody filled in must never wave a multi-GB allocation through.
func TestZeroValueStatusRefuses(t *testing.T) {
	var s MemoryStatus
	if s.CanLoad {
		t.Fatal("the zero value must not permit a load")
	}
	if s.Tier != TierRefused {
		t.Fatalf("zero tier = %v, want TierRefused", s.Tier)
	}
	if !errors.Is(s.Err(), ErrInsufficientMemory) {
		t.Fatal("the zero value must produce ErrInsufficientMemory")
	}
}

// TestMemAvailableAbsentFallsBackToMemFree covers minimal containers and old
// kernels. The substitution is allowed but must be flagged, because MemFree is
// far more pessimistic and would otherwise produce an unexplained refusal.
func TestMemAvailableAbsentFallsBackToMemFree(t *testing.T) {
	body := "MemTotal:       16316056 kB\nMemFree:         6000000 kB\nCached:          2000000 kB\n"
	got, err := CheckMemoryAt(writeMeminfo(t, body))
	if err != nil {
		t.Fatalf("CheckMemoryAt: %v", err)
	}
	if !got.AvailableEstimated {
		t.Fatal("the MemFree substitution must be flagged")
	}
	if got.AvailableBytes != 6_000_000*KiB {
		t.Fatalf("available = %d", got.AvailableBytes)
	}
	if !strings.Contains(got.Message, "under-reports") {
		t.Fatalf("message should carry the caveat: %q", got.Message)
	}
}

// TestMalformedMeminfoIsAnErrorNotAGuess: a preflight that cannot read the
// numbers must say so. Guessing here is how a 2 GB box tries to load 4 GiB.
func TestMalformedMeminfoIsAnErrorNotAGuess(t *testing.T) {
	bad := []struct{ name, body string }{
		{"empty", ""},
		{"no memtotal", "MemFree: 100 kB\n"},
		{"zero memtotal", "MemTotal: 0 kB\nMemAvailable: 100 kB\n"},
		{"non numeric", "MemTotal: lots kB\nMemAvailable: 100 kB\n"},
		{"no availability at all", "MemTotal: 16316056 kB\nCached: 100 kB\n"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := CheckMemoryAt(writeMeminfo(t, tc.body)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if _, err := CheckMemoryAt(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing meminfo must be an error")
	}
}

// TestMeminfoParsingHandlesUnitlessAndPaddedValues covers the two shapes the
// file actually uses, plus a key whose name is a prefix of another so the
// parser cannot get away with strings.Contains.
func TestMeminfoParsingHandlesUnitlessAndPaddedValues(t *testing.T) {
	body := "MemTotalHuge:        1 kB\nMemTotal:       16316056 kB\nMemAvailable:    8000000 kB\nHugePages_Total:       0\n"
	got, err := CheckMemoryAt(writeMeminfo(t, body))
	if err != nil {
		t.Fatalf("CheckMemoryAt: %v", err)
	}
	if got.TotalBytes != 16_316_056*KiB {
		t.Fatalf("MemTotalHuge leaked into MemTotal: %d", got.TotalBytes)
	}
	if v, ok := meminfoValueBytes([]byte(body), "HugePages_Total"); !ok || v != 0 {
		t.Fatalf("unitless value: %d %v", v, ok)
	}
}

// TestFormatBytes keeps the operator-facing numbers readable and honest.
func TestFormatBytes(t *testing.T) {
	tests := []struct {
		in   uint64
		want string
	}{
		{16 * GiB, "16.0 GiB"},
		{8261548 * KiB, "7.9 GiB"},
		{WorkingSetLowBytes, "4.6 GiB"},
		{WorkingSetHighBytes, "5.0 GiB"},
		{ModelFileBytes, "4.0 GiB"},
		{512 * MiB, "512 MiB"},
		{0, "0 MiB"},
	}
	for _, tc := range tests {
		if got := FormatBytes(tc.in); got != tc.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCheckMemoryOnThisHost is a smoke test. On Linux it reads the real file; on
// a developer Mac there is no /proc/meminfo and an error is the correct answer,
// never a fabricated tier.
func TestCheckMemoryOnThisHost(t *testing.T) {
	got, err := CheckMemory()
	if err != nil {
		if _, statErr := os.Stat(DefaultMeminfoPath); statErr == nil {
			t.Fatalf("this host has %s but CheckMemory failed: %v", DefaultMeminfoPath, err)
		}
		if got.CanLoad {
			t.Fatal("a failed check must never report CanLoad")
		}
		return
	}
	if got.TotalBytes == 0 {
		t.Fatal("a successful check must report a real MemTotal")
	}
	t.Logf("host memory: %s", got.Message)
}
