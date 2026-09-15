package llm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
)

// --- Arg construction and its guards -------------------------------------

// TestBuildArgsConstantSet pins the argv. --jinja is mandatory for this GGUF's
// chat template, and its absence would not error - it would assemble the prompt
// wrongly and degrade recognition quietly.
func TestBuildArgsConstantSet(t *testing.T) {
	opts := withDefaults(Options{ModelPath: "/m/weights.gguf", MMProjPath: "/m/mmproj.gguf"})
	args, err := BuildArgs(opts, 41234, "secret-key")
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--host 127.0.0.1",
		"--port 41234",
		"--api-key secret-key",
		"--jinja",
		"--ctx-size 1024",
		"--batch-size 512",
		"--ubatch-size 512",
		"--parallel 1",
		"--temp 0.2",
		"--top-k 40",
		"--top-p 0.95",
		"--model /m/weights.gguf",
		"--mmproj /m/mmproj.gguf",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "--no-mmproj-offload") {
		t.Error("the vision projector must stay on the accelerator")
	}
}

// TestRuntimeParamGuards is the refusal table. Every one of these shipped, or
// nearly shipped, as a silent recognition failure rather than a crash.
func TestRuntimeParamGuards(t *testing.T) {
	base := func() Options {
		return withDefaults(Options{ModelPath: "/m/w.gguf", MMProjPath: "/m/p.gguf"})
	}
	tests := []struct {
		name    string
		mutate  func(*Options)
		wantMsg string
	}{
		{
			name:    "context other than 1024",
			mutate:  func(o *Options) { o.ContextSize = 2048 },
			wantMsg: "2048 -> 1536 -> 1024",
		},
		{
			name:    "batch below 512",
			mutate:  func(o *Options) { o.BatchSize = 256 },
			wantMsg: "SILENTLY BROKE RECOGNITION",
		},
		{
			name:    "ubatch below 512",
			mutate:  func(o *Options) { o.UBatchSize = 128 },
			wantMsg: "silent-recognition failure",
		},
		{
			name:    "parallel above 1",
			mutate:  func(o *Options) { o.ParallelSlots = 2 },
			wantMsg: "512 tokens each",
		},
		{
			name:    "cpu side mmproj",
			mutate:  func(o *Options) { o.ExtraArgs = []string{"--no-mmproj-offload"} },
			wantMsg: "vision projector on the CPU",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := base()
			tc.mutate(&opts)
			_, err := BuildArgs(opts, 1, "k")
			if !errors.Is(err, ErrUnsafeRuntimeParams) {
				t.Fatalf("err = %v, want ErrUnsafeRuntimeParams", err)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("the refusal must carry the reason; got %v", err)
			}
			// The same configuration is allowed with the explicit override.
			opts.UnsafeRuntimeParams = true
			if _, err := BuildArgs(opts, 1, "k"); err != nil {
				t.Fatalf("--unsafe-runtime-params should permit it: %v", err)
			}
		})
	}
}

// TestParallelGuardShowsTheArithmetic: the message has to teach, because the
// next person to try -np 2 will be optimising throughput in good faith.
func TestParallelGuardShowsTheArithmetic(t *testing.T) {
	opts := withDefaults(Options{ModelPath: "/m/w", MMProjPath: "/m/p", ParallelSlots: 2})
	_, err := BuildArgs(opts, 1, "k")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"divides --ctx-size across slots", "512 tokens each", "256 vision tokens", "558-character prompt", "doubling"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message should mention %q: %v", want, err)
		}
	}
}

// TestNewRefusesAnUnsafeConfiguration: the guard fires at construction, not at
// the first scan, so a bad config cannot reach a user.
func TestNewRefusesAnUnsafeConfiguration(t *testing.T) {
	_, err := New(Options{ModelPath: "/m/w", MMProjPath: "/m/p", ContextSize: 4096})
	if !errors.Is(err, ErrUnsafeRuntimeParams) {
		t.Fatalf("err = %v, want ErrUnsafeRuntimeParams", err)
	}
}

// --- The happy path -------------------------------------------------------

// TestLoadServeAndUnload is the smoke test that everything else builds on.
func TestLoadServeAndUnload(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}
	if h.sup.State() != contract.Ready {
		t.Fatalf("state = %v, want Ready", h.sup.State())
	}

	resp, err := h.sup.Infer(context.Background(), Request{
		Kind: KindIdentifyAllItems, Prompt: "p", ImageJPEG: []byte{1, 2, 3},
	})
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Name != "Whole Milk" {
		t.Fatalf("items = %+v", resp.Items)
	}
	if resp.Epoch == 0 {
		t.Fatalf("the response must carry the generation it came from: %+v", resp)
	}
	if resp.PredictedPerSecond != 9.9 {
		t.Fatalf("the bench numbers were dropped: %+v", resp)
	}
	if resp.Elapsed <= 0 {
		t.Fatalf("elapsed = %v", resp.Elapsed)
	}
	if h.sup.State() != contract.Ready {
		t.Fatalf("state after inference = %v, want Ready", h.sup.State())
	}

	// The child got the guarded argv.
	args := strings.Join(h.childArgs(0), " ")
	if !strings.Contains(args, "--jinja") || !strings.Contains(args, "--parallel 1") {
		t.Fatalf("child argv = %s", args)
	}

	if d := h.sup.RequestUnload(); d != contract.UnloadProceed {
		t.Fatalf("RequestUnload at Ready = %v, want PROCEED", d)
	}
	h.waitState(contract.Unloaded, 5*time.Second)
	if h.sup.Stats().InferenceFailures != 0 {
		t.Fatalf("a clean run counted %d failures", h.sup.Stats().InferenceFailures)
	}
}

