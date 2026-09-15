package llm

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
)

// Supervisor is the impure half of shared/pipeline/model-lifecycle.md: it turns
// the pure machine's DECISIONS into a real llama-server child process.
//
// # State mapping
//
//	iOS                                  | Linux
//	-------------------------------------+------------------------------------
//	LlamaClient resident in-process      | supervised llama-server on loopback
//	UNLOADED                             | no child process
//	LOADING                              | child spawned, /health not yet ok
//	READY                                | health ok, zero in flight
//	INFERRING                            | at least one request in flight
//	UNLOADING                            | SIGTERM sent, awaiting exit
//	jetsam                               | the Linux OOM killer reaping the child
//
// # THE RULE: act on the returned decision, never on the reason
//
// model-lifecycle.md § The other bug: mid-operation free: "every decision in
// this machine is the function's RETURN VALUE, and is never inferred from the
// caller's reason for calling."
//
//	Return         | Supervisor action
//	---------------+--------------------------------------------------------
//	PROCEED        | drain in flight (bounded), SIGTERM -> grace -> SIGKILL,
//	               | then FinishUnload()
//	DEFERRED       | record only; the drain points return PROCEED later
//	NOTHING_TO_DO  | nothing
//
// The reason chooses the METHOD, never the action:
//
//	Reason                                     | Method
//	-------------------------------------------+---------------------------
//	operator                                   | RequestUnload()
//	memory pressure                            | HandleMemoryPressure(level)
//	health failure / config change / idle      | RequestDeferredUnload(reason)
//	child already exited                       | no unload call at all (35b)
//
// And the full decision table the methods return:
//
//	Method                       | UNLOADED | LOADING  | READY no session | READY in session | INFERRING | UNLOADING
//	-----------------------------+----------+----------+------------------+------------------+-----------+----------
//	RequestUnload()              | NOTHING  | DEFERRED | PROCEED          | PROCEED          | DEFERRED  | NOTHING
//	HandleMemoryPressure(WARN)   | NOTHING  | NOTHING  | PROCEED          | NOTHING          | NOTHING   | NOTHING
//	HandleMemoryPressure(CRIT)   | NOTHING  | DEFERRED | PROCEED          | DEFERRED         | DEFERRED  | NOTHING
//	RequestDeferredUnload(r)     | NOTHING  | DEFERRED | PROCEED          | DEFERRED         | DEFERRED  | NOTHING
//
// Two consequences that are easy to lose in a port:
//
//   - AN OPERATOR RESTART DURING A SCAN IS NOT IMMEDIATE. A scan alternates
//     READY between calls and INFERRING during a call. At READY it proceeds even
//     mid-session; at INFERRING it DEFERS and fires on the drain. Mapping "the
//     operator asked" straight to "SIGTERM now" kills a child mid-encode, which
//     is the build-1 use-after-free class wearing a different hat: the in-flight
//     request dies with connection-refused, the scan loop swallows it, and the
//     user sees a failed scan.
//   - A PLAIN WARNING AT READY AND IDLE REALLY DOES UNLOAD. Warning is ignored
//     only while BUSY or inside a session. A port that treats it as
//     always-ignorable keeps several gigabytes resident through exactly the
//     pressure it was supposed to relieve.
//
// Drain points, per § Adapter obligations: after every load, after every
// inference, and at EndSession.
type Supervisor struct {
	opts Options

	// inferSlot is the single global inference gate (plan §0.1, layer 1).
	//
	// Capacity 1, and acquired BEFORE BeginInference is ever called, so the
	// contract's single-state machine is only ever asked when it can say yes.
	// llama-server runs one slot by design, so queueing here rather than
	// server-side keeps the Go-side machine's state and reality in agreement,
	// which is the whole reason the machine exists.
	//
	// Nothing else in this package may call lifecycle.BeginInference. If a
	// second call site ever appears, the concurrency bug this closes comes
	// straight back.
	inferSlot chan struct{}

	mu sync.Mutex
	// lc is the pure machine. Every read and write happens under mu.
	lc contract.ModelLifecycle
	// cur is the live child generation, or nil.
	cur *generation
	// nextEpoch hands out monotonic generation numbers.
	nextEpoch uint64
	// inFlight counts bracketed requests, for the bounded teardown drain.
	inFlight sync.WaitGroup

	// loadWait is closed when the in-flight load finishes; nil when no load is
	// running. loadErr is that load's result.
	loadWait chan struct{}
	loadErr  error

	// recovery state. recoveryUsed is reset at the start of each outermost
	// session: at most one recovery per scan.
	recovering   bool
	recoveryDone chan struct{}
	recoveryUsed bool
	recovery     Recovery

	// pendingRestart is set by RequestRestart and consumed when the unload it
	// asked for actually completes, which may be several minutes later if the
	// decision was DEFERRED mid-scan.
	pendingRestart bool

	// releasedUnderPressure drives the load-phase UI state: the next load is a
	// reload after a memory release, not a first load.
	releasedUnderPressure bool

	lastUse time.Time
	stats   Stats

	runCtx    context.Context
	runCancel context.CancelFunc
	monitors  sync.WaitGroup
	started   bool
	closed    bool
}

// generation is one child process and everything pinned to it.
//
// An inference captures this BY VALUE (epoch, base URL, api key, *http.Client)
// at call time. That is the Go equivalent of the iOS adapter's second layer of
// safety, where an in-flight token stream holds its own strong reference to the
// client it started with so that clearing the handle cannot tear a running
// stream out from under itself.
type generation struct {
	epoch   uint64
	pid     int
	port    int
	baseURL string
	apiKey  string
	client  *Client
	proc    *os.Process
	exited  chan struct{}
	waitErr error
	stderr  *ringBuffer
	started time.Time
}

// Typed errors. Which of these COUNT as inference failures is a contract
// decision, not a style choice: see CountsAsInferenceFailure.
var (
	// ErrModelNotReady means the call never reached the model. With the gate
	// held the only remaining cause is a genuinely broken invariant: the child
	// died between gate acquisition and bracket open.
	ErrModelNotReady = errors.New("model not ready")
	// ErrRecoveryExhausted is ErrModelNotReady after the one-shot recovery has
	// already been spent in this session. It WRAPS ErrModelNotReady so a caller
	// matching the general case still matches, and the scan engine aborts with
	// reason model_not_ready.
	ErrRecoveryExhausted = fmt.Errorf("%w: the one-shot recovery is already spent in this session", ErrModelNotReady)
	// ErrInferenceQueueTimeout means the caller waited out inferQueueTimeout on
	// the gate. It COUNTS: the call was admitted, attempted, and gave up with
	// nothing to show.
	ErrInferenceQueueTimeout = errors.New("timed out waiting for the inference gate")
	// ErrCancelled means the caller's context was cancelled. It does NOT count:
	// cancellation has its own terminal phase.
	ErrCancelled = errors.New("inference cancelled")
	// ErrEpochChanged means the child was replaced while the request was in
	// flight, so the answer describes a model that no longer exists. It COUNTS,
	// and is never reported as an empty result.
	ErrEpochChanged = errors.New("the model was replaced while the request was in flight")
	// ErrUnsafeRuntimeParams means the configured llama-server arguments are
	// refused. See BuildArgs.
	ErrUnsafeRuntimeParams = errors.New("refusing unsafe llama-server parameters")
	// ErrStartupTimeout means the child never became healthy inside the budget.
	ErrStartupTimeout = errors.New("llama-server did not become healthy in time")
	// ErrSupervisorClosed means Shutdown has run.
	ErrSupervisorClosed = errors.New("supervisor is shut down")
)

