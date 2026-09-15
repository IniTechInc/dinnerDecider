package llm

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"syscall"
)

// newChildCommand builds the child command.
//
// exec.Command, deliberately NOT exec.CommandContext. CommandContext kills the
// process when its context ends, and the context available at spawn time is the
// one belonging to whatever asked for the load - an HTTP request, a scan, a
// startup routine. Tying a multi-gigabyte model's life to the request that
// happened to warm it is the same mistake as freeing it mid-inference. The child
// outlives every request and is ended only by an explicit lifecycle decision.
func newChildCommand(path string, args []string) *exec.Cmd {
	return exec.Command(path, args...)
}

// configureChildProcess sets the process attributes the supervised child needs.
//
// Setpgid puts llama-server in its own process group so teardown can signal the
// GROUP rather than one pid. That matters because a runtime build may exec
// helpers, and a SIGTERM that reaches only the parent leaves several gigabytes
// resident in something nobody is watching any more.
//
// Pdeathsig is deliberately NOT set, even though it looks like the obvious
// belt-and-braces for an orphaned 4 GB child. On Linux the parent-death signal
// fires when the OS THREAD that forked exits, not when the process does, and the
// Go runtime creates and retires threads freely: the documented result is a
// child killed at an arbitrary moment for no reason the logs can explain. That
// is the same class of bug as freeing a model mid-inference, arrived at from the
// other direction. Orphans are handled instead by signalling the process group
// on every teardown path, and by the systemd unit's own KillMode.
func configureChildProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// ChildOOMScoreAdj is the oom_score_adj written onto llama-server.
//
// Positive, on purpose, and paired with the service unit's OOMScoreAdjust=-100:
// if the box runs out of memory the kernel should reap the CHILD, which the
// supervisor sees, reports as Failed, and recovers from behind the Step 41
// preflight. Reaping the service instead loses the scan, the API, the UI and
// the explanation all at once, and leaves a 4 GB llama-server running with
// nobody to stop it.
const ChildOOMScoreAdj = 500

// adjustChildOOMScore nudges the kernel towards killing the child first.
//
// Best effort by design: the write fails when the service is not privileged
// enough to raise a score, and on every non-Linux host. Neither is worth
// refusing to serve over, so failures are returned for logging and nothing else.
func adjustChildOOMScore(pid int) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	path := fmt.Sprintf("/proc/%d/oom_score_adj", pid)
	if err := os.WriteFile(path, []byte(strconv.Itoa(ChildOOMScoreAdj)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// signalProcessGroup sends a signal to the child's whole process group, falling
// back to the single process when the group is not available.
func signalProcessGroup(proc *os.Process, sig syscall.Signal) error {
	if proc == nil {
		return nil
	}
	// The negative pid addresses the group. Setpgid made the child the leader,
	// so its pid is its pgid.
	if err := syscall.Kill(-proc.Pid, sig); err == nil {
		return nil
	}
	return proc.Signal(sig)
}

// ringBuffer keeps the last n bytes written to it.
//
// The child's stderr is the only evidence available when a runtime build fails
// to start: "libcuda.so.1: cannot open shared object file" and an illegal
// instruction both arrive here and nowhere else. Bounded because llama-server is
// chatty and a wedged child could otherwise fill memory with its own complaints
// on the box that is already short of it.
type ringBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func newRingBuffer(limit int) *ringBuffer {
	if limit <= 0 {
		limit = 16 << 10
	}
	return &ringBuffer{limit: limit}
}

// Write implements io.Writer and never fails.
func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.limit {
		r.buf = append([]byte(nil), r.buf[len(r.buf)-r.limit:]...)
	}
	return len(p), nil
}

// String returns the retained tail.
func (r *ringBuffer) String() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}