// --- 35a: act on the decision, never on the reason ------------------------

// TestNoSignalWhileARequestIsInFlightForAnyReason is the headline guarantee.
//
// Every unload entry point is exercised while a request is in flight, and every
// one of them must DEFER. Mapping a reason straight to SIGTERM kills the child
// mid-encode, which is the build-1 use-after-free wearing a different hat.
func TestNoSignalWhileARequestIsInFlightForAnyReason(t *testing.T) {
	h := newHarness(t, harnessOptions{env: []string{"FAKE_HANG_MS=600"}})
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := h.sup.Infer(context.Background(), Request{Kind: KindIdentifyAllItems, Prompt: "p", ImageJPEG: []byte{1}})
		done <- err
	}()
	h.waitFor("the request to reach the model", 5*time.Second, func() bool {
		return h.sup.State() == contract.Inferring
	})

	decisions := map[string]contract.UnloadDecision{
		"operator unload":  h.sup.RequestUnload(),
		"warning pressure": h.sup.HandleMemoryPressure(contract.PressureWarning),
		"critical pressure": func() contract.UnloadDecision {
			return h.sup.HandleMemoryPressure(contract.PressureCritical)
		}(),
		"config change": h.sup.RequestDeferredUnload(contract.UnloadReasonConfigChange),
		"idle timeout":  h.sup.RequestDeferredUnload(contract.UnloadReasonIdleTimeout),
	}
	for reason, d := range decisions {
		switch reason {
		case "warning pressure":
			if d != contract.UnloadNothingToDo {
				t.Errorf("%s while inferring = %v, want NOTHING_TO_DO", reason, d)
			}
		default:
			if d != contract.UnloadDeferred {
				t.Errorf("%s while inferring = %v, want DEFERRED", reason, d)
			}
		}
	}

	if err := <-done; err != nil {
		t.Fatalf("the in-flight request must survive every one of those: %v", err)
	}

	// The deferred unload fires on the post-inference drain, not before it.
	h.waitState(contract.Unloaded, 5*time.Second)

	intervals := h.requestIntervals()
	if len(intervals) != 1 {
		t.Fatalf("expected exactly one request, got %d", len(intervals))
	}
	for _, sig := range h.eventsOfKind(EventSignalSent) {
		if !sig.At.Before(intervals[0].start) && !sig.At.After(intervals[0].end) {
			t.Fatalf("a %s was sent at %s, inside the request that ran %s..%s",
				sig.Detail, sig.At, intervals[0].start, intervals[0].end)
		}
	}
	if h.countEvents(EventSignalSent) == 0 {
		t.Fatal("the deferred unload never actually fired")
	}
}

// TestOperatorRestartAtReadyMidSessionProceeds: an explicit request from the
// operator is trusted even inside a session. Only PRESSURE respects sessions.
func TestOperatorRestartAtReadyMidSessionProceeds(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.sup.BeginSession()
	defer h.sup.EndSession()

	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}
	if d := h.sup.RequestRestart(); d != contract.UnloadProceed {
		t.Fatalf("RequestRestart at Ready in session = %v, want PROCEED", d)
	}
	h.waitFor("the child to be restarted", 10*time.Second, func() bool {
		return h.countEvents(EventChildSpawned) == 2 && h.sup.State() == contract.Ready
	})
}

// TestOperatorRestartWhileInferringDefersAndFiresOnDrain is the same request
// one moment later in the scan, and it must behave completely differently.
func TestOperatorRestartWhileInferringDefersAndFiresOnDrain(t *testing.T) {
	h := newHarness(t, harnessOptions{env: []string{"FAKE_HANG_MS=500"}})
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := h.sup.Infer(context.Background(), Request{Kind: KindIdentifyAllItems, Prompt: "p", ImageJPEG: []byte{1}})
		done <- err
	}()
	h.waitFor("the request to reach the model", 5*time.Second, func() bool {
		return h.sup.State() == contract.Inferring
	})

	if d := h.sup.RequestRestart(); d != contract.UnloadDeferred {
		t.Fatalf("RequestRestart while inferring = %v, want DEFERRED", d)
	}
	if n := h.countEvents(EventSignalSent); n != 0 {
		t.Fatalf("%d signals were sent while a request was in flight", n)
	}
	if err := <-done; err != nil {
		t.Fatalf("the in-flight request failed: %v", err)
	}

	h.waitFor("the deferred restart to complete", 10*time.Second, func() bool {
		return h.countEvents(EventChildSpawned) == 2 && h.sup.State() == contract.Ready
	})
	if h.countEvents(EventSignalSent) == 0 {
		t.Fatal("the restart never signalled the old child")
	}
}

