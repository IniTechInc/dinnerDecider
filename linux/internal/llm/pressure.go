package llm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
)

// This file answers obligation 1 of shared/pipeline/model-lifecycle.md
// § Adapter obligations, and the TODO(unresolved) it carries:
//
//	"Linux/Go: there is no equivalent signal. TODO(unresolved): no
//	memory-pressure source is specified for a Linux port anywhere in this repo
//	... it simply may have no producer of pressure events, or may poll cgroup
//	memory pressure. That is a port-level design decision with no shipped
//	precedent to copy."
//
// The decision: poll, from three sources, and funnel all of them into the one
// idempotent handler the contract asks for. iOS subscribes to two sources for
// the same reason - UIKit only ever posts a plain warning, never a critical
// tier - so a single source here would have the same blind spot.
//
// Obligation 5 is enforced structurally rather than by convention: see
// PollerOptions.Disabled. The bench and the self-test must not install a poller,
// so that they behave exactly like the last known-good build rather than firing
// a storm of no-op handlers while the model loads.

// Pressure thresholds. Each is the tier boundary for one source.
const (
	// PSISomeAvg10WarnPercent: 10% of the last 10 seconds with at least one
	// task stalled on memory. That is real reclaim work, not noise.
	PSISomeAvg10WarnPercent = 10.0
	// PSIFullAvg10CriticalPercent: 5% of the last 10 seconds with EVERY task
	// stalled. "full" stall time is severe by construction, so its threshold is
	// lower than "some".
	PSIFullAvg10CriticalPercent = 5.0

	// CgroupUsageWarnRatio and CgroupUsageCriticalRatio compare memory.current
	// against memory.max inside a container.
	CgroupUsageWarnRatio     = 0.85
	CgroupUsageCriticalRatio = 0.92

	// MeminfoWarnRatio and MeminfoCriticalRatio are MemAvailable/MemTotal for
	// the fallback source.
	MeminfoWarnRatio     = 0.15
	MeminfoCriticalRatio = 0.08
)

// DefaultPressureInterval is the poll period. Two seconds is a compromise: long
// enough that the poller itself costs nothing on a Pi, short enough that a
// critical event arrives while there is still time to drain an inference rather
// than be OOM-killed mid-decode.
const DefaultPressureInterval = 2 * time.Second

// DefaultPressureRepeat bounds how often the same sustained level is re-emitted.
// The handler is idempotent, but a repeat every 2 seconds would bury the log.
const DefaultPressureRepeat = 30 * time.Second

// Reading is one classification of the current memory situation.
type Reading struct {
	// Level is the tier. Only meaningful when Actionable is true.
	Level contract.MemoryPressureLevel
	// Actionable is false when no source reported pressure. A poller emits
	// nothing in that case: the contract's handler takes WARNING and CRITICAL,
	// and there is no "all clear" event to deliver.
	Actionable bool
	// Source names the producer: psi, cgroup2 or meminfo.
	Source string
	// Detail is the numbers behind the decision, for the log line.
	Detail string
}

// String renders a reading for logs.
func (r Reading) String() string {
	if !r.Actionable {
		return "no pressure"
	}
	return fmt.Sprintf("%s pressure from %s (%s)", r.Level, r.Source, r.Detail)
}

// ambiguousLevel is the tier used when a source says something happened but the
// numbers cannot say how bad it is.
//
// model-lifecycle.md § Adapter obligations: "Map your platform's signals onto
// WARNING/CRITICAL and default to WARNING when the tier is unknown." WARNING is
// the safe default precisely because it is the tier that never sets
// pendingUnload: it frees an idle resident model and is ignored outright during
// a load, an inference or a session.
const ambiguousLevel = contract.PressureWarning

// Sampler reads the three pressure sources under a filesystem root.
//
// It is stateful because cgroup v2 reports memory.events as monotonic counters:
// a container that hit its limit an hour ago still reports high=7 forever, so
// only the DELTA between samples is an event. The first sample establishes the
// baseline and deliberately reports nothing.
type Sampler struct {
	root string

	cgroupDir     string
	cgroupChecked bool

	haveEventBaseline bool
	prevHigh          uint64
	prevMax           uint64
	prevOOMKill       uint64
}

// NewSampler returns a sampler reading under root. The empty root means the real
// filesystem.
func NewSampler(root string) *Sampler { return &Sampler{root: root} }

