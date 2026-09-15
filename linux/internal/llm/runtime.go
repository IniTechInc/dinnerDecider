package llm

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Typed failures from runtime acquisition.
var (
	// ErrNoOfficialBuild means this architecture has no official prebuilt
	// llama-server. The operator must supply one with --llama-server-path; the
	// port never compiles llama.cpp to fill the gap.
	ErrNoOfficialBuild = errors.New("no official llama-server build for this architecture")
	// ErrRuntimeUnpinned means the manifest has no sha256 for the variant. The
	// download is REFUSED rather than attempted: the service execs what it
	// downloads, and unverified bytes are not worth a working feature.
	ErrRuntimeUnpinned = errors.New("runtime variant is not pinned in the manifest")
	// ErrChecksumMismatch means the download did not match the pin.
	ErrChecksumMismatch = errors.New("runtime download failed its sha256 check")
	// ErrDownloadDisabled means no cached runtime exists and downloading is off.
	ErrDownloadDisabled = errors.New("no local llama-server and downloads are disabled")
	// ErrRuntimeProbeFailed means a binary was found but does not work here.
	ErrRuntimeProbeFailed = errors.New("llama-server failed its startup probe")
	// ErrUnsafeArchive means a tar entry tried to escape the destination.
	ErrUnsafeArchive = errors.New("refusing to extract an archive entry outside the destination")
)

// RuntimeSource records how the binary was found, for /api/status and for the
// bench, which must say whether a measurement used a downloaded build or the
// operator's own.
type RuntimeSource string

const (
	// SourceFlag is --llama-server-path.
	SourceFlag RuntimeSource = "flag"
	// SourcePATH is a binary already on PATH. This is the Docker case: the
	// official ghcr.io/ggml-org/llama.cpp:server image ships llama-server, so a
	// container never downloads anything.
	SourcePATH RuntimeSource = "path"
	// SourceCache is a previously extracted download.
	SourceCache RuntimeSource = "cache"
	// SourceDownload is a fresh download from the pinned release.
	SourceDownload RuntimeSource = "download"
)

// Runtime is a resolved, probed llama-server.
type Runtime struct {
	// BinaryPath is the executable.
	BinaryPath string
	// LibraryDir is the directory holding the shared objects the binary needs.
	// The official tarballs carry libllama, libmtmd and libggml next to the
	// binary with NO rpath, so the supervisor must put this on
	// LD_LIBRARY_PATH or the child dies at exec with a loader error.
	LibraryDir string
	// Variant is the asset variant actually in use, which is NOT necessarily
	// the detected one: see FellBackToCPU.
	Variant string
	// Tag is the llama.cpp build tag, empty for a binary found by flag or PATH.
	Tag string
	// Source is how it was found.
	Source RuntimeSource
	// Version is what --version printed, for /api/status.
	Version string
	// FellBackToCPU is true when an accelerated variant was detected but the
	// generic build is in use.
	FellBackToCPU bool
	// FallbackReason explains the fallback in the operator's words.
	FallbackReason string
	// Detected is the variant hardware detection asked for.
	Detected string
}

// Degraded reports whether the operator should be told something is off.
func (r Runtime) Degraded() bool { return r.FellBackToCPU }

// StateStore persists sticky decisions across restarts.
//
// Deliberately an interface with two methods: the `runtime_state` table in the
// store package satisfies it, and so does the JSON file below, so this package
// does not have to depend on the database to record a decision it made before
// the database was necessarily up.
type StateStore interface {
	GetRuntimeState(key string) (string, bool, error)
	SetRuntimeState(key, value string) error
}

// cpuFallbackKeyPrefix namespaces the sticky fallback records by variant, so a
// box that fell back because its CUDA userspace was broken still tries the ROCm
// build if the hardware later changes.
const cpuFallbackKeyPrefix = "runtime.cpu_fallback."

// cpuFallbackRecord is what gets stored.
type cpuFallbackRecord struct {
	Variant  string `json:"variant"`
	Fallback string `json:"fallback"`
	Reason   string `json:"reason"`
	At       string `json:"at"`
}

