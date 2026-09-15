package llm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
)

// fakeServerSource is a stand-in for llama-server.
//
// It is a REAL PROCESS, built and spawned by the test, rather than an in-process
// stub. Everything this task is about - SIGTERM versus SIGKILL, a child that
// exits mid-request, a wedged-but-alive child, the process group, the exit
// watcher - only exists at the process boundary, and a stub would test the
// supervisor against a model of a child rather than a child.
//
// It records request start and end timestamps to a log file, which is how the
// concurrency test proves ZERO OVERLAP between two sessions rather than merely
// asserting that the gate looked busy.
//
// Behaviour comes from the environment so one binary covers every scenario:
//
//	FAKE_LOG               file to append events to
//	FAKE_MODE              healthy | never_healthy | unhealthy_after | hang | exit_on_request
//	FAKE_HEALTHY_AFTER_MS  delay before /health starts answering 200
//	FAKE_UNHEALTHY_AFTER_MS delay after which /health starts answering 503
//	FAKE_HANG_MS           how long a chat request takes
//	FAKE_EXIT_CODE         exit code for exit_on_request (137 = SIGKILL by the OOM killer)
//	FAKE_REPLY             the completion content to return
//	FAKE_IGNORE_SIGTERM    ignore SIGTERM so the SIGKILL escalation can be seen
const fakeServerSource = `
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	logMu   sync.Mutex
	logFile *os.File
	nextID  atomic.Int64
	started = time.Now()
)

func logEvent(name, detail string) {
	if logFile == nil {
		return
	}
	logMu.Lock()
	defer logMu.Unlock()
	fmt.Fprintf(logFile, "%s %d %s\n", name, time.Now().UnixNano(), detail)
	_ = logFile.Sync()
}

func envDuration(key string) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	ms, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func main() {
	args := os.Args[1:]
	for _, a := range args {
		if a == "--version" {
			fmt.Println("fake llama-server version b10970")
			return
		}
	}

	if path := os.Getenv("FAKE_LOG"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			logFile = f
		}
	}
	logEvent("ARGS", strings.Join(args, "\x1f"))
	logEvent("PID", strconv.Itoa(os.Getpid()))

	if os.Getenv("FAKE_IGNORE_SIGTERM") != "" {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM)
		go func() {
			for range c {
				logEvent("SIGTERM_IGNORED", "")
			}
		}()
	}

	host := "127.0.0.1"
	port := ""
	for i, a := range args {
		if a == "--host" && i+1 < len(args) {
			host = args[i+1]
		}
		if a == "--port" && i+1 < len(args) {
			port = args[i+1]
		}
	}

	mode := os.Getenv("FAKE_MODE")
	if mode == "" {
		mode = "healthy"
	}
	healthyAfter := envDuration("FAKE_HEALTHY_AFTER_MS")
	unhealthyAfter := envDuration("FAKE_UNHEALTHY_AFTER_MS")
	hang := envDuration("FAKE_HANG_MS")
	exitCode := 137
	if v := os.Getenv("FAKE_EXIT_CODE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			exitCode = n
		}
	}
	reply := os.Getenv("FAKE_REPLY")
	if reply == "" {
		reply = "[{\"name\":\"Whole Milk\",\"brand\":\"Horizon\",\"category\":\"dairy\",\"confidence\":0.82}]"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		elapsed := time.Since(started)
		switch {
		case mode == "never_healthy":
			w.WriteHeader(503)
			fmt.Fprint(w, "{\"status\":\"loading model\"}")
			return
		case healthyAfter > 0 && elapsed < healthyAfter:
			w.WriteHeader(503)
			fmt.Fprint(w, "{\"status\":\"loading model\"}")
			return
		case mode == "unhealthy_after" && unhealthyAfter > 0 && elapsed >= unhealthyAfter:
			logEvent("HEALTH_FAIL", "")
			w.WriteHeader(503)
			fmt.Fprint(w, "{\"status\":\"error\"}")
			return
		}
		w.WriteHeader(200)
		fmt.Fprint(w, "{\"status\":\"ok\"}")
	})

	mux.HandleFunc("/completion", func(w http.ResponseWriter, r *http.Request) {
		logEvent("PROBE", "")
		if os.Getenv("FAKE_PROBE_EMPTY") != "" {
			fmt.Fprint(w, "{\"content\":\"\"}")
			return
		}
		fmt.Fprint(w, "{\"content\":\" Paris\"}")
	})

	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		id := strconv.FormatInt(nextID.Add(1), 10)
		raw, _ := io.ReadAll(r.Body)
		hasImage := strings.Contains(string(raw), "image_url")
		logEvent("REQ_START", id)
		if mode == "exit_on_request" {
			logEvent("EXIT", strconv.Itoa(exitCode))
			_ = logFile.Sync()
			os.Exit(exitCode)
		}
		if hang > 0 {
			time.Sleep(hang)
		}
		content := reply
		if !hasImage {
			// A text-only call is the recipe bundle, and it has to parse as
			// one: the scan and the recipe generation overlap by contract.
			content = "{\"makeNow\":[],\"almostThere\":[]}"
		}
		body := map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"content": content},
				"finish_reason": "stop",
			}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 20},
			"timings": map[string]any{"predicted_per_second": 9.9},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
		logEvent("REQ_END", id)
	})

	listener, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		logEvent("BIND_FAIL", err.Error())
		os.Exit(2)
	}
	logEvent("LISTENING", port)
	_ = http.Serve(listener, mux)
}
`

