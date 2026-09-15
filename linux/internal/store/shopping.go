package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
)

// ErrBlankName rejects a shopping line with nothing on it.
//
// shopping-list-item.schema.json: "Blank and whitespace-only names are dropped,
// never stored." Enforced at the store so no caller has to remember; the bulk
// path drops them silently, the single-item path says so.
var ErrBlankName = errors.New("store: shopping list name is blank")

// ShoppingRow is a stored shopping line: the contract's field set plus the
// identity the HTTP API addresses it by.
type ShoppingRow struct {
	ID string `json:"id"`
	contract.ShoppingListItem
}

const shoppingColumns = `id, name, isChecked, dateAdded, isManual`

// shoppingOrder is the canonical order: oldest first, which is the order the
// list is displayed in and therefore the order it exports in.
const shoppingOrder = ` ORDER BY dateAdded, id`

func scanShoppingRow(s interface{ Scan(...any) error }) (ShoppingRow, error) {
	var (
		row       ShoppingRow
		dateAdded string
	)
	if err := s.Scan(&row.ID, &row.Name, &row.IsChecked, &dateAdded, &row.IsManual); err != nil {
		return ShoppingRow{}, err
	}
	parsed, err := parseTime(dateAdded)
	if err != nil {
		return ShoppingRow{}, err
	}
	row.DateAdded = parsed
	return row, nil
}

// ListShoppingItems returns the whole list, oldest first. Never nil.
func (d *DB) ListShoppingItems(ctx context.Context) ([]ShoppingRow, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT `+shoppingColumns+` FROM shopping_list_item`+shoppingOrder)
	if err != nil {
		return nil, fmt.Errorf("store: listing shopping list: %w", err)
	}
	defer rows.Close()
	out := []ShoppingRow{}
	for rows.Next() {
		row, err := scanShoppingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing shopping list: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing shopping list: %w", err)
	}
	return out, nil
}

// GetShoppingItem returns one line by id, or ErrNotFound.
func (d *DB) GetShoppingItem(ctx context.Context, id string) (ShoppingRow, error) {
	row, err := scanShoppingRow(d.sql.QueryRowContext(ctx,
		`SELECT `+shoppingColumns+` FROM shopping_list_item WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ShoppingRow{}, fmt.Errorf("store: shopping item %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return ShoppingRow{}, fmt.Errorf("store: reading shopping item %q: %w", id, err)
	}
	return row, nil
}

// InsertShoppingItem adds one line, trimming the name and rejecting a blank one
// with ErrBlankName.
func (d *DB) InsertShoppingItem(ctx context.Context, item contract.ShoppingListItem) (ShoppingRow, error) {
	item.Name = strings.TrimSpace(item.Name)
	if item.Name == "" {
		return ShoppingRow{}, ErrBlankName
	}
	if item.DateAdded.IsZero() {
		item.DateAdded = time.Now()
	}
	id, err := newUUID()
	if err != nil {
		return ShoppingRow{}, err
	}
	if _, err := d.sql.ExecContext(ctx,
		`INSERT INTO shopping_list_item (`+shoppingColumns+`) VALUES (?, ?, ?, ?, ?)`,
		id, item.Name, item.IsChecked, formatTime(item.DateAdded), item.IsManual); err != nil {
		return ShoppingRow{}, fmt.Errorf("store: inserting shopping item: %w", err)
	}
	return ShoppingRow{ID: id, ShoppingListItem: item}, nil
}

