package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
)

// The ten settings keys, spelled exactly as iOS spells them.
//
// Nine come from `enum PrefKey` in DinnerDecider/Services/Preferences.swift and
// the tenth from ModelFileLocator.selectedModelKey. They are reused verbatim,
// not renamed to something tidier, so importing an iOS backup one day is a
// mechanical copy rather than a mapping exercise nobody can verify.
//
// `PrefKey.defaultHouseholdSize = 2` is a default VALUE, not an eleventh key.
//
// This count has nothing to do with the taste profile's THIRTEEN required JSON
// keys. Ten settings rows, thirteen keys inside one blob; unrelated things that
// get confused only because both answer to "the settings".
const (
	SettingDietPreference             = "dietPreference"
	SettingAllergies                  = "allergies"
	SettingCuisineLikes               = "cuisineLikes"
	SettingHouseholdSize              = "householdSize"
	SettingHasSeenModelSetup          = "hasSeenModelSetup"
	SettingHasSeenOnboarding          = "hasSeenOnboarding"
	SettingInventorySortOrder         = "inventorySortOrder"
	SettingDebugSimulateRecipeFailure = "debugSimulateRecipeFailure"
	SettingHasCompletedTasteProfile   = "hasCompletedTasteProfile"
	SettingSelectedModelFileName      = "selectedModelFileName"
)

// SettingKeys is the closed set of ten, in the order 0001_init.sql seeds them.
var SettingKeys = []string{
	SettingDietPreference,
	SettingAllergies,
	SettingCuisineLikes,
	SettingHouseholdSize,
	SettingHasSeenModelSetup,
	SettingHasSeenOnboarding,
	SettingInventorySortOrder,
	SettingDebugSimulateRecipeFailure,
	SettingHasCompletedTasteProfile,
	SettingSelectedModelFileName,
}

var settingKeySet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(SettingKeys))
	for _, k := range SettingKeys {
		set[k] = struct{}{}
	}
	return set
}()

// DefaultHouseholdSize is PrefKey.defaultHouseholdSize: the number of people to
// cook for when the user has not set one.
const DefaultHouseholdSize = 2

// ErrUnknownSetting rejects a write to a key outside the closed set.
//
// Deliberately strict. A settings table that accepts any key accumulates
// typos — "dietPreferences", "householdsize" — that read back as the default
// forever and look exactly like a user who never changed the setting. Anything
// that genuinely is not a user preference belongs in runtime_state.
var ErrUnknownSetting = errors.New("store: unknown setting key")

// Settings returns every stored setting. Never nil.
func (d *DB) Settings(ctx context.Context) (map[string]string, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT key, value FROM setting`)
	if err != nil {
		return nil, fmt.Errorf("store: reading settings: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("store: reading settings: %w", err)
		}
		out[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading settings: %w", err)
	}
	return out, nil
}

// Setting returns one value, or ErrNotFound when the key has no row.
func (d *DB) Setting(ctx context.Context, key string) (string, error) {
	var value string
	err := d.sql.QueryRowContext(ctx, `SELECT value FROM setting WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("store: setting %q: %w", key, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("store: reading setting %q: %w", key, err)
	}
	return value, nil
}

// SettingOr returns one value, falling back when the key has no row.
func (d *DB) SettingOr(ctx context.Context, key, fallback string) (string, error) {
	value, err := d.Setting(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return fallback, nil
	}
	if err != nil {
		return "", err
	}
	return value, nil
}

// SetSetting writes one value, rejecting a key outside the closed set.
func (d *DB) SetSetting(ctx context.Context, key, value string) error {
	if _, ok := settingKeySet[key]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownSetting, key)
	}
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO setting (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("store: writing setting %q: %w", key, err)
	}
	return nil
}