// Sample classifies the current memory situation.
//
// Source selection, in the plan's order: PSI first, then cgroup v2, then
// /proc/meminfo as a fallback. PSI and cgroup are consulted TOGETHER when both
// exist, and the higher tier wins: inside a container the cgroup can be at its
// hard limit while host PSI is calm, and on a host under reclaim PSI fires while
// the cgroup is unlimited. The meminfo fallback runs only when neither of the
// other two is available, because MemAvailable is a much cruder proxy and would
// otherwise double-report what PSI already said better.
func (s *Sampler) Sample() Reading {
	var best Reading

	if r := s.samplePSI(); r.Actionable {
		best = higher(best, r)
	}
	if r := s.sampleCgroup(); r.Actionable {
		best = higher(best, r)
	}
	if !s.psiAvailable() && !s.cgroupAvailable() {
		if r := s.sampleMeminfo(); r.Actionable {
			best = higher(best, r)
		}
	}
	return best
}

// higher keeps the more severe of two readings. An actionable reading always
// beats a non-actionable one.
func higher(a, b Reading) Reading {
	switch {
	case !a.Actionable:
		return b
	case !b.Actionable:
		return a
	case b.Level > a.Level:
		return b
	default:
		return a
	}
}

// --- Source 1: PSI --------------------------------------------------------

// psiPath is the kernel's pressure-stall information for memory.
func (s *Sampler) psiPath() string { return rootJoin(s.root, "proc", "pressure", "memory") }

func (s *Sampler) psiAvailable() bool { return fileExists(s.psiPath()) }

// samplePSI reads /proc/pressure/memory.
//
//	some avg10=0.00 avg60=0.00 avg300=0.00 total=0
//	full avg10=0.00 avg60=0.00 avg300=0.00 total=0
//
// "some" is the share of the window in which at least one task stalled waiting
// on memory; "full" is the share in which every runnable task did. avg10 is used
// rather than avg60 because the whole point is to react before the OOM killer
// does, and a 60 second average of an event that kills in 10 is useless.
func (s *Sampler) samplePSI() Reading {
	body, err := readSmallFile(s.psiPath())
	if err != nil {
		return Reading{}
	}
	some, someOK := psiAvg10(body, "some")
	full, fullOK := psiAvg10(body, "full")

	switch {
	case fullOK && full > PSIFullAvg10CriticalPercent:
		return Reading{
			Level: contract.PressureCritical, Actionable: true, Source: "psi",
			Detail: fmt.Sprintf("full avg10=%.2f > %.1f", full, PSIFullAvg10CriticalPercent),
		}
	case someOK && some > PSISomeAvg10WarnPercent:
		return Reading{
			Level: contract.PressureWarning, Actionable: true, Source: "psi",
			Detail: fmt.Sprintf("some avg10=%.2f > %.1f", some, PSISomeAvg10WarnPercent),
		}
	}
	return Reading{}
}

// psiAvg10 pulls the avg10 field out of the named PSI line.
func psiAvg10(body, prefix string) (float64, bool) {
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != prefix {
			continue
		}
		for _, f := range fields[1:] {
			key, value, found := strings.Cut(f, "=")
			if !found || key != "avg10" {
				continue
			}
			v, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return 0, false
			}
			return v, true
		}
	}
	return 0, false
}

// --- Source 2: cgroup v2 --------------------------------------------------

// cgroupDirectory resolves this process's cgroup v2 directory, once.
//
// /proc/self/cgroup on a v2 system has a single line "0::/path". The interface
// files live at /sys/fs/cgroup/<path>. When the path is "/" the files are at the
// root, which is the usual shape inside a container with its own namespace.
func (s *Sampler) cgroupDirectory() string {
	if s.cgroupChecked {
		return s.cgroupDir
	}
	s.cgroupChecked = true

	base := rootJoin(s.root, "sys", "fs", "cgroup")
	rel := "/"
	if body, err := readSmallFile(rootJoin(s.root, "proc", "self", "cgroup")); err == nil {
		for _, line := range strings.Split(body, "\n") {
			if after, found := strings.CutPrefix(strings.TrimSpace(line), "0::"); found {
				rel = after
				break
			}
		}
	}
	dir := filepath.Join(base, filepath.FromSlash(rel))
	if !fileExists(filepath.Join(dir, "memory.events")) {
		// A delegated path that is not visible from here (the common case when
		// the host cgroupfs is mounted but the process is in a namespace).
		// Fall back to the mount root, which is what a container sees.
		if fileExists(filepath.Join(base, "memory.events")) {
			dir = base
		} else {
			dir = ""
		}
	}
	s.cgroupDir = dir
	return dir
}