// CountsAsInferenceFailure is the single source of truth for the scan engine's
// failure counting, kept here because the supervisor is what produces these
// errors in the first place.
//
// From the plan's Step 49 table plus §0.1:
//
//	Outcome                             | Counted | Why
//	------------------------------------+---------+-------------------------
//	inference threw (transport, HTTP,    | yes     | both swallow sites count
//	bad response, deadline)              |         |
//	IdentifyAllItems returned [] from a  | no      | it answered, unreadably;
//	parse failure (no error at all)      |         | not an inference failure
//	ErrModelNotReady / recovery spent    | no      | never reached the model
//	ErrInferenceQueueTimeout             | yes     | admitted, attempted, gave up
//	ErrCancelled                         | no      | cancellation has its own phase
//	ErrEpochChanged                      | yes     | an answer about a dead model
//	ErrInsufficientMemory                | no      | an abort reason, not a call
//
// Counting a non-answer that never touched the model manufactures a spurious
// FAILED; not counting one that did manufactures "Nothing spotted". Both are
// shipped field bugs, in opposite directions.
func CountsAsInferenceFailure(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrModelNotReady):
		return false
	case errors.Is(err, ErrCancelled), errors.Is(err, context.Canceled):
		return false
	case errors.Is(err, ErrInsufficientMemory):
		return false
	default:
		return true
	}
}

// Options configures a supervisor.
type Options struct {
	// Runtime is the resolved llama-server (Step 34).
	Runtime Runtime
	// ModelPath and MMProjPath are the GGUF weights and the vision projector.
	ModelPath      string
	MMProjPath     string
	SkipFileChecks bool

	// Host is the loopback address the child binds. Port 0 picks an ephemeral
	// port per generation.
	Host string
	Port int

	// Runtime parameters. See BuildArgs for the guards.
	ContextSize         int
	BatchSize           int
	UBatchSize          int
	ParallelSlots       int
	ExtraArgs           []string
	UnsafeRuntimeParams bool

	// Env is appended to the child's environment.
	Env []string

	// StartupBudget bounds the wait for the first /health ok. Defaults to
	// DefaultStartupBudget.
	StartupBudget           time.Duration
	StartupPollInterval     time.Duration
	StartupProgressInterval time.Duration

	// Health checking while READY.
	HealthInterval         time.Duration
	HealthTimeout          time.Duration
	HealthFailureThreshold int

	// RequestTimeout bounds one model call; InferQueueTimeout bounds the wait
	// for the gate.
	RequestTimeout    time.Duration
	InferQueueTimeout time.Duration

	// DrainTimeout bounds the wait for in-flight requests before a teardown
	// escalates. TerminateGrace is how long SIGTERM gets before SIGKILL.
	DrainTimeout   time.Duration
	TerminateGrace time.Duration

	// IdleUnload unloads a resident, idle model after this long. Zero disables
	// it entirely, so the caller must set it explicitly; IdleUnloadPolicy turns
	// a preflight result into the right value.
	IdleUnload        time.Duration
	IdleCheckInterval time.Duration

	// MemCheck is the Step 41 preflight. Defaults to CheckMemory.
	MemCheck func() (MemoryStatus, error)

	// KnownAnswerProbe runs a one-token generation after the first successful
	// load. See Supervisor.knownAnswerProbe.
	KnownAnswerProbe bool
	// OnProbeFailure is called when that probe fails, wired to
	// Resolver.MarkCPUFallback so the degrade is sticky.
	OnProbeFailure func(variant, reason string)

	// Observer receives lifecycle events. Optional.
	Observer func(Event)
	// Logf receives operator-facing log lines. Optional.
	Logf func(string, ...any)

	// Test hooks. Unexported so no caller outside this package can reach them.
	// They exist to make three otherwise unreachable windows deterministic:
	// between gate acquisition and the bracket opening, immediately before the
	// HTTP call, and immediately after it returns.
	hookAfterGate  func()
	hookBeforeCall func()
	hookAfterCall  func()
}

// Defaults for the supervisor's timings.
const (
	// DefaultStartupBudget is 600 s, not the eval harness's 120 s.
	//
	// eval/run_eval.py waits 120 s for /health, which is fine for a warm Mac
	// with the weights in page cache. A cold 4 GB load from a Raspberry Pi's SD
	// card will exceed it, and a startup budget that expires on a working box
	// looks exactly like a broken one.
	DefaultStartupBudget = 600 * time.Second
	// DefaultStartupPollInterval matches eval's 1.5 s poll, rounded down.
	DefaultStartupPollInterval = time.Second
	// DefaultStartupProgressInterval is how often the wait says it is still
	// waiting. Ten minutes of silence is indistinguishable from a hang.
	DefaultStartupProgressInterval = 15 * time.Second
	// DefaultHealthInterval checks a resident model every 5 s while READY.
	DefaultHealthInterval = 5 * time.Second
	// DefaultHealthTimeout bounds one health request.
	DefaultHealthTimeout = 3 * time.Second
	// DefaultHealthFailureThreshold is 3 consecutive failures, about 15 s,
	// before the child is judged effectively gone.
	DefaultHealthFailureThreshold = 3
	// DefaultInferQueueTimeout bounds the wait on the gate.
	DefaultInferQueueTimeout = 10 * time.Minute
	// DefaultDrainTimeout bounds the wait for in-flight requests at teardown.
	DefaultDrainTimeout = 2 * time.Minute
	// DefaultTerminateGrace is how long SIGTERM gets. llama-server frees
	// several gigabytes of buffers on the way out.
	DefaultTerminateGrace = 15 * time.Second
	// DefaultIdleCheckInterval is how often the idle timer is examined.
	DefaultIdleCheckInterval = 30 * time.Second
	// DefaultIdleUnloadDelay is the idle unload on a memory-constrained box.
	DefaultIdleUnloadDelay = 15 * time.Minute
	// IdleUnloadDisabledAboveBytes is the total memory above which residency
	// costs nothing worth reclaiming, so the idle unload is off.
	IdleUnloadDisabledAboveBytes = 16 * GiB
)

// IdleUnloadFor is the idle-unload delay for a machine of this size: off at
// 16 GiB and up, 15 minutes below it. A cold reload costs minutes on a Pi, so
// unloading a model that a box has room to keep is a cost with no benefit.
func IdleUnloadFor(memTotalBytes uint64) time.Duration {
	if memTotalBytes >= IdleUnloadDisabledAboveBytes {
		return 0
	}
	return DefaultIdleUnloadDelay
}

// IdleUnloadPolicy resolves Options.IdleUnload from a Step 41 preflight result.
//
// It exists so the marginal tier's "force idle-unload" is not a field that the
// preflight sets and nobody reads. A 6 to 8 GiB box keeps the idle unload on
// even if some future size rule would have switched it off, because on that box
// residency is what makes the kernel evict weight pages and re-read them from an
// SD card mid-decode.
func IdleUnloadPolicy(status MemoryStatus) time.Duration {
	if status.ForceIdleUnload {
		return DefaultIdleUnloadDelay
	}
	return IdleUnloadFor(status.TotalBytes)
}

// EventKind names a supervisor event.
type EventKind string