// Resolver finds, verifies and probes a llama-server binary.
type Resolver struct {
	// Target is the detected hardware.
	Target Target
	// StateDir is $DD_STATE_DIR; the runtime cache lives at
	// <StateDir>/runtime/<tag>/<variant>/.
	StateDir string
	// ExplicitPath is --llama-server-path, and wins over everything.
	ExplicitPath string
	// Manifest is the pinned release. Zero value means EmbeddedManifest.
	Manifest Manifest
	// HTTP is the download transport.
	HTTP *http.Client
	// State records the sticky CPU fallback. Optional: without it the fallback
	// still happens, it is just re-derived on every start.
	State StateStore
	// AllowDownload gates the network entirely, for metered or air-gapped
	// installs.
	AllowDownload bool
	// LookPath resolves a binary on PATH. Defaults to exec.LookPath.
	LookPath func(string) (string, error)
	// VersionProbe runs the binary's --version. Defaults to running it for real
	// with a short timeout; injectable so the tests can simulate a CUDA
	// userspace mismatch without a CUDA box.
	VersionProbe func(ctx context.Context, binary, libraryDir string) (string, error)
	// Logf receives progress lines: a 240 MB ROCm download on a Pi's network is
	// not a silent operation.
	Logf func(string, ...any)
}

// DefaultProbeTimeout bounds `llama-server --version`. It loads no model, so
// seconds are plenty, and a hang here is itself a failed probe.
const DefaultProbeTimeout = 30 * time.Second

// Resolve produces a working llama-server or a typed error.
//
// Order, and why each step is where it is:
//
//  1. --llama-server-path. An operator who named a binary gets that binary; a
//     failure here is reported rather than silently worked around.
//  2. PATH. This is the Docker case. The official
//     ghcr.io/ggml-org/llama.cpp:server image already ships llama-server and is
//     multi-arch, so a container never downloads a runtime and never needs the
//     4 GB of network that would imply.
//  3. A cached extract under the state dir.
//  4. Download the pinned asset, VERIFY ITS SHA256 against the manifest,
//     extract, chmod +x.
//
// Every candidate is then probed before it is trusted, and a probe failure on an
// accelerated variant degrades to the generic CPU build, sticky. On IoT a CUDA
// userspace mismatch is the normal state of a box that was imaged a year ago,
// not an exceptional one, so "degrades gracefully to CPU" has to be a tested
// runtime path rather than a configuration option.
func (r *Resolver) Resolve(ctx context.Context) (Runtime, error) {
	if r.ExplicitPath != "" {
		rt, err := r.adopt(ctx, r.ExplicitPath, SourceFlag, "", "")
		if err != nil {
			// Do not fall through: the operator pointed at this binary on
			// purpose, and quietly running a different one would make every
			// later measurement a lie.
			return Runtime{}, fmt.Errorf("--llama-server-path %s: %w", r.ExplicitPath, err)
		}
		return rt, nil
	}

	manifest, err := r.manifest()
	if err != nil {
		return Runtime{}, err
	}

	if path, err := r.lookPath(manifest.BinaryName); err == nil && path != "" {
		rt, probeErr := r.adopt(ctx, path, SourcePATH, "", "")
		if probeErr == nil {
			return rt, nil
		}
		// A broken binary on PATH is worth saying out loud, then working around
		// if a download is possible at all.
		r.logf("llama-server on PATH at %s failed its probe (%v); falling through to the pinned runtime", path, probeErr)
	}

	variant := r.Target.AssetVariant
	if variant == "" {
		return Runtime{}, fmt.Errorf("%w: %s", ErrNoOfficialBuild, r.Target)
	}

	// A previous run already learned that the accelerated build does not work
	// here. Do not spend a 170 MB download and a failed probe learning it again
	// on every boot.
	if rec, ok := r.stickyFallback(variant); ok {
		r.logf("using the generic CPU runtime for %s: %s (recorded %s)", variant, rec.Reason, rec.At)
		rt, err := r.acquire(ctx, manifest, rec.Fallback)
		if err != nil {
			return Runtime{}, err
		}
		rt.FellBackToCPU = true
		rt.FallbackReason = rec.Reason
		rt.Detected = variant
		return rt, nil
	}

	rt, err := r.acquire(ctx, manifest, variant)
	if err == nil {
		rt.Detected = variant
		return rt, nil
	}

	generic := GenericVariant(r.Target.GOARCH)
	if generic == "" || generic == variant {
		return Runtime{}, err
	}
	// Only a probe failure degrades. A download that never happened (no
	// network, unpinned variant) would fail identically for the generic build,
	// and pretending otherwise turns one clear error into two.
	if !errors.Is(err, ErrRuntimeProbeFailed) {
		return Runtime{}, err
	}

	reason := describeProbeFailure(err)
	r.logf("the %s runtime does not work on this machine (%s); falling back to %s", variant, reason, generic)
	r.recordStickyFallback(variant, generic, reason)

	fallback, ferr := r.acquire(ctx, manifest, generic)
	if ferr != nil {
		// Report the ORIGINAL failure as the cause; the fallback failing on top
		// of it is the detail, not the headline.
		return Runtime{}, fmt.Errorf("%w (and the %s fallback also failed: %v)", err, generic, ferr)
	}
	fallback.FellBackToCPU = true
	fallback.FallbackReason = reason
	fallback.Detected = variant
	return fallback, nil
}