func (s *Sampler) cgroupAvailable() bool { return s.cgroupDirectory() != "" }

// sampleCgroup reads memory.events, memory.current and memory.max.
//
// Two independent signals:
//
//   - memory.events counters. "high" increments when the cgroup was throttled
//     above memory.high, "max" when an allocation hit the hard limit and
//     "oom_kill" when something in the cgroup was killed. They are MONOTONIC, so
//     only a delta since the previous sample is an event, and the first sample
//     only establishes the baseline.
//   - memory.current against memory.max. A cgroup sitting at 92% of its hard
//     limit is about to stall whether or not a counter moved yet.
func (s *Sampler) sampleCgroup() Reading {
	dir := s.cgroupDirectory()
	if dir == "" {
		return Reading{}
	}

	events := readKeyedCounters(filepath.Join(dir, "memory.events"))
	high, max, oomKill := events["high"], events["max"], events["oom_kill"]
	haveEvents := len(events) > 0

	var best Reading
	if haveEvents {
		if !s.haveEventBaseline {
			s.haveEventBaseline = true
		} else {
			switch {
			case oomKill > s.prevOOMKill:
				best = Reading{
					Level: contract.PressureCritical, Actionable: true, Source: "cgroup2",
					Detail: fmt.Sprintf("memory.events oom_kill %d -> %d", s.prevOOMKill, oomKill),
				}
			case max > s.prevMax:
				best = Reading{
					Level: contract.PressureCritical, Actionable: true, Source: "cgroup2",
					Detail: fmt.Sprintf("memory.events max %d -> %d", s.prevMax, max),
				}
			case high > s.prevHigh:
				best = Reading{
					// Throttled above memory.high but not yet at the hard limit:
					// something happened, the tier is not obvious. Default to
					// WARNING, per the adapter obligation.
					Level: ambiguousLevel, Actionable: true, Source: "cgroup2",
					Detail: fmt.Sprintf("memory.events high %d -> %d", s.prevHigh, high),
				}
			}
		}
		s.prevHigh, s.prevMax, s.prevOOMKill = high, max, oomKill
	}

	current, currentOK := readUintFile(filepath.Join(dir, "memory.current"))
	limit, limitOK := readCgroupLimit(filepath.Join(dir, "memory.max"))
	if currentOK && limitOK && limit > 0 {
		ratio := float64(current) / float64(limit)
		switch {
		case ratio >= CgroupUsageCriticalRatio:
			best = higher(best, Reading{
				Level: contract.PressureCritical, Actionable: true, Source: "cgroup2",
				Detail: fmt.Sprintf("memory.current %s of %s (%.0f%%)", FormatBytes(current), FormatBytes(limit), ratio*100),
			})
		case ratio >= CgroupUsageWarnRatio:
			best = higher(best, Reading{
				Level: contract.PressureWarning, Actionable: true, Source: "cgroup2",
				Detail: fmt.Sprintf("memory.current %s of %s (%.0f%%)", FormatBytes(current), FormatBytes(limit), ratio*100),
			})
		}
	}
	return best
}

// readKeyedCounters parses the "key value" lines cgroup v2 uses.
func readKeyedCounters(path string) map[string]uint64 {
	body, err := readSmallFile(path)
	if err != nil {
		return nil
	}
	out := make(map[string]uint64)
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if v, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
			out[fields[0]] = v
		}
	}
	return out
}