// Events. These drive /api/status, the logs, and the tests, which is why the
// signal ones exist: "no SIGTERM while a request is in flight" is only testable
// if sending a signal is observable.
const (
	EventChildSpawned     EventKind = "child_spawned"
	EventChildHealthy     EventKind = "child_healthy"
	EventChildExited      EventKind = "child_exited"
	EventSignalSent       EventKind = "signal_sent"
	EventLoadFailed       EventKind = "load_failed"
	EventUnloadProceed    EventKind = "unload_proceed"
	EventUnloadDeferred   EventKind = "unload_deferred"
	EventUnloadNothing    EventKind = "unload_nothing_to_do"
	EventUnloadFinished   EventKind = "unload_finished"
	EventHealthFailed     EventKind = "health_failed"
	EventRecoveryStarted  EventKind = "recovery_started"
	EventRecoveryBlocked  EventKind = "recovery_blocked"
	EventRecoveryFinished EventKind = "recovery_finished"
	EventRecoverySpent    EventKind = "recovery_spent"
	EventEpochDiscarded   EventKind = "epoch_discarded"
	EventModelNotReady    EventKind = "model_not_ready"
	EventProbeFailed      EventKind = "probe_failed"
	EventIdleUnload       EventKind = "idle_unload"
)

// Event is one observable thing the supervisor did.
type Event struct {
	Kind   EventKind
	At     time.Time
	Epoch  uint64
	Cause  string
	Detail string
	Err    error
}

// Stats are the counters /api/status and the bench read.
type Stats struct {
	InferencesStarted   uint64
	InferencesSucceeded uint64
	InferenceFailures   uint64
	Spawns              uint64
	Recoveries          uint64
	QueueTimeouts       uint64
	EpochDiscards       uint64
}

// LoadPhase is the small load-phase UI state from the contract.
type LoadPhase string

const (
	// LoadPhaseIdle means no load is running.
	LoadPhaseIdle LoadPhase = "idle"
	// LoadPhaseFirstLoad is the first load, about 20 s on a phone and minutes
	// on a Pi.
	LoadPhaseFirstLoad LoadPhase = "first_load"
	// LoadPhaseReloadingAfterMemory means the model was previously released
	// under pressure, so the user gets "warming up again" rather than a bare
	// spinner.
	LoadPhaseReloadingAfterMemory LoadPhase = "reloading_after_memory"
)

// Recovery describes a one-shot recovery attempt.
type Recovery struct {
	Cause      string
	StartedAt  time.Time
	FinishedAt time.Time
	Err        error
	Used       bool
}

// Status is the supervisor's whole observable state.
type Status struct {
	State          contract.ModelState
	PendingUnload  bool
	ActiveSessions int
	LoadPhase      LoadPhase
	Epoch          uint64
	ChildPID       int
	BaseURL        string
	Variant        string
	RuntimeSource  RuntimeSource
	FellBackToCPU  bool
	FallbackReason string
	Recovering     bool
	RecoveryUsed   bool
	LastRecovery   Recovery
	Stats          Stats
}

// New builds a supervisor and validates its runtime parameters up front, so a
// configuration that would silently degrade recognition is refused at startup
// rather than at the first scan.
func New(opts Options) (*Supervisor, error) {
	opts = withDefaults(opts)
	if _, err := BuildArgs(opts, opts.Port, "validation-only"); err != nil {
		return nil, err
	}
	return &Supervisor{
		opts:      opts,
		inferSlot: make(chan struct{}, 1),
		lastUse:   time.Now(),
	}, nil
}

func withDefaults(o Options) Options {
	if o.Host == "" {
		o.Host = "127.0.0.1"
	}
	if o.ContextSize == 0 {
		o.ContextSize = 1024
	}
	if o.BatchSize == 0 {
		o.BatchSize = 512
	}
	if o.UBatchSize == 0 {
		o.UBatchSize = 512
	}
	if o.ParallelSlots == 0 {
		o.ParallelSlots = 1
	}
	if o.StartupBudget <= 0 {
		o.StartupBudget = DefaultStartupBudget
	}
	if o.StartupPollInterval <= 0 {
		o.StartupPollInterval = DefaultStartupPollInterval
	}
	if o.StartupProgressInterval <= 0 {
		o.StartupProgressInterval = DefaultStartupProgressInterval
	}
	if o.HealthInterval <= 0 {
		o.HealthInterval = DefaultHealthInterval
	}
	if o.HealthTimeout <= 0 {
		o.HealthTimeout = DefaultHealthTimeout
	}
	if o.HealthFailureThreshold <= 0 {
		o.HealthFailureThreshold = DefaultHealthFailureThreshold
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = DefaultRequestTimeout
	}
	if o.InferQueueTimeout <= 0 {
		o.InferQueueTimeout = DefaultInferQueueTimeout
	}
	if o.DrainTimeout <= 0 {
		o.DrainTimeout = DefaultDrainTimeout
	}
	if o.TerminateGrace <= 0 {
		o.TerminateGrace = DefaultTerminateGrace
	}
	if o.IdleCheckInterval <= 0 {
		o.IdleCheckInterval = DefaultIdleCheckInterval
	}
	if o.MemCheck == nil {
		o.MemCheck = CheckMemory
	}
	return o
}

// BuildArgs constructs the llama-server command line, with hard guards.
//
// The constant set, every time:
//
//	--host 127.0.0.1 --port <ephemeral> --api-key <random> --jinja
//	--ctx-size 1024 --batch-size 512 --ubatch-size 512 --parallel 1
//	--temp 0.2 --top-k 40 --top-p 0.95 --model <weights> --mmproj <mmproj>
//
// --jinja is MANDATORY: this GGUF's July 2026 chat template needs it on CLI
// tools, and without it the prompt is assembled wrongly rather than rejected.
//
// The guards exist because of how these failures present. BATCH 256 AND A
// CPU-SIDE MMPROJ BOTH SHIPPED ON iOS AND SILENTLY BROKE RECOGNITION RATHER THAN
// CRASHING: the scan completes, the model answers, and the answers are wrong or
// empty. Nothing in the logs says why. So a context that is not 1024 or a batch
// below 512 is refused outright unless --unsafe-runtime-params is passed, and
// the refusal carries the reason.
//
// --parallel is a GUARDED CONSTANT at 1, with the arithmetic in the message.
// llama-server divides --ctx-size across slots: at ctx 1024, -np 2 gives each
// slot 512 tokens, which is less than one image's roughly 256 vision tokens plus
// a 558-character prompt plus a reply. Preserving 1024 per slot would need
// --ctx-size 2048, doubling the KV cache, which is exactly the wired-memory
// lever the contract records going 2048 -> 1536 -> 1024. On a device at its
// memory floor, -np > 1 buys concurrency by spending the one resource already
// scarce. Concurrency is admitted on the Go side instead, by the gate.
func BuildArgs(o Options, port int, apiKey string) ([]string, error) {
	if !o.UnsafeRuntimeParams {
		if o.ContextSize != 1024 {
			return nil, fmt.Errorf("%w: --ctx-size %d is not the tuned 1024. The wired-memory work went "+
				"2048 -> 1536 -> 1024 and the value is pinned by shared/; raising it doubles the KV cache on a "+
				"box already at its memory floor. Pass --unsafe-runtime-params to override",
				ErrUnsafeRuntimeParams, o.ContextSize)
		}
		if o.BatchSize < 512 {
			return nil, fmt.Errorf("%w: --batch-size %d is below 512. Batch 256 and a CPU-side mmproj both "+
				"shipped on iOS and SILENTLY BROKE RECOGNITION rather than crashing: the scan finishes, the "+
				"model answers, and the answers are wrong. Pass --unsafe-runtime-params to override",
				ErrUnsafeRuntimeParams, o.BatchSize)
		}
		if o.UBatchSize < 512 {
			return nil, fmt.Errorf("%w: --ubatch-size %d is below 512, which has the same silent-recognition "+
				"failure as a small --batch-size. Pass --unsafe-runtime-params to override",
				ErrUnsafeRuntimeParams, o.UBatchSize)
		}
		if o.ParallelSlots != 1 {
			perSlot := o.ContextSize / maxInt(o.ParallelSlots, 1)
			return nil, fmt.Errorf("%w: --parallel %d is not 1. llama-server divides --ctx-size across slots, "+
				"so %d slots at ctx %d leave %d tokens each, against roughly 256 vision tokens for one image "+
				"plus a 558-character prompt plus a reply. Keeping 1024 per slot would need ctx %d, doubling "+
				"the KV cache. Concurrency is admitted by the Go-side gate instead. Pass "+
				"--unsafe-runtime-params to override",
				ErrUnsafeRuntimeParams, o.ParallelSlots, o.ParallelSlots, o.ContextSize, perSlot,
				o.ContextSize*o.ParallelSlots)
		}
		for _, arg := range o.ExtraArgs {
			if strings.HasPrefix(arg, "--no-mmproj-offload") {
				return nil, fmt.Errorf("%w: --no-mmproj-offload runs the vision projector on the CPU, which "+
					"is the other half of the iOS silent-recognition failure. Pass --unsafe-runtime-params "+
					"to override", ErrUnsafeRuntimeParams)
			}
		}
	}
	if !o.SkipFileChecks {
		if o.ModelPath == "" {
			return nil, fmt.Errorf("no model weights configured")
		}
		if o.MMProjPath == "" {
			return nil, fmt.Errorf("no mmproj configured: without the vision projector the model cannot see the photo at all")
		}
	}

	args := []string{
		"--host", o.Host,
		"--port", strconv.Itoa(port),
		"--api-key", apiKey,
		// Mandatory for this GGUF's chat template.
		"--jinja",
		"--ctx-size", strconv.Itoa(o.ContextSize),
		"--batch-size", strconv.Itoa(o.BatchSize),
		"--ubatch-size", strconv.Itoa(o.UBatchSize),
		"--parallel", strconv.Itoa(o.ParallelSlots),
		"--temp", strconv.FormatFloat(SamplingTemperature, 'g', -1, 64),
		"--top-k", strconv.Itoa(SamplingTopK),
		"--top-p", strconv.FormatFloat(SamplingTopP, 'g', -1, 64),
		"--model", o.ModelPath,
		"--mmproj", o.MMProjPath,
	}
	return append(args, o.ExtraArgs...), nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Start begins the background monitors. It does NOT load the model: readiness
// means "serving", not "warm", and the HTTP listener must come up first.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	if s.closed {
		s.mu.Unlock()
		return ErrSupervisorClosed
	}
	s.started = true
	s.runCtx, s.runCancel = context.WithCancel(context.WithoutCancel(ctx))
	runCtx := s.runCtx
	s.mu.Unlock()

	s.monitors.Add(2)
	go func() {
		defer s.monitors.Done()
		s.healthLoop(runCtx)
	}()
	go func() {
		defer s.monitors.Done()
		s.idleLoop(runCtx)
	}()
	return nil
}