// TestWarningAtReadyIdleUnloadsAndInSessionDoesNot is the pair the contract
// added late and called load-bearing. A port that treats WARNING as
// always-ignorable keeps several gigabytes resident through exactly the pressure
// it exists to relieve; one that treats it as always-actionable guts a scan
// between crops.
func TestWarningAtReadyIdleUnloadsAndInSessionDoesNot(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		h := newHarness(t, harnessOptions{})
		if err := h.sup.EnsureLoaded(context.Background()); err != nil {
			t.Fatalf("EnsureLoaded: %v", err)
		}
		if d := h.sup.HandleMemoryPressure(contract.PressureWarning); d != contract.UnloadProceed {
			t.Fatalf("warning at Ready idle = %v, want PROCEED", d)
		}
		h.waitState(contract.Unloaded, 5*time.Second)
	})

	t.Run("mid session", func(t *testing.T) {
		h := newHarness(t, harnessOptions{})
		h.sup.BeginSession()
		if err := h.sup.EnsureLoaded(context.Background()); err != nil {
			t.Fatalf("EnsureLoaded: %v", err)
		}
		if d := h.sup.HandleMemoryPressure(contract.PressureWarning); d != contract.UnloadNothingToDo {
			t.Fatalf("warning at Ready in session = %v, want NOTHING_TO_DO", d)
		}
		if h.sup.State() != contract.Ready {
			t.Fatalf("state = %v, want the model still resident", h.sup.State())
		}
		if h.countEvents(EventSignalSent) != 0 {
			t.Fatal("a warning mid-session must not signal anything")
		}
		h.sup.EndSession()
	})
}

// TestCriticalMidSessionDefersToSessionEnd: the model is freed the moment the
// whole session drains, not between crops.
func TestCriticalMidSessionDefersToSessionEnd(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.sup.BeginSession()
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}
	if d := h.sup.HandleMemoryPressure(contract.PressureCritical); d != contract.UnloadDeferred {
		t.Fatalf("critical at Ready in session = %v, want DEFERRED", d)
	}
	// Still resident: the rest of the scan keeps its model.
	if h.sup.State() != contract.Ready {
		t.Fatalf("state = %v, want Ready", h.sup.State())
	}
	if _, err := h.sup.Infer(context.Background(), Request{Kind: KindIdentifyAllItems, Prompt: "p", ImageJPEG: []byte{1}}); err != nil {
		t.Fatalf("the remaining crops must still get real inference: %v", err)
	}
	if h.sup.State() != contract.Ready {
		t.Fatalf("a mid-session drain freed the model between crops (state=%v)", h.sup.State())
	}

	if d := h.sup.EndSession(); d != contract.UnloadProceed {
		t.Fatalf("EndSession = %v, want PROCEED from the deferred unload", d)
	}
	h.waitState(contract.Unloaded, 5*time.Second)
}

// TestRequestDeferredUnloadMatchesCriticalPressureInEveryState pins the Linux
// addition to being a rename and nothing more. Six states, driven on the pure
// machine so every one is reachable, plus the supervisor's own method on the
// states a live supervisor can be in.
func TestRequestDeferredUnloadMatchesCriticalPressureInEveryState(t *testing.T) {
	states := []struct {
		name  string
		build func() contract.ModelLifecycle
	}{
		{"unloaded", func() contract.ModelLifecycle { return contract.ModelLifecycle{} }},
		{"loading", func() contract.ModelLifecycle {
			var m contract.ModelLifecycle
			m.BeginLoad()
			return m
		}},
		{"ready no session", func() contract.ModelLifecycle {
			var m contract.ModelLifecycle
			m.BeginLoad()
			m.FinishLoad(true)
			return m
		}},
		{"ready in session", func() contract.ModelLifecycle {
			var m contract.ModelLifecycle
			m.BeginSession()
			m.BeginLoad()
			m.FinishLoad(true)
			return m
		}},
		{"inferring", func() contract.ModelLifecycle {
			var m contract.ModelLifecycle
			m.BeginLoad()
			m.FinishLoad(true)
			m.BeginInference()
			return m
		}},
		{"unloading", func() contract.ModelLifecycle {
			var m contract.ModelLifecycle
			m.BeginLoad()
			m.FinishLoad(true)
			m.RequestUnload()
			return m
		}},
	}
	for _, tc := range states {
		t.Run(tc.name, func(t *testing.T) {
			viaPressure := tc.build()
			viaReason := tc.build()
			want := viaPressure.HandleMemoryPressure(contract.PressureCritical)
			got := viaReason.RequestDeferredUnload(contract.UnloadReasonHealthFailure)
			if got != want {
				t.Fatalf("RequestDeferredUnload = %v, HandleMemoryPressure(CRITICAL) = %v", got, want)
			}
			if viaReason.State() != viaPressure.State() || viaReason.PendingUnload() != viaPressure.PendingUnload() {
				t.Fatalf("state diverged: %v/%v vs %v/%v",
					viaReason.State(), viaReason.PendingUnload(), viaPressure.State(), viaPressure.PendingUnload())
			}
		})
	}

	// And through the supervisor, on the two states a live one reaches without
	// a child process.
	h := newHarness(t, harnessOptions{})
	if d := h.sup.RequestDeferredUnload(contract.UnloadReasonConfigChange); d != contract.UnloadNothingToDo {
		t.Fatalf("unloaded supervisor = %v, want NOTHING_TO_DO", d)
	}
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}
	if d := h.sup.RequestDeferredUnload(contract.UnloadReasonConfigChange); d != contract.UnloadProceed {
		t.Fatalf("ready idle supervisor = %v, want PROCEED", d)
	}
	h.waitState(contract.Unloaded, 5*time.Second)
}

// --- 35b: the gate, epochs and one-shot recovery --------------------------

