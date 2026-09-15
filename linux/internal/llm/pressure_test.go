package llm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
)

// psiBody renders a /proc/pressure/memory file.
func psiBody(someAvg10, fullAvg10 float64) string {
	return fmt.Sprintf("some avg10=%.2f avg60=0.00 avg300=0.00 total=1234\n"+
		"full avg10=%.2f avg60=0.00 avg300=0.00 total=567\n", someAvg10, fullAvg10)
}

// TestPSISource covers the kernel's pressure-stall information, which is the
// best of the three sources and the only one that measures actual stall time
// rather than a proxy for it.
func TestPSISource(t *testing.T) {
	tests := []struct {
		name           string
		some, full     float64
		wantActionable bool
		wantLevel      contract.MemoryPressureLevel
	}{
		{name: "idle machine", some: 0, full: 0},
		{name: "some stalls below the threshold", some: 9.5, full: 0},
		{name: "some stalls above the threshold", some: 12.4, full: 0, wantActionable: true, wantLevel: contract.PressureWarning},
		{name: "full stalls above the threshold", some: 40, full: 6.2, wantActionable: true, wantLevel: contract.PressureCritical},
		{name: "full stalls below its threshold but some above", some: 30, full: 4, wantActionable: true, wantLevel: contract.PressureWarning},
		{name: "exactly at the some threshold is not pressure", some: 10, full: 0},
		{name: "exactly at the full threshold is not critical", some: 0, full: 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := fakeRoot{"proc/pressure/memory": psiBody(tc.some, tc.full)}.build(t)
			got := NewSampler(root).Sample()
			if got.Actionable != tc.wantActionable {
				t.Fatalf("actionable = %v, want %v (%s)", got.Actionable, tc.wantActionable, got)
			}
			if !tc.wantActionable {
				return
			}
			if got.Level != tc.wantLevel {
				t.Fatalf("level = %v, want %v (%s)", got.Level, tc.wantLevel, got)
			}
			if got.Source != "psi" {
				t.Fatalf("source = %q, want psi", got.Source)
			}
		})
	}
}

// TestPSIUnparsableIsSilence: a PSI file that exists but cannot be read must not
// manufacture a warning. Inventing pressure would unload a resident model on
// every poll on a machine that is perfectly healthy.
func TestPSIUnparsableIsSilence(t *testing.T) {
	root := fakeRoot{"proc/pressure/memory": "some avg10=banana avg60=0.00\nfull nonsense\n"}.build(t)
	if got := NewSampler(root).Sample(); got.Actionable {
		t.Fatalf("unparsable PSI produced %s", got)
	}
}

// TestCgroupEventsNeedABaseline is the counter trap: memory.events values are
// monotonic for the life of the cgroup, so a container that was throttled an
// hour ago still reports high=7. Only a delta is an event.
func TestCgroupEventsNeedABaseline(t *testing.T) {
	root := fakeRoot{
		"proc/self/cgroup":             "0::/\n",
		"sys/fs/cgroup/memory.events":  "low 0\nhigh 7\nmax 0\noom 0\noom_kill 0\n",
		"sys/fs/cgroup/memory.current": "1000\n",
		"sys/fs/cgroup/memory.max":     "max\n",
	}.build(t)

	s := NewSampler(root)
	if got := s.Sample(); got.Actionable {
		t.Fatalf("the first sample must only establish the baseline, got %s", got)
	}
	if got := s.Sample(); got.Actionable {
		t.Fatalf("an unchanged counter is not an event, got %s", got)
	}

	writeFile(t, filepath.Join(root, "sys/fs/cgroup/memory.events"), "low 0\nhigh 8\nmax 0\noom 0\noom_kill 0\n")
	got := s.Sample()
	if !got.Actionable || got.Level != contract.PressureWarning {
		t.Fatalf("a memory.high increment should warn, got %s", got)
	}
	if got.Source != "cgroup2" {
		t.Fatalf("source = %q", got.Source)
	}
}

// TestCgroupHardLimitEventsAreCritical: hitting memory.max means an allocation
// already stalled, and an oom_kill means something in the cgroup is already
// dead - on this box that is usually llama-server.
func TestCgroupHardLimitEventsAreCritical(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{
		{"max", "low 0\nhigh 0\nmax 3\noom 0\noom_kill 0\n", "low 0\nhigh 0\nmax 4\noom 0\noom_kill 0\n"},
		{"oom_kill", "low 0\nhigh 0\nmax 0\noom 1\noom_kill 0\n", "low 0\nhigh 0\nmax 0\noom 2\noom_kill 1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fakeRoot{
				"proc/self/cgroup":            "0::/\n",
				"sys/fs/cgroup/memory.events": tc.before,
				"sys/fs/cgroup/memory.max":    "max\n",
			}.build(t)
			s := NewSampler(root)
			s.Sample() // baseline
			writeFile(t, filepath.Join(root, "sys/fs/cgroup/memory.events"), tc.after)
			got := s.Sample()
			if !got.Actionable || got.Level != contract.PressureCritical {
				t.Fatalf("got %s, want critical", got)
			}
		})
	}
}