// Shutdown stops the monitors and tears the child down.
//
// The teardown here is unconditional rather than decision-driven: the process is
// exiting, so there is no later drain point that could honour a deferral, and
// leaving a 4 GB child behind is the worst outcome available. In-flight requests
// are still drained first, bounded.
func (s *Supervisor) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cancel := s.runCancel
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	s.monitors.Wait()

	s.waitInFlight(s.opts.DrainTimeout)
	s.mu.Lock()
	gen := s.cur
	s.cur = nil
	s.mu.Unlock()
	if gen != nil {
		s.stopChild(gen, "shutdown")
	}
	s.mu.Lock()
	s.lc.FinishUnload()
	s.mu.Unlock()
	return nil
}

// --- Sessions -------------------------------------------------------------

// BeginSession opens a user-visible model session (a whole scan, a whole recipe
// generation). Sessions nest.
//
// Opening the OUTERMOST session also resets the one-shot recovery budget: at
// most one recovery per scan. A cold reload costs minutes on a Pi, and burning
// the user's time twice in silence is worse than an honest error.
func (s *Supervisor) BeginSession() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.lc.SessionActive() {
		s.recoveryUsed = false
	}
	s.lc.BeginSession()
}

// EndSession closes a session and honours any unload deferred during it. It must
// run on EVERY exit path including cancellation: closing the session is what
// runs a deferred unload, so skipping it leaves the model wired forever.
func (s *Supervisor) EndSession() contract.UnloadDecision {
	s.mu.Lock()
	d := s.lc.EndSession()
	s.mu.Unlock()
	s.applyUnloadDecision(d, "session end")
	return d
}

// --- The four unload entry points -----------------------------------------

// RequestUnload is the operator's explicit unload. At READY it PROCEEDS even
// mid-session; at INFERRING it DEFERS.
func (s *Supervisor) RequestUnload() contract.UnloadDecision {
	s.mu.Lock()
	d := s.lc.RequestUnload()
	s.mu.Unlock()
	s.applyUnloadDecision(d, "operator unload")
	return d
}

// RequestRestart is the operator's restart: the same decision as RequestUnload,
// with a reload queued behind whatever that decision turns out to be.
//
// At INFERRING this returns DEFERRED and sends NO SIGNAL. The restart happens on
// the post-inference drain. That is the whole point of acting on the return
// value: an operator restart mid-encode is the build-1 use-after-free in
// different clothes.
func (s *Supervisor) RequestRestart() contract.UnloadDecision {
	s.mu.Lock()
	s.pendingRestart = true
	d := s.lc.RequestUnload()
	state := s.lc.State()
	s.mu.Unlock()

	if d == contract.UnloadNothingToDo && state == contract.Unloaded {
		s.mu.Lock()
		s.pendingRestart = false
		s.mu.Unlock()
		go s.loadAsync("operator restart from unloaded")
		return d
	}
	s.applyUnloadDecision(d, "operator restart")
	return d
}

// HandleMemoryPressure funnels every pressure source into the machine. The
// poller calls this; so does anything else that learns the box is unhappy.
func (s *Supervisor) HandleMemoryPressure(level contract.MemoryPressureLevel) contract.UnloadDecision {
	s.mu.Lock()
	d := s.lc.HandleMemoryPressure(level)
	if d == contract.UnloadProceed || d == contract.UnloadDeferred {
		s.releasedUnderPressure = true
	}
	s.mu.Unlock()
	s.applyUnloadDecision(d, "memory pressure "+level.String())
	return d
}

// RequestDeferredUnload is the non-memory, session-respecting unload: a config
// change, an idle timeout, or a health failure the recovery cannot or should not
// handle. Identical semantics to HandleMemoryPressure(CRITICAL) in every state.
func (s *Supervisor) RequestDeferredUnload(reason contract.UnloadReason) contract.UnloadDecision {
	s.mu.Lock()
	d := s.lc.RequestDeferredUnload(reason)
	s.mu.Unlock()
	s.applyUnloadDecision(d, string(reason))
	return d
}

// applyUnloadDecision is the ONE place a decision becomes an action. Nothing
// else in this file may send a signal on a lifecycle decision.
func (s *Supervisor) applyUnloadDecision(d contract.UnloadDecision, cause string) {
	switch d {
	case contract.UnloadProceed:
		s.event(Event{Kind: EventUnloadProceed, Cause: cause})
		s.performUnload(cause)
	case contract.UnloadDeferred:
		// Record only. The drain points return PROCEED later.
		s.event(Event{Kind: EventUnloadDeferred, Cause: cause})
		s.logf("unload deferred (%s): the model is busy or mid-session and will be freed when it drains", cause)
	case contract.UnloadNothingToDo:
		s.event(Event{Kind: EventUnloadNothing, Cause: cause})
	}
}

// drain runs DrainPendingUnload and acts on its decision. Called after every
// load and after every inference; EndSession drains through the machine itself.
func (s *Supervisor) drain(cause string) {
	s.mu.Lock()
	d := s.lc.DrainPendingUnload()
	s.mu.Unlock()
	if d != contract.UnloadNothingToDo {
		s.applyUnloadDecision(d, "drain: "+cause)
	}
}

