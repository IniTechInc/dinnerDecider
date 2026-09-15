package model

import (
	"os"
	"path/filepath"
	"testing"
)

// Unit tests for model file selection.
//
// A 1:1 port of DinnerDeciderTests/ModelFileLocatorTests.swift - all 13 tests,
// with the Swift names preserved (only the leading `test` capitalized, as Go
// requires) so a failure maps straight onto the iOS specification and onto
// shared/runtime/model-files.md § Selection algorithm.
//
// Plus Go-only tests for the three case asymmetries (invisible on iOS until a
// fine-tuned build is named "Gemma-4-...") and for the Linux search order,
// which the contract left as a TODO(unresolved) for ports to resolve.

var candidates = []string{
	"gemma-4-E4B-it-Q4_K_M.gguf",
	"gemma-4-E4B-it-finetuned-Q4_K_M.gguf",
	"mmproj-F16.gguf",
	"some-other-model.gguf",
	"README.txt",
}

func assertChosen(t *testing.T, gotName string, gotOK bool, want string) {
	t.Helper()
	if !gotOK {
		t.Fatalf("expected a choice of %q, got none", want)
	}
	if gotName != want {
		t.Errorf("chose %q, want %q", gotName, want)
	}
}

func assertNoChoice(t *testing.T, gotName string, gotOK bool) {
	t.Helper()
	if gotOK {
		t.Fatalf("expected no choice, got %q", gotName)
	}
	if gotName != "" {
		t.Errorf("expected an empty name with ok=false, got %q", gotName)
	}
}

func TestPrefersSelectedFileWhenPresent(t *testing.T) {
	name, ok := ChooseModelFile(candidates, "gemma-4-E4B-it-finetuned-Q4_K_M.gguf")
	assertChosen(t, name, ok, "gemma-4-E4B-it-finetuned-Q4_K_M.gguf")
}

func TestFallsBackToFirstMatchWhenPreferredMissing(t *testing.T) {
	// A pin naming a file that is not installed is ignored, not an error: the
	// pin survives app updates and reinstalls, so it routinely outlives the
	// file it names.
	name, ok := ChooseModelFile(candidates, "gemma-4-E4B-it-does-not-exist.gguf")
	assertChosen(t, name, ok, "gemma-4-E4B-it-Q4_K_M.gguf")
}

func TestFallsBackWhenNoPreferenceSet(t *testing.T) {
	name, ok := ChooseModelFile(candidates, "")
	assertChosen(t, name, ok, "gemma-4-E4B-it-Q4_K_M.gguf")
}

func TestExcludesMmprojAndNonMatchingFiles(t *testing.T) {
	name, ok := ChooseModelFile([]string{"mmproj-F16.gguf", "some-other-model.gguf", "README.txt"}, "")
	assertNoChoice(t, name, ok)
}

func TestReturnsNilForEmptyCandidates(t *testing.T) {
	name, ok := ChooseModelFile(nil, "anything.gguf")
	assertNoChoice(t, name, ok)
}

// MARK: - Real model filename matching

func TestChoosesRealQ4WeightsFile(t *testing.T) {
	name, ok := ChooseModelFile([]string{"gemma-4-E4B-it-Q4_0.gguf", "mmproj-gemma-4-E4B-it-Q8_0.gguf"}, "")
	assertChosen(t, name, ok, "gemma-4-E4B-it-Q4_0.gguf")
}

// MARK: - Memory-optimized default preference

func TestPrefersSmallerQuantOverQ4WhenBothPresent(t *testing.T) {
	// No pin: the memory-optimized Q3_K_S should win over the larger Q4_0 to
	// keep wired memory down on a device that is already at the floor.
	name, ok := ChooseModelFile([]string{
		"gemma-4-E4B-it-Q4_0.gguf",
		"gemma-4-E4B-it-Q3_K_S.gguf",
		"mmproj-gemma-4-E4B-it-Q8_0.gguf",
	}, "")
	assertChosen(t, name, ok, "gemma-4-E4B-it-Q3_K_S.gguf")
}

