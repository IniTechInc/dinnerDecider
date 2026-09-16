package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
	"github.com/IniTechInc/dinnerDecider/linux/internal/store"
)

// inventoryAPI serves the inventory CRUD routes and the cook applier.
type inventoryAPI struct {
	base
	db *store.DB
}

// handleList returns the whole inventory in the user's saved sort order.
//
// The order is a stored preference rather than a query parameter default,
// because it is the same preference iOS keeps under the same key: a household
// that sorts by category on the phone sees category on the tablet.
func (a *inventoryAPI) handleList(w http.ResponseWriter, r *http.Request) {
	order := a.order(r.Context())
	if requested := strings.TrimSpace(r.URL.Query().Get("sort")); requested != "" {
		order = inventoryOrderFrom(requested)
	}
	rows, err := a.db.ListInventoryItems(r.Context(), order)
	if err != nil {
		writeInternal(a.logf, w, "listing the inventory", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows, "sort": string(order)})
}

// inventoryRequest is a manual add or an edit. Every field is optional on a
// PATCH; Name is required on a create.
type inventoryRequest struct {
	Name          *string `json:"name"`
	Brand         *string `json:"brand"`
	Category      *string `json:"category"`
	Quantity      *int    `json:"quantity"`
	IsStaple      *bool   `json:"isStaple"`
	SourcePhotoID *string `json:"sourcePhotoID"`
}

// handleCreate adds one item by hand.
func (a *inventoryAPI) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req inventoryRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeLimitError(w, err)
		return
	}
	name := ""
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
	}
	if name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "An inventory item needs a name.")
		return
	}
	quantity := 1
	if req.Quantity != nil && *req.Quantity > 0 {
		quantity = *req.Quantity
	}
	item := contract.InventoryItem{
		Name:          name,
		Brand:         normalizeBrand(req.Brand),
		Quantity:      quantity,
		DateAdded:     a.now().UTC(),
		SourcePhotoID: req.SourcePhotoID,
		IsStaple:      req.IsStaple != nil && *req.IsStaple,
	}
	// Through the typed category, never the raw string: that is what keeps a
	// mixed-case "Dairy" from the model, or a typo from a client, out of the
	// stored column. Anything unrecognised becomes other, silently, exactly as
	// the taxonomy says.
	category := contract.CategoryOther
	if req.Category != nil {
		category = contract.FromModelString(*req.Category)
	}
	item.SetCategory(category)

	row, err := a.db.InsertInventoryItem(r.Context(), item)
	if err != nil {
		writeInternal(a.logf, w, "inserting an inventory item", err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

// handleUpdate edits one item in place.
func (a *inventoryAPI) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	row, err := a.db.GetInventoryItem(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "There is no inventory item with that id.")
		return
	}
	if err != nil {
		writeInternal(a.logf, w, "reading an inventory item", err)
		return
	}

	var req inventoryRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeLimitError(w, err)
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "An inventory item needs a name.")
			return
		}
		row.Name = name
	}
	if req.Brand != nil {
		row.Brand = normalizeBrand(req.Brand)
	}
	if req.Category != nil {
		row.SetCategory(contract.FromModelString(*req.Category))
	}
	if req.Quantity != nil {
		if *req.Quantity < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "A quantity cannot be negative.")
			return
		}
		row.Quantity = *req.Quantity
	}
	if req.IsStaple != nil {
		row.IsStaple = *req.IsStaple
	}

	// Quantity zero means the row is spent. Deleting it here rather than
	// storing a zero keeps the list honest: a row at zero is not something the
	// household has.
	if row.Quantity == 0 {
		if err := a.db.DeleteInventoryItem(r.Context(), row.ID); err != nil {
			writeInternal(a.logf, w, "removing a spent inventory item", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := a.db.UpdateInventoryItem(r.Context(), row); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "There is no inventory item with that id.")
			return
		}
		writeInternal(a.logf, w, "updating an inventory item", err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

// handleDelete removes one item.
func (a *inventoryAPI) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := a.db.DeleteInventoryItem(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Already gone is the state the caller asked for.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeInternal(a.logf, w, "deleting an inventory item", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// cookRequest names the ingredients the user actually had, which is the subset
// the recipe marked hasIt.
type cookRequest struct {
	IngredientNames []string `json:"ingredientNames"`
}

// handleCook decrements the inventory for a cooked recipe.
//
// The keys are NAME-ONLY, because recipe ingredients carry no brand. Two rows
// differing only by brand therefore share a key and the first in canonical
// order absorbs the decrement; that is the shipped behaviour, not an accident
// of this port. The plan is produced by contract.CookPlan and applied by the
// store, and neither step re-derives the other's work.
func (a *inventoryAPI) handleCook(w http.ResponseWriter, r *http.Request) {
	var req cookRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeLimitError(w, err)
		return
	}
	ctx := r.Context()

	usedKeys := make([]string, 0, len(req.IngredientNames))
	for _, name := range req.IngredientNames {
		key := contract.NameKey(name)
		if key == "" {
			continue
		}
		// NOT de-duplicated: two eggs in a recipe take two eggs, and the
		// planner counts repeats deliberately.
		usedKeys = append(usedKeys, key)
	}

	stock, err := a.db.NameKeyStock(ctx)
	if err != nil {
		writeInternal(a.logf, w, "reading the inventory before cooking", err)
		return
	}
	changes := contract.CookPlan(usedKeys, stock)
	if err := a.db.ApplyCookPlan(ctx, changes); err != nil {
		writeInternal(a.logf, w, "applying a cook plan", err)
		return
	}

	rows, err := a.db.ListInventoryItems(ctx, a.order(ctx))
	if err != nil {
		writeInternal(a.logf, w, "reading the inventory after cooking", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"changed":   len(changes),
		"inventory": rows,
	})
}

func (a *inventoryAPI) order(ctx context.Context) store.InventoryOrder {
	value, err := a.db.SettingOr(ctx, store.SettingInventorySortOrder, string(store.OrderByCategory))
	if err != nil {
		return store.OrderByCategory
	}
	return inventoryOrderFrom(value)
}

// inventoryOrderFrom maps a stored or requested sort onto the three the store
// knows, falling back rather than erroring: a sort order is a display
// preference, and refusing to show an inventory because a preference string is
// unfamiliar helps nobody.
func inventoryOrderFrom(value string) store.InventoryOrder {
	switch store.InventoryOrder(strings.TrimSpace(value)) {
	case store.OrderByName:
		return store.OrderByName
	case store.OrderByDateAdded:
		return store.OrderByDateAdded
	default:
		return store.OrderByCategory
	}
}

// normalizeBrand turns a present-but-empty brand into nil.
//
// Empty string and nil are different things for the merge key, and a client
// that clears a text field sends "". Storing that would make "Milk" with brand
// "" a different item from "Milk" with no brand, and the user would see two
// rows for one carton.
func normalizeBrand(brand *string) *string {
	if brand == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*brand)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// nowOrTime is the clock a handler group uses, defaulted.
func nowOrTime(now func() time.Time) func() time.Time {
	if now != nil {
		return now
	}
	return time.Now
}
