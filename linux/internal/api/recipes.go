package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
	"github.com/IniTechInc/dinnerDecider/linux/internal/llm"
	"github.com/IniTechInc/dinnerDecider/linux/internal/store"
)

// RecipeUnreadableMessage is the user-facing error for a reply that could not
// be decoded.
//
// EXACT, and asserted by a test. shared/prompts/recipes.md and
// shared/parser/response-parsing.md both pin this string, and AppModel.swift
// sets it verbatim. It is short, it does not blame the user, and it tells them
// the one useful thing: try again. A port that paraphrases it makes two
// products out of one.
const RecipeUnreadableMessage = "We could not read the recipe ideas this time. Please try again."

// RecipeGenerator is the one model call this group makes.
//
// scan.Engine satisfies it. The engine brackets the call as its own session and
// takes the capacity-1 inference gate, which is exactly why recipes are never
// admitted with a 409 against a running scan: nesting is contractual, and the
// gate is what serialises them.
type RecipeGenerator interface {
	Recipes(ctx context.Context, prompt string) (contract.RecipeBundleResponse, error)
}

// recipesAPI serves POST /api/recipes.
type recipesAPI struct {
	base
	db      *store.DB
	engine  RecipeGenerator
	timeout time.Duration

	// mu guards the last generated bundle.
	//
	// Recipes are NOT persisted, on iOS or here: RecipeSuggestion is produced
	// on the fly by the model and only a PlannedMeal survives a restart. So the
	// service holds the last bundle in memory, which is also what makes
	// "leave the existing lists untouched on a decode failure" mean something
	// over a stateless protocol: the response still carries the lists that were
	// there before, rather than blanking a screen the user was reading.
	mu                  sync.Mutex
	makeNow             []contract.RecipeSuggestion
	almostThere         []contract.RecipeSuggestion
	hasGeneratedRecipes bool
	generatedAt         time.Time
}

// recipesRequest carries the optional mood text.
type recipesRequest struct {
	Mood string `json:"mood"`
}

// recipesResponse is the whole recipe state, sent on success and on failure
// alike.
type recipesResponse struct {
	MakeNow     []contract.RecipeSuggestion `json:"makeNow"`
	AlmostThere []contract.RecipeSuggestion `json:"almostThere"`
	// HasGeneratedRecipes distinguishes "never generated" from "generated, and
	// nothing matched". Without it an empty makeNow and an empty almostThere
	// are the same JSON in both cases, and the UI has to choose between telling
	// a user who has not pressed the button that nothing matched, or telling a
	// user whose inventory genuinely matched nothing to press a button they
	// already pressed. shared/prompts/recipes.md asks a port to carry it.
	HasGeneratedRecipes bool `json:"hasGeneratedRecipes"`
	// Error is the user-facing failure, empty on success.
	Error       string    `json:"error,omitempty"`
	GeneratedAt time.Time `json:"generatedAt,omitempty"`
}

// handleGenerate asks the model for recipes.
//
// NOT admitted against a running scan. Plan section 0.1: recipe generation
// nests inside or overlaps a scan by contract, it is one short text-only call,
// and it queues on the layer-1 inference gate like everything else. Returning
// 409 here would break a legitimate flow to defend against a problem the gate
// already handles.
func (a *recipesAPI) handleGenerate(w http.ResponseWriter, r *http.Request) {
	var req recipesRequest
	if err := decodeOptionalJSON(w, r, &req); err != nil {
		writeLimitError(w, err)
		return
	}

	// This handler legitimately runs for minutes: it may queue on the inference
	// gate behind a scan and then wait on the call itself. The server's write
	// deadline is sized for an ordinary request, so extend it here rather than
	// letting a correct, slow answer be cut off mid-write.
	extendWriteDeadline(w, LongInferenceWriteDeadline)

	ctx := r.Context()
	if a.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.timeout)
		defer cancel()
	}

	names, err := a.inventoryNames(ctx)
	if err != nil {
		writeInternal(a.logf, w, "reading the inventory for a recipe prompt", err)
		return
	}
	prefs, taste, simulateFailure, err := a.preferences(ctx)
	if err != nil {
		writeInternal(a.logf, w, "reading the preferences for a recipe prompt", err)
		return
	}

	if simulateFailure {
		// The hidden debug switch, ported from AppModel.generateRecipes: it
		// returns a deliberately broken reply so the real error-and-retry path
		// can be demonstrated end to end without breaking the model. Skipping
		// the call entirely is the same observable behaviour and costs nobody
		// four minutes.
		a.logf("[API] debugSimulateRecipeFailure is on; returning the unreadable-recipes error without calling the model")
		a.writeState(w, http.StatusOK, RecipeUnreadableMessage)
		return
	}

	prompt := contract.RecipePrompt(names, prefs, req.Mood, taste)
	bundle, err := a.engine.Recipes(ctx, prompt)
	if err != nil {
		a.writeGenerateError(w, err)
		return
	}

	a.mu.Lock()
	a.makeNow = bundle.MakeNow
	a.almostThere = bundle.AlmostThere
	a.hasGeneratedRecipes = true
	a.generatedAt = a.now().UTC()
	a.mu.Unlock()

	a.writeState(w, http.StatusOK, "")
}