// TestHealthFailureDuringSessionRecoversRatherThanDefers. A wedged-but-alive
// child is the case an earlier revision got wrong: the process was still there,
// so nothing recovered it, and every remaining call in the scan failed against
// it in turn.
func TestHealthFailureDuringSessionRecoversRatherThanDefers(t *testing.T) {
	h := newHarness(t, harnessOptions{env: []string{
		"FAKE_MODE=unhealthy_after",
		"FAKE_UNHEALTHY_AFTER_MS=300",
	}})
	h.sup.BeginSession()
	defer h.sup.EndSession()

	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}
	h.waitFor("the health failures to trigger a recovery", 10*time.Second, func() bool {
		return h.countEvents(EventRecoveryStarted) >= 1
	})
	h.waitFor("the recovery to reload the model", 15*time.Second, func() bool {
		return h.countEvents(EventChildSpawned) >= 2
	})

	// A recovery, not a deferral: the child was effectively gone, so no unload
	// method was called and no pendingUnload was recorded for it.
	for _, e := range h.eventsOfKind(EventUnloadDeferred) {
		t.Fatalf("a health failure produced a deferral (%s) instead of a recovery", e.Cause)
	}
	if h.sup.Stats().Recoveries != 1 {
		t.Fatalf("recoveries = %d, want exactly 1", h.sup.Stats().Recoveries)
	}
}

// TestRecoveryReRunsThePreflightAndRefusesToReload is NB-4, and it is the whole
// reason the preflight is exported.
//
// The usual trigger for this path is the OOM killer reaping llama-server, so a
// blind reload fires under exactly the pressure that caused the kill.
func TestRecoveryReRunsThePreflightAndRefusesToReload(t *testing.T) {
	h := newHarness(t, harnessOptions{env: []string{"FAKE_MODE=exit_on_request"}})
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}

	// The box is now under exactly the pressure that killed the child.
	h.setMemory(starvedMemory())

	_, err := h.sup.Infer(context.Background(), Request{Kind: KindIdentifyAllItems, Prompt: "p", ImageJPEG: []byte{1}})
	if err == nil {
		t.Fatal("the request should have failed against a child that exited mid-request")
	}
	h.waitFor("the recovery to be blocked", 10*time.Second, func() bool {
		return h.countEvents(EventRecoveryBlocked) >= 1
	})

	blocked := h.eventsOfKind(EventRecoveryBlocked)[0]
	if !errors.Is(blocked.Err, ErrInsufficientMemory) {
		t.Fatalf("blocked with %v, want ErrInsufficientMemory", blocked.Err)
	}
	if !strings.Contains(blocked.Detail, "Not enough free memory") {
		t.Fatalf("the operator needs the real numbers: %q", blocked.Detail)
	}
	if n := h.countEvents(EventChildSpawned); n != 1 {
		t.Fatalf("%d children were spawned; the refused reload must not start one", n)
	}
	if state := h.sup.State(); state != contract.Unloaded {
		t.Fatalf("state = %v, want Unloaded so the box can recover", state)
	}

	// Calling it directly gives the typed error the scan engine aborts on.
	recErr := h.sup.Recover(context.Background(), "test")
	if !errors.Is(recErr, ErrInsufficientMemory) && !errors.Is(recErr, ErrModelNotReady) {
		t.Fatalf("Recover = %v, want a typed insufficient-memory or spent-recovery error", recErr)
	}
	if CountsAsInferenceFailure(ErrInsufficientMemory) {
		t.Fatal("insufficient memory is an abort reason, not an inference failure")
	}
}

// TestExactlyOneRecoveryPerSession. A cold reload costs minutes on a Pi;
// burning the user's time twice in silence is worse than an honest error.
func TestExactlyOneRecoveryPerSession(t *testing.T) {
	h := newHarness(t, harnessOptions{env: []string{"FAKE_MODE=exit_on_request"}})
	h.sup.BeginSession()
	defer h.sup.EndSession()

	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}

	// First failure: the child exits mid-request, the recovery reloads.
	if _, err := h.sup.Infer(context.Background(), Request{Kind: KindIdentifyAllItems, Prompt: "p", ImageJPEG: []byte{1}}); err == nil {
		t.Fatal("expected the first call to fail")
	}
	h.waitFor("the one-shot recovery to reload", 15*time.Second, func() bool {
		return h.countEvents(EventChildSpawned) == 2 && h.sup.State() == contract.Ready
	})

	// Second failure in the same session: no second recovery.
	if _, err := h.sup.Infer(context.Background(), Request{Kind: KindIdentifyAllItems, Prompt: "p", ImageJPEG: []byte{1}}); err == nil {
		t.Fatal("expected the second call to fail too")
	}
	h.waitFor("the second recovery to be refused", 10*time.Second, func() bool {
		return h.countEvents(EventRecoverySpent) >= 1
	})
	if n := h.countEvents(EventChildSpawned); n != 2 {
		t.Fatalf("%d spawns, want 2: the second recovery must not reload", n)
	}

	err := h.sup.Recover(context.Background(), "test")
	if !errors.Is(err, ErrRecoveryExhausted) {
		t.Fatalf("Recover = %v, want ErrRecoveryExhausted", err)
	}
	if !errors.Is(err, ErrModelNotReady) {
		t.Fatal("ErrRecoveryExhausted must wrap ErrModelNotReady so the scan engine aborts with model_not_ready")
	}
	if CountsAsInferenceFailure(err) {
		t.Fatal("a spent recovery never reached the model and must not be counted")
	}

	// A new session gets a fresh budget: the cap is per scan, not per process.
	h.sup.EndSession()
	h.sup.BeginSession()
	if _, used := h.sup.ActiveRecovery(); used {
		t.Fatal("no recovery should be running")
	}
	if h.sup.Status().RecoveryUsed {
		t.Fatal("a new session must reset the one-shot recovery budget")
	}
}

