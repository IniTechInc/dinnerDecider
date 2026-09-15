package llm

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Byte units. Everything in this file is exact binary units, because
// /proc/meminfo reports kibibytes and mixing GB and GiB at a 5 GiB floor is a
// 7% error in the one number the whole design turns on.
const (
	// KiB is 1024 bytes, the unit /proc/meminfo uses.
	KiB = 1 << 10
	// MiB is 1024 KiB.
	MiB = 1 << 20
	// GiB is 1024 MiB.
	GiB = 1 << 30
)

// The resource floor.
//
// The files alone are 3.98 GiB: weights 3.46 GiB (gemma-4-E4B-it-UD-IQ3_XXS)
// plus the 0.53 GiB Q8_0 mmproj, per CLAUDE.md § Model files. On top of that
// llama-server allocates the KV cache for --ctx-size 1024 and the compute
// buffers for --batch-size 512 / --ubatch-size 512, and mmap means the weight
// pages are resident whenever they are being read. The practical working set is
// 4.6 to 5.0 GiB.
//
// These are the numbers /api/status reports verbatim. Do not round them for
// display: on a box that refuses to load, "4.1 GiB available, 4.6 GiB needed"
// is actionable and "not enough memory" is not.
const (
	// ModelFileBytes is weights plus projector on disk: 3.98 GiB.
	ModelFileBytes = 4076 * MiB
	// WorkingSetLowBytes is the bottom of the practical working set, 4.6 GiB.
	// Below this a load cannot succeed, so it is the hard gate.
	WorkingSetLowBytes = 4710 * MiB
	// WorkingSetHighBytes is the top of the practical working set, 5.0 GiB.
	// Between the two a load is expected to work but with no headroom, which is
	// what Degraded reports.
	WorkingSetHighBytes = 5 * GiB

	// SupportedMemTotalBytes is the NOMINAL RAM at which the device is
	// supported: 8 GiB.
	SupportedMemTotalBytes = 8 * GiB
	// MarginalMemTotalBytes is the NOMINAL RAM below which the device is
	// refused: 6 GiB.
	MarginalMemTotalBytes = 6 * GiB

	// carveoutPercent converts a nominal RAM figure into the MemTotal a kernel
	// actually reports.
	//
	// This is the one judgement call in this file and it is load-bearing.
	// MemTotal is always BELOW the nominal RAM because the kernel, the GPU and
	// the firmware take carveouts first: a Raspberry Pi 5 "8 GB" reports
	// MemTotal 8261548 kB, which is 7.88 GiB. Comparing that against a literal
	// 8 GiB would put the port's primary target device in the marginal tier,
	// force idle unloads on it and mark it degraded - a misclassification of
	// exactly the machine this was written for.
	//
	// 92% is chosen so that both nominal-8 GB targets clear it: the Pi 5 at
	// 7.88 GiB, and a Jetson Orin Nano 8 GB, whose GPU carveout is larger and
	// which is expected to land near 7.4 GiB. That expectation is NOT a
	// measurement - no Jetson exists to measure (docs/PORTING_NOTES.md
	// § Available test hardware) - so the allowance is deliberately generous at
	// the top of the band. It stays strict at the bottom: a 4 GB Pi (3.79 GiB)
	// and a 6 GB Android-class device that reports 5.4 GiB are both still
	// refused (floors: 7.36 GiB supported, 5.52 GiB marginal).
	//
	// The looseness costs little because MemTotal only picks the tier. The load
	// itself is gated on MemAvailable against the real working set, which is the
	// number that actually decides whether an allocation survives.
	carveoutPercent = 92
)

// supportedFloorBytes and marginalFloorBytes are the nominal tiers adjusted for
// kernel carveouts. See carveoutPercent.
const (
	supportedFloorBytes = SupportedMemTotalBytes / 100 * carveoutPercent
	marginalFloorBytes  = MarginalMemTotalBytes / 100 * carveoutPercent
)