var (
	fakeBuildOnce sync.Once
	fakeBinary    string
	fakeBuildErr  error
)

// buildFakeServer compiles the fake once per test binary.
func buildFakeServer(t *testing.T) string {
	t.Helper()
	fakeBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fake-llama-server-*")
		if err != nil {
			fakeBuildErr = err
			return
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(fakeServerSource), 0o644); err != nil {
			fakeBuildErr = err
			return
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fakellamaserver\n\ngo 1.27\n"), 0o644); err != nil {
			fakeBuildErr = err
			return
		}
		binary := filepath.Join(dir, "llama-server")
		cmd := exec.Command("go", "build", "-o", binary, ".")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOFLAGS=")
		if out, err := cmd.CombinedOutput(); err != nil {
			fakeBuildErr = fmt.Errorf("build the fake llama-server: %v: %s", err, out)
			return
		}
		fakeBinary = binary
	})
	if fakeBuildErr != nil {
		t.Fatalf("%v", fakeBuildErr)
	}
	return fakeBinary
}

// harness wires a supervisor to the fake and records everything observable.
type harness struct {
	t       *testing.T
	sup     *Supervisor
	logPath string

	mu     sync.Mutex
	events []Event
	logs   []string
	mem    MemoryStatus
	memErr error
}

// healthyMemory is a machine with plenty of room.
func healthyMemory() MemoryStatus { return Classify(16*GiB, 12*GiB, false) }

// starvedMemory is an 8 GiB box with three other services on it: supported tier,
// but nowhere near enough free memory to load 4.6 GiB.
func starvedMemory() MemoryStatus { return Classify(8*GiB, 2*GiB, false) }

type harnessOptions struct {
	env       []string
	mutate    func(*Options)
	memStatus MemoryStatus
}