// acquire finds a working binary for one variant: cache first, then download.
func (r *Resolver) acquire(ctx context.Context, manifest Manifest, variant string) (Runtime, error) {
	dir := r.variantDir(manifest.Tag, variant)
	if binary, libDir, ok := findBinary(dir, manifest.BinaryName); ok {
		rt, err := r.adopt(ctx, binary, SourceCache, variant, manifest.Tag)
		if err == nil {
			rt.LibraryDir = libDir
			return rt, nil
		}
		return Runtime{}, err
	}

	if !r.AllowDownload {
		return Runtime{}, fmt.Errorf("%w: no cached %s runtime under %s", ErrDownloadDisabled, variant, dir)
	}
	pin, ok := manifest.Pin(variant)
	if !ok {
		url := manifest.AssetURL(variant)
		return Runtime{}, fmt.Errorf("%w: %s has no sha256 in runtime_manifest.json; "+
			"pin it (see make pin-runtime) or pass --llama-server-path. Asset: %s",
			ErrRuntimeUnpinned, variant, url)
	}
	if err := r.download(ctx, manifest, variant, pin, dir); err != nil {
		return Runtime{}, err
	}
	binary, libDir, ok := findBinary(dir, manifest.BinaryName)
	if !ok {
		return Runtime{}, fmt.Errorf("extracted %s but found no %s inside it", pin.Asset, manifest.BinaryName)
	}
	rt, err := r.adopt(ctx, binary, SourceDownload, variant, manifest.Tag)
	if err != nil {
		return Runtime{}, err
	}
	rt.LibraryDir = libDir
	return rt, nil
}

// adopt probes a candidate binary and wraps it up.
//
// PROBE BEFORE TRUSTING. `--version` is only the first half: it proves the
// dynamic loader resolved every .so, which is exactly what a CUDA userspace
// mismatch breaks (libcuda.so.1 missing, or a driver too old for the toolkit the
// build used), and it catches an illegal instruction from a build assuming CPU
// features this machine lacks. The second half - a known-answer 1-token
// generation - needs the weights and a running server, so it lives in the
// supervisor's first successful start (see Supervisor.knownAnswerProbe) and
// reports back here through MarkCPUFallback. Splitting it that way avoids
// loading 4 GB twice on a Pi just to prove the build works.
func (r *Resolver) adopt(ctx context.Context, binary string, source RuntimeSource, variant, tag string) (Runtime, error) {
	info, err := os.Stat(binary)
	if err != nil {
		return Runtime{}, err
	}
	if info.IsDir() {
		return Runtime{}, fmt.Errorf("%s is a directory", binary)
	}
	if err := os.Chmod(binary, 0o755); err != nil && !errors.Is(err, fs.ErrPermission) {
		return Runtime{}, fmt.Errorf("chmod %s: %w", binary, err)
	}
	libDir := filepath.Dir(binary)

	version, err := r.probeVersion(ctx, binary, libDir)
	if err != nil {
		return Runtime{}, fmt.Errorf("%w: %s: %v", ErrRuntimeProbeFailed, binary, err)
	}
	return Runtime{
		BinaryPath: binary,
		LibraryDir: libDir,
		Variant:    variant,
		Tag:        tag,
		Source:     source,
		Version:    version,
	}, nil
}

// probeVersion runs `llama-server --version`.
func (r *Resolver) probeVersion(ctx context.Context, binary, libraryDir string) (string, error) {
	if r.VersionProbe != nil {
		return r.VersionProbe(ctx, binary, libraryDir)
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, "--version")
	cmd.Env = appendLibraryPath(os.Environ(), libraryDir)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			return "", err
		}
		return "", fmt.Errorf("%v: %s", err, snippet([]byte(text)))
	}
	if text == "" {
		return "", fmt.Errorf("--version printed nothing")
	}
	return snippet([]byte(text)), nil
}

// LibraryPathEnv is the loader variable the extracted tarballs need.
const LibraryPathEnv = "LD_LIBRARY_PATH"