// writeGenerateError separates the two kinds of failure, which deserve
// different answers.
//
//   - The reply could not be DECODED. The model answered; we could not read it.
//     That is the contract's failure, it gets the exact contractual string, and
//     the existing lists are left alone. 200, because the endpoint's answer is
//     the recipe STATE and the state is what the client renders. A non-2xx here
//     would invite a fetch wrapper to throw away the body, which is the one
//     thing carrying the lists the client must keep showing.
//   - Anything else is a service failure: the model was not ready, the gate
//     timed out, memory ran short. Those are 503, because retrying later is the
//     right advice and because they say nothing about the recipes.
func (a *recipesAPI) writeGenerateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		// The client went away. Nothing to report to it.
		a.logf("[API] Recipe generation was cancelled by the client")
		return
	case errors.Is(err, llm.ErrBadResponse):
		a.logf("[API] The recipe bundle could not be decoded: %v", err)
		a.writeState(w, http.StatusOK, RecipeUnreadableMessage)
	case errors.Is(err, llm.ErrInsufficientMemory):
		writeRetryAfter(w, http.StatusServiceUnavailable, llm.AbortReasonInsufficientMemory,
			"There is not enough free memory to run the model right now. Close other programs on the server and try again.",
			ScanRetryAfter)
	case errors.Is(err, llm.ErrModelNotReady):
		writeRetryAfter(w, http.StatusServiceUnavailable, "model_not_ready",
			"The model is not ready yet. Try again in a moment.", ScanRetryAfter)
	case errors.Is(err, context.DeadlineExceeded):
		writeRetryAfter(w, http.StatusServiceUnavailable, "timeout",
			"The model took too long to answer. Try again.", ScanRetryAfter)
	default:
		a.logf("[API] Recipe generation failed: %v", err)
		writeRetryAfter(w, http.StatusServiceUnavailable, "model_unavailable",
			"The model could not be reached. Try again.", ScanRetryAfter)
	}
}

// writeState sends the current recipe state, with an optional user-facing
// error. On the failure path the lists are exactly what they were before, which
// is the whole point.
func (a *recipesAPI) writeState(w http.ResponseWriter, status int, userError string) {
	a.mu.Lock()
	resp := recipesResponse{
		MakeNow:             append([]contract.RecipeSuggestion(nil), a.makeNow...),
		AlmostThere:         append([]contract.RecipeSuggestion(nil), a.almostThere...),
		HasGeneratedRecipes: a.hasGeneratedRecipes,
		Error:               userError,
		GeneratedAt:         a.generatedAt,
	}
	a.mu.Unlock()
	// Never null on the wire: an empty bucket is [], which is what the schema
	// says and what a client can iterate without a guard.
	if resp.MakeNow == nil {
		resp.MakeNow = []contract.RecipeSuggestion{}
	}
	if resp.AlmostThere == nil {
		resp.AlmostThere = []contract.RecipeSuggestion{}
	}
	writeJSON(w, status, resp)
}

// inventoryNames is the comma list the prompt is built from, in the canonical
// display order so the prompt matches what the user is looking at.
func (a *recipesAPI) inventoryNames(ctx context.Context) ([]string, error) {
	rows, err := a.db.ListInventoryItems(ctx, store.OrderByCategory)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(row.Name)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

// preferences gathers the prompt's inputs from the settings table.
//
// A VALUE passed into contract.RecipePrompt, never read inside it: that is what
// keeps the prompt assembly pure and its tests free of a settings store.
func (a *recipesAPI) preferences(ctx context.Context) (contract.RecipePreferences, *contract.TasteProfile, bool, error) {
	settings, err := a.db.Settings(ctx)
	if err != nil {
		return contract.RecipePreferences{}, nil, false, err
	}

	householdSize := store.DefaultHouseholdSize
	if raw, ok := settings[store.SettingHouseholdSize]; ok {
		if parsed, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			householdSize = parsed
		}
	}
	prefs := contract.RecipePreferences{
		Diet:          valueOr(settings, store.SettingDietPreference, contract.DietPreferenceNone),
		Allergies:     settings[store.SettingAllergies],
		Cuisines:      settings[store.SettingCuisineLikes],
		HouseholdSize: householdSize,
	}

	var taste *contract.TasteProfile
	profile, ok, err := a.db.TasteProfile(ctx)
	if err != nil {
		return contract.RecipePreferences{}, nil, false, err
	}
	if ok {
		taste = &profile
	}

	simulate := strings.EqualFold(strings.TrimSpace(settings[store.SettingDebugSimulateRecipeFailure]), "true")
	return prefs, taste, simulate, nil
}

func valueOr(values map[string]string, key, fallback string) string {
	if v, ok := values[key]; ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

// extendWriteDeadline pushes this connection's write deadline out for a handler
// that legitimately runs long. Unsupported writers (a ResponseRecorder, a
// wrapper that does not forward it) are fine: they simply keep whatever
// deadline they had.
func extendWriteDeadline(w http.ResponseWriter, d time.Duration) {
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Now().Add(d)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		// Nothing actionable: the request proceeds under the server default.
		return
	}
}