// SetSettings writes several values in one transaction. Every key is validated
// BEFORE anything is written, so a typo in one key cannot leave the others
// half-applied.
func (d *DB) SetSettings(ctx context.Context, values map[string]string) error {
	for key := range values {
		if _, ok := settingKeySet[key]; !ok {
			return fmt.Errorf("%w: %q", ErrUnknownSetting, key)
		}
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		for key, value := range values {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO setting (key, value) VALUES (?, ?)
				 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
				return fmt.Errorf("store: writing setting %q: %w", key, err)
			}
		}
		return nil
	})
}

// BoolSetting reads a boolean setting.
//
// Tolerant like UserDefaults.bool(forKey:): a missing row, an empty value or
// anything unparseable is false. A settings read is not a place to fail a
// request.
func (d *DB) BoolSetting(ctx context.Context, key string) (bool, error) {
	value, err := d.SettingOr(ctx, key, "")
	if err != nil {
		return false, err
	}
	parsed, parseErr := strconv.ParseBool(value)
	if parseErr != nil {
		return false, nil
	}
	return parsed, nil
}

// SetBoolSetting writes a boolean as "true"/"false".
func (d *DB) SetBoolSetting(ctx context.Context, key string, value bool) error {
	return d.SetSetting(ctx, key, strconv.FormatBool(value))
}

// HouseholdSize reads the household size with the shipped fallback.
//
// RecipePreferences.current treats a size of 0 — which is what
// UserDefaults.integer returns for an unset key — as "use the default", so a
// user who never opened Settings gets 2 rather than a prompt that asks the
// model to cook for nobody. Unparseable values take the same branch.
func (d *DB) HouseholdSize(ctx context.Context) (int, error) {
	value, err := d.SettingOr(ctx, SettingHouseholdSize, "")
	if err != nil {
		return 0, err
	}
	size, parseErr := strconv.Atoi(value)
	if parseErr != nil || size == 0 {
		return DefaultHouseholdSize, nil
	}
	return size, nil
}

// ---------------------------------------------------------------------------
// Taste profile: one row, one JSON blob, thirteen keys, always.
// ---------------------------------------------------------------------------

// tasteProfileRequiredKeys is the thirteen required keys of
// taste-profile.schema.json. Every write must carry all of them.
var tasteProfileRequiredKeys = []string{
	"favoriteFoods", "favoriteFoodsOther",
	"dislikedFoods", "dislikedFoodsOther",
	"cuisinesLoved", "cuisinesLovedOther",
	"cuisinesAvoided", "cuisinesAvoidedOther",
	"allergies", "allergiesOther",
	"texturesAvoided", "texturesAvoidedOther",
	"spiceLevel",
}

// DefaultSpiceLevel is 3, the middle of the 1-5 scale.
const DefaultSpiceLevel = 3

// normalizeTasteProfile makes a profile safe to WRITE.
//
// Two traps, both recorded in the schema's $comment, both with the same
// consequence — the entire profile silently disappears, allergies included —
// and both reachable from ordinary Go code:
//
//  1. A PARTIAL blob. Swift's synthesized Decodable init does not fall back to a
//     property's default for a missing key on a non-optional property, so a blob
//     missing even one key fails to decode outright, TasteProfile.load()
//     swallows the error with `try?` and returns nil, and the profile is gone.
//     Since none of the struct's fields carry omitempty, encoding/json emits all
//     thirteen; this function exists to guarantee the second trap too.
//
//  2. A NIL SLICE. In Go a nil slice marshals to `null`, not `[]`. The six list
//     properties are "type": "array", so null violates the schema, AND Swift
//     decodes them into non-optional [String], where null fails exactly as a
//     missing key does. A Go writer that never initializes the six lists
//     produces a blob that passes its own round trip and is unreadable by iOS.
//
// The zero-value struct is the case that gets this wrong, and it is not an
// exotic one: it is what `var p contract.TasteProfile` gives you, and what a
// user who skipped the wizard has.
func normalizeTasteProfile(p contract.TasteProfile) contract.TasteProfile {
	emptyIfNil(&p.FavoriteFoods)
	emptyIfNil(&p.DislikedFoods)
	emptyIfNil(&p.CuisinesLoved)
	emptyIfNil(&p.CuisinesAvoided)
	emptyIfNil(&p.Allergies)
	emptyIfNil(&p.TexturesAvoided)
	return p
}