// ErrInsufficientMemory is returned whenever a load or a reload is refused for
// memory. It is a sentinel because two very different call sites need to
// recognise it: the initial load (the service still starts, the UI is still
// navigable, the mock backs the API and the demo badge shows), and the Step 35b
// recovery, where refusing to reload is what lets the box recover from the OOM
// kill that triggered the recovery in the first place.
var ErrInsufficientMemory = errors.New("insufficient memory for the model")

// AbortReasonInsufficientMemory is the machine-readable reason the API reports
// on a scan aborted by this check. The web UI appends the real numbers to the
// "close other apps" copy, which on a Pi is literally correct advice.
const AbortReasonInsufficientMemory = "insufficient_memory"

// MemoryTier is the support tier of a machine, from MemTotal.
type MemoryTier int

const (
	// TierRefused is below the floor. The model is never loaded.
	//
	// The zero value on purpose: a MemoryStatus nobody filled in must refuse,
	// never wave a load through.
	TierRefused MemoryTier = iota
	// TierMarginal is the 6 to 8 GiB band: load, warn, force idle unload, and
	// report degraded. With mmap the kernel evicts weight pages under pressure
	// and re-reads them from an SD card mid-decode; that is the thrash.
	TierMarginal
	// TierSupported is 8 GiB and up.
	TierSupported
)

// String renders the tier for /api/status.
func (t MemoryTier) String() string {
	switch t {
	case TierSupported:
		return "supported"
	case TierMarginal:
		return "marginal"
	case TierRefused:
		return "refused"
	}
	return "unknown"
}

// MemoryStatus is the whole answer of one preflight check: the real numbers,
// the tier, and the three decisions that follow from them.
type MemoryStatus struct {
	// TotalBytes is MemTotal.
	TotalBytes uint64
	// AvailableBytes is MemAvailable.
	AvailableBytes uint64
	// AvailableEstimated is true when MemAvailable was absent and MemFree was
	// used instead. MemFree is a much more pessimistic number, so a refusal
	// carrying this flag deserves the caveat in its message.
	AvailableEstimated bool
	// Tier is the support tier from MemTotal.
	Tier MemoryTier
	// CanLoad is the actual gate: the tier is not refused AND MemAvailable is
	// at least WorkingSetLowBytes right now.
	CanLoad bool
	// Degraded marks a machine that can load but has no headroom. Surfaced by
	// /api/status so the operator sees why scans are slow.
	Degraded bool
	// ForceIdleUnload tells the supervisor to keep the idle unload on even when
	// it would otherwise be disabled.
	ForceIdleUnload bool
	// Message is the human-readable status, carrying the real numbers.
	Message string
}

// Err returns ErrInsufficientMemory, wrapped with the real numbers, when the
// model must not be loaded. It returns nil when CanLoad is true.
func (s MemoryStatus) Err() error {
	if s.CanLoad {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrInsufficientMemory, s.Message)
}

// DefaultMeminfoPath is the kernel's memory summary.
const DefaultMeminfoPath = "/proc/meminfo"

// CheckMemory reads /proc/meminfo and classifies the machine.
//
// Exported and cheap on purpose: Step 35b's one-shot recovery re-runs it before
// every reload. FinishUnload clears pendingUnload, so after a recovery nothing
// else stands between the supervisor and a multi-GB allocation - and the most
// common trigger for that path is the OOM killer reaping llama-server, meaning
// a blind reload fires under exactly the pressure that caused the kill.
func CheckMemory() (MemoryStatus, error) { return CheckMemoryAt(DefaultMeminfoPath) }

// CheckMemoryAt is CheckMemory against an arbitrary meminfo file, which is what
// makes the tiers testable on a machine that is none of them.
func CheckMemoryAt(path string) (MemoryStatus, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return MemoryStatus{}, fmt.Errorf("read %s: %w", path, err)
	}
	total, ok := meminfoValueBytes(data, "MemTotal")
	if !ok || total == 0 {
		return MemoryStatus{}, fmt.Errorf("read %s: no usable MemTotal", path)
	}
	available, ok := meminfoValueBytes(data, "MemAvailable")
	estimated := false
	if !ok {
		// Pre-3.14 kernels and some minimal containers omit MemAvailable.
		// MemFree is the pessimistic substitute: it excludes reclaimable page
		// cache, so it under-reports what a load could actually use.
		available, ok = meminfoValueBytes(data, "MemFree")
		if !ok {
			return MemoryStatus{}, fmt.Errorf("read %s: neither MemAvailable nor MemFree", path)
		}
		estimated = true
	}
	return Classify(total, available, estimated), nil
}