func TestUserPinnedFileStillWinsOverDefaultPreference(t *testing.T) {
	// An explicit user selection always overrides the built-in preference, even
	// though this particular pin selects the memory hog. Settings > Model is how
	// a fine-tuned build gets chosen; a locator that overrode the pin "for the
	// user's own good" would make the setting inert.
	name, ok := ChooseModelFile([]string{
		"gemma-4-E4B-it-Q4_0.gguf",
		"gemma-4-E4B-it-Q3_K_S.gguf",
	}, "gemma-4-E4B-it-Q4_0.gguf")
	assertChosen(t, name, ok, "gemma-4-E4B-it-Q4_0.gguf")
}

func TestPrefersIQ3XXSOverQ3AndQ4WhenAllPresent(t *testing.T) {
	// Full preference order: the most memory-efficient quant wins when nothing
	// is pinned.
	name, ok := ChooseModelFile([]string{
		"gemma-4-E4B-it-Q4_0.gguf",
		"gemma-4-E4B-it-Q3_K_S.gguf",
		"gemma-4-E4B-it-UD-IQ3_XXS.gguf",
	}, "")
	assertChosen(t, name, ok, "gemma-4-E4B-it-UD-IQ3_XXS.gguf")
}

func TestFallsBackToFirstEligibleWhenNoPreferredQuantPresent(t *testing.T) {
	name, ok := ChooseModelFile([]string{"gemma-4-E4B-it-Q4_K_M.gguf", "gemma-4-E4B-it-Q4_0.gguf"}, "")
	assertChosen(t, name, ok, "gemma-4-E4B-it-Q4_K_M.gguf")
}

// MARK: - Projector selection

func TestPrefersQ8ProjectorWhenPresent(t *testing.T) {
	name, ok := ChooseMmprojFile([]string{
		"mmproj-gemma-4-E4B-it-BF16.gguf",
		"mmproj-gemma-4-E4B-it-Q8_0.gguf",
	})
	assertChosen(t, name, ok, "mmproj-gemma-4-E4B-it-Q8_0.gguf")
}

func TestFallsBackToAnyProjectorWhenPreferredMissing(t *testing.T) {
	name, ok := ChooseMmprojFile([]string{"mmproj-F16.gguf", "gemma-4-E4B-it-Q4_0.gguf"})
	assertChosen(t, name, ok, "mmproj-F16.gguf")
}

func TestReturnsNilWhenNoProjectorPresent(t *testing.T) {
	name, ok := ChooseMmprojFile([]string{"gemma-4-E4B-it-Q4_0.gguf", "README.txt"})
	assertNoChoice(t, name, ok)
}

// MARK: - Go-only: the case-sensitivity asymmetry

// TestWeightsMatchingCaseAsymmetry pins all three casing rules at once, because
// they only ever go wrong together: someone "tidies" them into a single
// convention and either a fine-tuned build stops being offered or the projector
// gets loaded as weights.
//
// Prefix: case-SENSITIVE. Suffix: case-INSENSITIVE. mmproj exclusion:
// case-INSENSITIVE. See isEligibleWeights and shared/runtime/model-files.md
// § Selection algorithm ("Keep that asymmetry").
func TestWeightsMatchingCaseAsymmetry(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		want    bool
		because string
	}{
		{"exact prefix", "gemma-4-E4B-it-Q4_0.gguf", true, "the shipped spelling"},
		{"capitalized prefix", "Gemma-4-E4B-it-Q4_0.gguf", false, "the prefix match is case-sensitive"},
		{"lowercased prefix", "gemma-4-e4b-it-Q4_0.gguf", false, "the prefix match is case-sensitive"},
		{"uppercase suffix", "gemma-4-E4B-it-Q4_0.GGUF", true, "the suffix match is case-insensitive"},
		{"mixed-case suffix", "gemma-4-E4B-it-Q4_0.GgUf", true, "the suffix match is case-insensitive"},
		{"uppercase mmproj", "gemma-4-E4B-it-MMPROJ-Q8_0.gguf", false, "the mmproj exclusion is case-insensitive"},
		{"mixed-case mmproj", "gemma-4-E4B-it-MmProj-Q8_0.gguf", false, "the mmproj exclusion is case-insensitive"},
		{"lowercase mmproj", "gemma-4-E4B-it-mmproj-Q8_0.gguf", false, "the mmproj exclusion is case-insensitive"},
		{"fine-tuned build", "gemma-4-E4B-it-finetuned-Q4_K_M.gguf", true, "fine-tunes are auto-eligible by prefix"},
		{"capitalized fine-tune", "Gemma-4-E4B-it-finetuned-Q4_K_M.gguf", false, "this is the file the asymmetry loses"},
		{"no extension", "gemma-4-E4B-it-Q4_0", false, "must end in .gguf"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isEligibleWeights(tc.file); got != tc.want {
				t.Errorf("isEligibleWeights(%q) = %v, want %v, because %s", tc.file, got, tc.want, tc.because)
			}
			// And through the public entry point, so the asymmetry cannot be
			// bypassed by a caller.
			name, ok := ChooseModelFile([]string{tc.file}, "")
			if ok != tc.want {
				t.Errorf("ChooseModelFile(%q) ok = %v, want %v, because %s", tc.file, ok, tc.want, tc.because)
			}
			if tc.want && name != tc.file {
				t.Errorf("ChooseModelFile(%q) = %q", tc.file, name)
			}
		})
	}
}