func newHarness(t *testing.T, ho harnessOptions) *harness {
	t.Helper()
	binary := buildFakeServer(t)

	dir := t.TempDir()
	model := filepath.Join(dir, "gemma-4-E4B-it-UD-IQ3_XXS.gguf")
	mmproj := filepath.Join(dir, "mmproj-gemma-4-E4B-it-Q8_0.gguf")
	for _, p := range []string{model, mmproj} {
		if err := os.WriteFile(p, []byte("gguf"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(dir, "fake.log")

	h := &harness{t: t, logPath: logPath, mem: ho.memStatus}
	if h.mem.TotalBytes == 0 {
		h.mem = healthyMemory()
	}

	opts := Options{
		Runtime: Runtime{
			BinaryPath: binary,
			LibraryDir: filepath.Dir(binary),
			Variant:    VariantUbuntuARM64,
			Source:     SourceCache,
			Tag:        "b10970",
		},
		ModelPath:               model,
		MMProjPath:              mmproj,
		Host:                    "127.0.0.1",
		StartupBudget:           20 * time.Second,
		StartupPollInterval:     10 * time.Millisecond,
		StartupProgressInterval: time.Second,
		HealthInterval:          40 * time.Millisecond,
		HealthTimeout:           300 * time.Millisecond,
		HealthFailureThreshold:  3,
		RequestTimeout:          10 * time.Second,
		InferQueueTimeout:       10 * time.Second,
		DrainTimeout:            10 * time.Second,
		TerminateGrace:          400 * time.Millisecond,
		IdleCheckInterval:       20 * time.Millisecond,
		Env:                     append([]string{"FAKE_LOG=" + logPath}, ho.env...),
		MemCheck: func() (MemoryStatus, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.mem, h.memErr
		},
		Observer: func(e Event) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.events = append(h.events, e)
		},
		Logf: func(format string, args ...any) {
			line := fmt.Sprintf(format, args...)
			h.mu.Lock()
			h.logs = append(h.logs, line)
			h.mu.Unlock()
			t.Logf("[sup] %s", line)
		},
	}
	if ho.mutate != nil {
		ho.mutate(&opts)
	}

	sup, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.sup = sup
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = sup.Shutdown(ctx)
	})
	return h
}

// setMemory changes what the injected preflight reports.
func (h *harness) setMemory(status MemoryStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.mem = status
}

func (h *harness) snapshotEvents() []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Event(nil), h.events...)
}

func (h *harness) eventsOfKind(kinds ...EventKind) []Event {
	want := map[EventKind]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	var out []Event
	for _, e := range h.snapshotEvents() {
		if want[e.Kind] {
			out = append(out, e)
		}
	}
	return out
}

func (h *harness) countEvents(kinds ...EventKind) int { return len(h.eventsOfKind(kinds...)) }

// waitFor polls a condition, which is how a test observes an asynchronous
// supervisor without sleeping for a guessed duration.
func (h *harness) waitFor(what string, timeout time.Duration, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("timed out after %s waiting for %s (state=%v events=%v)",
		timeout, what, h.sup.State(), h.eventKinds())
}

func (h *harness) eventKinds() []EventKind {
	var out []EventKind
	for _, e := range h.snapshotEvents() {
		out = append(out, e.Kind)
	}
	return out
}

func (h *harness) waitState(state contract.ModelState, timeout time.Duration) {
	h.t.Helper()
	h.waitFor("state "+state.String(), timeout, func() bool { return h.sup.State() == state })
}

// fakeEvent is one line from the fake's log.
type fakeEvent struct {
	name   string
	at     time.Time
	detail string
}

func (h *harness) fakeEvents() []fakeEvent {
	data, err := os.ReadFile(h.logPath)
	if err != nil {
		return nil
	}
	var out []fakeEvent
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), " ", 3)
		if len(fields) < 2 {
			continue
		}
		nanos, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		ev := fakeEvent{name: fields[0], at: time.Unix(0, nanos)}
		if len(fields) == 3 {
			ev.detail = fields[2]
		}
		out = append(out, ev)
	}
	return out
}

// interval is one request's occupancy of the single server slot.
type interval struct {
	id         string
	start, end time.Time
}

// requestIntervals pairs REQ_START with REQ_END.
func (h *harness) requestIntervals() []interval {
	open := map[string]time.Time{}
	var out []interval
	for _, e := range h.fakeEvents() {
		switch e.name {
		case "REQ_START":
			open[e.detail] = e.at
		case "REQ_END":
			if start, ok := open[e.detail]; ok {
				out = append(out, interval{id: e.detail, start: start, end: e.at})
				delete(open, e.detail)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	return out
}

// childArgs returns the argv of the nth spawned child.
func (h *harness) childArgs(n int) []string {
	var seen int
	for _, e := range h.fakeEvents() {
		if e.name != "ARGS" {
			continue
		}
		if seen == n {
			return strings.Split(e.detail, "\x1f")
		}
		seen++
	}
	return nil
}