// TestCgroupUsageRatio covers the second cgroup signal: sitting at the hard
// limit is pressure whether or not a counter has moved yet.
func TestCgroupUsageRatio(t *testing.T) {
	tests := []struct {
		name           string
		current, max   string
		wantActionable bool
		wantLevel      contract.MemoryPressureLevel
	}{
		{name: "half used", current: "4000000000", max: "8000000000"},
		{name: "86 percent", current: "6900000000", max: "8000000000", wantActionable: true, wantLevel: contract.PressureWarning},
		{name: "95 percent", current: "7600000000", max: "8000000000", wantActionable: true, wantLevel: contract.PressureCritical},
		{name: "unlimited cgroup", current: "7600000000", max: "max"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := fakeRoot{
				"proc/self/cgroup":             "0::/\n",
				"sys/fs/cgroup/memory.events":  "low 0\nhigh 0\nmax 0\noom 0\noom_kill 0\n",
				"sys/fs/cgroup/memory.current": tc.current + "\n",
				"sys/fs/cgroup/memory.max":     tc.max + "\n",
			}.build(t)
			s := NewSampler(root)
			s.Sample() // baseline for the counters
			got := s.Sample()
			if got.Actionable != tc.wantActionable {
				t.Fatalf("actionable = %v, want %v (%s)", got.Actionable, tc.wantActionable, got)
			}
			if tc.wantActionable && got.Level != tc.wantLevel {
				t.Fatalf("level = %v, want %v", got.Level, tc.wantLevel)
			}
		})
	}
}

// TestCgroupDelegatedPathFallsBackToTheMountRoot covers the common container
// shape: /proc/self/cgroup names a host path that is not visible from inside the
// namespace, while the interface files sit at the mount root.
func TestCgroupDelegatedPathFallsBackToTheMountRoot(t *testing.T) {
	root := fakeRoot{
		"proc/self/cgroup":             "0::/system.slice/docker-abc123.scope\n",
		"sys/fs/cgroup/memory.events":  "low 0\nhigh 0\nmax 0\noom 0\noom_kill 0\n",
		"sys/fs/cgroup/memory.current": "7600000000\n",
		"sys/fs/cgroup/memory.max":     "8000000000\n",
	}.build(t)
	s := NewSampler(root)
	s.Sample()
	got := s.Sample()
	if !got.Actionable || got.Level != contract.PressureCritical {
		t.Fatalf("got %s, want a critical reading from the mount root", got)
	}
}

// TestMeminfoFallback is the source of last resort, for kernels built without
// PSI on hosts without cgroup v2.
func TestMeminfoFallback(t *testing.T) {
	tests := []struct {
		name                   string
		totalKiB, availableKiB int64
		wantActionable         bool
		wantLevel              contract.MemoryPressureLevel
	}{
		{name: "plenty free", totalKiB: 8_000_000, availableKiB: 6_000_000},
		{name: "20 percent free", totalKiB: 8_000_000, availableKiB: 1_600_000},
		{name: "12 percent free", totalKiB: 8_000_000, availableKiB: 960_000, wantActionable: true, wantLevel: contract.PressureWarning},
		{name: "5 percent free", totalKiB: 8_000_000, availableKiB: 400_000, wantActionable: true, wantLevel: contract.PressureCritical},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := fakeRoot{"proc/meminfo": meminfo(tc.totalKiB, tc.availableKiB)}.build(t)
			got := NewSampler(root).Sample()
			if got.Actionable != tc.wantActionable {
				t.Fatalf("actionable = %v, want %v (%s)", got.Actionable, tc.wantActionable, got)
			}
			if tc.wantActionable {
				if got.Level != tc.wantLevel {
					t.Fatalf("level = %v, want %v", got.Level, tc.wantLevel)
				}
				if got.Source != "meminfo" {
					t.Fatalf("source = %q, want meminfo", got.Source)
				}
			}
		})
	}
}

// TestMeminfoIsOnlyAFallback: when PSI exists it is the authority, and the
// cruder proxy must not second-guess it. A box with a small MemAvailable and no
// stalls is a box using its page cache well.
func TestMeminfoIsOnlyAFallback(t *testing.T) {
	root := fakeRoot{
		"proc/pressure/memory": psiBody(0, 0),
		"proc/meminfo":         meminfo(8_000_000, 400_000),
	}.build(t)
	if got := NewSampler(root).Sample(); got.Actionable {
		t.Fatalf("meminfo must not fire while PSI is available and calm, got %s", got)
	}
}

// TestHigherTierWinsAcrossSources: inside a container the cgroup can be at its
// hard limit while host PSI is calm, and on a busy host the reverse. Both are
// consulted and the worse answer wins.
func TestHigherTierWinsAcrossSources(t *testing.T) {
	root := fakeRoot{
		"proc/pressure/memory":         psiBody(12, 0),
		"proc/self/cgroup":             "0::/\n",
		"sys/fs/cgroup/memory.events":  "low 0\nhigh 0\nmax 0\noom 0\noom_kill 0\n",
		"sys/fs/cgroup/memory.current": "7600000000\n",
		"sys/fs/cgroup/memory.max":     "8000000000\n",
	}.build(t)
	s := NewSampler(root)
	s.Sample()
	got := s.Sample()
	if got.Level != contract.PressureCritical {
		t.Fatalf("level = %v, want critical from the cgroup while PSI only warns (%s)", got.Level, got)
	}
}

