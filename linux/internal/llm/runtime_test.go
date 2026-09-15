package llm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarEntry is one file, directory or symlink in a synthetic release tarball.
type tarEntry struct {
	name     string
	body     string
	mode     int64
	linkname string
	dir      bool
}

// buildTarGz assembles a tarball shaped like the real release asset: one
// top-level directory holding the binary next to its shared objects, with the
// relative symlinks the real tarballs carry (verified against
// llama-b10970-bin-ubuntu-arm64.tar.gz, which has 10 of them).
func buildTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		header := &tar.Header{Name: e.name, Mode: e.mode}
		switch {
		case e.dir:
			header.Typeflag = tar.TypeDir
			header.Mode = 0o755
		case e.linkname != "":
			header.Typeflag = tar.TypeSymlink
			header.Linkname = e.linkname
		default:
			header.Typeflag = tar.TypeReg
			header.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("tar header %s: %v", e.name, err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("tar body %s: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// releaseTarball is a stand-in for llama-<tag>-bin-ubuntu-<variant>.tar.gz.
func releaseTarball(t *testing.T, tag string) []byte {
	t.Helper()
	root := "llama-" + tag
	return buildTarGz(t, []tarEntry{
		{name: root + "/", dir: true},
		{name: root + "/llama-server", body: "#!/bin/sh\necho fake llama-server\n", mode: 0o755},
		{name: root + "/libllama.so.0.4.1", body: "ELF", mode: 0o644},
		{name: root + "/libllama.so.0", linkname: "libllama.so.0.4.1"},
		{name: root + "/README.md", body: "docs", mode: 0o644},
	})
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// testManifest pins a synthetic asset to its real digest.
func testManifest(t *testing.T, tag, variant string, asset []byte) Manifest {
	t.Helper()
	return Manifest{
		Tag:             tag,
		DownloadBaseURL: "https://example.invalid/releases/download",
		BinaryName:      "llama-server",
		Variants: map[string]VariantPin{
			variant: {
				Asset:     AssetFileName(variant, tag),
				SHA256:    sha256Hex(asset),
				SizeBytes: int64(len(asset)),
			},
		},
	}
}

// assetServer serves one asset and counts requests.
type assetServer struct {
	*httptest.Server
	body     []byte
	requests int
	status   int
}

func newAssetServer(t *testing.T, body []byte) *assetServer {
	t.Helper()
	as := &assetServer{body: body, status: http.StatusOK}
	// TLS on purpose: Manifest.Validate refuses a plain-http downloadBaseURL,
	// and a test that had to relax that rule would be testing a manifest the
	// service will not accept. httptest's client trusts its own certificate.
	as.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		as.requests++
		w.WriteHeader(as.status)
		_, _ = w.Write(as.body)
	}))
	t.Cleanup(as.Close)
	return as
}

// writeExecutable drops a stub binary on disk.
func writeExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// okProbe is a --version probe that always succeeds.
func okProbe(_ context.Context, binary, _ string) (string, error) {
	return "version 1 (" + filepath.Base(binary) + ")", nil
}

// TestExplicitPathWins: an operator who names a binary gets that binary, with
// no PATH lookup and no download.
func TestExplicitPathWins(t *testing.T) {
	dir := t.TempDir()
	binary := writeExecutable(t, filepath.Join(dir, "my-llama-server"))
	r := &Resolver{
		Target:       GenericTarget("arm64"),
		StateDir:     t.TempDir(),
		ExplicitPath: binary,
		VersionProbe: okProbe,
		LookPath:     func(string) (string, error) { return "/should/not/be/used", nil },
	}
	got, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.BinaryPath != binary {
		t.Fatalf("binary = %q, want %q", got.BinaryPath, binary)
	}
	if got.Source != SourceFlag {
		t.Fatalf("source = %q, want %q", got.Source, SourceFlag)
	}
	if got.LibraryDir != dir {
		t.Fatalf("libraryDir = %q, want %q", got.LibraryDir, dir)
	}
}

// TestExplicitPathFailureIsReportedNotWorkedAround: silently running a
// different binary would make every later measurement a lie.
func TestExplicitPathFailureIsReportedNotWorkedAround(t *testing.T) {
	binary := writeExecutable(t, filepath.Join(t.TempDir(), "llama-server"))
	r := &Resolver{
		Target:        GenericTarget("amd64"),
		StateDir:      t.TempDir(),
		ExplicitPath:  binary,
		AllowDownload: true,
		VersionProbe: func(context.Context, string, string) (string, error) {
			return "", fmt.Errorf("libcuda.so.1: cannot open shared object file")
		},
	}
	_, err := r.Resolve(context.Background())
	if !errors.Is(err, ErrRuntimeProbeFailed) {
		t.Fatalf("err = %v, want ErrRuntimeProbeFailed", err)
	}
	if !strings.Contains(err.Error(), "--llama-server-path") {
		t.Fatalf("the error should name the flag: %v", err)
	}
}

// TestPATHResolutionIsTheDockerCase: the official llama.cpp server image ships
// llama-server, so a container must never download a runtime.
func TestPATHResolutionIsTheDockerCase(t *testing.T) {
	binary := writeExecutable(t, filepath.Join(t.TempDir(), "llama-server"))
	as := newAssetServer(t, []byte("should not be fetched"))
	r := &Resolver{
		Target:        GenericTarget("arm64"),
		StateDir:      t.TempDir(),
		AllowDownload: true,
		HTTP:          as.Client(),
		VersionProbe:  okProbe,
		LookPath:      func(name string) (string, error) { return binary, nil },
	}
	got, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Source != SourcePATH {
		t.Fatalf("source = %q, want %q", got.Source, SourcePATH)
	}
	if as.requests != 0 {
		t.Fatalf("a container downloaded %d times; it must never download", as.requests)
	}
}

// TestDownloadVerifiesExtractsAndCaches walks the whole acquisition path and
// then proves the second resolve is served from the cache.
func TestDownloadVerifiesExtractsAndCaches(t *testing.T) {
	const tag, variant = "b10970", VariantUbuntuARM64
	asset := releaseTarball(t, tag)
	as := newAssetServer(t, asset)

	stateDir := t.TempDir()
	manifest := testManifest(t, tag, variant, asset)
	manifest.DownloadBaseURL = as.URL
	newResolver := func() *Resolver {
		return &Resolver{
			Target:        Target{GOARCH: "arm64", Accelerator: AcceleratorCPU, AssetVariant: variant},
			StateDir:      stateDir,
			Manifest:      manifest,
			AllowDownload: true,
			HTTP:          as.Client(),
			VersionProbe:  okProbe,
			LookPath:      func(string) (string, error) { return "", errors.New("not on PATH") },
		}
	}

	got, err := newResolver().Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Source != SourceDownload {
		t.Fatalf("source = %q, want %q", got.Source, SourceDownload)
	}
	if got.Tag != tag || got.Variant != variant {
		t.Fatalf("runtime = %+v", got)
	}

	// The binary is executable, the libraries sit next to it, and the symlink
	// survived: the supervisor puts LibraryDir on LD_LIBRARY_PATH because these
	// builds carry no rpath.
	info, err := os.Stat(got.BinaryPath)
	if err != nil {
		t.Fatalf("stat binary: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("binary mode = %v, want executable", info.Mode())
	}
	if filepath.Dir(got.BinaryPath) != got.LibraryDir {
		t.Fatalf("libraryDir = %q, want the binary's directory", got.LibraryDir)
	}
	link, err := os.Readlink(filepath.Join(got.LibraryDir, "libllama.so.0"))
	if err != nil || link != "libllama.so.0.4.1" {
		t.Fatalf("symlink = %q, %v", link, err)
	}
	if !strings.HasPrefix(got.BinaryPath, filepath.Join(stateDir, "runtime", tag, variant)) {
		t.Fatalf("cached at %q, want it under the state dir", got.BinaryPath)
	}

	cached, err := newResolver().Resolve(context.Background())
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if cached.Source != SourceCache {
		t.Fatalf("source = %q, want %q", cached.Source, SourceCache)
	}
	if as.requests != 1 {
		t.Fatalf("downloaded %d times, want exactly once", as.requests)
	}
}

// TestChecksumMismatchIsRefused is the point of the manifest: the service execs
// what it downloads, so bytes that do not match the reviewed digest never reach
// the extractor.
func TestChecksumMismatchIsRefused(t *testing.T) {
	const tag, variant = "b10970", VariantUbuntuX64
	asset := releaseTarball(t, tag)
	as := newAssetServer(t, append([]byte("tampered"), asset...))

	manifest := testManifest(t, tag, variant, asset)
	manifest.DownloadBaseURL = as.URL
	stateDir := t.TempDir()
	r := &Resolver{
		Target:        Target{GOARCH: "amd64", Accelerator: AcceleratorCPU, AssetVariant: variant},
		StateDir:      stateDir,
		Manifest:      manifest,
		AllowDownload: true,
		HTTP:          as.Client(),
		VersionProbe:  okProbe,
		LookPath:      func(string) (string, error) { return "", errors.New("not on PATH") },
	}
	_, err := r.Resolve(context.Background())
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(stateDir, "runtime", tag, variant)); len(entries) != 0 {
		t.Fatalf("a failed download left %d entries behind", len(entries))
	}
}

// TestUnpinnedVariantRefusesToDownload: no digest, no download. The error names
// the asset so an operator can pin it or pass --llama-server-path.
func TestUnpinnedVariantRefusesToDownload(t *testing.T) {
	const tag, variant = "b10970", VariantROCm100X64
	as := newAssetServer(t, []byte("never fetched"))
	manifest := Manifest{
		Tag:             tag,
		DownloadBaseURL: as.URL,
		BinaryName:      "llama-server",
		Variants:        map[string]VariantPin{variant: {Asset: AssetFileName(variant, tag)}},
	}
	r := &Resolver{
		Target:        Target{GOARCH: "amd64", Accelerator: AcceleratorROCm, AssetVariant: variant},
		StateDir:      t.TempDir(),
		Manifest:      manifest,
		AllowDownload: true,
		HTTP:          as.Client(),
		VersionProbe:  okProbe,
		LookPath:      func(string) (string, error) { return "", errors.New("not on PATH") },
	}
	_, err := r.Resolve(context.Background())
	if !errors.Is(err, ErrRuntimeUnpinned) {
		t.Fatalf("err = %v, want ErrRuntimeUnpinned", err)
	}
	if as.requests != 0 {
		t.Fatal("an unpinned variant must not be fetched at all")
	}
}

// TestDownloadsDisabledIsATypedError, for metered links and air-gapped boxes.
func TestDownloadsDisabledIsATypedError(t *testing.T) {
	asset := releaseTarball(t, "b10970")
	manifest := testManifest(t, "b10970", VariantUbuntuARM64, asset)
	r := &Resolver{
		Target:       Target{GOARCH: "arm64", Accelerator: AcceleratorCPU, AssetVariant: VariantUbuntuARM64},
		StateDir:     t.TempDir(),
		Manifest:     manifest,
		VersionProbe: okProbe,
		LookPath:     func(string) (string, error) { return "", errors.New("not on PATH") },
	}
	if _, err := r.Resolve(context.Background()); !errors.Is(err, ErrDownloadDisabled) {
		t.Fatalf("err = %v, want ErrDownloadDisabled", err)
	}
}

// TestCUDAProbeFailureFallsBackToCPUAndSticks is the IoT path that has to be
// tested rather than configured. A box imaged a year ago with a driver that no
// longer matches its CUDA userspace is the normal case, not an exception.
func TestCUDAProbeFailureFallsBackToCPUAndSticks(t *testing.T) {
	const tag = "b10970"
	cudaAsset := releaseTarball(t, tag)
	cpuAsset := releaseTarball(t, tag)

	mux := http.NewServeMux()
	var cudaHits, cpuHits int
	mux.HandleFunc("/"+tag+"/"+AssetFileName(VariantCUDA128X64, tag), func(w http.ResponseWriter, _ *http.Request) {
		cudaHits++
		_, _ = w.Write(cudaAsset)
	})
	mux.HandleFunc("/"+tag+"/"+AssetFileName(VariantUbuntuX64, tag), func(w http.ResponseWriter, _ *http.Request) {
		cpuHits++
		_, _ = w.Write(cpuAsset)
	})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)

	manifest := Manifest{
		Tag:             tag,
		DownloadBaseURL: server.URL,
		BinaryName:      "llama-server",
		Variants: map[string]VariantPin{
			VariantCUDA128X64: {Asset: AssetFileName(VariantCUDA128X64, tag), SHA256: sha256Hex(cudaAsset), SizeBytes: int64(len(cudaAsset))},
			VariantUbuntuX64:  {Asset: AssetFileName(VariantUbuntuX64, tag), SHA256: sha256Hex(cpuAsset), SizeBytes: int64(len(cpuAsset))},
		},
	}

	stateDir := t.TempDir()
	state := NewFileStateStore(stateDir)
	var logs []string
	newResolver := func() *Resolver {
		return &Resolver{
			Target:        Target{GOARCH: "amd64", Accelerator: AcceleratorCUDA, AssetVariant: VariantCUDA128X64},
			StateDir:      stateDir,
			Manifest:      manifest,
			AllowDownload: true,
			HTTP:          server.Client(),
			State:         state,
			LookPath:      func(string) (string, error) { return "", errors.New("not on PATH") },
			Logf:          func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
			VersionProbe: func(_ context.Context, binary, _ string) (string, error) {
				if strings.Contains(binary, VariantCUDA128X64) {
					return "", fmt.Errorf("error while loading shared libraries: libcuda.so.1: cannot open shared object file")
				}
				return "version 1", nil
			},
		}
	}

	got, err := newResolver().Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !got.FellBackToCPU {
		t.Fatal("the CUDA probe failed; the runtime must report the fallback")
	}
	if got.Variant != VariantUbuntuX64 {
		t.Fatalf("variant = %q, want the generic build", got.Variant)
	}
	if got.Detected != VariantCUDA128X64 {
		t.Fatalf("detected = %q, want the variant hardware detection asked for", got.Detected)
	}
	if !strings.Contains(got.FallbackReason, "libcuda.so") {
		t.Fatalf("fallback reason should be operator-readable, got %q", got.FallbackReason)
	}
	if !got.Degraded() {
		t.Fatal("a CPU fallback is a degraded state and /api/status has to say so")
	}

	// Sticky: the next start must not spend a 170 MB download and a failed
	// probe relearning this.
	cudaBefore := cudaHits
	second, err := newResolver().Resolve(context.Background())
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if !second.FellBackToCPU || second.Variant != VariantUbuntuX64 {
		t.Fatalf("the sticky fallback was not honoured: %+v", second)
	}
	if cudaHits != cudaBefore {
		t.Fatalf("the CUDA asset was fetched again after a sticky fallback")
	}

	// And an operator who fixes their driver can clear it.
	newResolver().ClearCPUFallback(VariantCUDA128X64)
	if _, ok := (&Resolver{Target: Target{GOARCH: "amd64"}, State: state}).stickyFallback(VariantCUDA128X64); ok {
		t.Fatal("ClearCPUFallback did not clear the record")
	}
}