// TestChildThatExitedIsNeverSignalled: "child already exited" is its own row in
// the reason table, with no unload call and no signal.
func TestChildThatExitedIsNeverSignalled(t *testing.T) {
	h := newHarness(t, harnessOptions{env: []string{"FAKE_MODE=exit_on_request", "FAKE_EXIT_CODE=137"}})
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}
	if _, err := h.sup.Infer(context.Background(), Request{Kind: KindIdentifyAllItems, Prompt: "p", ImageJPEG: []byte{1}}); err == nil {
		t.Fatal("expected a failure against a child that exited mid-request")
	}
	h.waitFor("the exit to be noticed", 10*time.Second, func() bool {
		return h.countEvents(EventChildExited) >= 1
	})
	h.waitFor("the recovery to finish", 15*time.Second, func() bool {
		return h.countEvents(EventRecoveryFinished) >= 1
	})
	for _, sig := range h.eventsOfKind(EventSignalSent) {
		if sig.Epoch == 1 {
			t.Fatalf("a %s was sent to a child that had already exited", sig.Detail)
		}
	}
}

// TestEpochCrossingResponseIsDiscardedAndCounted. An answer that lands after its
// generation was replaced describes a model that no longer exists. Reporting it
// as an empty result would be "Nothing spotted" with extra steps.
func TestEpochCrossingResponseIsDiscardedAndCounted(t *testing.T) {
	var h *harness
	h = newHarness(t, harnessOptions{mutate: func(o *Options) {
		o.hookAfterCall = func() {
			// The generation is retired while the answer is on the wire. This
			// is what a recovery or an operator restart does.
			h.sup.mu.Lock()
			h.sup.cur = nil
			h.sup.mu.Unlock()
		}
	}})
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}

	_, err := h.sup.Infer(context.Background(), Request{Kind: KindIdentifyAllItems, Prompt: "p", ImageJPEG: []byte{1}})
	if !errors.Is(err, ErrEpochChanged) {
		t.Fatalf("err = %v, want ErrEpochChanged", err)
	}
	if !CountsAsInferenceFailure(err) {
		t.Fatal("an epoch-crossing answer is a failure, never an empty result")
	}
	stats := h.sup.Stats()
	if stats.InferenceFailures != 1 || stats.EpochDiscards != 1 {
		t.Fatalf("stats = %+v, want one counted failure and one discard", stats)
	}
	if h.countEvents(EventEpochDiscarded) != 1 {
		t.Fatal("the discard should be observable")
	}
}

// --- Concurrency (plan §0.1) ---------------------------------------------

// TestTwoOverlappingSessionsNeverOverlapAtTheModel is the LAN problem iOS never
// had: two phones, two tabs, or a recipe generation overlapping a scan. Sessions
// nest by contract, so activeSessionCount reaching 2 is normal, and the gate is
// the only thing that keeps the single-state machine honest.
//
// The fake records start and end timestamps per request; this asserts ZERO
// overlap between them rather than merely trusting the gate to look busy.
func TestTwoOverlappingSessionsNeverOverlapAtTheModel(t *testing.T) {
	h := newHarness(t, harnessOptions{env: []string{"FAKE_HANG_MS=40"}})
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}

	h.sup.BeginSession() // a scan
	h.sup.BeginSession() // a recipe generation, nested
	if got := h.sup.Status().ActiveSessions; got != 2 {
		t.Fatalf("activeSessionCount = %d, want 2", got)
	}

	const perSession = 4
	var wg sync.WaitGroup
	errs := make(chan error, 2*perSession)
	for session := range 2 {
		for range perSession {
			wg.Add(1)
			go func(kind RequestKind) {
				defer wg.Done()
				_, err := h.sup.Infer(context.Background(), Request{Kind: kind, Prompt: "p", ImageJPEG: []byte{1}})
				errs <- err
				// Session 0 is a scan (image calls); session 1 is a recipe
				// generation (one text-only call), which is exactly the
				// overlap the contract permits and iOS never had to handle.
			}(map[int]RequestKind{0: KindIdentifyAllItems, 1: KindRecipes}[session])
		}
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err == nil {
			continue
		}
		if errors.Is(err, ErrModelNotReady) {
			t.Fatalf("BeginInference was observed false under normal concurrent load: %v", err)
		}
		t.Fatalf("concurrent inference failed: %v", err)
	}

	h.sup.EndSession()
	h.sup.EndSession()

	intervals := h.requestIntervals()
	if len(intervals) != 2*perSession {
		t.Fatalf("the model served %d requests, want %d", len(intervals), 2*perSession)
	}
	for i := 1; i < len(intervals); i++ {
		prev, cur := intervals[i-1], intervals[i]
		if cur.start.Before(prev.end) {
			t.Fatalf("requests %s and %s overlapped at the model: %s..%s and %s..%s",
				prev.id, cur.id, prev.start, prev.end, cur.start, cur.end)
		}
	}
	stats := h.sup.Stats()
	if stats.InferencesStarted != uint64(2*perSession) {
		t.Fatalf("started = %d, want %d", stats.InferencesStarted, 2*perSession)
	}
	if stats.InferenceFailures != 0 {
		t.Fatalf("failures = %d under normal concurrent load, want 0", stats.InferenceFailures)
	}
}

