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

// ---------------------------------------------------------------------------

// InventoryRow is a stored inventory row: the contract's field set plus the
// identity the HTTP API addresses it by.
//
// The embedded struct is flattened by encoding/json, so the wire shape is
// inventory-item.schema.json's properties with an `id` alongside.
type InventoryRow struct {
	ID string `json:"id"`
	contract.InventoryItem
}

// ConfirmItem is the full row payload for one item being confirmed.
//
// Paired POSITIONALLY with the contract.ConfirmAction at the same index, exactly as the
// shipped Swift does (`case let .insert(_, quantity): let review =
// reviewItems[index]`) — the insert action deliberately ignores its own key and
// takes the payload from the matching slot.
//
// Category is a typed contract.FoodCategory rather than a raw string so the
// canonical-value rule is structural instead of a comment someone has to obey:
// there is no way to express "the model said Dairy" here, so a mixed-case
// string cannot reach the column.
type ConfirmItem struct {
	Name          string
	Brand         *string
	Category      contract.FoodCategory
	SourcePhotoID *string
	IsStaple      bool
}

// inventoryOrder is the canonical row order for this table.
//
// One ordering, defined once, used by the list, the stock snapshots and the
// appliers' first-match lookups. It has to be the same everywhere: a plan is
// computed from a stock snapshot and then applied by resolving keys back to
// rows, and if the two disagree about which row comes first, "the first row
// with this name" means two different rows on the two sides of that round trip.
const inventoryOrder = ` ORDER BY dateAdded, id`

const inventoryColumns = `id, name, brand, categoryRaw, quantity, dateAdded, sourcePhotoID, isStaple`

// InventoryOrder is a sort option for ListInventoryItems, matching the three
// raw values of iOS's `enum InventorySort` and therefore the stored
// `inventorySortOrder` setting.
type InventoryOrder string

const (
	// OrderByCategory groups by category, then name. The iOS default.
	OrderByCategory InventoryOrder = "category"
	// OrderByDateAdded is newest first.
	OrderByDateAdded InventoryOrder = "dateAdded"
	// OrderByName is alphabetical, case-insensitive.
	OrderByName InventoryOrder = "name"
)

// orderClause maps a sort option to SQL, falling back to the canonical order
// for anything unrecognized — same spirit as the taxonomy's silent fallback:
// an unknown stored preference is a display concern, never a query failure.
func (o InventoryOrder) orderClause() string {
	switch o {
	case OrderByCategory:
		return ` ORDER BY categoryRaw, name COLLATE NOCASE, id`
	case OrderByDateAdded:
		return ` ORDER BY dateAdded DESC, id`
	case OrderByName:
		return ` ORDER BY name COLLATE NOCASE, id`
	default:
		return inventoryOrder
	}
}

// scanInventoryRow reads one row in inventoryColumns order.
func scanInventoryRow(s interface{ Scan(...any) error }) (InventoryRow, error) {
	var (
		row       InventoryRow
		brand     sql.NullString
		photoID   sql.NullString
		dateAdded string
	)
	if err := s.Scan(&row.ID, &row.Name, &brand, &row.CategoryRaw, &row.Quantity,
		&dateAdded, &photoID, &row.IsStaple); err != nil {
		return InventoryRow{}, err
	}
	if brand.Valid {
		row.Brand = &brand.String
	}
	if photoID.Valid {
		row.SourcePhotoID = &photoID.String
	}
	parsed, err := parseTime(dateAdded)
	if err != nil {
		return InventoryRow{}, err
	}
	row.DateAdded = parsed
	return row, nil
}

// ListInventoryItems returns every inventory row in the requested order.
func (d *DB) ListInventoryItems(ctx context.Context, order InventoryOrder) ([]InventoryRow, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT `+inventoryColumns+` FROM inventory_item`+order.orderClause())
	if err != nil {
		return nil, fmt.Errorf("store: listing inventory: %w", err)
	}
	defer rows.Close()
	// Never nil: an empty inventory is a real, valid answer and must not be
	// confused with a failure — and a nil slice marshals to JSON null.
	out := []InventoryRow{}
	for rows.Next() {
		row, err := scanInventoryRow(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing inventory: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing inventory: %w", err)
	}
	return out, nil
}

// GetInventoryItem returns one row by id, or ErrNotFound.
func (d *DB) GetInventoryItem(ctx context.Context, id string) (InventoryRow, error) {
	row, err := scanInventoryRow(d.sql.QueryRowContext(ctx,
		`SELECT `+inventoryColumns+` FROM inventory_item WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return InventoryRow{}, fmt.Errorf("store: inventory item %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return InventoryRow{}, fmt.Errorf("store: reading inventory item %q: %w", id, err)
	}
	return row, nil
}