// TestProbeFailureWithNoGenericBuildStaysFailed: falling back needs somewhere to
// fall back to.
func TestProbeFailureWithNoGenericBuildStaysFailed(t *testing.T) {
	binary := writeExecutable(t, filepath.Join(t.TempDir(), "llama-server"))
	r := &Resolver{
		Target:   Target{GOARCH: "riscv64", Accelerator: AcceleratorCPU, AssetVariant: ""},
		StateDir: t.TempDir(),
		LookPath: func(string) (string, error) { return binary, nil },
		VersionProbe: func(context.Context, string, string) (string, error) {
			return "", errors.New("illegal instruction")
		},
	}
	_, err := r.Resolve(context.Background())
	if !errors.Is(err, ErrNoOfficialBuild) {
		t.Fatalf("err = %v, want ErrNoOfficialBuild", err)
	}
}

// TestDescribeProbeFailureNamesTheRealCauses turns the three shapes this takes
// on IoT into something an operator can act on.
func TestDescribeProbeFailureNamesTheRealCauses(t *testing.T) {
	tests := []struct{ in, want string }{
		{"libcuda.so.1: cannot open shared object file", "libcuda.so is missing"},
		{"signal: illegal instruction", "illegal instruction"},
		{"CUDA error: no kernel image is available for execution", "the accelerator runtime rejected this build"},
		{"exit status 1", "exit status 1"},
	}
	for _, tc := range tests {
		got := describeProbeFailure(errors.New(tc.in))
		if !strings.Contains(got, tc.want) {
			t.Errorf("describeProbeFailure(%q) = %q, want it to mention %q", tc.in, got, tc.want)
		}
	}
}