// TestMmprojMatchingIsFullyCaseInsensitive is the other half of the asymmetry:
// unlike the weights prefix, the projector prefix does NOT care about case.
func TestMmprojMatchingIsFullyCaseInsensitive(t *testing.T) {
	cases := []struct {
		file string
		want bool
	}{
		{"mmproj-F16.gguf", true},
		{"MMPROJ-F16.gguf", true},
		{"MmProj-gemma-4-E4B-it-Q8_0.GGUF", true},
		{"gemma-4-E4B-it-Q4_0.gguf", false},
		{"projector-mmproj.gguf", false},
		{"mmproj-F16.bin", false},
	}
	for _, tc := range cases {
		if got := IsMmproj(tc.file); got != tc.want {
			t.Errorf("IsMmproj(%q) = %v, want %v", tc.file, got, tc.want)
		}
	}
}

// TestPinnedCapitalizedFileIsNotHonoured records the consequence of the
// asymmetry for the pin path specifically: a pin is only honoured when the file
// it names is ELIGIBLE, so pinning "Gemma-4-..." silently falls through to the
// preference order rather than loading the pinned file.
func TestPinnedCapitalizedFileIsNotHonoured(t *testing.T) {
	name, ok := ChooseModelFile([]string{
		"Gemma-4-E4B-it-finetuned-Q4_K_M.gguf",
		"gemma-4-E4B-it-Q3_K_S.gguf",
	}, "Gemma-4-E4B-it-finetuned-Q4_K_M.gguf")
	assertChosen(t, name, ok, "gemma-4-E4B-it-Q3_K_S.gguf")
}

// MARK: - Go-only: the Linux search order

func TestSearchDirectoriesOrder(t *testing.T) {
	got := Locator{ModelsDir: "/srv/models", StateDir: "/data/dd"}.SearchDirectories()
	want := []string{"/srv/models", "/data/dd/models", DefaultStateModelsDir, SharedModelsDir}
	assertDirs(t, got, want)
}

func TestSearchDirectoriesWithoutOverrides(t *testing.T) {
	got := Locator{}.SearchDirectories()
	assertDirs(t, got, []string{DefaultStateModelsDir, SharedModelsDir})
}

// A state directory that happens to be the default must not be searched twice.
func TestSearchDirectoriesDeduplicates(t *testing.T) {
	got := Locator{ModelsDir: SharedModelsDir, StateDir: "/var/lib/dinnerdecider/"}.SearchDirectories()
	assertDirs(t, got, []string{SharedModelsDir, DefaultStateModelsDir})
}

func assertDirs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("search directories: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("search directories: got %v, want %v", got, want)
		}
	}
}