// appendLibraryPath puts dir at the FRONT of LD_LIBRARY_PATH.
//
// Front, not back: a host with its own libllama.so from a distro package would
// otherwise win, and mixing a distro libllama with a pinned llama-server is a
// silent ABI mismatch rather than an error.
func appendLibraryPath(env []string, dir string) []string {
	if dir == "" {
		return env
	}
	out := make([]string, 0, len(env)+1)
	existing := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, LibraryPathEnv+"="); ok {
			existing = v
			continue
		}
		out = append(out, kv)
	}
	value := dir
	if existing != "" {
		value = dir + string(os.PathListSeparator) + existing
	}
	return append(out, LibraryPathEnv+"="+value)
}

// describeProbeFailure turns a probe error into an operator-facing reason,
// naming the three shapes this actually takes on IoT.
func describeProbeFailure(err error) string {
	text := err.Error()
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "libcuda"):
		return "libcuda.so is missing: the NVIDIA driver userspace is not installed or does not match the toolkit this build needs"
	case strings.Contains(lower, "cannot open shared object") || strings.Contains(lower, "no such file or directory") && strings.Contains(lower, ".so"):
		return "a shared library the build needs is missing"
	case strings.Contains(lower, "illegal instruction") || strings.Contains(lower, "sigill") || strings.Contains(lower, "signal: illegal"):
		return "illegal instruction: the build assumes CPU features this machine does not have"
	case strings.Contains(lower, "cuda") || strings.Contains(lower, "hip") || strings.Contains(lower, "rocm"):
		return "the accelerator runtime rejected this build: " + snippet([]byte(text))
	default:
		return snippet([]byte(text))
	}
}

// MarkCPUFallback records a sticky degrade to the generic CPU build.
//
// Exported because the supervisor calls it: the known-answer generation probe
// runs there, and a build that passes --version but produces garbage or crashes
// on the first real token is exactly the failure a version check cannot see.
func (r *Resolver) MarkCPUFallback(variant, reason string) {
	generic := GenericVariant(r.Target.GOARCH)
	if generic == "" || generic == variant {
		return
	}
	r.recordStickyFallback(variant, generic, reason)
}

func (r *Resolver) recordStickyFallback(variant, generic, reason string) {
	if r.State == nil {
		return
	}
	rec := cpuFallbackRecord{
		Variant:  variant,
		Fallback: generic,
		Reason:   reason,
		At:       time.Now().UTC().Format(time.RFC3339),
	}
	encoded, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if err := r.State.SetRuntimeState(cpuFallbackKeyPrefix+variant, string(encoded)); err != nil {
		r.logf("could not record the CPU fallback for %s: %v", variant, err)
	}
}

func (r *Resolver) stickyFallback(variant string) (cpuFallbackRecord, bool) {
	if r.State == nil {
		return cpuFallbackRecord{}, false
	}
	raw, ok, err := r.State.GetRuntimeState(cpuFallbackKeyPrefix + variant)
	if err != nil || !ok || raw == "" {
		return cpuFallbackRecord{}, false
	}
	var rec cpuFallbackRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return cpuFallbackRecord{}, false
	}
	if rec.Fallback == "" {
		return cpuFallbackRecord{}, false
	}
	return rec, true
}

// ClearCPUFallback forgets the sticky decision, for an operator who has just
// fixed their driver install.
func (r *Resolver) ClearCPUFallback(variant string) {
	if r.State == nil {
		return
	}
	_ = r.State.SetRuntimeState(cpuFallbackKeyPrefix+variant, "")
}

// --- Download and extraction ---------------------------------------------

// download fetches, verifies and extracts one asset.
//
// The verification is not optional and not after the fact: the archive is hashed
// while it streams to a temp file, the digest is compared to the manifest BEFORE
// a single entry is extracted, and the extraction goes to a temp directory that
// is only renamed into place once it is complete. A half-extracted tree that
// looked cached on the next boot would be worse than no cache at all.
func (r *Resolver) download(ctx context.Context, manifest Manifest, variant string, pin VariantPin, dest string) error {
	url := manifest.AssetURL(variant)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	r.logf("downloading %s (%s) from %s", pin.Asset, FormatBytes(uint64(max64(pin.SizeBytes, 0))), url)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := r.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", pin.Asset, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", pin.Asset, resp.Status)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".download-*.tar.gz")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), resp.Body)
	if err != nil {
		return fmt.Errorf("download %s: %w", pin.Asset, err)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if got != pin.SHA256 {
		return fmt.Errorf("%w: %s is %s (%d bytes), manifest pins %s",
			ErrChecksumMismatch, pin.Asset, got, written, pin.SHA256)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}

	staging, err := os.MkdirTemp(filepath.Dir(dest), ".extract-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	if err := extractTarGz(tmp, staging); err != nil {
		return fmt.Errorf("extract %s: %w", pin.Asset, err)
	}
	_ = os.RemoveAll(dest)
	if err := os.Rename(staging, dest); err != nil {
		return fmt.Errorf("install %s: %w", pin.Asset, err)
	}
	r.logf("installed %s into %s", pin.Asset, dest)
	return nil
}

