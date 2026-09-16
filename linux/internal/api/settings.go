package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
	"github.com/IniTechInc/dinnerDecider/linux/internal/store"
)

// settingsAPI serves the ten settings keys and the thirteen-key taste profile.
//
// Two separate resources that are easy to confuse because both answer to "the
// settings": ten rows in the setting table, and thirteen keys inside one JSON
// blob. They are unrelated, and the counts are not a coincidence anyone should
// try to reconcile.
type settingsAPI struct {
	base
	db *store.DB
}

// settingDefaults is the value each key reads as when it has never been set.
//
// Spelled here rather than left to a zero value because a settings response
// with a key missing and a settings response with a key set to its default are
// different things to a client, and only one of them is true. The strings match
// what iOS reads for the same keys.
var settingDefaults = map[string]string{
	store.SettingDietPreference:             contract.DietPreferenceNone,
	store.SettingAllergies:                  "",
	store.SettingCuisineLikes:               "",
	store.SettingHouseholdSize:              strconv.Itoa(store.DefaultHouseholdSize),
	store.SettingHasSeenModelSetup:          "false",
	store.SettingHasSeenOnboarding:          "false",
	store.SettingInventorySortOrder:         string(store.OrderByCategory),
	store.SettingDebugSimulateRecipeFailure: "false",
	store.SettingHasCompletedTasteProfile:   "false",
	store.SettingSelectedModelFileName:      "",
}

// handleGet returns all ten keys, always, with defaults filled in.
func (a *settingsAPI) handleGet(w http.ResponseWriter, r *http.Request) {
	stored, err := a.db.Settings(r.Context())
	if err != nil {
		writeInternal(a.logf, w, "reading the settings", err)
		return
	}
	values := make(map[string]string, len(store.SettingKeys))
	for _, key := range store.SettingKeys {
		if v, ok := stored[key]; ok {
			values[key] = v
			continue
		}
		values[key] = settingDefaults[key]
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": values})
}

// settingsRequest is a partial write: only the keys present are changed.
type settingsRequest struct {
	Settings map[string]string `json:"settings"`
}

// handlePut writes settings, refusing any key outside the closed set of ten.
//
// Strict on purpose. A settings table that accepts any key accumulates typos
// such as "dietPreferences" or "householdsize" that read back as the default
// forever and look exactly like a user who never changed the setting. The
// refusal names the offending key so the client can be fixed rather than
// guessed at.
func (a *settingsAPI) handlePut(w http.ResponseWriter, r *http.Request) {
	var req settingsRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeLimitError(w, err)
		return
	}
	if len(req.Settings) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Send at least one setting to change.")
		return
	}
	for key, value := range req.Settings {
		if _, ok := settingDefaults[key]; !ok {
			writeError(w, http.StatusBadRequest, "unknown_setting",
				"There is no setting called "+strconv.Quote(key)+".")
			return
		}
		if key == store.SettingHouseholdSize {
			size, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || size < 1 {
				writeError(w, http.StatusBadRequest, "bad_request",
					"The household size must be a whole number of at least 1.")
				return
			}
		}
	}

	if err := a.db.SetSettings(r.Context(), req.Settings); err != nil {
		if errors.Is(err, store.ErrUnknownSetting) {
			writeError(w, http.StatusBadRequest, "unknown_setting", err.Error())
			return
		}
		writeInternal(a.logf, w, "writing the settings", err)
		return
	}
	a.handleGet(w, r)
}

// handleGetTasteProfile returns the profile, or the documented defaults when
// none has been saved.
//
// All thirteen keys, every time, and never a null for one of the six lists. In
// Go a nil slice marshals as null, and a null is a schema violation that Swift's
// synthesized Decodable init fails on exactly as it fails on a missing key: the
// whole profile is lost, ALLERGIES INCLUDED. That is a safety bug reachable
// from a zero-value struct, which is precisely the case that gets it wrong.
func (a *settingsAPI) handleGetTasteProfile(w http.ResponseWriter, r *http.Request) {
	profile, ok, err := a.db.TasteProfile(r.Context())
	if err != nil {
		writeInternal(a.logf, w, "reading the taste profile", err)
		return
	}
	if !ok {
		profile = defaultTasteProfile()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tasteProfile": wireTasteProfile(profile),
		"hasContent":   profile.HasContent(),
		"saved":        ok,
	})
}

// tasteProfileRequest is the PUT body, shaped like the GET response so a
// client can read, edit and write back without reshaping.
//
// HasContent and Saved are derived, read-only, and accepted only so that a
// naive round-trip of the GET response is not rejected for carrying them. They
// are ignored.
type tasteProfileRequest struct {
	TasteProfile *contract.TasteProfile `json:"tasteProfile"`
	HasContent   *bool                  `json:"hasContent"`
	Saved        *bool                  `json:"saved"`
}

// handlePutTasteProfile saves the profile.
func (a *settingsAPI) handlePutTasteProfile(w http.ResponseWriter, r *http.Request) {
	// Pre-filled with the defaults, so a client that omits a key keeps the
	// documented value rather than writing a zero. Reading is allowed to be
	// more forgiving than writing; writing must always emit all thirteen.
	profile := defaultTasteProfile()
	req := tasteProfileRequest{TasteProfile: &profile}
	if err := decodeJSON(w, r, &req); err != nil {
		writeLimitError(w, err)
		return
	}
	if req.TasteProfile == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Send the profile under a tasteProfile key.")
		return
	}
	profile = wireTasteProfile(*req.TasteProfile)

	if err := a.db.SaveTasteProfile(r.Context(), profile); err != nil {
		writeInternal(a.logf, w, "saving the taste profile", err)
		return
	}
	// Completing the wizard is what this key records, and it is the same key
	// iOS sets at the same moment.
	if err := a.db.SetSetting(r.Context(), store.SettingHasCompletedTasteProfile, "true"); err != nil {
		a.logf("[API] Could not record that the taste profile was completed: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tasteProfile": profile,
		"hasContent":   profile.HasContent(),
		"saved":        true,
	})
}

// defaultTasteProfile is an empty profile with every list allocated and the
// documented spice level.
func defaultTasteProfile() contract.TasteProfile {
	return wireTasteProfile(contract.TasteProfile{SpiceLevel: store.DefaultSpiceLevel})
}

// wireTasteProfile makes a profile safe to marshal: every list non-nil, so no
// key can go out as null.
func wireTasteProfile(p contract.TasteProfile) contract.TasteProfile {
	p.FavoriteFoods = nonNilStrings(p.FavoriteFoods)
	p.DislikedFoods = nonNilStrings(p.DislikedFoods)
	p.CuisinesLoved = nonNilStrings(p.CuisinesLoved)
	p.CuisinesAvoided = nonNilStrings(p.CuisinesAvoided)
	p.Allergies = nonNilStrings(p.Allergies)
	p.TexturesAvoided = nonNilStrings(p.TexturesAvoided)
	return p
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