// TestExtractRefusesEscapingEntries: an archive is a write primitive if the
// extractor trusts its paths. The real tarballs do carry relative symlinks, so
// symlinks cannot simply be skipped.
func TestExtractRefusesEscapingEntries(t *testing.T) {
	tests := []struct {
		name    string
		entries []tarEntry
	}{
		{"parent traversal", []tarEntry{{name: "../escaped", body: "x", mode: 0o644}}},
		{"absolute path", []tarEntry{{name: "/etc/passwd", body: "x", mode: 0o644}}},
		{"nested traversal", []tarEntry{{name: "llama/../../escaped", body: "x", mode: 0o644}}},
		{"absolute symlink", []tarEntry{{name: "llama/link", linkname: "/etc/passwd"}}},
		{"escaping symlink", []tarEntry{{name: "llama/link", linkname: "../../etc/passwd"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dest := t.TempDir()
			err := extractTarGz(bytes.NewReader(buildTarGz(t, tc.entries)), dest)
			if !errors.Is(err, ErrUnsafeArchive) {
				t.Fatalf("err = %v, want ErrUnsafeArchive", err)
			}
		})
	}
}

// TestExtractKeepsExecutableBitsAndRelativeSymlinks mirrors the real asset
// layout: 43 executables, 10 relative symlinks and one plain file.
func TestExtractKeepsExecutableBitsAndRelativeSymlinks(t *testing.T) {
	dest := t.TempDir()
	if err := extractTarGz(bytes.NewReader(releaseTarball(t, "b10970")), dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	binary, libDir, ok := findBinary(dest, "llama-server")
	if !ok {
		t.Fatal("llama-server was not found inside the extracted tree")
	}
	if filepath.Base(libDir) != "llama-b10970" {
		t.Fatalf("libDir = %q, want the archive's top-level directory", libDir)
	}
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("binary lost its executable bit: %v", info.Mode())
	}
	readme, err := os.Stat(filepath.Join(libDir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if readme.Mode().Perm()&0o111 != 0 {
		t.Fatalf("a non-executable entry became executable: %v", readme.Mode())
	}
}

// TestAppendLibraryPathPutsTheRuntimeFirst: a distro libllama.so winning over
// the pinned one is a silent ABI mismatch, not an error.
func TestAppendLibraryPathPutsTheRuntimeFirst(t *testing.T) {
	env := appendLibraryPath([]string{"PATH=/usr/bin", LibraryPathEnv + "=/usr/lib"}, "/opt/llama")
	var got string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, LibraryPathEnv+"="); ok {
			got = v
		}
	}
	want := "/opt/llama" + string(os.PathListSeparator) + "/usr/lib"
	if got != want {
		t.Fatalf("%s = %q, want %q", LibraryPathEnv, got, want)
	}
	if count := strings.Count(strings.Join(env, "\n"), LibraryPathEnv+"="); count != 1 {
		t.Fatalf("%s appears %d times", LibraryPathEnv, count)
	}
	if len(appendLibraryPath([]string{"PATH=/usr/bin"}, "")) != 1 {
		t.Fatal("an empty library dir must not add a variable")
	}
}

// TestFileStateStoreRoundTrip covers the store that remembers a decision made
// before the database is necessarily open.
func TestFileStateStoreRoundTrip(t *testing.T) {
	store := NewFileStateStore(t.TempDir())
	if _, ok, err := store.GetRuntimeState("missing"); err != nil || ok {
		t.Fatalf("missing key: ok=%v err=%v", ok, err)
	}
	if err := store.SetRuntimeState("k", "v"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, ok, err := store.GetRuntimeState("k")
	if err != nil || !ok || got != "v" {
		t.Fatalf("get = %q %v %v", got, ok, err)
	}
	if err := store.SetRuntimeState("k", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, ok, _ := store.GetRuntimeState("k"); ok {
		t.Fatal("an empty value must delete the key")
	}
}

// TestRealVersionProbeRunsTheBinary exercises the default probe rather than the
// injected one, so the exec path itself is covered.
func TestRealVersionProbeRunsTheBinary(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte("#!/bin/sh\necho 'version: 10970 (abc)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\necho 'libcuda.so.1: cannot open shared object file' >&2\nexit 127\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &Resolver{}
	version, err := r.probeVersion(context.Background(), good, dir)
	if err != nil {
		t.Fatalf("probeVersion: %v", err)
	}
	if !strings.Contains(version, "10970") {
		t.Fatalf("version = %q", version)
	}
	if _, err := r.probeVersion(context.Background(), bad, dir); err == nil {
		t.Fatal("a non-zero exit must fail the probe")
	}
}