// extractTarGz unpacks an archive into dest.
//
// Every entry's destination is checked to be inside dest, and every symlink
// target as well. The tarballs legitimately contain ten relative symlinks
// (libggml.so -> libggml.so.0 and so on) so symlinks cannot simply be skipped,
// but a link pointing at /etc or ../.. is how an archive turns into a write
// primitive.
func extractTarGz(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := fs.FileMode(header.Mode).Perm()
			if mode&0o111 != 0 {
				// Anything the archive marked executable stays executable: the
				// binary itself, and every helper next to it.
				mode = 0o755
			} else {
				mode = 0o644
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if filepath.IsAbs(header.Linkname) {
				return fmt.Errorf("%w: symlink %s -> %s", ErrUnsafeArchive, header.Name, header.Linkname)
			}
			if _, err := safeJoin(dest, filepath.Join(filepath.Dir(header.Name), header.Linkname)); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		default:
			// Devices, fifos and hard links have no business in a release
			// tarball. Skipping them is safer than reproducing them.
			continue
		}
	}
}

// safeJoin resolves name under dest and refuses anything that escapes it.
func safeJoin(dest, name string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErrUnsafeArchive, name)
	}
	target := filepath.Join(dest, cleaned)
	if target != dest && !strings.HasPrefix(target, dest+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErrUnsafeArchive, name)
	}
	return target, nil
}

// findBinary looks for the named executable anywhere under dir, which is where
// the single top-level directory inside the tarball (llama-b10970/) is handled.
func findBinary(dir, name string) (binary, libraryDir string, ok bool) {
	direct := filepath.Join(dir, name)
	if info, err := os.Stat(direct); err == nil && !info.IsDir() {
		return direct, dir, true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		nested := filepath.Join(dir, entry.Name(), name)
		if info, err := os.Stat(nested); err == nil && !info.IsDir() {
			return nested, filepath.Join(dir, entry.Name()), true
		}
	}
	return "", "", false
}

// variantDir is the cache location for one tag and variant.
func (r *Resolver) variantDir(tag, variant string) string {
	return filepath.Join(r.StateDir, "runtime", tag, variant)
}

func (r *Resolver) manifest() (Manifest, error) {
	if r.Manifest.Tag != "" {
		return r.Manifest, r.Manifest.Validate()
	}
	return EmbeddedManifest()
}

func (r *Resolver) lookPath(name string) (string, error) {
	if r.LookPath != nil {
		return r.LookPath(name)
	}
	return exec.LookPath(name)
}

func (r *Resolver) httpClient() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

func (r *Resolver) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// --- A file-backed StateStore --------------------------------------------

// FileStateStore persists runtime decisions as JSON under the state dir.
//
// The `runtime_state` table satisfies StateStore too, and is the eventual home
// of this data. The file exists so a decision made during startup - before the
// database is necessarily open, and possibly because the database's own disk is
// the thing that is unhappy - is still remembered across a reboot.
type FileStateStore struct {
	Path string
}

// NewFileStateStore returns a store under <stateDir>/runtime/state.json.
func NewFileStateStore(stateDir string) *FileStateStore {
	return &FileStateStore{Path: filepath.Join(stateDir, "runtime", "state.json")}
}

// GetRuntimeState reads one key.
func (f *FileStateStore) GetRuntimeState(key string) (string, bool, error) {
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var all map[string]string
	if err := json.Unmarshal(data, &all); err != nil {
		return "", false, err
	}
	v, ok := all[key]
	return v, ok, nil
}

// SetRuntimeState writes one key, replacing the file atomically.
func (f *FileStateStore) SetRuntimeState(key, value string) error {
	all := map[string]string{}
	if data, err := os.ReadFile(f.Path); err == nil {
		_ = json.Unmarshal(data, &all)
	}
	if value == "" {
		delete(all, key)
	} else {
		all[key] = value
	}
	encoded, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.Path), ".state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, f.Path)
}