// performUnload does the real teardown. PRECONDITION: the machine returned
// PROCEED, which it only ever does from READY, so nothing is in flight.
//
// The WaitGroup is still waited on. It is cheap when the precondition holds and
// it is the only thing standing between a teardown and a use-after-free if the
// precondition ever stops holding. Because the decision was taken under the same
// mutex that guards BeginInference, no new request can be admitted between the
// decision and this wait.
func (s *Supervisor) performUnload(cause string) {
	s.waitInFlight(s.opts.DrainTimeout)

	s.mu.Lock()
	gen := s.cur
	s.cur = nil
	restart := s.pendingRestart
	s.pendingRestart = false
	s.mu.Unlock()

	if gen != nil {
		s.stopChild(gen, cause)
	}

	s.mu.Lock()
	s.lc.FinishUnload()
	s.mu.Unlock()
	s.event(Event{Kind: EventUnloadFinished, Cause: cause})

	if restart {
		go s.loadAsync("operator restart")
	}
}

// stopChild ends a child: SIGTERM to the process group, grace, then SIGKILL.
//
// A child that has ALREADY EXITED is never signalled. That is not an
// optimisation: "the child already exited" is its own row in the reason table
// with "no unload call at all", and signalling a corpse would show up in the
// event stream as a SIGTERM that the tests, correctly, forbid.
func (s *Supervisor) stopChild(gen *generation, cause string) {
	if gen == nil || gen.proc == nil {
		return
	}
	select {
	case <-gen.exited:
		s.logf("child %d already exited (%s); no signal sent", gen.pid, cause)
		return
	default:
	}

	s.event(Event{Kind: EventSignalSent, Epoch: gen.epoch, Cause: cause, Detail: "SIGTERM"})
	s.logf("stopping llama-server pid %d (%s): SIGTERM", gen.pid, cause)
	if err := signalProcessGroup(gen.proc, syscall.SIGTERM); err != nil {
		s.logf("SIGTERM to %d failed: %v", gen.pid, err)
	}

	select {
	case <-gen.exited:
		return
	case <-time.After(s.opts.TerminateGrace):
	}

	s.event(Event{Kind: EventSignalSent, Epoch: gen.epoch, Cause: cause, Detail: "SIGKILL"})
	s.logf("llama-server pid %d did not exit within %s; SIGKILL", gen.pid, s.opts.TerminateGrace)
	if err := signalProcessGroup(gen.proc, syscall.SIGKILL); err != nil {
		s.logf("SIGKILL to %d failed: %v", gen.pid, err)
	}
	select {
	case <-gen.exited:
	case <-time.After(s.opts.TerminateGrace):
		s.logf("llama-server pid %d is unreapable; leaking the handle rather than blocking forever", gen.pid)
	}
}

// waitInFlight waits for bracketed requests to finish, bounded. Returns false
// when the bound was hit.
func (s *Supervisor) waitInFlight(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.inFlight.Wait()
		close(done)
	}()
	if timeout <= 0 {
		<-done
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// --- Loading --------------------------------------------------------------

// EnsureLoaded loads the model if it is not already resident, coalescing with
// any load already in flight.
func (s *Supervisor) EnsureLoaded(ctx context.Context) error { return s.load(ctx, "ensure loaded") }

func (s *Supervisor) loadAsync(cause string) {
	s.mu.Lock()
	ctx := s.runCtx
	s.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.load(ctx, cause); err != nil {
		s.logf("%s: load failed: %v", cause, err)
	}
}

func (s *Supervisor) load(ctx context.Context, cause string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrSupervisorClosed
	}
	switch s.lc.BeginLoad() {
	case contract.LoadAlreadyLoaded:
		s.mu.Unlock()
		return nil
	case contract.LoadCoalesced:
		wait := s.loadWait
		s.mu.Unlock()
		if wait == nil {
			// Coalesced onto an UNLOADING state: there is no load to wait for,
			// and the caller retries once idle.
			return ErrModelNotReady
		}
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
		s.mu.Lock()
		err := s.loadErr
		s.mu.Unlock()
		return err
	}

	wait := make(chan struct{})
	s.loadWait = wait
	s.loadErr = nil
	s.mu.Unlock()

	err := s.performLoad(ctx, cause)

	s.mu.Lock()
	s.loadErr = err
	s.loadWait = nil
	s.lc.FinishLoad(err == nil)
	if err == nil {
		s.releasedUnderPressure = false
		s.lastUse = time.Now()
	}
	s.mu.Unlock()
	close(wait)

	// Drain after every load: a CRITICAL that arrived while the multi-GB
	// allocation was in flight is honoured the moment it is safe.
	s.drain("post-load")
	return err
}

// performLoad runs the preflight, spawns the child and waits for health.
func (s *Supervisor) performLoad(ctx context.Context, cause string) error {
	status, err := s.opts.MemCheck()
	if err != nil {
		s.logf("memory preflight failed to read the system's numbers (%v); refusing to load blind", err)
		s.event(Event{Kind: EventLoadFailed, Cause: cause, Err: err})
		return fmt.Errorf("memory preflight: %w", err)
	}
	if !status.CanLoad {
		s.event(Event{Kind: EventLoadFailed, Cause: cause, Detail: status.Message, Err: status.Err()})
		s.logf("refusing to load the model: %s", status.Message)
		return status.Err()
	}

	gen, err := s.spawn()
	if err != nil {
		s.event(Event{Kind: EventLoadFailed, Cause: cause, Err: err})
		return err
	}

	if err := s.waitHealthy(ctx, gen); err != nil {
		s.stopChild(gen, "startup failed")
		s.event(Event{Kind: EventLoadFailed, Cause: cause, Epoch: gen.epoch, Err: err})
		return err
	}

	s.mu.Lock()
	s.cur = gen
	s.mu.Unlock()
	s.event(Event{Kind: EventChildHealthy, Epoch: gen.epoch, Cause: cause})
	s.logf("llama-server pid %d is serving on %s after %s", gen.pid, gen.baseURL, time.Since(gen.started).Round(time.Millisecond))

	if s.opts.KnownAnswerProbe {
		if err := s.knownAnswerProbe(ctx, gen); err != nil {
			s.mu.Lock()
			s.cur = nil
			s.mu.Unlock()
			s.stopChild(gen, "known-answer probe failed")
			s.event(Event{Kind: EventProbeFailed, Epoch: gen.epoch, Err: err})
			if s.opts.OnProbeFailure != nil {
				s.opts.OnProbeFailure(s.opts.Runtime.Variant, describeProbeFailure(err))
			}
			return fmt.Errorf("%w: %v", ErrRuntimeProbeFailed, err)
		}
	}
	return nil
}

