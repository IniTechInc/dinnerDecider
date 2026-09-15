// Package model resolves and provisions the two on-device model files the
// llama-server child needs: the quantized GGUF weights and the vision
// projector (mmproj). Either one missing means no vision-capable model, and
// the service falls back to the mock backend with a visible demo badge rather
// than pretending (shared/runtime/model-files.md, "Fallback when files are
// absent": never silently present canned data as real recognition).
//
// The selection rules are a verbatim port of
// DinnerDecider/Services/ModelFileLocator.swift. They contain three deliberate
// case asymmetries that look like sloppiness and are not; see ChooseModelFile.
package model

import (
	"os"
	"path/filepath"
	"strings"
)

// ModelPrefix is the prefix every acceptable weights file must start with,
// matched case-sensitively. It also means a fine-tuned build is auto-eligible
// as long as it is named gemma-4-E4B-it-finetuned-*.gguf.
const ModelPrefix = "gemma-4-E4B-it"

// PreferredMmprojName is the vision projector the runtime wants: the good
// post-June-4 Q8_0 build.
const PreferredMmprojName = "mmproj-gemma-4-E4B-it-Q8_0.gguf"

// SelectedModelKey is the settings key holding the user's pinned weights
// filename. Same string as the iOS UserDefaults key, and one of the ten keys
// the setting table seeds, so a pin means the same thing on both platforms.
const SelectedModelKey = "selectedModelFileName"

// DefaultModelPreference is the built-in weights preference, most-preferred
// first, used when the user has not pinned a file.
//
// This is a MEMORY ranking, not a quality ranking. The whole model is
// GPU/accelerator-resident, so a smaller weights file is directly less wired
// memory, which is what caused the on-device jetsam that this order exists to
// avoid (shared/runtime/model-files.md, "Weights preference order"):
//
//   - UD-IQ3_XXS (3.46 GiB) saves ~0.82 GiB of wired memory vs Q4_0, emitted
//     the cleanest and most compact JSON, and decoded faster than Q3_K_S on
//     Apple Metal (10.3 vs 8.8 tok/s). Preferred.
//   - Q3_K_S (3.60 GiB) saves ~0.68 GiB, also correct. Kept as the fallback.
var DefaultModelPreference = []string{
	"gemma-4-E4B-it-UD-IQ3_XXS.gguf",
	"gemma-4-E4B-it-Q3_K_S.gguf",
}

// Search locations, lowest priority last. See Locator.SearchDirectories for why
// this particular order resolves the porting question the contract left open.
const (
	// StateModelsDirName is appended to the service's state directory
	// (--state-dir / DD_STATE_DIR) to form its models directory.
	StateModelsDirName = "models"
	// DefaultStateModelsDir is the models directory under the default state
	// directory, used when no state directory was configured.
	DefaultStateModelsDir = "/var/lib/dinnerdecider/models"
	// SharedModelsDir is the read-only, package-shipped location. Last, so a
	// user-supplied file always wins over a shipped one.
	SharedModelsDir = "/usr/share/dinnerdecider/models"
)

// isEligibleWeights reports whether a filename can be used as weights.
//
// Three checks, three different casing rules, all deliberate:
//
//   - the ModelPrefix match is CASE-SENSITIVE, so "Gemma-4-E4B-it-x.gguf" is
//     NOT eligible;
//   - the ".gguf" suffix match is case-INSENSITIVE, so "...-Q4_0.GGUF" IS
//     eligible;
//   - the "mmproj" exclusion is case-INSENSITIVE, so "...-MMPROJ-...gguf" is
//     excluded.
//
// This asymmetry is pinned by ModelFileLocatorTests and restated in
// shared/runtime/model-files.md ("Selection algorithm"): "The prefix match is
// case-sensitive, while the .gguf suffix and the mmproj exclusion are
// case-insensitive. Keep that asymmetry."
//
// It is invisible until someone names a fine-tuned build "Gemma-4-...", at
// which point the file silently stops being offered. Do not tidy the three
// checks into one casing convention: doing so would change which files an
// existing install picks up, which is a worse bug than the inconsistency.
func isEligibleWeights(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(name, ModelPrefix) &&
		strings.HasSuffix(lower, ".gguf") &&
		!strings.Contains(lower, "mmproj")
}

// IsMmproj reports whether a filename is an acceptable vision projector.
// Both the prefix and the suffix are matched case-insensitively here, unlike
// the weights prefix above.
func IsMmproj(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, "mmproj") && strings.HasSuffix(lower, ".gguf")
}

// ChooseModelFile picks the weights file from a list of candidate filenames.
// The second result is false when nothing is eligible.
//
// Selection order:
//
//  1. the user's explicitly pinned file, if it is present among the eligible
//     candidates. A pin ALWAYS wins, even when it selects the memory-hungry
//     Q4_0: that is how a fine-tuned build is chosen, and second-guessing an
//     explicit choice would make the setting a lie. A pin naming a file that is
//     not present is ignored and falls through to 2 and 3.
//  2. the first entry of DefaultModelPreference that is present, so the
//     memory-optimized quant wins over a larger build when both are installed.
//  3. otherwise the first eligible candidate, in candidate-list order. That is
//     directory-enumeration order, so it is not deterministic across platforms.
//     Do not depend on it.
//
// Pass an empty preferred string for "no pin".
func ChooseModelFile(candidates []string, preferred string) (string, bool) {
	eligible := make([]string, 0, len(candidates))
	for _, name := range candidates {
		if isEligibleWeights(name) {
			eligible = append(eligible, name)
		}
	}
	if preferred != "" && contains(eligible, preferred) {
		return preferred, true
	}
	for _, quant := range DefaultModelPreference {
		if contains(eligible, quant) {
			return quant, true
		}
	}
	if len(eligible) > 0 {
		return eligible[0], true
	}
	return "", false
}