// TestChildKilledBetweenGateAndBracketIsNotCounted covers plan §0.1's definition
// of BeginInference() == false: release the gate, return ErrModelNotReady, never
// count it, and never silently skip it either.
func TestChildKilledBetweenGateAndBracketIsNotCounted(t *testing.T) {
	var h *harness
	var once sync.Once
	h = newHarness(t, harnessOptions{mutate: func(o *Options) {
		o.hookAfterGate = func() {
			once.Do(func() {
				// The child dies in the window between gate acquisition and the
				// bracket opening. This is the only remaining cause of a false
				// BeginInference once the gate is held.
				h.sup.mu.Lock()
				h.sup.cur = nil
				h.sup.mu.Unlock()
			})
		}
	}})
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}

	_, err := h.sup.Infer(context.Background(), Request{Kind: KindIdentifyAllItems, Prompt: "p", ImageJPEG: []byte{1}})
	if !errors.Is(err, ErrModelNotReady) {
		t.Fatalf("err = %v, want ErrModelNotReady", err)
	}
	if got := h.sup.Stats().InferenceFailures; got != 0 {
		t.Fatalf("failures = %d; a call that never reached the model must not be counted", got)
	}
	// The gate is free: a second caller gets in immediately.
	select {
	case h.sup.inferSlot <- struct{}{}:
		<-h.sup.inferSlot
	default:
		t.Fatal("the gate was not released")
	}
}

// TestQueuedCallerCancelledReturnsErrCancelledUncounted: cancellation has its
// own terminal phase and must never push a scan towards Failed.
func TestQueuedCallerCancelledReturnsErrCancelledUncounted(t *testing.T) {
	h := newHarness(t, harnessOptions{env: []string{"FAKE_HANG_MS=600"}})
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}

	holder := make(chan struct{})
	go func() {
		defer close(holder)
		_, _ = h.sup.Infer(context.Background(), Request{Kind: KindIdentifyAllItems, Prompt: "p", ImageJPEG: []byte{1}})
	}()
	h.waitFor("the gate to be held", 5*time.Second, func() bool { return h.sup.State() == contract.Inferring })

	ctx, cancel := context.WithCancel(context.Background())
	queued := make(chan error, 1)
	go func() {
		_, err := h.sup.Infer(ctx, Request{Kind: KindIdentifyAllItems, Prompt: "p", ImageJPEG: []byte{1}})
		queued <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	err := <-queued
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("err = %v, want ErrCancelled", err)
	}
	if CountsAsInferenceFailure(err) {
		t.Fatal("a cancelled caller must not be counted")
	}
	<-holder
	if got := h.sup.Stats().InferenceFailures; got != 0 {
		t.Fatalf("failures = %d, want 0", got)
	}
}

// TestQueueTimeoutIsTypedAndCounted: it was admitted, attempted, and gave up
// with nothing to show, so unlike every other non-answer here it counts.
func TestQueueTimeoutIsTypedAndCounted(t *testing.T) {
	h := newHarness(t, harnessOptions{
		env:    []string{"FAKE_HANG_MS=700"},
		mutate: func(o *Options) { o.InferQueueTimeout = 60 * time.Millisecond },
	})
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}

	holder := make(chan struct{})
	go func() {
		defer close(holder)
		_, _ = h.sup.Infer(context.Background(), Request{Kind: KindIdentifyAllItems, Prompt: "p", ImageJPEG: []byte{1}})
	}()
	h.waitFor("the gate to be held", 5*time.Second, func() bool { return h.sup.State() == contract.Inferring })

	_, err := h.sup.Infer(context.Background(), Request{Kind: KindIdentifyAllItems, Prompt: "p", ImageJPEG: []byte{1}})
	if !errors.Is(err, ErrInferenceQueueTimeout) {
		t.Fatalf("err = %v, want ErrInferenceQueueTimeout", err)
	}
	if !CountsAsInferenceFailure(err) {
		t.Fatal("a queue timeout counts")
	}
	<-holder
	stats := h.sup.Stats()
	if stats.InferenceFailures != 1 || stats.QueueTimeouts != 1 {
		t.Fatalf("stats = %+v, want one counted queue timeout", stats)
	}
}

// TestGateIsReleasedOnPanic. A leaked slot would wedge every later call in the
// process behind a holder that no longer exists.
func TestGateIsReleasedOnPanic(t *testing.T) {
	var panicOnce sync.Once
	h := newHarness(t, harnessOptions{mutate: func(o *Options) {
		o.hookBeforeCall = func() {
			panicOnce.Do(func() { panic("injected panic inside the inference bracket") })
		}
	}})
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the injected panic did not propagate")
			}
		}()
		_, _ = h.sup.Infer(context.Background(), Request{Kind: KindIdentifyAllItems, Prompt: "p", ImageJPEG: []byte{1}})
	}()

	// The bracket closed on the way out, so the machine is back at Ready.
	if state := h.sup.State(); state != contract.Ready {
		t.Fatalf("state after a panic = %v, want Ready", state)
	}
	// And the gate is free, which is only observable by using it.
	if _, err := h.sup.Infer(context.Background(), Request{Kind: KindIdentifyAllItems, Prompt: "p", ImageJPEG: []byte{1}}); err != nil {
		t.Fatalf("the next call after a panic failed: %v", err)
	}
}

// --- Startup, teardown and idle -------------------------------------------

