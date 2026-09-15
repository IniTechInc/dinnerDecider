package llm

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

//go:embed runtime_manifest.json
var embeddedManifestJSON []byte

// VariantPin is one pinned release asset.
type VariantPin struct {
	// Asset is the release asset file name.
	Asset string `json:"asset"`
	// SHA256 is the lowercase hex digest of the asset. An EMPTY digest means
	// the variant is unpinned, and an unpinned variant is refused rather than
	// downloaded. See Manifest.Pin.
	SHA256 string `json:"sha256"`
	// SizeBytes is the asset size, used only to give an operator an honest
	// progress figure and an early "this is a 240 MB download on a metered
	// link" warning.
	SizeBytes int64 `json:"sizeBytes"`
}

// Pinned reports whether this variant carries a verifiable digest.
func (v VariantPin) Pinned() bool { return v.SHA256 != "" }

// Manifest is the pinned llama.cpp release: one build tag and a sha256 per
// asset variant.
//
// Why a committed manifest rather than an API query at runtime:
//
//   - The service downloads a binary and then EXECUTES it. Nothing about that
//     is safe if the only thing vouching for the bytes is the connection they
//     arrived over. The digest is reviewed in a pull request, on a machine that
//     is not the one being attacked.
//   - The GitHub "latest release" endpoint is actively wrong for this repo: it
//     answers with the stray tag v0.4.1 (re-verified 2026-09-15), which is a
//     shared-library soname, not a build. Code that trusted it would pin an
//     asset set that does not exist.
//   - An IoT box may have no network at all at first run, and a container
//     already ships llama-server. Both paths have to work without ever asking
//     GitHub anything.
type Manifest struct {
	// Tag is the pinned llama.cpp build tag, e.g. "b10970".
	Tag string `json:"tag"`
	// DownloadBaseURL is the release download prefix; the asset URL is
	// <base>/<tag>/<asset>.
	DownloadBaseURL string `json:"downloadBaseURL"`
	// ReleasesListURL is recorded for the operator and for `make pin-runtime`.
	// It is the LIST endpoint on purpose. Never /releases/latest.
	ReleasesListURL string `json:"releasesListURL"`
	// BinaryName is the executable to find inside the extracted tree.
	BinaryName string `json:"binaryName"`
	// Variants maps an asset variant (see hwdetect.go) to its pin.
	Variants map[string]VariantPin `json:"variants"`
}

// buildTagPattern is the shape of a real llama.cpp release tag. The releases
// list has to be filtered through this; matching anything else is how the
// v0.4.1 tag gets picked up.
var buildTagPattern = regexp.MustCompile(`^b[0-9]+$`)

// IsBuildTag reports whether a release tag is a llama.cpp build tag.
//
// Exported because `make pin-runtime` and any future tag-bump tooling must
// filter the releases list with exactly this rule, not a hand-written one.
func IsBuildTag(tag string) bool { return buildTagPattern.MatchString(tag) }

var (
	embeddedManifestOnce sync.Once
	embeddedManifest     Manifest
	embeddedManifestErr  error
)

// EmbeddedManifest returns the manifest compiled into the binary.
//
// It is parsed once and validated, so a malformed pin is a hard failure at the
// first call rather than a surprise in the middle of a 240 MB download.
func EmbeddedManifest() (Manifest, error) {
	embeddedManifestOnce.Do(func() {
		embeddedManifest, embeddedManifestErr = ParseManifest(embeddedManifestJSON)
	})
	return embeddedManifest, embeddedManifestErr
}

// ParseManifest decodes and validates a manifest document.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("runtime manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// sha256HexPattern matches a lowercase hex sha256. Uppercase is rejected rather
// than folded: the digest is compared as a string against hex.EncodeToString
// output, and a silently accepted uppercase pin would fail every verification
// with a confusing "checksum mismatch" instead of a clear parse error.
var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Validate checks the manifest is internally consistent.
func (m Manifest) Validate() error {
	if !IsBuildTag(m.Tag) {
		return fmt.Errorf("runtime manifest: tag %q is not a llama.cpp build tag (b<number>); "+
			"do not take it from /releases/latest, which answers v0.4.1 for this repo", m.Tag)
	}
	if m.DownloadBaseURL == "" {
		return fmt.Errorf("runtime manifest: downloadBaseURL is empty")
	}
	if !strings.HasPrefix(m.DownloadBaseURL, "https://") {
		return fmt.Errorf("runtime manifest: downloadBaseURL %q is not https", m.DownloadBaseURL)
	}
	if m.BinaryName == "" {
		return fmt.Errorf("runtime manifest: binaryName is empty")
	}
	if len(m.Variants) == 0 {
		return fmt.Errorf("runtime manifest: no variants pinned")
	}
	for variant, pin := range m.Variants {
		if pin.Asset == "" {
			return fmt.Errorf("runtime manifest: variant %q has no asset name", variant)
		}
		if want := AssetFileName(variant, m.Tag); pin.Asset != want {
			return fmt.Errorf("runtime manifest: variant %q pins asset %q but the tag implies %q",
				variant, pin.Asset, want)
		}
		if pin.SHA256 != "" && !sha256HexPattern.MatchString(pin.SHA256) {
			return fmt.Errorf("runtime manifest: variant %q has a malformed sha256 %q "+
				"(want 64 lowercase hex characters)", variant, pin.SHA256)
		}
		if pin.SizeBytes < 0 {
			return fmt.Errorf("runtime manifest: variant %q has a negative size", variant)
		}
	}
	return nil
}

// Pin returns the pin for a variant.
//
// A variant that is absent, or present with an empty digest, returns false: in
// both cases there is nothing to verify a download against, and the caller must
// refuse to download rather than execute unverified bytes.
func (m Manifest) Pin(variant string) (VariantPin, bool) {
	pin, ok := m.Variants[variant]
	if !ok || !pin.Pinned() {
		return pin, false
	}
	return pin, true
}

// AssetURL is the download URL for a variant, or "" when the variant is not in
// the manifest at all.
func (m Manifest) AssetURL(variant string) string {
	pin, ok := m.Variants[variant]
	if !ok {
		return ""
	}
	return strings.TrimSuffix(m.DownloadBaseURL, "/") + "/" + m.Tag + "/" + pin.Asset
}