// TestUserSuppliedLocationWinsOverSharedLocation is the invariant the contract
// asked ports to preserve: a file the operator dropped in beats the same
// filename from the shipped location.
func TestUserSuppliedLocationWinsOverSharedLocation(t *testing.T) {
	modelsDir := t.TempDir()
	stateDir := t.TempDir()
	writeFile(t, filepath.Join(modelsDir, "gemma-4-E4B-it-Q3_K_S.gguf"), "operator copy")
	writeFile(t, filepath.Join(stateDir, "models", "gemma-4-E4B-it-Q3_K_S.gguf"), "downloaded copy")

	locator := Locator{ModelsDir: modelsDir, StateDir: stateDir}
	path, ok := locator.ModelPath()
	if !ok {
		t.Fatal("expected to resolve weights")
	}
	if want := filepath.Join(modelsDir, "gemma-4-E4B-it-Q3_K_S.gguf"); path != want {
		t.Errorf("resolved %q, want %q", path, want)
	}
}

func TestResolvesBothFilesAcrossDirectories(t *testing.T) {
	modelsDir := t.TempDir()
	stateDir := t.TempDir()
	writeFile(t, filepath.Join(modelsDir, "gemma-4-E4B-it-UD-IQ3_XXS.gguf"), "weights")
	writeFile(t, filepath.Join(stateDir, "models", PreferredMmprojName), "projector")

	locator := Locator{ModelsDir: modelsDir, StateDir: stateDir}
	if !locator.IsModelPresent() {
		t.Fatal("both files exist, so the model must count as present")
	}
	mmproj, ok := locator.MmprojPath()
	if !ok || mmproj != filepath.Join(stateDir, "models", PreferredMmprojName) {
		t.Errorf("projector resolved to %q (ok=%v)", mmproj, ok)
	}
}

// Weights without a projector is not a usable model, which is what sends the
// service to the mock backend with a visible badge.
func TestWeightsWithoutProjectorIsNotPresent(t *testing.T) {
	modelsDir := t.TempDir()
	writeFile(t, filepath.Join(modelsDir, "gemma-4-E4B-it-UD-IQ3_XXS.gguf"), "weights")

	locator := Locator{ModelsDir: modelsDir}
	if locator.IsModelPresent() {
		t.Error("a projector-less install must not report the model as present")
	}
}

func TestMissingDirectoriesAreSkippedNotFatal(t *testing.T) {
	locator := Locator{ModelsDir: filepath.Join(t.TempDir(), "absent")}
	if names := locator.AvailableModelFileNames(); len(names) != 0 {
		t.Errorf("expected no names, got %v", names)
	}
	if locator.IsModelPresent() {
		t.Error("nothing is installed, so the model must not be present")
	}
}

// A pin on disk behaves like the pure-logic pin: honoured when present, ignored
// when it names a file that is not installed.
func TestPinnedFileOnDiskWinsAndAbsentPinFallsThrough(t *testing.T) {
	modelsDir := t.TempDir()
	writeFile(t, filepath.Join(modelsDir, "gemma-4-E4B-it-UD-IQ3_XXS.gguf"), "preferred quant")
	writeFile(t, filepath.Join(modelsDir, "gemma-4-E4B-it-Q4_0.gguf"), "memory hog")

	pinned := Locator{ModelsDir: modelsDir, Preferred: "gemma-4-E4B-it-Q4_0.gguf"}
	path, ok := pinned.ModelPath()
	if !ok || filepath.Base(path) != "gemma-4-E4B-it-Q4_0.gguf" {
		t.Errorf("pinned resolution gave %q (ok=%v)", path, ok)
	}

	stale := Locator{ModelsDir: modelsDir, Preferred: "gemma-4-E4B-it-finetuned-v9.gguf"}
	path, ok = stale.ModelPath()
	if !ok || filepath.Base(path) != "gemma-4-E4B-it-UD-IQ3_XXS.gguf" {
		t.Errorf("stale pin resolution gave %q (ok=%v)", path, ok)
	}
}

// Directories never masquerade as model files, even when named like one.
func TestDirectoriesAreNotCandidates(t *testing.T) {
	modelsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(modelsDir, "gemma-4-E4B-it-Q4_0.gguf"), 0o755); err != nil {
		t.Fatal(err)
	}
	locator := Locator{ModelsDir: modelsDir}
	if names := locator.AvailableModelFileNames(); len(names) != 0 {
		t.Errorf("expected no names, got %v", names)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
