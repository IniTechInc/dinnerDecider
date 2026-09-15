package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
)

// Ten settings keys and thirteen taste-profile keys are unrelated counts. They
// are asserted side by side precisely because conflating them is the easy
// mistake.
func TestSettingsKeyCountIsTenAndTasteKeyCountIsThirteen(t *testing.T) {
	if len(SettingKeys) != 10 {
		t.Errorf("SettingKeys has %d entries, want 10", len(SettingKeys))
	}
	if len(tasteProfileRequiredKeys) != 13 {
		t.Errorf("tasteProfileRequiredKeys has %d entries, want 13", len(tasteProfileRequiredKeys))
	}
}

// The seeded keys must be exactly the ten iOS strings, spelled iOS's way, so a
// future import is a copy and not a mapping exercise.
func TestMigrationSeedsExactlyTheTenIOSKeys(t *testing.T) {
	db := openTestDB(t)
	settings, err := db.Settings(context.Background())
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if len(settings) != 10 {
		t.Fatalf("seeded %d keys, want 10: %v", len(settings), settings)
	}
	want := []string{
		"allergies",
		"cuisineLikes",
		"debugSimulateRecipeFailure",
		"dietPreference",
		"hasCompletedTasteProfile",
		"hasSeenModelSetup",
		"hasSeenOnboarding",
		"householdSize",
		"inventorySortOrder",
		"selectedModelFileName",
	}
	var got []string
	for key := range settings {
		got = append(got, key)
	}
	sort.Strings(got)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("key[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// And SettingKeys must name the same ten.
	named := append([]string(nil), SettingKeys...)
	sort.Strings(named)
	for i := range want {
		if named[i] != want[i] {
			t.Errorf("SettingKeys[%d] = %q, want %q", i, named[i], want[i])
		}
	}
}

func TestSeededSettingDefaults(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	cases := map[string]string{
		SettingDietPreference:             "none",
		SettingAllergies:                  "",
		SettingCuisineLikes:               "",
		SettingHouseholdSize:              "2",
		SettingHasSeenModelSetup:          "false",
		SettingHasSeenOnboarding:          "false",
		SettingInventorySortOrder:         "category",
		SettingDebugSimulateRecipeFailure: "false",
		SettingHasCompletedTasteProfile:   "false",
		SettingSelectedModelFileName:      "",
	}
	for key, want := range cases {
		got, err := db.Setting(ctx, key)
		if err != nil {
			t.Fatalf("Setting(%q): %v", key, err)
		}
		if got != want {
			t.Errorf("Setting(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestSettingReadWrite(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.SetSetting(ctx, SettingDietPreference, "vegetarian"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	got, err := db.Setting(ctx, SettingDietPreference)
	if err != nil || got != "vegetarian" {
		t.Errorf("Setting = %q, %v", got, err)
	}

	if _, err := db.Setting(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Setting on a missing key = %v, want ErrNotFound", err)
	}
	fallback, err := db.SettingOr(ctx, "nope", "fallback")
	if err != nil || fallback != "fallback" {
		t.Errorf("SettingOr = %q, %v", fallback, err)
	}

	if err := db.SetSettings(ctx, map[string]string{
		SettingAllergies:    "peanuts",
		SettingCuisineLikes: "thai",
	}); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	all, _ := db.Settings(ctx)
	if all[SettingAllergies] != "peanuts" || all[SettingCuisineLikes] != "thai" {
		t.Errorf("SetSettings did not stick: %v", all)
	}
}

// A settings table that accepts any key accumulates typos that read back as the
// default forever and look exactly like a user who never changed the setting.
func TestUnknownSettingKeysAreRejected(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.SetSetting(ctx, "dietPreferences", "vegan"); !errors.Is(err, ErrUnknownSetting) {
		t.Errorf("SetSetting with a typo = %v, want ErrUnknownSetting", err)
	}
	// Validation happens before anything is written, so one bad key cannot
	// leave the good ones half-applied.
	err := db.SetSettings(ctx, map[string]string{
		SettingAllergies: "peanuts",
		"householdsize":  "4",
	})
	if !errors.Is(err, ErrUnknownSetting) {
		t.Fatalf("SetSettings = %v, want ErrUnknownSetting", err)
	}
	got, _ := db.Setting(ctx, SettingAllergies)
	if got != "" {
		t.Errorf("allergies = %q; the batch should not have been partly applied", got)
	}
}

func TestBoolSettingIsTolerant(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	got, err := db.BoolSetting(ctx, SettingHasSeenOnboarding)
	if err != nil || got {
		t.Errorf("seeded hasSeenOnboarding = %v, %v; want false", got, err)
	}
	if err := db.SetBoolSetting(ctx, SettingHasSeenOnboarding, true); err != nil {
		t.Fatalf("SetBoolSetting: %v", err)
	}
	if got, _ := db.BoolSetting(ctx, SettingHasSeenOnboarding); !got {
		t.Error("hasSeenOnboarding did not round trip")
	}
	// Garbage reads as false rather than failing a request.
	if err := db.SetSetting(ctx, SettingHasSeenOnboarding, "banana"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if got, err := db.BoolSetting(ctx, SettingHasSeenOnboarding); err != nil || got {
		t.Errorf("unparseable bool = %v, %v; want false, nil", got, err)
	}
}

// RecipePreferences.current treats 0 — what UserDefaults.integer returns for an
// unset key — as "use the default", so a user who never opened Settings gets 2.
func TestHouseholdSizeFallback(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if got, err := db.HouseholdSize(ctx); err != nil || got != 2 {
		t.Errorf("seeded householdSize = %d, %v; want 2", got, err)
	}
	for _, value := range []string{"0", "", "banana"} {
		if err := db.SetSetting(ctx, SettingHouseholdSize, value); err != nil {
			t.Fatalf("SetSetting: %v", err)
		}
		if got, err := db.HouseholdSize(ctx); err != nil || got != DefaultHouseholdSize {
			t.Errorf("HouseholdSize for %q = %d, %v; want %d", value, got, err, DefaultHouseholdSize)
		}
	}
	if err := db.SetSetting(ctx, SettingHouseholdSize, "5"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if got, _ := db.HouseholdSize(ctx); got != 5 {
		t.Errorf("HouseholdSize = %d, want 5", got)
	}
}

// ---------------------------------------------------------------------------
// The taste profile, and the two traps that silently lose it.
// ---------------------------------------------------------------------------

// THE case that gets this wrong. A zero-value struct is what `var p
// contract.TasteProfile` gives you and what a user who skipped the wizard has,
// and in Go every one of its six lists is nil — which marshals to `null`, not
// `[]`. Swift decodes those properties into non-optional [String], so a null
// fails the synthesized init exactly as a missing key does: the whole profile
// is lost, allergies included.
func TestZeroValueTasteProfileWritesEmptyArraysNotNull(t *testing.T) {
	var zero contract.TasteProfile
	blob, err := MarshalTasteProfile(zero)
	if err != nil {
		t.Fatalf("MarshalTasteProfile: %v", err)
	}
	if bytes.Contains(blob, []byte("null")) {
		t.Fatalf("a zero-value profile emitted null somewhere: %s", blob)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(blob, &raw); err != nil {
		t.Fatalf("decoding the blob: %v", err)
	}
	// All thirteen keys present. A partial blob fails Swift's decode entirely.
	if len(raw) != 13 {
		t.Errorf("blob has %d keys, want 13: %s", len(raw), blob)
	}
	for _, key := range tasteProfileRequiredKeys {
		if _, ok := raw[key]; !ok {
			t.Errorf("blob is missing required key %q: %s", key, blob)
		}
	}
	// And all six lists are [] rather than null.
	for _, key := range []string{
		"favoriteFoods", "dislikedFoods", "cuisinesLoved",
		"cuisinesAvoided", "allergies", "texturesAvoided",
	} {
		if string(raw[key]) != "[]" {
			t.Errorf("%s = %s, want []", key, raw[key])
		}
	}
}

// The same guarantee through the database, which is the path that actually
// ships: a zero-value profile saved and reloaded must come back with empty
// slices, and the stored blob must carry all thirteen keys.
func TestZeroValueTasteProfileRoundTripsThroughTheDatabase(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var zero contract.TasteProfile
	if err := db.SaveTasteProfile(ctx, zero); err != nil {
		t.Fatalf("SaveTasteProfile: %v", err)
	}

	var blob string
	if err := db.SQL().QueryRow(`SELECT profile FROM taste_profile WHERE id = 1`).Scan(&blob); err != nil {
		t.Fatalf("reading the stored blob: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		t.Fatalf("decoding the stored blob: %v", err)
	}
	if len(raw) != 13 {
		t.Errorf("stored blob has %d keys, want 13: %s", len(raw), blob)
	}
	if string(raw["allergies"]) != "[]" {
		t.Errorf("stored allergies = %s, want []", raw["allergies"])
	}

	got, ok, err := db.TasteProfile(ctx)
	if err != nil {
		t.Fatalf("TasteProfile: %v", err)
	}
	if !ok {
		t.Error("TasteProfile reported no profile after saving one")
	}
	if got.Allergies == nil || got.FavoriteFoods == nil || got.DislikedFoods == nil ||
		got.CuisinesLoved == nil || got.CuisinesAvoided == nil || got.TexturesAvoided == nil {
		t.Errorf("a list came back nil, which would be re-emitted as null: %+v", got)
	}
}

func TestTasteProfileRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	want := contract.TasteProfile{
		FavoriteFoods:        []string{"pasta", "tacos"},
		FavoriteFoodsOther:   "anything grilled",
		DislikedFoods:        []string{"olives"},
		DislikedFoodsOther:   "",
		CuisinesLoved:        []string{"thai"},
		CuisinesLovedOther:   "",
		CuisinesAvoided:      []string{},
		CuisinesAvoidedOther: "",
		Allergies:            []string{"peanuts", "shellfish"},
		AllergiesOther:       "sesame",
		TexturesAvoided:      []string{"slimy"},
		TexturesAvoidedOther: "",
		SpiceLevel:           4,
	}
	if err := db.SaveTasteProfile(ctx, want); err != nil {
		t.Fatalf("SaveTasteProfile: %v", err)
	}
	got, ok, err := db.TasteProfile(ctx)
	if err != nil || !ok {
		t.Fatalf("TasteProfile = %v, %v", ok, err)
	}
	if got.SpiceLevel != 4 || got.AllergiesOther != "sesame" {
		t.Errorf("scalars did not round trip: %+v", got)
	}
	if len(got.Allergies) != 2 || got.Allergies[0] != "peanuts" || got.Allergies[1] != "shellfish" {
		t.Errorf("allergies = %v", got.Allergies)
	}
	if len(got.FavoriteFoods) != 2 || got.FavoriteFoods[1] != "tacos" {
		t.Errorf("favoriteFoods = %v", got.FavoriteFoods)
	}

	// Saving again overwrites the single row rather than adding another.
	if err := db.SaveTasteProfile(ctx, contract.TasteProfile{SpiceLevel: 1}); err != nil {
		t.Fatalf("second SaveTasteProfile: %v", err)
	}
	var rows int
	if err := db.SQL().QueryRow(`SELECT count(*) FROM taste_profile`).Scan(&rows); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if rows != 1 {
		t.Errorf("taste_profile rows = %d, want 1", rows)
	}
	got, _, _ = db.TasteProfile(ctx)
	if got.SpiceLevel != 1 || len(got.Allergies) != 0 {
		t.Errorf("overwrite = %+v", got)
	}
}

// "No profile yet" and "an empty profile" are different answers: only the first
// should be offered the wizard again.
func TestTasteProfileAbsenceIsDistinguishable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	got, ok, err := db.TasteProfile(ctx)
	if err != nil {
		t.Fatalf("TasteProfile: %v", err)
	}
	if ok {
		t.Error("reported a profile before one was ever saved")
	}
	if got.SpiceLevel != DefaultSpiceLevel {
		t.Errorf("default spiceLevel = %d, want %d", got.SpiceLevel, DefaultSpiceLevel)
	}
	if got.Allergies == nil {
		t.Error("the absent-profile default has a nil list, which marshals to null")
	}

	if err := db.SaveTasteProfile(ctx, contract.TasteProfile{SpiceLevel: 2}); err != nil {
		t.Fatalf("SaveTasteProfile: %v", err)
	}
	if _, ok, _ := db.TasteProfile(ctx); !ok {
		t.Error("reported no profile after saving one")
	}
	if err := db.DeleteTasteProfile(ctx); err != nil {
		t.Fatalf("DeleteTasteProfile: %v", err)
	}
	if _, ok, _ := db.TasteProfile(ctx); ok {
		t.Error("reported a profile after deleting it")
	}
	// Deleting nothing is a no-op, not an error.
	if err := db.DeleteTasteProfile(ctx); err != nil {
		t.Errorf("second DeleteTasteProfile: %v", err)
	}
}

// Reading is tolerant where writing is complete: being more forgiving than iOS
// keeps the profile, being less forgiving loses it.
func TestUnmarshalTasteProfileToleratesMissingAndNullKeys(t *testing.T) {
	// A blob written by some older or partial writer: three keys, one of them
	// an explicit null where an array belongs.
	partial := []byte(`{"allergies": ["peanuts"], "favoriteFoods": null, "spiceLevel": 5}`)
	got, err := UnmarshalTasteProfile(partial)
	if err != nil {
		t.Fatalf("UnmarshalTasteProfile: %v", err)
	}
	if len(got.Allergies) != 1 || got.Allergies[0] != "peanuts" {
		t.Errorf("allergies = %v; the one thing that must survive did not", got.Allergies)
	}
	if got.SpiceLevel != 5 {
		t.Errorf("spiceLevel = %d, want 5", got.SpiceLevel)
	}
	// Missing keys keep their defaults; the explicit null becomes [].
	if got.FavoriteFoods == nil {
		t.Error("an explicit null list decoded to nil; re-emitting it would produce null again")
	}
	if got.DislikedFoods == nil || got.CuisinesLoved == nil || got.CuisinesAvoided == nil ||
		got.TexturesAvoided == nil {
		t.Errorf("a missing list decoded to nil: %+v", got)
	}
	if got.FavoriteFoodsOther != "" {
		t.Errorf("favoriteFoodsOther = %q, want the empty default", got.FavoriteFoodsOther)
	}

	// An empty object still yields a usable, writable profile.
	empty, err := UnmarshalTasteProfile([]byte(`{}`))
	if err != nil {
		t.Fatalf("UnmarshalTasteProfile({}): %v", err)
	}
	if empty.SpiceLevel != DefaultSpiceLevel {
		t.Errorf("spiceLevel = %d, want the %d default", empty.SpiceLevel, DefaultSpiceLevel)
	}
	blob, err := MarshalTasteProfile(empty)
	if err != nil {
		t.Fatalf("MarshalTasteProfile: %v", err)
	}
	if bytes.Contains(blob, []byte("null")) {
		t.Errorf("re-encoding a tolerantly-read profile produced null: %s", blob)
	}

	if _, err := UnmarshalTasteProfile([]byte(`not json`)); err == nil {
		t.Error("UnmarshalTasteProfile accepted garbage")
	}
}

// Whatever comes out of a read must be safe to write straight back. This is the
// round trip a settings screen performs on every save.
func TestTasteProfileReadWriteReadIsStable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Store a deliberately partial blob behind the API's back, as an importer
	// or an older writer would.
	if _, err := db.SQL().Exec(
		`INSERT INTO taste_profile (id, profile, updatedAt) VALUES (1, ?, ?)`,
		`{"allergies":["peanuts"]}`, formatTime(nowForTest())); err != nil {
		t.Fatalf("seeding a partial blob: %v", err)
	}

	got, ok, err := db.TasteProfile(ctx)
	if err != nil || !ok {
		t.Fatalf("TasteProfile = %v, %v", ok, err)
	}
	if err := db.SaveTasteProfile(ctx, got); err != nil {
		t.Fatalf("SaveTasteProfile: %v", err)
	}

	var blob string
	if err := db.SQL().QueryRow(`SELECT profile FROM taste_profile WHERE id = 1`).Scan(&blob); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(raw) != 13 {
		t.Errorf("rewritten blob has %d keys, want 13: %s", len(raw), blob)
	}
	if string(raw["allergies"]) != `["peanuts"]` {
		t.Errorf("allergies = %s, want [\"peanuts\"]", raw["allergies"])
	}
}

// nowForTest keeps the seeded-blob test from importing time just for one call.
func nowForTest() time.Time { return time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC) }