// InsertInventoryItem adds one row, generating its id and defaulting an unset
// dateAdded to now.
//
// The category is canonicalized through contract.FromStorage, so the column can
// only ever hold one of the nine raw values. Note that FromStorage does NOT
// lowercase, by contract: a caller holding untrusted text — anything that came
// from the model, or straight off the wire — must map it with
// contract.FromModelString first, at the boundary where that text enters. The
// store is not that boundary and will not pretend to be.
func (d *DB) InsertInventoryItem(ctx context.Context, item contract.InventoryItem) (InventoryRow, error) {
	id, err := newUUID()
	if err != nil {
		return InventoryRow{}, err
	}
	if item.DateAdded.IsZero() {
		item.DateAdded = time.Now()
	}
	item.SetCategory(contract.FromStorage(item.CategoryRaw))

	_, err = d.sql.ExecContext(ctx,
		`INSERT INTO inventory_item (`+inventoryColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, item.Name, nullString(item.Brand), item.CategoryRaw, item.Quantity,
		formatTime(item.DateAdded), nullString(item.SourcePhotoID), item.IsStaple)
	if err != nil {
		return InventoryRow{}, fmt.Errorf("store: inserting inventory item: %w", err)
	}
	return InventoryRow{ID: id, InventoryItem: item}, nil
}

// UpdateInventoryItem overwrites every mutable field of an existing row.
// Returns ErrNotFound when the id does not exist.
func (d *DB) UpdateInventoryItem(ctx context.Context, row InventoryRow) error {
	row.SetCategory(contract.FromStorage(row.CategoryRaw))
	if row.DateAdded.IsZero() {
		row.DateAdded = time.Now()
	}
	res, err := d.sql.ExecContext(ctx,
		`UPDATE inventory_item
		    SET name = ?, brand = ?, categoryRaw = ?, quantity = ?, dateAdded = ?,
		        sourcePhotoID = ?, isStaple = ?
		  WHERE id = ?`,
		row.Name, nullString(row.Brand), row.CategoryRaw, row.Quantity, formatTime(row.DateAdded),
		nullString(row.SourcePhotoID), row.IsStaple, row.ID)
	if err != nil {
		return fmt.Errorf("store: updating inventory item %q: %w", row.ID, err)
	}
	return requireAffected(res, "inventory item", row.ID)
}

// DeleteInventoryItem removes one row. Returns ErrNotFound when it is not there.
func (d *DB) DeleteInventoryItem(ctx context.Context, id string) error {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM inventory_item WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: deleting inventory item %q: %w", id, err)
	}
	return requireAffected(res, "inventory item", id)
}

// MergeKeyStock snapshots the inventory as merge-keyed StockRefs, the input
// ConfirmPlan expects.
func (d *DB) MergeKeyStock(ctx context.Context) ([]contract.StockRef, error) {
	return d.stock(ctx, func(row InventoryRow) string { return contract.MergeKey(row.Name, row.Brand) })
}

// NameKeyStock snapshots the inventory as name-keyed StockRefs, the input
// CookPlan expects. Recipe ingredients carry no brand, so two rows that differ
// only by brand collapse to the same key here — deliberately, and matching the
// shipped call site.
func (d *DB) NameKeyStock(ctx context.Context) ([]contract.StockRef, error) {
	return d.stock(ctx, func(row InventoryRow) string { return contract.NameKey(row.Name) })
}

func (d *DB) stock(ctx context.Context, key func(InventoryRow) string) ([]contract.StockRef, error) {
	rows, err := d.ListInventoryItems(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]contract.StockRef, 0, len(rows))
	for _, row := range rows {
		out = append(out, contract.StockRef{Key: key(row), Quantity: row.Quantity})
	}
	return out, nil
}