// spawn starts one child generation.
func (s *Supervisor) spawn() (*generation, error) {
	if s.opts.Runtime.BinaryPath == "" {
		return nil, fmt.Errorf("no llama-server binary resolved")
	}
	if !s.opts.SkipFileChecks {
		for _, path := range []string{s.opts.ModelPath, s.opts.MMProjPath} {
			if _, err := os.Stat(path); err != nil {
				return nil, fmt.Errorf("model file: %w", err)
			}
		}
	}

	port := s.opts.Port
	if port == 0 {
		free, err := freePort(s.opts.Host)
		if err != nil {
			return nil, fmt.Errorf("pick a port: %w", err)
		}
		port = free
	}
	apiKey, err := randomAPIKey()
	if err != nil {
		return nil, err
	}
	args, err := BuildArgs(s.opts, port, apiKey)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.nextEpoch++
	epoch := s.nextEpoch
	s.stats.Spawns++
	s.mu.Unlock()

	stderr := newRingBuffer(16 << 10)
	cmd := newChildCommand(s.opts.Runtime.BinaryPath, args)
	cmd.Env = append(appendLibraryPath(os.Environ(), s.opts.Runtime.LibraryDir), s.opts.Env...)
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	configureChildProcess(cmd)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start llama-server: %w", err)
	}
	if err := adjustChildOOMScore(cmd.Process.Pid); err != nil {
		s.logf("could not raise the child's oom_score_adj (%v); the kernel may reap the service instead of llama-server", err)
	}

	baseURL := "http://" + net.JoinHostPort(s.opts.Host, strconv.Itoa(port))
	gen := &generation{
		epoch:   epoch,
		pid:     cmd.Process.Pid,
		port:    port,
		baseURL: baseURL,
		apiKey:  apiKey,
		client:  NewClient(baseURL, apiKey, s.opts.RequestTimeout),
		proc:    cmd.Process,
		exited:  make(chan struct{}),
		stderr:  stderr,
		started: time.Now(),
	}
	s.event(Event{Kind: EventChildSpawned, Epoch: epoch, Detail: baseURL})
	s.logf("spawned llama-server pid %d on %s (%s)", gen.pid, baseURL, s.opts.Runtime.Variant)

	go func() {
		gen.waitErr = cmd.Wait()
		close(gen.exited)
		s.onChildExit(gen)
	}()
	return gen, nil
}

// waitHealthy polls /health until the child serves, the budget expires, or the
// child dies.
func (s *Supervisor) waitHealthy(ctx context.Context, gen *generation) error {
	deadline := time.Now().Add(s.opts.StartupBudget)
	lastProgress := time.Now()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-gen.exited:
			return fmt.Errorf("llama-server exited during startup (%v): %s",
				gen.waitErr, snippet([]byte(gen.stderr.String())))
		default:
		}

		healthCtx, cancel := context.WithTimeout(ctx, s.opts.HealthTimeout)
		err := gen.client.Health(healthCtx)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %s elapsed waiting for %s (%v)",
				ErrStartupTimeout, s.opts.StartupBudget, gen.baseURL, err)
		}
		if time.Since(lastProgress) >= s.opts.StartupProgressInterval {
			lastProgress = time.Now()
			// Progress logging is not decoration: a cold 4 GB load from an SD
			// card takes minutes, and silence is indistinguishable from a hang.
			s.logf("still loading the model: %s elapsed of a %s budget (%s)",
				time.Since(gen.started).Round(time.Second), s.opts.StartupBudget, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.opts.StartupPollInterval):
		}
	}
}

// knownAnswerProbe is the second half of "probe before trusting".
//
// --version proves the loader resolved every .so. It does NOT prove the build
// can compute: a CUDA build against a mismatched driver can start, answer
// /health, and then produce garbage or die on the first real token. So the first
// successful load spends one token on a known question.
//
// The PASS criterion is deliberately weak: a non-empty completion. A single
// greedy token from a 3-bit quant is not a contract, so a wrong answer is logged
// and not acted on, while an EMPTY answer or an error fails the probe and
// triggers the sticky CPU fallback. Testing content equality here would demote
// working CUDA boxes to the CPU over a tokenizer difference.
func (s *Supervisor) knownAnswerProbe(ctx context.Context, gen *generation) error {
	const prompt = "The capital of France is"
	const expect = "paris"

	body, err := json.Marshal(map[string]any{
		"prompt":       prompt,
		"n_predict":    1,
		"temperature":  0,
		"cache_prompt": false,
	})
	if err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, gen.baseURL+"/completion", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+gen.apiKey)

	resp, err := gen.client.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("known-answer probe: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("known-answer probe: %s", resp.Status)
	}
	var payload struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("known-answer probe: %w", err)
	}
	if strings.TrimSpace(payload.Content) == "" {
		return fmt.Errorf("known-answer probe produced no tokens")
	}
	if !strings.Contains(strings.ToLower(payload.Content), expect) {
		s.logf("known-answer probe returned %q rather than something containing %q; "+
			"one greedy token is not a contract, so this is noted and not acted on", payload.Content, expect)
	}
	return nil
}

// --- Inference ------------------------------------------------------------

// RequestKind selects which call this is.
type RequestKind string

const (
	// KindIdentifyItem is the single-item crop call. Parse failures THROW.
	KindIdentifyItem RequestKind = "identify_item"
	// KindIdentifyAllItems is the whole-image pass. Parse failures return an
	// empty list with no error.
	KindIdentifyAllItems RequestKind = "identify_all_items"
	// KindRecipes is the text-only recipe call.
	KindRecipes RequestKind = "recipes"
)

// Request is one model call.
type Request struct {
	Kind      RequestKind
	Prompt    string
	ImageJPEG []byte
}

// Response is one model answer. Exactly one of the typed fields is meaningful,
// chosen by the request kind.
type Response struct {
	Kind               RequestKind
	Text               string
	Item               contract.IdentifiedItem
	Items              []contract.IdentifiedItem
	Bundle             contract.RecipeBundleResponse
	Epoch              uint64
	PredictedPerSecond float64
	Elapsed            time.Duration
}

// Infer is the ONLY admission point to the model.
//
//	select {
//	case gate <- struct{}{}:
//	case <-ctx.Done():          return ErrCancelled              // NOT counted
//	case <-time.After(queueTO): return ErrInferenceQueueTimeout  // COUNTED
//	}
//	defer func(){ <-gate }()
//	if !lifecycle.BeginInference() { return ErrModelNotReady }   // NOT counted
//	defer lifecycle.FinishInference(); defer drain()
//
// Gate acquisition is context-aware so it QUEUES rather than fails: on a Pi the
// user is already waiting minutes and a queue wait is invisible against a
// four-minute scan. The bounded timeout exists only so a wedged holder cannot
// block every caller forever.
//
// The gate is released on EVERY path, including a panic: the release is a
// separate, earlier-registered defer, so it still runs while the panic unwinds
// through the bracket-closing defer.
func (s *Supervisor) Infer(ctx context.Context, req Request) (Response, error) {
	queue := time.NewTimer(s.opts.InferQueueTimeout)
	defer queue.Stop()

	select {
	case s.inferSlot <- struct{}{}:
	case <-ctx.Done():
		// Never admitted, never attempted. Not a failure.
		return Response{}, ErrCancelled
	case <-queue.C:
		return Response{}, s.record(ErrInferenceQueueTimeout)
	}
	// Registered FIRST so it runs LAST, and so a panic inside the bracket
	// still unwinds through it.
	defer func() { <-s.inferSlot }()

	if s.opts.hookAfterGate != nil {
		s.opts.hookAfterGate()
	}

	// A restart or a recovery may have a load in flight. Waiting for it is the
	// same queueing decision as the gate itself: the caller wanted the model,
	// and the model is on its way.
	if err := s.waitForLoad(ctx); err != nil {
		return Response{}, err
	}

	gen, ok := s.beginInference()
	if !ok {
		s.event(Event{Kind: EventModelNotReady})
		// NOT counted: it never reached the model. The caller runs the one-shot
		// recovery and retries once; it must not silently skip either, or the
		// scan ends at TerminalPhase(0, 0) = Empty, the "Nothing spotted" lie.
		return Response{}, ErrModelNotReady
	}
	defer s.closeInferenceBracket()

	if s.opts.hookBeforeCall != nil {
		s.opts.hookBeforeCall()
	}

	started := time.Now()
	callCtx, cancel := context.WithTimeout(ctx, s.opts.RequestTimeout)
	defer cancel()
	resp, err := s.call(callCtx, gen, req)

	if s.opts.hookAfterCall != nil {
		s.opts.hookAfterCall()
	}

	if err != nil {
		return Response{}, s.record(classifyCallError(ctx, err))
	}
	// Epoch pinning. If the generation moved while this was in flight, the
	// answer describes a model that no longer exists: discard it and count it
	// as a failure. Reporting it as an empty result would be the "Nothing
	// spotted" lie with extra steps.
	if !s.epochAlive(gen.epoch) {
		s.event(Event{Kind: EventEpochDiscarded, Epoch: gen.epoch})
		s.mu.Lock()
		s.stats.EpochDiscards++
		s.mu.Unlock()
		return Response{}, s.record(ErrEpochChanged)
	}

	resp.Kind = req.Kind
	resp.Epoch = gen.epoch
	resp.Elapsed = time.Since(started)
	s.mu.Lock()
	s.stats.InferencesSucceeded++
	s.mu.Unlock()
	return resp, nil
}

