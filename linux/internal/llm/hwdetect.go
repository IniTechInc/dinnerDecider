// Package llm supervises the local inference runtime.
//
// The port never compiles llama.cpp (docs/PORTING_NOTES.md § Linux/IoT: DO NOT
// COMPILE llama.cpp). It detects the hardware, acquires the matching official
// prebuilt `llama-server`, supervises it as a child process on loopback HTTP,
// and admits inference through a single capacity-1 gate so the contract's
// single-state ModelLifecycle is only ever asked when it can say yes.
//
// Files:
//
//	hwdetect.go  - arch + accelerator detection, asset variant naming
//	manifest.go  - the pinned llama.cpp tag and per-variant sha256
//	runtime.go   - resolve / download / verify / extract / probe the binary
//	supervisor.go- the lifecycle adapter: spawn, health, gate, drain, recovery
//	client.go    - the /v1/chat/completions wire shape
//	pressure.go  - PSI / cgroup v2 / meminfo memory-pressure poller
//	preflight.go - the resource floor, re-run before any reload
package llm

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Accelerator names the compute backend a runtime build targets.
type Accelerator string

const (
	// AcceleratorCPU is the generic build. Every target can fall back to it,
	// and on IoT that fallback is a routine path rather than a failure mode.
	AcceleratorCPU Accelerator = "cpu"
	// AcceleratorCUDA is an NVIDIA build (discrete x86 card or a Jetson).
	AcceleratorCUDA Accelerator = "cuda"
	// AcceleratorROCm is an AMD build.
	AcceleratorROCm Accelerator = "rocm"
	// AcceleratorSYCL is an Intel build.
	AcceleratorSYCL Accelerator = "sycl"
)

// Asset variants, spelled exactly as the official release assets spell them.
// Source: docs/PORTING_NOTES.md § Linux/IoT: DO NOT COMPILE llama.cpp, which
// lists the per-release Linux assets confirmed present for b10970.
//
// Note the deliberate asymmetry: the generic variants carry the `ubuntu-`
// prefix and the accelerator variants do not, because the asset names are
// `llama-<tag>-bin-ubuntu-arm64.tar.gz` but
// `llama-<tag>-bin-ubuntu-cuda-12.8-x64.tar.gz`. AssetFileName reassembles
// both shapes; do not "regularise" these strings.
const (
	// VariantUbuntuARM64 is the generic arm64 build (Raspberry Pi 5).
	VariantUbuntuARM64 = "ubuntu-arm64"
	// VariantUbuntuX64 is the generic x86_64 build.
	VariantUbuntuX64 = "ubuntu-x64"
	// VariantUbuntuS390X is the generic s390x build.
	VariantUbuntuS390X = "ubuntu-s390x"
	// VariantCUDA128X64 is x86_64 + CUDA 12.8.
	VariantCUDA128X64 = "cuda-12.8-x64"
	// VariantCUDA133ARM64 is arm64 + CUDA 13.3, the Jetson build.
	VariantCUDA133ARM64 = "cuda-13.3-arm64"
	// VariantROCm100X64 is x86_64 + AMD ROCm 10.0.
	VariantROCm100X64 = "rocm-10.0-x64"
	// VariantSYCLFP16X64 is x86_64 + Intel SYCL (fp16).
	VariantSYCLFP16X64 = "sycl-fp16-x64"
)

// Target is the detected hardware and the runtime build that matches it.
type Target struct {
	// GOARCH is the Go architecture name: arm64, amd64, s390x.
	GOARCH string
	// Accelerator is the compute backend the chosen variant uses.
	Accelerator Accelerator
	// AssetVariant is the official release asset variant, or "" when no
	// official build exists for this architecture. See HasOfficialBuild.
	AssetVariant string
}

// HasOfficialBuild reports whether an official prebuilt llama-server exists for
// this target. When it is false the operator must supply --llama-server-path;
// the port never compiles llama.cpp to fill the gap.
func (t Target) HasOfficialBuild() bool { return t.AssetVariant != "" }

// AssetFileName is the release asset that carries this variant at the given
// build tag, e.g. "llama-b10970-bin-ubuntu-cuda-12.8-x64.tar.gz".
func (t Target) AssetFileName(tag string) string { return AssetFileName(t.AssetVariant, tag) }

// String renders the target for logs and /api/status.
func (t Target) String() string {
	if t.AssetVariant == "" {
		return t.GOARCH + "/" + string(t.Accelerator) + " (no official build)"
	}
	return t.GOARCH + "/" + string(t.Accelerator) + " " + t.AssetVariant
}

