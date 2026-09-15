package llm

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeRoot builds a throwaway filesystem tree. Each entry is a path relative to
// the root; a path ending in "/" becomes a directory, anything else a file with
// the given contents.
type fakeRoot map[string]string

func (f fakeRoot) build(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for rel, contents := range f {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if rel[len(rel)-1] == '/' {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", rel, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir parent of %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

// TestDetectAtTable covers every hardware class the port claims to support, and
// the two that only exist as fallbacks. The fake tree is the point: none of
// these machines is available (docs/PORTING_NOTES.md § Available test hardware
// records that no Pi, Jetson or physical Linux box exists), so the detection has
// to be provable from the filesystem shape alone.
func TestDetectAtTable(t *testing.T) {
	tests := []struct {
		name   string
		goarch string
		tree   fakeRoot
		want   Target
	}{
		{
			name:   "raspberry pi 5",
			goarch: "arm64",
			tree: fakeRoot{
				"proc/device-tree/model": "Raspberry Pi 5 Model B Rev 1.0\x00",
				"proc/meminfo":           "MemTotal:        8261548 kB\n",
			},
			want: Target{GOARCH: "arm64", Accelerator: AcceleratorCPU, AssetVariant: VariantUbuntuARM64},
		},
		{
			name:   "jetson orin via nv_tegra_release",
			goarch: "arm64",
			tree: fakeRoot{
				"etc/nv_tegra_release": "# R36 (release), REVISION: 3.0\n",
				"dev/nvidia0":          "",
			},
			want: Target{GOARCH: "arm64", Accelerator: AcceleratorCUDA, AssetVariant: VariantCUDA133ARM64},
		},
		{
			name:   "jetson orin via device-tree model only",
			goarch: "arm64",
			tree: fakeRoot{
				"proc/device-tree/model": "NVIDIA Jetson Orin Nano Developer Kit\x00",
			},
			want: Target{GOARCH: "arm64", Accelerator: AcceleratorCUDA, AssetVariant: VariantCUDA133ARM64},
		},
		{
			name:   "x86 with discrete nvidia",
			goarch: "amd64",
			tree: fakeRoot{
				"proc/driver/nvidia/version": "NVRM version: NVIDIA UNIX x86_64 Kernel Module  580.82.07\n",
				"dev/nvidia0":                "",
			},
			want: Target{GOARCH: "amd64", Accelerator: AcceleratorCUDA, AssetVariant: VariantCUDA128X64},
		},
		{
			name:   "x86 with nvidia device node only",
			goarch: "amd64",
			tree:   fakeRoot{"dev/nvidia0": ""},
			want:   Target{GOARCH: "amd64", Accelerator: AcceleratorCUDA, AssetVariant: VariantCUDA128X64},
		},
		{
			name:   "x86 with nvidia-smi only",
			goarch: "amd64",
			tree:   fakeRoot{"usr/bin/nvidia-smi": "#!/bin/sh\n"},
			want:   Target{GOARCH: "amd64", Accelerator: AcceleratorCUDA, AssetVariant: VariantCUDA128X64},
		},
		{
			name:   "x86 with amd rocm",
			goarch: "amd64",
			tree: fakeRoot{
				"dev/kfd":                                "",
				"sys/class/kfd/kfd/":                     "",
				"dev/dri/renderD128":                     "",
				"sys/class/drm/renderD128/device/vendor": "0x1002\n",
			},
			want: Target{GOARCH: "amd64", Accelerator: AcceleratorROCm, AssetVariant: VariantROCm100X64},
		},
		{
			name:   "x86 with intel igpu",
			goarch: "amd64",
			tree: fakeRoot{
				"dev/dri/renderD128":                     "",
				"sys/class/drm/renderD128/device/vendor": "0x8086\n",
			},
			want: Target{GOARCH: "amd64", Accelerator: AcceleratorSYCL, AssetVariant: VariantSYCLFP16X64},
		},
		{
			name:   "x86 with a render node of an unknown vendor",
			goarch: "amd64",
			tree: fakeRoot{
				"dev/dri/renderD128":                     "",
				"sys/class/drm/renderD128/device/vendor": "0x1af4\n",
			},
			want: Target{GOARCH: "amd64", Accelerator: AcceleratorCPU, AssetVariant: VariantUbuntuX64},
		},
		{
			name:   "bare x86",
			goarch: "amd64",
			tree:   fakeRoot{"proc/meminfo": "MemTotal:       16316056 kB\n"},
			want:   Target{GOARCH: "amd64", Accelerator: AcceleratorCPU, AssetVariant: VariantUbuntuX64},
		},
		{
			name:   "s390x",
			goarch: "s390x",
			tree:   fakeRoot{"proc/meminfo": "MemTotal:        8261548 kB\n"},
			want:   Target{GOARCH: "s390x", Accelerator: AcceleratorCPU, AssetVariant: VariantUbuntuS390X},
		},
		{
			name:   "architecture with no official build",
			goarch: "riscv64",
			tree:   fakeRoot{},
			want:   Target{GOARCH: "riscv64", Accelerator: AcceleratorCPU, AssetVariant: ""},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.tree.build(t)
			got := DetectAt(root, tc.goarch)
			if got != tc.want {
				t.Fatalf("DetectAt = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestJetsonBeatsDiscreteNVIDIA pins the precedence directly. A Jetson exposes
// /dev/nvidia0 exactly like a discrete card does; taking the x86 CUDA asset for
// it would download an amd64 tarball onto an arm64 board.
func TestJetsonBeatsDiscreteNVIDIA(t *testing.T) {
	root := fakeRoot{
		"etc/nv_tegra_release":       "# R36 (release), REVISION: 3.0\n",
		"dev/nvidia0":                "",
		"proc/driver/nvidia/version": "NVRM version: 540.0\n",
	}.build(t)

	if got := DetectAt(root, "arm64"); got.AssetVariant != VariantCUDA133ARM64 {
		t.Fatalf("variant = %q, want %q", got.AssetVariant, VariantCUDA133ARM64)
	}
	if !HasNVIDIADiscrete(root) {
		t.Fatal("the discrete probe should still fire on a Jetson tree, which is why order matters")
	}
}

// TestNVIDIADiscreteIsNotClaimedOnARM64 guards the architecture gate: there is
// no cuda-12.8-arm64 asset, so a non-Jetson arm64 box with an NVIDIA node must
// still take the generic build rather than an asset that does not exist.
func TestNVIDIADiscreteIsNotClaimedOnARM64(t *testing.T) {
	root := fakeRoot{"dev/nvidia0": ""}.build(t)
	if got := DetectAt(root, "arm64"); got.AssetVariant != VariantUbuntuARM64 {
		t.Fatalf("variant = %q, want %q", got.AssetVariant, VariantUbuntuARM64)
	}
}

// TestAMDNeedsBothMarkers documents why the AMD probe is an AND. A half
// configured box would otherwise take a ROCm build that fails at dlopen.
func TestAMDNeedsBothMarkers(t *testing.T) {
	onlyNode := fakeRoot{"dev/kfd": ""}.build(t)
	if HasAMDGPU(onlyNode) {
		t.Fatal("device node without the amdkfd sysfs tree must not count")
	}
	onlySysfs := fakeRoot{"sys/class/kfd/kfd/": ""}.build(t)
	if HasAMDGPU(onlySysfs) {
		t.Fatal("amdkfd sysfs tree without the device node must not count")
	}
}

// TestIntelProbeIgnoresNodesWithoutSysfs covers the container case: /dev is bind
// mounted, /sys is not, so the vendor id is unreadable and the probe must not
// guess Intel from the node name alone.
func TestIntelProbeIgnoresNodesWithoutSysfs(t *testing.T) {
	root := fakeRoot{"dev/dri/renderD128": ""}.build(t)
	if HasIntelGPU(root) {
		t.Fatal("a render node with no readable vendor id must not be claimed as Intel")
	}
}

// TestIntelProbeScansEveryRenderNode covers a hybrid laptop: the discrete card
// enumerates first, and the Intel iGPU is renderD129.
func TestIntelProbeScansEveryRenderNode(t *testing.T) {
	root := fakeRoot{
		"dev/dri/renderD128":                     "",
		"sys/class/drm/renderD128/device/vendor": "0x1002\n",
		"dev/dri/renderD129":                     "",
		"sys/class/drm/renderD129/device/vendor": "0x8086\n",
	}.build(t)
	if !HasIntelGPU(root) {
		t.Fatal("the second render node carries the Intel vendor id")
	}
}

// TestAssetFileName pins the naming asymmetry between the generic and the
// accelerator variants. Getting it wrong is a 404 at download time.
func TestAssetFileName(t *testing.T) {
	tests := []struct{ variant, want string }{
		{VariantUbuntuARM64, "llama-b10970-bin-ubuntu-arm64.tar.gz"},
		{VariantUbuntuX64, "llama-b10970-bin-ubuntu-x64.tar.gz"},
		{VariantUbuntuS390X, "llama-b10970-bin-ubuntu-s390x.tar.gz"},
		{VariantCUDA128X64, "llama-b10970-bin-ubuntu-cuda-12.8-x64.tar.gz"},
		{VariantCUDA133ARM64, "llama-b10970-bin-ubuntu-cuda-13.3-arm64.tar.gz"},
		{VariantROCm100X64, "llama-b10970-bin-ubuntu-rocm-10.0-x64.tar.gz"},
		{VariantSYCLFP16X64, "llama-b10970-bin-ubuntu-sycl-fp16-x64.tar.gz"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := AssetFileName(tc.variant, "b10970"); got != tc.want {
			t.Errorf("AssetFileName(%q) = %q, want %q", tc.variant, got, tc.want)
		}
	}
	if got := AssetFileName(VariantUbuntuX64, ""); got != "" {
		t.Errorf("an empty tag must not produce an asset name, got %q", got)
	}
}

// TestGenericVariantCoversEveryDetectableArch keeps the CPU fallback total: the
// sticky fallback in runtime.go has nowhere to go if this returns "" for an
// architecture Detect can produce.
func TestGenericVariantCoversEveryDetectableArch(t *testing.T) {
	for _, arch := range []string{"arm64", "amd64", "s390x"} {
		if GenericVariant(arch) == "" {
			t.Errorf("no generic variant for %s", arch)
		}
		if !GenericTarget(arch).HasOfficialBuild() {
			t.Errorf("generic target for %s claims no official build", arch)
		}
	}
	if GenericVariant("riscv64") != "" {
		t.Error("riscv64 has no official build and must not claim one")
	}
	if GenericTarget("riscv64").HasOfficialBuild() {
		t.Error("HasOfficialBuild must be false when the variant is empty")
	}
}

// TestDetectUsesRealRoot is a smoke test: Detect must not panic on the host it
// happens to run on, whatever that is.
func TestDetectUsesRealRoot(t *testing.T) {
	got := Detect()
	if got.GOARCH == "" {
		t.Fatal("Detect returned an empty GOARCH")
	}
	if got.String() == "" {
		t.Fatal("Target.String returned empty")
	}
}

// TestRootJoinTreatsEmptyRootAsFilesystemRoot is the guard for a silent-failure
// bug: joining onto "" yields a RELATIVE path, so every probe on a real machine
// would look under the process working directory and return false, and the
// service would quietly take the CPU build on a CUDA box.
func TestRootJoinTreatsEmptyRootAsFilesystemRoot(t *testing.T) {
	if got := rootJoin("", "proc", "meminfo"); got != "/proc/meminfo" {
		t.Fatalf("rootJoin(\"\") = %q, want /proc/meminfo", got)
	}
	if got := rootJoin("/fake", "dev", "kfd"); got != "/fake/dev/kfd" {
		t.Fatalf("rootJoin(\"/fake\") = %q, want /fake/dev/kfd", got)
	}
}