// beginInference takes the bracket under the mutex.
//
// The pre-checks are not redundant with BeginInference: the machine can still be
// READY while the child is known to be gone, which is exactly the window a
// recovery runs in.
func (s *Supervisor) beginInference() (generation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.recovering || s.cur == nil {
		return generation{}, false
	}
	if !s.lc.BeginInference() {
		return generation{}, false
	}
	s.inFlight.Add(1)
	s.stats.InferencesStarted++
	s.lastUse = time.Now()
	// By value: the epoch, base URL, api key and client are pinned for the
	// whole call, so a teardown cannot change what this request is talking to.
	return *s.cur, true
}

// closeInferenceBracket runs FinishInference, releases the in-flight count, and
// drains, in that order. It is a single defer so the ordering is explicit rather
// than a property of how the defers were registered.
func (s *Supervisor) closeInferenceBracket() {
	s.mu.Lock()
	s.lc.FinishInference()
	s.lastUse = time.Now()
	s.mu.Unlock()
	s.inFlight.Done()
	s.drain("post-inference")
}

// waitForLoad blocks while a load is in flight, so a caller queues behind a
// restart rather than failing against a model that is two seconds from ready.
func (s *Supervisor) waitForLoad(ctx context.Context) error {
	s.mu.Lock()
	wait := s.loadWait
	loading := s.lc.State() == contract.Loading
	s.mu.Unlock()
	if wait == nil || !loading {
		return nil
	}
	select {
	case <-wait:
		return nil
	case <-ctx.Done():
		return ErrCancelled
	}
}

// call dispatches to the client and applies the per-call-site failure semantics.
func (s *Supervisor) call(ctx context.Context, gen generation, req Request) (Response, error) {
	switch req.Kind {
	case KindIdentifyItem:
		item, completion, err := gen.client.IdentifyItem(ctx, req.Prompt, req.ImageJPEG)
		if err != nil {
			return Response{}, err
		}
		return Response{Item: item, Text: completion.Text, PredictedPerSecond: completion.PredictedPerSecond}, nil
	case KindIdentifyAllItems:
		items, completion, err := gen.client.IdentifyAllItems(ctx, req.Prompt, req.ImageJPEG)
		if err != nil {
			return Response{}, err
		}
		return Response{Items: items, Text: completion.Text, PredictedPerSecond: completion.PredictedPerSecond}, nil
	case KindRecipes:
		bundle, completion, err := gen.client.GenerateRecipes(ctx, req.Prompt)
		if err != nil {
			return Response{}, err
		}
		return Response{Bundle: bundle, Text: completion.Text, PredictedPerSecond: completion.PredictedPerSecond}, nil
	default:
		return Response{}, fmt.Errorf("unknown request kind %q", req.Kind)
	}
}

// classifyCallError maps a transport error onto the typed errors the counting
// rules are written against.
//
// The caller's OWN cancellation becomes ErrCancelled and is not counted. A
// deadline, including our own request timeout, stays counted: the call was
// admitted and attempted and produced nothing, which is the same shape as a
// queue timeout.
func classifyCallError(callerCtx context.Context, err error) error {
	if callerCtx.Err() != nil && errors.Is(callerCtx.Err(), context.Canceled) {
		return fmt.Errorf("%w: %v", ErrCancelled, err)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %v", ErrCancelled, err)
	}
	return err
}

// record counts an error if the contract says it counts, and returns it
// unchanged so call sites read as `return s.record(err)`.
func (s *Supervisor) record(err error) error {
	if !CountsAsInferenceFailure(err) {
		return err
	}
	s.mu.Lock()
	s.stats.InferenceFailures++
	if errors.Is(err, ErrInferenceQueueTimeout) {
		s.stats.QueueTimeouts++
	}
	s.mu.Unlock()
	return err
}

func (s *Supervisor) epochAlive(epoch uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur != nil && s.cur.epoch == epoch
}

// --- Health, exit and recovery -------------------------------------------

// healthLoop checks a resident model every HealthInterval while READY.
//
// Only while READY, deliberately. During an inference the request itself is the
// liveness proof, and a health probe competing with a multi-minute vision call
// on a one-slot server produces false failures on exactly the box least able to
// afford a spurious reload.
func (s *Supervisor) healthLoop(ctx context.Context) {
	ticker := time.NewTicker(s.opts.HealthInterval)
	defer ticker.Stop()
	consecutive := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		s.mu.Lock()
		gen := s.cur
		ready := s.lc.State() == contract.Ready && !s.recovering
		s.mu.Unlock()
		if gen == nil || !ready {
			consecutive = 0
			continue
		}

		healthCtx, cancel := context.WithTimeout(ctx, s.opts.HealthTimeout)
		err := gen.client.Health(healthCtx)
		cancel()
		if err == nil {
			consecutive = 0
			continue
		}
		consecutive++
		s.event(Event{Kind: EventHealthFailed, Epoch: gen.epoch, Detail: strconv.Itoa(consecutive), Err: err})
		s.logf("health check %d/%d failed for pid %d: %v", consecutive, s.opts.HealthFailureThreshold, gen.pid, err)
		if consecutive < s.opts.HealthFailureThreshold {
			continue
		}
		consecutive = 0
		s.childEffectivelyGone(ctx, fmt.Sprintf("%d consecutive health checks failed", s.opts.HealthFailureThreshold))
	}
}

// onChildExit runs when a child process ends.
func (s *Supervisor) onChildExit(gen *generation) {
	s.mu.Lock()
	current := s.cur != nil && s.cur.epoch == gen.epoch
	ctx := s.runCtx
	closed := s.closed
	s.mu.Unlock()

	s.event(Event{Kind: EventChildExited, Epoch: gen.epoch, Err: gen.waitErr,
		Detail: snippet([]byte(gen.stderr.String()))})

	if !current || closed {
		// Expected: we asked it to stop, or a newer generation already
		// replaced it.
		return
	}
	s.logf("llama-server pid %d exited unexpectedly (%v)", gen.pid, gen.waitErr)
	if ctx == nil {
		ctx = context.Background()
	}
	s.childEffectivelyGone(ctx, fmt.Sprintf("the child exited (%v)", gen.waitErr))
}

// childEffectivelyGone is the ONE entry point for both failure shapes.
//
// The child is "effectively gone" if EITHER the process exited OR
// HealthFailureThreshold consecutive health checks failed, about 15 seconds.
// Treating those asymmetrically is what an earlier revision did, and it let a
// wedged-but-alive child guarantee that every remaining call in a scan failed:
// the process was still there, so nothing recovered it, and every call timed out
// against it in turn.
//
// NOTE which method is NOT called here. The child is already gone, so there is
// nothing to unload and no decision to act on: the reason table's last row is
// "child already exited -> no unload call". The recovery calls FinishUnload()
// directly, as the adapter reporting a teardown that already happened.
func (s *Supervisor) childEffectivelyGone(ctx context.Context, cause string) {
	go func() {
		if err := s.Recover(ctx, cause); err != nil {
			s.logf("recovery after %s did not restore the model: %v", cause, err)
		}
	}()
}