// AssetFileName builds the release asset name for a variant and build tag.
//
// The generic variants already contain the distro segment ("ubuntu-arm64"), the
// accelerator ones do not ("cuda-12.8-x64"), so one of the two shapes needs the
// prefix inserted. Getting this wrong produces a 404 at download time, which is
// why manifest.go records the file name per variant as well and a test asserts
// the two agree.
func AssetFileName(variant, tag string) string {
	if variant == "" || tag == "" {
		return ""
	}
	if strings.HasPrefix(variant, "ubuntu-") {
		return "llama-" + tag + "-bin-" + variant + ".tar.gz"
	}
	return "llama-" + tag + "-bin-ubuntu-" + variant + ".tar.gz"
}

// GenericVariant is the CPU-only variant for an architecture, or "" when no
// official build exists. This is the sticky fallback target: on IoT a CUDA
// userspace mismatch is the norm, not an exception, so degrading to the generic
// build has to be a tested runtime path rather than a configuration option.
func GenericVariant(goarch string) string {
	switch goarch {
	case "arm64":
		return VariantUbuntuARM64
	case "amd64":
		return VariantUbuntuX64
	case "s390x":
		return VariantUbuntuS390X
	}
	return ""
}

// GenericTarget is the CPU target for an architecture.
func GenericTarget(goarch string) Target {
	return Target{GOARCH: goarch, Accelerator: AcceleratorCPU, AssetVariant: GenericVariant(goarch)}
}

// Detect inspects the running machine and returns the runtime build to use.
//
// It is a thin wrapper over DetectAt so that production code cannot accidentally
// be written against a probe that takes no root.
func Detect() Target { return DetectAt("", runtime.GOARCH) }

// DetectAt is Detect against an arbitrary filesystem root, which is what makes
// every probe testable: the whole detection is a handful of path lookups and a
// couple of small file reads, so a fake tree reproduces a Jetson or an AMD box
// exactly.
//
// Precedence, and why:
//
//  1. Jetson (arm64 + CUDA 13.3). Checked before the discrete-NVIDIA probe
//     because a Jetson is an NVIDIA machine that must NOT take the x86 CUDA
//     asset, and because the tegra markers are unambiguous.
//  2. Discrete NVIDIA on x86_64 (CUDA 12.8).
//  3. AMD on x86_64 (ROCm 10.0).
//  4. Intel on x86_64 (SYCL fp16).
//  5. Generic CPU for the architecture.
//
// Every accelerator branch is gated on the architecture as well as the device
// probe, because the official releases only publish CUDA for arm64 (Jetson) and
// CUDA/ROCm/SYCL for x86_64. A probe that fired on an unpublished combination
// would resolve to an asset that does not exist.
func DetectAt(root, goarch string) Target {
	switch goarch {
	case "arm64":
		if HasJetson(root) {
			return Target{GOARCH: goarch, Accelerator: AcceleratorCUDA, AssetVariant: VariantCUDA133ARM64}
		}
	case "amd64":
		switch {
		case HasNVIDIADiscrete(root):
			return Target{GOARCH: goarch, Accelerator: AcceleratorCUDA, AssetVariant: VariantCUDA128X64}
		case HasAMDGPU(root):
			return Target{GOARCH: goarch, Accelerator: AcceleratorROCm, AssetVariant: VariantROCm100X64}
		case HasIntelGPU(root):
			return Target{GOARCH: goarch, Accelerator: AcceleratorSYCL, AssetVariant: VariantSYCLFP16X64}
		}
	}
	return GenericTarget(goarch)
}

// HasJetson reports whether this is an NVIDIA Jetson (Orin, Xavier, Nano).
//
// Two independent markers, because L4T images vary: the release file that every
// L4T userspace ships, and the device-tree model string. A Jetson does expose
// /dev/nvidia* as well, which is precisely why this probe runs first.
func HasJetson(root string) bool {
	if fileExists(rootJoin(root, "etc", "nv_tegra_release")) {
		return true
	}
	model, err := readSmallFile(rootJoin(root, "proc", "device-tree", "model"))
	if err != nil {
		return false
	}
	// Device-tree strings are NUL-terminated.
	model = strings.TrimRight(model, "\x00")
	return strings.Contains(model, "NVIDIA Jetson")
}

