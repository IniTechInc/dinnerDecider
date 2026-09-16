package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
	"github.com/IniTechInc/dinnerDecider/linux/internal/store"
)

// shoppingAPI serves the shopping list routes and the plain-text export.
type shoppingAPI struct {
	base
	db *store.DB
}

// handleList returns the whole list in canonical order.
func (a *shoppingAPI) handleList(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.ListShoppingItems(r.Context())
	if err != nil {
		writeInternal(a.logf, w, "listing the shopping list", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

// shoppingCreateRequest adds one typed line, or several from a recipe's
// missingItems.
type shoppingCreateRequest struct {
	// Name is a single hand-typed line.
	Name string `json:"name"`
	// Names is a batch, normally a recipe's missingItems.
	Names []string `json:"names"`
	// IsManual marks a hand-typed line. It defaults to true for Name and false
	// for Names, which is what each path means.
	IsManual *bool `json:"isManual"`
}

// handleCreate adds lines, de-duplicated against what is already there.
//
// The de-duplication is contract.Aggregate then contract.NewItems, and it is
// NOT repeated in the store. Those two carry an asymmetry that looks like an
// oversight and is pinned by tests: existing names are trimmed AND lowercased
// to build the comparison set, while candidates are already trimmed by
// Aggregate so the filter only lowercases them. The first-seen casing is what
// survives, so the user sees "Onion" and not "onion". A store that also
// de-duplicated would be a second, unpinned implementation of that rule.
func (a *shoppingAPI) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req shoppingCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeLimitError(w, err)
		return
	}

	candidates := req.Names
	isManual := false
	if strings.TrimSpace(req.Name) != "" {
		candidates = append(append([]string(nil), candidates...), req.Name)
		isManual = true
	}
	if req.IsManual != nil {
		isManual = *req.IsManual
	}
	if len(candidates) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Name at least one thing to add.")
		return
	}

	ctx := r.Context()
	existing, err := a.db.ExistingNames(ctx)
	if err != nil {
		writeInternal(a.logf, w, "reading the shopping list", err)
		return
	}
	toAdd := contract.NewItems(candidates, existing)
	added, err := a.db.InsertShoppingNames(ctx, toAdd, isManual)
	if err != nil {
		writeInternal(a.logf, w, "adding to the shopping list", err)
		return
	}

	rows, err := a.db.ListShoppingItems(ctx)
	if err != nil {
		writeInternal(a.logf, w, "listing the shopping list", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"added": len(added),
		// Names the caller sent that were already on the list are reported
		// rather than silently dropped, so an "add missing to shopping list"
		// button can say "already on your list" instead of appearing to do
		// nothing.
		"skipped": len(contract.Aggregate(candidates)) - len(toAdd),
		"items":   rows,
	})
}

// shoppingUpdateRequest edits one line. Both fields are optional.
type shoppingUpdateRequest struct {
	Name      *string `json:"name"`
	IsChecked *bool   `json:"isChecked"`
}

// handleUpdate renames a line or checks it off.
func (a *shoppingAPI) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	row, err := a.db.GetShoppingItem(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "There is no shopping list item with that id.")
		return
	}
	if err != nil {
		writeInternal(a.logf, w, "reading a shopping list item", err)
		return
	}

	var req shoppingUpdateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeLimitError(w, err)
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			// Blank names are dropped, never stored: a nameless row on a
			// shopping list is a line the user cannot shop for.
			writeError(w, http.StatusBadRequest, "bad_request", "A shopping list line needs a name.")
			return
		}
		row.Name = name
	}
	if req.IsChecked != nil {
		row.IsChecked = *req.IsChecked
	}

	if err := a.db.UpdateShoppingItem(r.Context(), row); err != nil {
		if errors.Is(err, store.ErrBlankName) {
			writeError(w, http.StatusBadRequest, "bad_request", "A shopping list line needs a name.")
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "There is no shopping list item with that id.")
			return
		}
		writeInternal(a.logf, w, "updating a shopping list item", err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

// handleDelete removes one line.
func (a *shoppingAPI) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := a.db.DeleteShoppingItem(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeInternal(a.logf, w, "deleting a shopping list item", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleExport serves the contractual plain-text list.
//
// The shape is part of the contract, not a convenience: header "Shopping List",
// a blank line, then "[x] Name" or "[ ] Name" per line, with the whole string
// trimmed of trailing whitespace, and an EMPTY list rendering as exactly
// "Shopping List\n\n(empty)". Checked items are exported MARKED DONE rather
// than filtered out, because a shared list reads better when it shows what has
// been got as well as what is left.
func (a *shoppingAPI) handleExport(w http.ResponseWriter, r *http.Request) {
	text, err := a.db.ExportText(r.Context())
	if err != nil {
		writeInternal(a.logf, w, "exporting the shopping list", err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// A filename so a browser "save as" lands on something sensible, but
	// inline so the LAN UI can just show it.
	w.Header().Set("Content-Disposition", `inline; filename="shopping-list.txt"`)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(text)); err != nil {
		a.logf("[API] Could not write the shopping list export: %v", err)
	}
}