// Classify turns raw byte counts into the tier and the three decisions.
//
// Split out from the file read so the table of tiers is testable directly and
// so the supervisor can classify numbers it already has.
func Classify(total, available uint64, availableEstimated bool) MemoryStatus {
	s := MemoryStatus{
		TotalBytes:         total,
		AvailableBytes:     available,
		AvailableEstimated: availableEstimated,
	}
	switch {
	case total >= supportedFloorBytes:
		s.Tier = TierSupported
	case total >= marginalFloorBytes:
		s.Tier = TierMarginal
		// Marginal machines keep the idle unload on whatever the operator's
		// memory-size heuristic would otherwise say.
		s.ForceIdleUnload = true
		s.Degraded = true
	default:
		s.Tier = TierRefused
	}

	// The tier comes from MemTotal, but the LOAD is gated on MemAvailable. An
	// 8 GB Pi running three other services is a 4 GB Pi as far as this
	// allocation is concerned, and MemTotal cannot see that.
	s.CanLoad = s.Tier != TierRefused && available >= WorkingSetLowBytes
	if s.CanLoad && available < WorkingSetHighBytes {
		s.Degraded = true
	}
	s.Message = describeMemory(s)
	return s
}

// describeMemory writes the operator-facing sentence. Every branch names the
// actual numbers: refusing is not failing, but it is only defensible if the user
// can see why.
func describeMemory(s MemoryStatus) string {
	avail := FormatBytes(s.AvailableBytes)
	if s.AvailableEstimated {
		avail += " free (MemAvailable absent, MemFree used, which under-reports)"
	} else {
		avail += " available"
	}
	base := fmt.Sprintf("%s total, %s", FormatBytes(s.TotalBytes), avail)

	switch {
	case s.Tier == TierRefused:
		return fmt.Sprintf("%s. Below the %s floor: the model needs %s to %s at run time and will not be loaded. "+
			"The service still runs and the demo badge shows that recognition is mocked.",
			base, FormatBytes(MarginalMemTotalBytes), FormatBytes(WorkingSetLowBytes), FormatBytes(WorkingSetHighBytes))
	case !s.CanLoad:
		return fmt.Sprintf("%s. Not enough free memory right now: the model needs at least %s. "+
			"Close other services and try again.", base, FormatBytes(WorkingSetLowBytes))
	case s.Tier == TierMarginal:
		return fmt.Sprintf("%s. Marginal: between %s and %s the model fits but has no headroom, so weight pages "+
			"get evicted and re-read mid-decode. Scans will be slow and the model is unloaded when idle.",
			base, FormatBytes(MarginalMemTotalBytes), FormatBytes(SupportedMemTotalBytes))
	case s.Degraded:
		return fmt.Sprintf("%s. Supported, but free memory is below the %s working set, so expect paging.",
			base, FormatBytes(WorkingSetHighBytes))
	default:
		return base + ". Supported."
	}
}

// FormatBytes renders a byte count in GiB with one decimal, or MiB below a
// gibibyte. Used in operator messages and in /api/status.
func FormatBytes(b uint64) string {
	if b >= GiB {
		return strconv.FormatFloat(float64(b)/float64(GiB), 'f', 1, 64) + " GiB"
	}
	return strconv.FormatUint(b/MiB, 10) + " MiB"
}

// meminfoValueBytes pulls one key out of a /proc/meminfo body and converts it to
// bytes.
//
// The format is "Key:<whitespace><number> kB", where the unit is always kB
// (meaning KiB) for memory sizes and absent for counts. Both shapes are handled;
// anything else is treated as absent rather than guessed at.
func meminfoValueBytes(data []byte, key string) (uint64, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found || name != key {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0, false
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0, false
		}
		if len(fields) >= 2 && strings.EqualFold(fields[1], "kB") {
			return n * KiB, true
		}
		return n, true
	}
	return 0, false
}