// TestNoSourcesAtAllIsSilence covers a stripped container: no PSI, no cgroup
// files, no meminfo. The correct behaviour is to produce no events at all, which
// is exactly what the contract anticipated for a Linux port.
func TestNoSourcesAtAllIsSilence(t *testing.T) {
	root := fakeRoot{"etc/hostname": "box\n"}.build(t)
	if got := NewSampler(root).Sample(); got.Actionable {
		t.Fatalf("got %s from an empty tree", got)
	}
}

// TestPollerEmissionPolicy pins when the handler is called: once on the way up,
// again when the tier rises, and no more often than Repeat while it is
// sustained. The handler is idempotent by contract, but a log line every two
// seconds is its own failure.
func TestPollerEmissionPolicy(t *testing.T) {
	root := fakeRoot{"proc/meminfo": meminfo(8_000_000, 960_000)}.build(t)

	var mu sync.Mutex
	var levels []contract.MemoryPressureLevel
	p := NewPoller(PollerOptions{
		Root:   root,
		Repeat: time.Minute,
		Handler: func(l contract.MemoryPressureLevel, _ Reading) {
			mu.Lock()
			defer mu.Unlock()
			levels = append(levels, l)
		},
	})
	if p == nil {
		t.Fatal("NewPoller returned nil for an enabled poller")
	}

	now := time.Now()
	p.step(now)
	p.step(now.Add(2 * time.Second))
	p.step(now.Add(4 * time.Second))
	if len(levels) != 1 || levels[0] != contract.PressureWarning {
		t.Fatalf("sustained warning emitted %v, want exactly one warning", levels)
	}

	// The tier rises: emit immediately, Repeat notwithstanding.
	writeFile(t, filepath.Join(root, "proc/meminfo"), meminfo(8_000_000, 400_000))
	p.step(now.Add(6 * time.Second))
	if len(levels) != 2 || levels[1] != contract.PressureCritical {
		t.Fatalf("levels = %v, want a critical emitted on the rise", levels)
	}

	// Pressure clears: no event at all, because the machine has no all-clear.
	writeFile(t, filepath.Join(root, "proc/meminfo"), meminfo(8_000_000, 6_000_000))
	p.step(now.Add(8 * time.Second))
	if len(levels) != 2 {
		t.Fatalf("levels = %v, want no event when pressure clears", levels)
	}

	// It comes back after clearing: emit again even inside the Repeat window.
	writeFile(t, filepath.Join(root, "proc/meminfo"), meminfo(8_000_000, 960_000))
	p.step(now.Add(10 * time.Second))
	if len(levels) != 3 {
		t.Fatalf("levels = %v, want a fresh event after the pressure cleared", levels)
	}

	// Sustained past Repeat: one more.
	p.step(now.Add(2 * time.Minute))
	if len(levels) != 4 {
		t.Fatalf("levels = %v, want a repeat after the Repeat window", levels)
	}
}

// TestDisabledPollerInstallsNothing is obligation 5 of § Adapter obligations:
// the bench and the self-test must install no pressure source, so that the
// process behaves exactly like the last known-good build instead of firing a
// storm of no-op handlers while it loads the model.
func TestDisabledPollerInstallsNothing(t *testing.T) {
	called := false
	p := NewPoller(PollerOptions{
		Disabled: true,
		Handler:  func(contract.MemoryPressureLevel, Reading) { called = true },
	})
	if p != nil {
		t.Fatal("a disabled poller must be nil so it cannot be started by accident")
	}
	// Every entry point has to tolerate the nil, or "disabled" would be a
	// panic waiting for the bench to hit it.
	stop := p.Start(context.Background())
	stop()
	p.Run(context.Background())
	if called {
		t.Fatal("a disabled poller must never call the handler")
	}
	if NewPoller(PollerOptions{}) != nil {
		t.Fatal("a poller with no handler has nothing to funnel into and must be nil")
	}
}

// TestPollerRunStopsWithTheContext keeps the goroutine from outliving the
// service.
func TestPollerRunStopsWithTheContext(t *testing.T) {
	root := fakeRoot{"proc/meminfo": meminfo(8_000_000, 400_000)}.build(t)
	var mu sync.Mutex
	count := 0
	p := NewPoller(PollerOptions{
		Root:     root,
		Interval: time.Millisecond,
		Repeat:   time.Millisecond,
		Handler: func(contract.MemoryPressureLevel, Reading) {
			mu.Lock()
			count++
			mu.Unlock()
		},
	})
	stop := p.Start(context.Background())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := count
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	stop()

	mu.Lock()
	seen := count
	mu.Unlock()
	if seen == 0 {
		t.Fatal("the poller never emitted")
	}
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	after := count
	mu.Unlock()
	if after != seen {
		t.Fatalf("the poller kept running after stop: %d -> %d", seen, after)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