// Recover runs the one-shot recovery: at most once per session.
//
// Steps, in order, from the plan's 35b:
//
//  1. Mark the epoch dead, so in-flight answers are discarded rather than
//     believed.
//  2. Let in-flight requests fail naturally; their brackets run FinishInference.
//  3. FinishUnload() - the adapter reporting teardown complete.
//  4. RE-RUN THE STEP 41 MEMORY PREFLIGHT BEFORE RELOADING. FinishUnload clears
//     pendingUnload, so nothing else stands between recovery and a multi-GB
//     allocation, and the most common trigger for this path is the OOM killer,
//     which means a blind reload fires under exactly the pressure that caused
//     the kill. If the preflight fails, DO NOT RELOAD: abort with
//     insufficient_memory and leave the model unloaded so the box recovers.
//  5. Otherwise BeginLoad() and reload.
func (s *Supervisor) Recover(ctx context.Context, cause string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrSupervisorClosed
	}
	if s.recovering {
		done := s.recoveryDone
		s.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return ErrCancelled
		}
		s.mu.Lock()
		err := s.recovery.Err
		s.mu.Unlock()
		return err
	}
	if s.recoveryUsed {
		s.mu.Unlock()
		s.event(Event{Kind: EventRecoverySpent, Cause: cause})
		return ErrRecoveryExhausted
	}

	s.recoveryUsed = true
	s.recovering = true
	done := make(chan struct{})
	s.recoveryDone = done
	s.recovery = Recovery{Cause: cause, StartedAt: time.Now(), Used: true}
	s.stats.Recoveries++
	// Step 1: mark the epoch dead. Everything still in flight now carries a
	// generation that epochAlive will reject.
	gen := s.cur
	s.cur = nil
	s.mu.Unlock()

	s.event(Event{Kind: EventRecoveryStarted, Cause: cause})
	s.logf("recovering the model after %s", cause)

	err := s.runRecovery(ctx, gen, cause)

	s.mu.Lock()
	s.recovering = false
	s.recovery.FinishedAt = time.Now()
	s.recovery.Err = err
	s.mu.Unlock()
	close(done)

	s.event(Event{Kind: EventRecoveryFinished, Cause: cause, Err: err})
	return err
}

func (s *Supervisor) runRecovery(ctx context.Context, gen *generation, cause string) error {
	// Step 2: in-flight requests fail on their own against a dead or wedged
	// child; their brackets run FinishInference and the drain.
	s.waitInFlight(s.opts.DrainTimeout)

	// A wedged-but-alive child still has to be ended. One that already exited
	// is not signalled: see stopChild.
	if gen != nil {
		s.stopChild(gen, "recovery: "+cause)
	}

	// Step 3.
	s.mu.Lock()
	s.lc.FinishUnload()
	s.mu.Unlock()

	// Step 4.
	status, err := s.opts.MemCheck()
	if err != nil {
		s.event(Event{Kind: EventRecoveryBlocked, Cause: cause, Err: err})
		return fmt.Errorf("recovery preflight: %w", err)
	}
	if !status.CanLoad {
		s.event(Event{Kind: EventRecoveryBlocked, Cause: cause, Detail: status.Message, Err: status.Err()})
		s.logf("NOT reloading the model after %s: %s", cause, status.Message)
		return status.Err()
	}

	// Step 5.
	return s.load(ctx, "recovery: "+cause)
}

// ActiveRecovery reports the current or most recent recovery. The bool is true
// only while one is actually running.
func (s *Supervisor) ActiveRecovery() (Recovery, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recovery, s.recovering
}

// --- Idle unload ----------------------------------------------------------

// idleLoop unloads a model nobody has used, through RequestDeferredUnload so it
// is session-respecting: a scan that is between calls must never lose its model
// to an idle timer.
func (s *Supervisor) idleLoop(ctx context.Context) {
	if s.opts.IdleUnload <= 0 {
		return
	}
	ticker := time.NewTicker(s.opts.IdleCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.mu.Lock()
		idle := time.Since(s.lastUse)
		// Deliberately NOT filtered on SessionActive. Pre-filtering here would
		// be inferring the action from the reason, which is the one thing 35a
		// forbids: the timer's job is to ASK, and the machine's job is to
		// answer PROCEED at Ready and idle, or DEFERRED inside a session. The
		// PendingUnload check only stops the log line repeating every tick
		// once the answer is already recorded.
		eligible := s.lc.State() == contract.Ready && !s.lc.PendingUnload() && !s.recovering
		s.mu.Unlock()
		if !eligible || idle < s.opts.IdleUnload {
			continue
		}
		s.event(Event{Kind: EventIdleUnload, Detail: idle.Round(time.Second).String()})
		s.logf("unloading the model after %s idle", idle.Round(time.Second))
		s.RequestDeferredUnload(contract.UnloadReasonIdleTimeout)
	}
}

// --- Observability --------------------------------------------------------

// Status snapshots everything /api/status reports.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{
		State:          s.lc.State(),
		PendingUnload:  s.lc.PendingUnload(),
		ActiveSessions: s.lc.ActiveSessionCount(),
		LoadPhase:      LoadPhaseIdle,
		Variant:        s.opts.Runtime.Variant,
		RuntimeSource:  s.opts.Runtime.Source,
		FellBackToCPU:  s.opts.Runtime.FellBackToCPU,
		FallbackReason: s.opts.Runtime.FallbackReason,
		Recovering:     s.recovering,
		RecoveryUsed:   s.recoveryUsed,
		LastRecovery:   s.recovery,
		Stats:          s.stats,
	}
	if s.lc.State() == contract.Loading {
		st.LoadPhase = LoadPhaseFirstLoad
		if s.releasedUnderPressure {
			st.LoadPhase = LoadPhaseReloadingAfterMemory
		}
	}
	if s.cur != nil {
		st.Epoch = s.cur.epoch
		st.ChildPID = s.cur.pid
		st.BaseURL = s.cur.baseURL
	}
	return st
}

// Stats returns the counters.
func (s *Supervisor) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// State returns the lifecycle state.
func (s *Supervisor) State() contract.ModelState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lc.State()
}

// SetRuntime swaps the resolved runtime, for the caller that re-resolves after a
// known-answer probe failure recorded a sticky CPU fallback.
func (s *Supervisor) SetRuntime(rt Runtime) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts.Runtime = rt
}

func (s *Supervisor) event(e Event) {
	if s.opts.Observer == nil {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	s.opts.Observer(e)
}

func (s *Supervisor) logf(format string, args ...any) {
	if s.opts.Logf != nil {
		s.opts.Logf(format, args...)
	}
}

// freePort asks the kernel for an unused port and releases it immediately.
//
// There is a window between the close and the child's bind. It is accepted
// rather than engineered away: the alternative is having llama-server pick its
// own port and parsing it out of its log, which couples startup to a log format
// that is not part of any contract. A lost race fails the spawn, which the load
// path reports as a normal startup failure.
func freePort(host string) (int, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, err
	}
	defer func() { _ = listener.Close() }()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener address %T", listener.Addr())
	}
	return addr.Port, nil
}

// randomAPIKey mints a per-generation key.
//
// Loopback plus a random key, not loopback alone: anything else on the box could
// otherwise drive a 4 GB model, and on a shared machine that is both a denial of
// service and a way to read whatever is in the KV cache.
func randomAPIKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate an api key: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