// InsertShoppingNames adds several lines in one transaction, skipping blanks.
//
// It does NOT deduplicate. Aggregation — case-insensitive dedupe with the
// FIRST-SEEN casing preserved, and the trim/lowercase asymmetry against the
// existing list — is pure logic that belongs in contract.Aggregate and
// contract.NewItems, pinned by ShoppingListLogicTests.swift. Feed this the
// names those return. A store that also deduplicates is a second, unpinned
// implementation of a rule that already has one.
//
// ExistingNames is the other half of that seam.
func (d *DB) InsertShoppingNames(ctx context.Context, names []string, isManual bool) ([]ShoppingRow, error) {
	if len(names) == 0 {
		return []ShoppingRow{}, nil
	}
	now := time.Now()
	inserted := make([]ShoppingRow, 0, len(names))
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		for _, raw := range names {
			name := strings.TrimSpace(raw)
			if name == "" {
				continue
			}
			id, err := newUUID()
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO shopping_list_item (`+shoppingColumns+`) VALUES (?, ?, ?, ?, ?)`,
				id, name, false, formatTime(now), isManual); err != nil {
				return fmt.Errorf("store: inserting shopping item %q: %w", name, err)
			}
			inserted = append(inserted, ShoppingRow{
				ID: id,
				ShoppingListItem: contract.ShoppingListItem{
					Name: name, IsChecked: false, DateAdded: now, IsManual: isManual,
				},
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return inserted, nil
}

// ExistingNames returns the names already on the list, in canonical order, as
// the input to contract.NewItems.
func (d *DB) ExistingNames(ctx context.Context) ([]string, error) {
	rows, err := d.ListShoppingItems(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Name)
	}
	return out, nil
}

// UpdateShoppingItem overwrites every mutable field of an existing line.
func (d *DB) UpdateShoppingItem(ctx context.Context, row ShoppingRow) error {
	row.Name = strings.TrimSpace(row.Name)
	if row.Name == "" {
		return ErrBlankName
	}
	if row.DateAdded.IsZero() {
		row.DateAdded = time.Now()
	}
	res, err := d.sql.ExecContext(ctx,
		`UPDATE shopping_list_item SET name = ?, isChecked = ?, dateAdded = ?, isManual = ? WHERE id = ?`,
		row.Name, row.IsChecked, formatTime(row.DateAdded), row.IsManual, row.ID)
	if err != nil {
		return fmt.Errorf("store: updating shopping item %q: %w", row.ID, err)
	}
	return requireAffected(res, "shopping item", row.ID)
}

// SetShoppingItemChecked is the check-off toggle. Returns ErrNotFound when the
// line is gone.
func (d *DB) SetShoppingItemChecked(ctx context.Context, id string, checked bool) error {
	res, err := d.sql.ExecContext(ctx,
		`UPDATE shopping_list_item SET isChecked = ? WHERE id = ?`, checked, id)
	if err != nil {
		return fmt.Errorf("store: checking off shopping item %q: %w", id, err)
	}
	return requireAffected(res, "shopping item", id)
}

// DeleteShoppingItem removes one line. Returns ErrNotFound when it is not there.
func (d *DB) DeleteShoppingItem(ctx context.Context, id string) error {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM shopping_list_item WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: deleting shopping item %q: %w", id, err)
	}
	return requireAffected(res, "shopping item", id)
}

// ClearCheckedShoppingItems removes every checked line and reports how many
// went. Zero is a normal answer, never an error: "clear checked" on a list with
// nothing checked is a no-op the user is allowed to perform.
func (d *DB) ClearCheckedShoppingItems(ctx context.Context) (int, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM shopping_list_item WHERE isChecked = 1`)
	if err != nil {
		return 0, fmt.Errorf("store: clearing checked shopping items: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: clearing checked shopping items: %w", err)
	}
	return int(n), nil
}

// ExportText renders the whole list as the plain text the share/export path
// serves.
func (d *DB) ExportText(ctx context.Context) (string, error) {
	rows, err := d.ListShoppingItems(ctx)
	if err != nil {
		return "", err
	}
	lines := make([]ExportLine, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, ExportLine{Name: row.Name, IsChecked: row.IsChecked})
	}
	return exportText(lines), nil
}

// ---------------------------------------------------------------------------
// MERGE NOTE — provisional, pending internal/contract/shoppinglist.go
//
// Plan task T4 (steps 15-16) owns contract/shoppinglist.go: Aggregate, NewItems
// and ExportText, pinned by ShoppingListLogicTests.swift. That file had not
// landed when this store was written, so the export renderer is reproduced here
// from ShoppingListLogic.exportText (ShoppingListLogic.swift:38-46) and the
// $comment in shopping-list-item.schema.json, which make the format part of the
// contract rather than a presentation detail.
//
// At merge: delete exportText and ExportLine, and have DB.ExportText call
// contract.ExportText over contract.Line. Nothing else changes — DB.ExportText
// already does only the loading.
// ---------------------------------------------------------------------------

// ExportLine is one plain line for export.
type ExportLine struct {
	Name      string
	IsChecked bool
}

// exportText renders the list. Checked items are marked done so a shared list
// still reads clearly.
//
// The empty case is the exact string "Shopping List\n\n(empty)" — not a bare
// header, not an empty string. It is asserted literally.
func exportText(lines []ExportLine) string {
	if len(lines) == 0 {
		return "Shopping List\n\n(empty)"
	}
	var b strings.Builder
	b.WriteString("Shopping List\n\n")
	for _, line := range lines {
		box := "[ ]"
		if line.IsChecked {
			box = "[x]"
		}
		b.WriteString(box + " " + line.Name + "\n")
	}
	// Swift trims .whitespacesAndNewlines from both ends; the header means
	// there is never anything to trim from the front, so this removes exactly
	// the trailing newline of the last line.
	return strings.TrimSpace(b.String())
}