// HasNVIDIADiscrete reports whether a discrete NVIDIA card with a loaded driver
// is present.
//
// The driver version file and the device node both come from a loaded kernel
// module, so either one proves the kernel side is up. nvidia-smi only proves the
// userspace tools are installed, which is weaker evidence but is what an
// operator expects to be honoured; it is checked last.
//
// None of the three proves the CUDA *userspace* matches the driver, which is the
// failure this design plans around: runtime.go probes the binary and falls back
// to the generic build when the probe fails.
func HasNVIDIADiscrete(root string) bool {
	if fileExists(rootJoin(root, "proc", "driver", "nvidia", "version")) {
		return true
	}
	if fileExists(rootJoin(root, "dev", "nvidia0")) {
		return true
	}
	_, ok := nvidiaSMIPath(root)
	return ok
}

// HasAMDGPU reports whether an AMD ROCm-capable GPU is present.
//
// Both markers are required: /dev/kfd is the compute device node and
// /sys/class/kfd/kfd is the amdkfd driver's sysfs root. A node without the
// driver tree (or the reverse) is a half-configured box, and shipping it the
// ROCm build would fail at dlopen time rather than at detection time.
func HasAMDGPU(root string) bool {
	return fileExists(rootJoin(root, "dev", "kfd")) &&
		fileExists(rootJoin(root, "sys", "class", "kfd", "kfd"))
}

// HasIntelGPU reports whether an Intel render node is present.
//
// Every DRM-capable GPU exposes /dev/dri/renderD*, so the node alone means
// nothing; the vendor id in sysfs is what distinguishes Intel (0x8086) from the
// AMD and NVIDIA render nodes that live in the same directory.
func HasIntelGPU(root string) bool {
	nodes, err := filepath.Glob(rootJoin(root, "dev", "dri", "renderD*"))
	if err != nil {
		return false
	}
	for _, node := range nodes {
		if drmVendorID(root, filepath.Base(node)) == intelPCIVendorID {
			return true
		}
	}
	return false
}

// intelPCIVendorID is Intel Corporation's PCI vendor id as sysfs renders it.
const intelPCIVendorID = "0x8086"

// drmVendorID reads the PCI vendor id backing a DRM node name such as
// "renderD128". Returns "" when the node has no sysfs entry, which is the
// normal case inside a container that bind-mounts /dev without /sys.
func drmVendorID(root, node string) string {
	v, err := readSmallFile(rootJoin(root, "sys", "class", "drm", node, "device", "vendor"))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(v))
}

// nvidiaSMIDirs are the locations nvidia-smi is installed to, in the order a
// PATH lookup would find them. Used only when probing a fake root, where
// exec.LookPath would consult the real host's PATH and make the test lie.
var nvidiaSMIDirs = []string{
	"usr/bin",
	"usr/local/bin",
	"usr/local/nvidia/bin",
	"bin",
}

// nvidiaSMIPath locates nvidia-smi. Against the real root it uses the process
// PATH; against a fake root it walks the well-known install directories, since
// a test must never be able to pass or fail because of the host's PATH.
func nvidiaSMIPath(root string) (string, bool) {
	if root == "" {
		return lookPathNVIDIASMI()
	}
	for _, dir := range nvidiaSMIDirs {
		p := rootJoin(root, dir, "nvidia-smi")
		if fileExists(p) {
			return p, true
		}
	}
	return "", false
}

// lookPathNVIDIASMI is a variable so the one probe that cannot be rooted can
// still be stubbed by a test.
var lookPathNVIDIASMI = func() (string, bool) {
	p, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return "", false
	}
	return p, true
}

// rootJoin joins a probe path under a filesystem root, where the empty root
// means the real "/" rather than a relative path. Without this an empty root
// would silently probe paths relative to the process working directory, which
// would make every probe on a real machine return false.
func rootJoin(root string, parts ...string) string {
	if root == "" {
		root = string(filepath.Separator)
	}
	return filepath.Join(append([]string{root}, parts...)...)
}

// fileExists reports whether a path exists, of any type. Device nodes,
// directories and regular files all count: the probes above care only that the
// kernel put something there.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// readSmallFile reads a bounded amount of a sysfs or procfs file. The bound
// exists because these probes run against paths an operator can point anywhere,
// and a detection routine must never be the thing that allocates a gigabyte.
func readSmallFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, maxProbeFileBytes)
	n, err := f.Read(buf)
	if n == 0 && err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

// maxProbeFileBytes bounds every probe read. The largest thing read here is a
// device-tree model string.
const maxProbeFileBytes = 4096