// TestStartupBudgetExpires: a child that never serves is a failed load, the
// child is cleaned up, and the machine is back at Unloaded rather than stuck.
func TestStartupBudgetExpires(t *testing.T) {
	h := newHarness(t, harnessOptions{
		env:    []string{"FAKE_MODE=never_healthy"},
		mutate: func(o *Options) { o.StartupBudget = 400 * time.Millisecond },
	})
	err := h.sup.EnsureLoaded(context.Background())
	if !errors.Is(err, ErrStartupTimeout) {
		t.Fatalf("err = %v, want ErrStartupTimeout", err)
	}
	if state := h.sup.State(); state != contract.Unloaded {
		t.Fatalf("state = %v, want Unloaded", state)
	}
	if h.countEvents(EventSignalSent) == 0 {
		t.Fatal("the child that never became healthy was left running")
	}
}

// TestSlowStartupStillSucceeds is the Pi case: a cold 4 GB load from an SD card
// takes minutes, which is why the budget is 600 s and not the eval harness's
// 120 s.
func TestSlowStartupStillSucceeds(t *testing.T) {
	h := newHarness(t, harnessOptions{env: []string{"FAKE_HEALTHY_AFTER_MS=400"}})
	start := time.Now()
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Fatal("the load returned before the child was actually serving")
	}
	if h.sup.State() != contract.Ready {
		t.Fatalf("state = %v", h.sup.State())
	}
}

// TestSIGKILLEscalation: llama-server frees several gigabytes on the way out, so
// SIGTERM gets a grace period, and a child that ignores it still dies.
func TestSIGKILLEscalation(t *testing.T) {
	h := newHarness(t, harnessOptions{env: []string{"FAKE_IGNORE_SIGTERM=1"}})
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}
	if d := h.sup.RequestUnload(); d != contract.UnloadProceed {
		t.Fatalf("RequestUnload = %v", d)
	}
	h.waitState(contract.Unloaded, 10*time.Second)

	var sawTerm, sawKill bool
	for _, e := range h.eventsOfKind(EventSignalSent) {
		switch e.Detail {
		case "SIGTERM":
			sawTerm = true
		case "SIGKILL":
			sawKill = true
		}
	}
	if !sawTerm || !sawKill {
		t.Fatalf("signals: sigterm=%v sigkill=%v, want the escalation", sawTerm, sawKill)
	}
}

// TestIdleUnloadIsSessionRespecting: an idle timer must never take a scan's
// model away between crops, which is why it is routed through
// RequestDeferredUnload rather than RequestUnload.
func TestIdleUnloadIsSessionRespecting(t *testing.T) {
	h := newHarness(t, harnessOptions{mutate: func(o *Options) {
		o.IdleUnload = 60 * time.Millisecond
		o.IdleCheckInterval = 20 * time.Millisecond
	}})
	h.sup.BeginSession()
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}
	h.waitFor("the idle timer to fire", 5*time.Second, func() bool {
		return h.countEvents(EventIdleUnload) >= 1
	})
	if state := h.sup.State(); state != contract.Ready {
		t.Fatalf("the idle timer unloaded a model mid-session (state=%v)", state)
	}
	if !h.sup.Status().PendingUnload {
		t.Fatal("the idle unload should have been deferred, not dropped")
	}
	h.sup.EndSession()
	h.waitState(contract.Unloaded, 5*time.Second)
}

// TestIdleUnloadPolicy: residency costs nothing worth reclaiming on a big box,
// and a cold reload costs minutes on a small one.
func TestIdleUnloadPolicy(t *testing.T) {
	if got := IdleUnloadFor(16 * GiB); got != 0 {
		t.Errorf("16 GiB = %v, want the idle unload off", got)
	}
	if got := IdleUnloadFor(32 * GiB); got != 0 {
		t.Errorf("32 GiB = %v, want the idle unload off", got)
	}
	if got := IdleUnloadFor(8 * GiB); got != 15*time.Minute {
		t.Errorf("8 GiB = %v, want 15m", got)
	}
	if got := IdleUnloadFor(8261548 * KiB); got != 15*time.Minute {
		t.Errorf("a Pi 5 8 GB = %v, want 15m", got)
	}
}

// TestKnownAnswerProbe is the half of "probe before trusting" that --version
// cannot do: a build that starts, answers /health, and then cannot produce a
// token.
func TestKnownAnswerProbe(t *testing.T) {
	t.Run("passes", func(t *testing.T) {
		h := newHarness(t, harnessOptions{mutate: func(o *Options) { o.KnownAnswerProbe = true }})
		if err := h.sup.EnsureLoaded(context.Background()); err != nil {
			t.Fatalf("EnsureLoaded: %v", err)
		}
		if h.sup.State() != contract.Ready {
			t.Fatalf("state = %v", h.sup.State())
		}
	})

	t.Run("no tokens fails the load and records the fallback", func(t *testing.T) {
		var gotVariant, gotReason string
		h := newHarness(t, harnessOptions{
			env: []string{"FAKE_PROBE_EMPTY=1"},
			mutate: func(o *Options) {
				o.KnownAnswerProbe = true
				o.OnProbeFailure = func(variant, reason string) { gotVariant, gotReason = variant, reason }
			},
		})
		err := h.sup.EnsureLoaded(context.Background())
		if !errors.Is(err, ErrRuntimeProbeFailed) {
			t.Fatalf("err = %v, want ErrRuntimeProbeFailed", err)
		}
		if gotVariant != VariantUbuntuARM64 || gotReason == "" {
			t.Fatalf("the sticky fallback was not recorded: %q %q", gotVariant, gotReason)
		}
		if h.sup.State() != contract.Unloaded {
			t.Fatalf("state = %v, want Unloaded", h.sup.State())
		}
	})
}