func emptyIfNil(s *[]string) {
	if *s == nil {
		*s = []string{}
	}
}

// MarshalTasteProfile encodes a profile with all thirteen keys present and the
// six lists emitted as [] rather than null.
//
// Exported because the HTTP layer echoes a profile back on PUT and must not
// hand the client a shape the iOS decoder would reject either.
func MarshalTasteProfile(p contract.TasteProfile) ([]byte, error) {
	data, err := json.Marshal(normalizeTasteProfile(p))
	if err != nil {
		return nil, fmt.Errorf("store: encoding taste profile: %w", err)
	}
	return data, nil
}

// UnmarshalTasteProfile decodes a profile TOLERANTLY.
//
// Reading is deliberately more forgiving than writing. A missing key keeps the
// documented default rather than failing the whole decode — being more
// forgiving than iOS is safe in the direction that matters, since the profile
// survives; being less forgiving would lose it. An explicit null for one of the
// six lists is normalized back to an empty slice on the way out, so nothing
// downstream ever holds a nil that would be re-emitted as null.
func UnmarshalTasteProfile(data []byte) (contract.TasteProfile, error) {
	// Pre-filled, so a missing key leaves the default in place.
	p := normalizeTasteProfile(contract.TasteProfile{SpiceLevel: DefaultSpiceLevel})
	if err := json.Unmarshal(data, &p); err != nil {
		return contract.TasteProfile{}, fmt.Errorf("store: decoding taste profile: %w", err)
	}
	return normalizeTasteProfile(p), nil
}

// TasteProfile returns the stored profile and whether one exists.
//
// The bool matters: "no profile yet" and "an empty profile" are different
// answers. The first means the wizard has not run; the second means the user
// ran it and chose nothing, and only the first should be offered the wizard
// again.
func (d *DB) TasteProfile(ctx context.Context) (contract.TasteProfile, bool, error) {
	var blob string
	err := d.sql.QueryRowContext(ctx, `SELECT profile FROM taste_profile WHERE id = 1`).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return normalizeTasteProfile(contract.TasteProfile{SpiceLevel: DefaultSpiceLevel}), false, nil
	}
	if err != nil {
		return contract.TasteProfile{}, false, fmt.Errorf("store: reading taste profile: %w", err)
	}
	p, err := UnmarshalTasteProfile([]byte(blob))
	if err != nil {
		return contract.TasteProfile{}, false, err
	}
	return p, true, nil
}

// SaveTasteProfile writes the profile as one complete thirteen-key blob.
//
// There is no partial write and no field-level update by design: every path
// that touches this row goes through the same encoder, so there is no route by
// which a partial blob can be produced at all.
func (d *DB) SaveTasteProfile(ctx context.Context, p contract.TasteProfile) error {
	blob, err := MarshalTasteProfile(p)
	if err != nil {
		return err
	}
	_, err = d.sql.ExecContext(ctx,
		`INSERT INTO taste_profile (id, profile, updatedAt) VALUES (1, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET profile = excluded.profile, updatedAt = excluded.updatedAt`,
		string(blob), formatTime(time.Now()))
	if err != nil {
		return fmt.Errorf("store: writing taste profile: %w", err)
	}
	return nil
}

// DeleteTasteProfile removes the profile, returning to the "wizard has not run"
// state. A no-op when there is nothing stored.
func (d *DB) DeleteTasteProfile(ctx context.Context) error {
	if _, err := d.sql.ExecContext(ctx, `DELETE FROM taste_profile WHERE id = 1`); err != nil {
		return fmt.Errorf("store: deleting taste profile: %w", err)
	}
	return nil
}
