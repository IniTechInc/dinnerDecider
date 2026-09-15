package contract

// DefaultScanCallLimit is the scan-wide cap on heavy (vision) model calls.
//
// Four, from the field fix the Swift initializer's default argument encodes: an
// 8GB phone jetsammed on the FIFTH heavy call of a scan, so a whole scan now
// gets at most four. One whole-image pass plus three crops.
//
// Raising this is follow-up F2 and is blocked: it is the one number that, on a
// device already sitting at its memory floor, turns a working scan into a kill.
const DefaultScanCallLimit = 4

// ScanCallBudget is the scan-wide budget of heavy model calls.
//
// Port of `struct ScanCallBudget` in DinnerDecider/Services/AppModel.swift.
// Pure value type, no I/O. One budget is created per scan and threaded through
// the whole pipeline: the whole-image pass spends one, and the crop loop is
// capped at Remaining() BEFORE it starts consuming.
//
// Once drained it stays drained forever; there is no refill, by design.
//
// The zero value is an already-exhausted budget, which is deliberate: it fails
// closed (no model calls) rather than open (unbounded calls). Construct with
// NewScanCallBudget or NewDefaultScanCallBudget.
type ScanCallBudget struct {
	remaining int
}

// NewScanCallBudget returns a budget of limit heavy calls.
//
// A limit below zero is clamped to zero. Swift cannot receive one in practice
// (the initializer is called with the default or with a test literal), and a
// negative Remaining() would panic the `crops[:budget.Remaining()]` capping
// expression at the scan-engine call site. That clamp is the only Linux-port
// addition here and is unreachable for every legal input.
func NewScanCallBudget(limit int) ScanCallBudget {
	if limit < 0 {
		limit = 0
	}
	return ScanCallBudget{remaining: limit}
}

// NewDefaultScanCallBudget returns a budget of DefaultScanCallLimit calls. It is
// the Go equivalent of Swift's `ScanCallBudget()` default argument.
func NewDefaultScanCallBudget() ScanCallBudget {
	return NewScanCallBudget(DefaultScanCallLimit)
}

// Consume spends one call. Returns false when the budget is exhausted, and an
// exhausted budget stays exhausted for the rest of the scan.
func (b *ScanCallBudget) Consume() bool {
	if b.remaining <= 0 {
		return false
	}
	b.remaining--
	return true
}

// Remaining reports how many heavy calls are left. Readable before consuming:
// the crop loop caps its work at this value up front, so the cap reflects what
// the whole-image pass already spent.
func (b ScanCallBudget) Remaining() int { return b.remaining }