// readUintFile reads a single-number cgroup file.
func readUintFile(path string) (uint64, bool) {
	body, err := readSmallFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(body), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// readCgroupLimit reads memory.max, where the literal "max" means unlimited.
// Unlimited is reported as not-ok rather than as a huge number, so the ratio
// check is skipped instead of always reading as 0%.
func readCgroupLimit(path string) (uint64, bool) {
	body, err := readSmallFile(path)
	if err != nil {
		return 0, false
	}
	trimmed := strings.TrimSpace(body)
	if trimmed == "max" {
		return 0, false
	}
	v, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// --- Source 3: /proc/meminfo fallback -------------------------------------

// sampleMeminfo is the last resort, for kernels built without PSI and hosts
// without cgroup v2. It is the same file the Step 41 preflight reads, but a
// different question: the preflight asks "can a 4.6 GiB allocation succeed", and
// this asks "is the machine under pressure right now".
func (s *Sampler) sampleMeminfo() Reading {
	data, err := os.ReadFile(rootJoin(s.root, "proc", "meminfo"))
	if err != nil {
		return Reading{}
	}
	total, totalOK := meminfoValueBytes(data, "MemTotal")
	available, availableOK := meminfoValueBytes(data, "MemAvailable")
	if !availableOK {
		available, availableOK = meminfoValueBytes(data, "MemFree")
	}
	if !totalOK || !availableOK || total == 0 {
		return Reading{}
	}
	ratio := float64(available) / float64(total)
	detail := fmt.Sprintf("MemAvailable %s of %s (%.0f%%)", FormatBytes(available), FormatBytes(total), ratio*100)
	switch {
	case ratio < MeminfoCriticalRatio:
		return Reading{Level: contract.PressureCritical, Actionable: true, Source: "meminfo", Detail: detail}
	case ratio < MeminfoWarnRatio:
		return Reading{Level: contract.PressureWarning, Actionable: true, Source: "meminfo", Detail: detail}
	}
	return Reading{}
}

// --- The poller -----------------------------------------------------------

// PollerOptions configures the pressure poller.
type PollerOptions struct {
	// Root is the filesystem root to read from. Empty means the real one.
	Root string
	// Interval defaults to DefaultPressureInterval.
	Interval time.Duration
	// Repeat bounds how often a sustained level is re-emitted; defaults to
	// DefaultPressureRepeat.
	Repeat time.Duration
	// Handler receives every emitted event. It MUST be idempotent: the contract
	// requires all sources funnel into one handler, and this poller can deliver
	// the same level from two sources or twice in a row.
	Handler func(contract.MemoryPressureLevel, Reading)
	// Disabled suppresses the poller entirely, returning a nil *Poller whose
	// Start is a no-op.
	//
	// This exists for obligation 5 of § Adapter obligations: the bench and the
	// self-test must install NO pressure source, so the process "behaves exactly
	// like the last known-good build: no extra memory-pressure machinery firing
	// a storm of no-op handlers while it loads the model". Making it an option
	// on the constructor means the bench states its intent in one place instead
	// of relying on nobody wiring a poller in by habit.
	Disabled bool
	// Logf receives one line per emitted event. Optional.
	Logf func(string, ...any)
}

// Poller samples the pressure sources on a ticker and funnels every actionable
// reading into one handler.
type Poller struct {
	sampler  *Sampler
	interval time.Duration
	repeat   time.Duration
	handler  func(contract.MemoryPressureLevel, Reading)
	logf     func(string, ...any)

	lastLevel     contract.MemoryPressureLevel
	lastActive    bool
	lastEmittedAt time.Time
}

// NewPoller builds a poller, or returns nil when Disabled is set.
func NewPoller(opts PollerOptions) *Poller {
	if opts.Disabled || opts.Handler == nil {
		return nil
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultPressureInterval
	}
	if opts.Repeat <= 0 {
		opts.Repeat = DefaultPressureRepeat
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	return &Poller{
		sampler:  NewSampler(opts.Root),
		interval: opts.Interval,
		repeat:   opts.Repeat,
		handler:  opts.Handler,
		logf:     opts.Logf,
	}
}

// Run polls until the context is cancelled. A nil poller returns immediately,
// which is what makes the Disabled option safe to pass straight through.
func (p *Poller) Run(ctx context.Context) {
	if p == nil {
		return
	}
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.step(time.Now())
		}
	}
}

// Start runs the poller in the background and returns a stop function. A nil
// poller returns a no-op stop.
func (p *Poller) Start(ctx context.Context) (stop func()) {
	if p == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

// step takes one sample and decides whether to emit.
//
// Emission policy: emit on the first actionable sample, on any rise in tier, and
// on a sustained tier no more often than Repeat. Do not emit anything when the
// pressure clears - the contract's handler has no "all clear" event, and
// inventing one would mean inventing a decision the machine has no table for.
func (p *Poller) step(now time.Time) {
	r := p.sampler.Sample()
	if !r.Actionable {
		p.lastActive = false
		return
	}
	emit := !p.lastActive ||
		r.Level > p.lastLevel ||
		now.Sub(p.lastEmittedAt) >= p.repeat
	p.lastActive = true
	p.lastLevel = r.Level
	if !emit {
		return
	}
	p.lastEmittedAt = now
	p.logf("memory pressure: %s", r)
	p.handler(r.Level, r)
}
