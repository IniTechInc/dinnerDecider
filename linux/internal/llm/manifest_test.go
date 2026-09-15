package llm

import (
	"strings"
	"testing"
)

// TestEmbeddedManifestParsesAndValidates is the guard that a bad pin cannot be
// committed: the manifest is embedded, so this test compiles the real file.
func TestEmbeddedManifestParsesAndValidates(t *testing.T) {
	m, err := EmbeddedManifest()
	if err != nil {
		t.Fatalf("EmbeddedManifest: %v", err)
	}
	if !IsBuildTag(m.Tag) {
		t.Fatalf("tag %q is not a build tag", m.Tag)
	}
	if m.BinaryName != "llama-server" {
		t.Fatalf("binaryName = %q, want llama-server", m.BinaryName)
	}
	if strings.Contains(m.ReleasesListURL, "latest") {
		t.Fatalf("releasesListURL %q points at the latest endpoint, which answers the stray v0.4.1 tag", m.ReleasesListURL)
	}
}

// TestEveryDetectableVariantIsPinned closes the loop between detection and
// acquisition: a variant Detect can return but the manifest does not pin is a
// box that detects its hardware correctly and then cannot run anything.
func TestEveryDetectableVariantIsPinned(t *testing.T) {
	m, err := EmbeddedManifest()
	if err != nil {
		t.Fatalf("EmbeddedManifest: %v", err)
	}
	detectable := []string{
		VariantUbuntuARM64,
		VariantUbuntuX64,
		VariantUbuntuS390X,
		VariantCUDA128X64,
		VariantCUDA133ARM64,
		VariantROCm100X64,
		VariantSYCLFP16X64,
	}
	for _, variant := range detectable {
		pin, ok := m.Pin(variant)
		if !ok {
			t.Errorf("variant %q is not pinned; an unpinned variant is refused, not downloaded", variant)
			continue
		}
		if want := AssetFileName(variant, m.Tag); pin.Asset != want {
			t.Errorf("variant %q asset = %q, want %q", variant, pin.Asset, want)
		}
		if len(pin.SHA256) != 64 {
			t.Errorf("variant %q sha256 %q is not 64 hex characters", variant, pin.SHA256)
		}
		if pin.SizeBytes <= 0 {
			t.Errorf("variant %q has no recorded size", variant)
		}
		url := m.AssetURL(variant)
		if !strings.HasPrefix(url, "https://github.com/ggml-org/llama.cpp/releases/download/"+m.Tag+"/") {
			t.Errorf("variant %q URL = %q", variant, url)
		}
		if !strings.HasSuffix(url, pin.Asset) {
			t.Errorf("variant %q URL does not end in its asset name: %q", variant, url)
		}
	}
}

// TestIsBuildTag pins the filter that keeps the v0.4.1 tag out. This is not
// hypothetical: /releases/latest answered v0.4.1 on 2026-09-15 while the newest
// real build was b10976.
func TestIsBuildTag(t *testing.T) {
	good := []string{"b10970", "b1", "b999999"}
	bad := []string{"v0.4.1", "", "b", "B10970", "b10970-rc1", "10970", "latest"}
	for _, tag := range good {
		if !IsBuildTag(tag) {
			t.Errorf("IsBuildTag(%q) = false, want true", tag)
		}
	}
	for _, tag := range bad {
		if IsBuildTag(tag) {
			t.Errorf("IsBuildTag(%q) = true, want false", tag)
		}
	}
}

// TestUnpinnedVariantIsNotUsable is the fail-closed rule: an entry with no
// digest must report itself as unusable so the resolver refuses the download
// instead of executing unverified bytes.
func TestUnpinnedVariantIsNotUsable(t *testing.T) {
	m, err := ParseManifest([]byte(`{
		"tag": "b10970",
		"downloadBaseURL": "https://github.com/ggml-org/llama.cpp/releases/download",
		"binaryName": "llama-server",
		"variants": {
			"ubuntu-arm64": {"asset": "llama-b10970-bin-ubuntu-arm64.tar.gz", "sha256": ""}
		}
	}`))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if _, ok := m.Pin(VariantUbuntuARM64); ok {
		t.Fatal("an empty digest must not count as pinned")
	}
	if _, ok := m.Pin(VariantCUDA128X64); ok {
		t.Fatal("an absent variant must not count as pinned")
	}
	// The URL is still constructible, so the error message can name it.
	if m.AssetURL(VariantUbuntuARM64) == "" {
		t.Fatal("AssetURL should still resolve for an unpinned but listed variant")
	}
	if m.AssetURL(VariantCUDA128X64) != "" {
		t.Fatal("AssetURL must be empty for a variant that is not listed at all")
	}
}

// TestValidateRejectsBadManifests covers every rule Validate enforces. Each of
// these would otherwise surface as a failed download or, worse, a verification
// that silently never happens.
func TestValidateRejectsBadManifests(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "latest endpoint tag",
			doc:  `{"tag":"v0.4.1","downloadBaseURL":"https://x/","binaryName":"llama-server","variants":{"ubuntu-x64":{"asset":"a","sha256":""}}}`,
			want: "build tag",
		},
		{
			name: "plain http base url",
			doc:  `{"tag":"b1","downloadBaseURL":"http://x/","binaryName":"llama-server","variants":{"ubuntu-x64":{"asset":"llama-b1-bin-ubuntu-x64.tar.gz"}}}`,
			want: "not https",
		},
		{
			name: "no variants",
			doc:  `{"tag":"b1","downloadBaseURL":"https://x/","binaryName":"llama-server","variants":{}}`,
			want: "no variants pinned",
		},
		{
			name: "asset name disagrees with the tag",
			doc:  `{"tag":"b2","downloadBaseURL":"https://x/","binaryName":"llama-server","variants":{"ubuntu-x64":{"asset":"llama-b1-bin-ubuntu-x64.tar.gz"}}}`,
			want: "the tag implies",
		},
		{
			name: "uppercase digest",
			doc:  `{"tag":"b1","downloadBaseURL":"https://x/","binaryName":"llama-server","variants":{"ubuntu-x64":{"asset":"llama-b1-bin-ubuntu-x64.tar.gz","sha256":"AB"}}}`,
			want: "malformed sha256",
		},
		{
			name: "no binary name",
			doc:  `{"tag":"b1","downloadBaseURL":"https://x/","binaryName":"","variants":{"ubuntu-x64":{"asset":"llama-b1-bin-ubuntu-x64.tar.gz"}}}`,
			want: "binaryName is empty",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.doc))
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