// TestMemoryPreflightGatesTheFirstLoadToo, not only the reload. Refusing is not
// failing: the service keeps running and the status message carries the numbers.
func TestMemoryPreflightGatesTheFirstLoadToo(t *testing.T) {
	h := newHarness(t, harnessOptions{memStatus: Classify(4*GiB, 3*GiB, false)})
	err := h.sup.EnsureLoaded(context.Background())
	if !errors.Is(err, ErrInsufficientMemory) {
		t.Fatalf("err = %v, want ErrInsufficientMemory", err)
	}
	if h.countEvents(EventChildSpawned) != 0 {
		t.Fatal("a refused load must not spawn anything")
	}
	if h.sup.State() != contract.Unloaded {
		t.Fatalf("state = %v, want Unloaded", h.sup.State())
	}
	if !strings.Contains(err.Error(), "4.0 GiB total") {
		t.Fatalf("the error must carry the real numbers: %v", err)
	}
}

// TestDuplicateLoadsCoalesce: two callers, one multi-GB allocation.
func TestDuplicateLoadsCoalesce(t *testing.T) {
	h := newHarness(t, harnessOptions{env: []string{"FAKE_HEALTHY_AFTER_MS=300"}})
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = h.sup.EnsureLoaded(context.Background())
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("loader %d: %v", i, err)
		}
	}
	if n := h.countEvents(EventChildSpawned); n != 1 {
		t.Fatalf("%d children spawned for four concurrent loads, want 1", n)
	}
}

// TestStatusReportsWhatTheAPINeeds keeps /api/status honest about the runtime.
func TestStatusReportsWhatTheAPINeeds(t *testing.T) {
	h := newHarness(t, harnessOptions{mutate: func(o *Options) {
		o.Runtime.FellBackToCPU = true
		o.Runtime.FallbackReason = "libcuda.so is missing"
	}})
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}
	st := h.sup.Status()
	if st.State != contract.Ready || st.ChildPID == 0 || st.BaseURL == "" {
		t.Fatalf("status = %+v", st)
	}
	if !st.FellBackToCPU || st.FallbackReason == "" {
		t.Fatal("a CPU fallback has to reach /api/status")
	}
	if st.Variant != VariantUbuntuARM64 || st.RuntimeSource != SourceCache {
		t.Fatalf("runtime identity missing from status: %+v", st)
	}
}

// TestCountsAsInferenceFailureTable is the counting contract on its own, since
// the scan engine is written against it and gets it wrong in both directions if
// it is not pinned.
func TestCountsAsInferenceFailureTable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"success", nil, false},
		{"model not ready", ErrModelNotReady, false},
		{"recovery exhausted", ErrRecoveryExhausted, false},
		{"cancelled", ErrCancelled, false},
		{"context cancelled", context.Canceled, false},
		{"insufficient memory", ErrInsufficientMemory, false},
		{"queue timeout", ErrInferenceQueueTimeout, true},
		{"epoch changed", ErrEpochChanged, true},
		{"bad response", ErrBadResponse, true},
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"transport failure", errors.New("connection refused"), true},
		{"wrapped model not ready", errors.Join(errors.New("context"), ErrModelNotReady), false},
	}
	for _, tc := range tests {
		if got := CountsAsInferenceFailure(tc.err); got != tc.want {
			t.Errorf("%s: CountsAsInferenceFailure(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

// TestParseFailureAsymmetryThroughTheSupervisor carries the contract's
// per-call-site semantics all the way to the counter the scan engine reads.
//
// The same unreadable reply is a counted failure for one call site and a
// perfectly ordinary empty answer for the other. Getting this backwards
// manufactures either a spurious Failed or a "Nothing spotted" lie, which are
// the two shipped field bugs this whole subsystem exists to prevent.
func TestParseFailureAsymmetryThroughTheSupervisor(t *testing.T) {
	h := newHarness(t, harnessOptions{env: []string{"FAKE_REPLY=I think that is probably some milk?"}})
	if err := h.sup.EnsureLoaded(context.Background()); err != nil {
		t.Fatalf("EnsureLoaded: %v", err)
	}

	// identifyItem throws, and a thrown inference counts.
	_, err := h.sup.Infer(context.Background(), Request{Kind: KindIdentifyItem, Prompt: "p", ImageJPEG: []byte{1}})
	if !errors.Is(err, ErrBadResponse) {
		t.Fatalf("err = %v, want ErrBadResponse", err)
	}
	if got := h.sup.Stats().InferenceFailures; got != 1 {
		t.Fatalf("failures = %d, want 1: a thrown inference counts", got)
	}

	// identifyAllItems returns an empty list with no error at all, and nothing
	// is counted: it answered, unreadably.
	resp, err := h.sup.Infer(context.Background(), Request{Kind: KindIdentifyAllItems, Prompt: "p", ImageJPEG: []byte{1}})
	if err != nil {
		t.Fatalf("IdentifyAllItems must not error on a parse failure: %v", err)
	}
	if resp.Items == nil || len(resp.Items) != 0 {
		t.Fatalf("items = %#v, want a non-nil empty slice", resp.Items)
	}
	if got := h.sup.Stats().InferenceFailures; got != 1 {
		t.Fatalf("failures = %d, want still 1: an unreadable answer is not an inference failure", got)
	}
}