// ChooseMmprojFile picks the projector from candidate filenames, preferring the
// Q8_0 build. There is no user pin for the projector.
func ChooseMmprojFile(candidates []string) (string, bool) {
	eligible := make([]string, 0, len(candidates))
	for _, name := range candidates {
		if IsMmproj(name) {
			eligible = append(eligible, name)
		}
	}
	if contains(eligible, PreferredMmprojName) {
		return PreferredMmprojName, true
	}
	if len(eligible) > 0 {
		return eligible[0], true
	}
	return "", false
}

func contains(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// Locator resolves the weights and projector files on a Linux filesystem.
//
// The zero value searches the two system locations only. ModelsDir and StateDir
// are supplied by the caller from the resolved configuration (--models-dir /
// DD_MODELS_DIR and --state-dir / DD_STATE_DIR respectively); this package
// deliberately reads no environment itself, so the config precedence rules stay
// in one place.
type Locator struct {
	// ModelsDir is an explicit models directory (--models-dir). Empty means
	// unset, not "current directory".
	ModelsDir string
	// StateDir is the service state directory (--state-dir). Its "models"
	// subdirectory is searched.
	StateDir string
	// Preferred is the user's pinned weights filename, read from the
	// SelectedModelKey setting. Empty means no pin.
	Preferred string
}

// SearchDirectories returns the directories searched, highest priority first,
// with duplicates removed.
//
// Order:
//
//  1. --models-dir, the explicit operator override;
//  2. $DD_STATE_DIR/models, the service's own writable state, which is where a
//     download lands;
//  3. /var/lib/dinnerdecider/models, the same location under the default state
//     directory, for the common case where no state directory was configured;
//  4. /usr/share/dinnerdecider/models, the read-only location a distribution
//     package would ship.
//
// PORTING DECISION. shared/runtime/model-files.md ("Search order") records the
// iOS order as Documents then Bundle, and carries an explicit
// TODO(unresolved): "no search path is specified for a Linux or Android port
// ... a port must choose its own equivalents, keeping the 'user-supplied
// location wins over shipped location' ordering." The order above is that
// resolution. Entries 1 to 3 are the Documents analogue (writable, user- or
// operator-supplied, where a transferred or downloaded file lands) and entry 4
// is the Bundle analogue (read-only, shipped). A file the operator put there
// therefore always beats a file the package shipped, which is the invariant the
// contract asked a port to preserve.
func (l Locator) SearchDirectories() []string {
	candidates := make([]string, 0, 4)
	if l.ModelsDir != "" {
		candidates = append(candidates, l.ModelsDir)
	}
	if l.StateDir != "" {
		candidates = append(candidates, filepath.Join(l.StateDir, StateModelsDirName))
	}
	candidates = append(candidates, DefaultStateModelsDir, SharedModelsDir)

	dirs := make([]string, 0, len(candidates))
	for _, dir := range candidates {
		clean := filepath.Clean(dir)
		if !contains(dirs, clean) {
			dirs = append(dirs, clean)
		}
	}
	return dirs
}

// AvailableModelFileNames lists every weights file the user could pick from,
// across all search directories, first-seen order, deduplicated by name.
// Unreadable directories are skipped rather than reported: a missing
// /usr/share on a hand-installed box is normal, not an error.
func (l Locator) AvailableModelFileNames() []string {
	return l.scan(isEligibleWeights)
}

// AvailableMmprojFileNames lists every projector file across the search
// directories.
func (l Locator) AvailableMmprojFileNames() []string {
	return l.scan(IsMmproj)
}

func (l Locator) scan(accept func(string) bool) []string {
	var names []string
	for _, dir := range l.SearchDirectories() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if accept(name) && !contains(names, name) {
				names = append(names, name)
			}
		}
	}
	return names
}

// ModelPath resolves the weights file, honouring the pin. The second result is
// false when no eligible file exists anywhere.
func (l Locator) ModelPath() (string, bool) {
	chosen, ok := ChooseModelFile(l.AvailableModelFileNames(), l.Preferred)
	if !ok {
		return "", false
	}
	return l.locate(chosen)
}

// MmprojPath resolves the vision projector, preferring the Q8_0 build.
func (l Locator) MmprojPath() (string, bool) {
	chosen, ok := ChooseMmprojFile(l.AvailableMmprojFileNames())
	if !ok {
		return "", false
	}
	return l.locate(chosen)
}

// locate returns the path of the first search directory holding the name.
func (l Locator) locate(name string) (string, bool) {
	for _, dir := range l.SearchDirectories() {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, true
		}
	}
	return "", false
}

// IsModelPresent reports whether both files resolve, which is the single
// condition for running the real model instead of the mock.
func (l Locator) IsModelPresent() bool {
	_, weights := l.ModelPath()
	_, mmproj := l.MmprojPath()
	return weights && mmproj
}