// ApplyConfirmPlan replays a ConfirmPlan against the database.
//
// It APPLIES the plan and never re-derives it. Whether a key is an insert or an
// increment, and what quantity each carries, was decided by the pure logic that
// InventoryLogicTests.swift pins. Deciding it a second time here from whatever
// happens to be in the table is a second implementation of the rule, and the
// second implementation is the one that drifts.
//
// actions and confirming are paired positionally and must be the same length —
// the insert action carries only a key and a quantity, and the row's name,
// brand and category come from confirming[i]. A length mismatch is a caller
// bug, reported rather than guessed at.
//
// Faithful to the shipped Swift, deliberately:
//   - an insert whose name is blank after trimming is SKIPPED, and still
//     consumes its slot, so the indices stay aligned;
//   - a brand that is present but empty is stored as NULL, not as "";
//   - the name is stored trimmed.
//
// ONE DOCUMENTED DIVERGENCE, at the call site rather than in the logic. On iOS
// the increment branch looks the row up in a SwiftData @Query snapshot captured
// before the loop, so an increment for a key INSERTED EARLIER IN THE SAME BATCH
// finds nothing and is silently dropped — confirming the same item twice lands
// a quantity of 1 where the plan said 2. Here a newly inserted row is
// registered immediately, so the following increment applies. That is what the
// plan's own replay rule requires ("dropping the insert loses the row and
// dropping the increment loses the quantity"), and what both other ports'
// confirmPlan docs describe. The pure logic is unchanged; only the replay is
// not lossy.
//
// The whole plan lands in one transaction: a half-applied confirm would leave
// rows inserted with their matching increments missing.
func (d *DB) ApplyConfirmPlan(ctx context.Context, actions []contract.ConfirmAction, confirming []ConfirmItem) error {
	if len(actions) != len(confirming) {
		return fmt.Errorf("store: confirm plan has %d actions for %d items; they are paired positionally",
			len(actions), len(confirming))
	}
	if len(actions) == 0 {
		return nil
	}
	now := formatTime(time.Now())

	return d.WithTx(ctx, func(tx *sql.Tx) error {
		// Read the key -> id map inside the transaction, under the write lock
		// taken by BEGIN IMMEDIATE, so nothing moves under the applier.
		idByKey, err := inventoryIDsByKey(ctx, tx, func(name string, brand *string) string {
			return contract.MergeKey(name, brand)
		})
		if err != nil {
			return err
		}

		for i, action := range actions {
			switch action.Kind {
			case contract.ConfirmIncrement:
				id, ok := idByKey[action.Key]
				if !ok {
					// No row for this key. The shipped Swift skips silently
					// (`if let match = ...`), and so do we: an increment with
					// nothing to increment is not an error worth failing an
					// entire confirm over.
					continue
				}
				if _, err := tx.ExecContext(ctx,
					`UPDATE inventory_item SET quantity = ? WHERE id = ?`,
					action.Quantity, id); err != nil {
					return fmt.Errorf("store: incrementing %q: %w", action.Key, err)
				}

			case contract.ConfirmInsert:
				item := confirming[i]
				name := strings.TrimSpace(item.Name)
				if name == "" {
					continue
				}
				brand := item.Brand
				if brand != nil && strings.TrimSpace(*brand) == "" {
					brand = nil
				}
				id, err := newUUID()
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO inventory_item (`+inventoryColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
					id, name, nullString(brand), string(item.Category), action.Quantity,
					now, nullString(item.SourcePhotoID), item.IsStaple); err != nil {
					return fmt.Errorf("store: inserting %q: %w", action.Key, err)
				}
				// Register it so a later increment in this same batch lands.
				if _, exists := idByKey[contract.MergeKey(name, brand)]; !exists {
					idByKey[contract.MergeKey(name, brand)] = id
				}

			default:
				return fmt.Errorf("store: unknown confirm action kind %d", action.Kind)
			}
		}
		return nil
	})
}

// ApplyCookPlan replays a CookPlan against the database: each change either
// sets a row's quantity or deletes the row that reached zero.
//
// Keys here are NAME-ONLY, because recipe ingredients carry no brand, and they
// resolve to the FIRST matching row in the canonical order — the same rule the
// snapshot that produced the plan used, which is why there is one canonical
// order and not one per query. Two rows differing only by brand therefore share
// a key and the first of them absorbs the decrement; that is the shipped
// behavior, not an accident of this port.
//
// One transaction: a partly-cooked recipe that decremented three ingredients
// and lost the fourth is worse than one that did nothing.
func (d *DB) ApplyCookPlan(ctx context.Context, changes []contract.CookChange) error {
	if len(changes) == 0 {
		return nil
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		idByKey, err := inventoryIDsByKey(ctx, tx, func(name string, _ *string) string {
			return contract.NameKey(name)
		})
		if err != nil {
			return err
		}
		for _, change := range changes {
			id, ok := idByKey[change.Key]
			if !ok {
				// Same silent skip as the Swift's `guard let item = ... else
				// { continue }`.
				continue
			}
			if change.Removed {
				if _, err := tx.ExecContext(ctx, `DELETE FROM inventory_item WHERE id = ?`, id); err != nil {
					return fmt.Errorf("store: removing cooked-out %q: %w", change.Key, err)
				}
				delete(idByKey, change.Key)
				continue
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE inventory_item SET quantity = ? WHERE id = ?`,
				change.NewQuantity, id); err != nil {
				return fmt.Errorf("store: decrementing %q: %w", change.Key, err)
			}
		}
		return nil
	})
}

// inventoryIDsByKey maps each key to the id of the FIRST row producing it, in
// the canonical order.
func inventoryIDsByKey(ctx context.Context, tx *sql.Tx, key func(string, *string) string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, name, brand FROM inventory_item`+inventoryOrder)
	if err != nil {
		return nil, fmt.Errorf("store: snapshotting inventory keys: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		var brand sql.NullString
		if err := rows.Scan(&id, &name, &brand); err != nil {
			return nil, fmt.Errorf("store: snapshotting inventory keys: %w", err)
		}
		var brandPtr *string
		if brand.Valid {
			brandPtr = &brand.String
		}
		k := key(name, brandPtr)
		if _, seen := out[k]; !seen {
			out[k] = id
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: snapshotting inventory keys: %w", err)
	}
	return out, nil
}

// nullString turns an optional string into a NULL-able SQL argument, so an
// absent brand is stored as NULL rather than as the empty string. The contract
// keeps those distinguishable and so must the column.
func nullString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// requireAffected turns "no rows matched" into ErrNotFound so the HTTP layer
// can answer 404 without a second round trip.
func requireAffected(res sql.Result, what, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: %s %q: %w", what, id, err)
	}
	if n == 0 {
		return fmt.Errorf("store: %s %q: %w", what, id, ErrNotFound)
	}
	return nil
}
