package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
)

// contract.PlannedMeal already carries its own id, so unlike inventory and the
// shopping list there is no separate row wrapper here.

const plannedMealColumns = `id, recipeName, ingredientNames, steps, timeMinutes, plannedDate, calendarEventId`

// plannedMealOrder is chronological: the meal plan is a calendar, so the
// soonest meal comes first.
const plannedMealOrder = ` ORDER BY plannedDate, id`

// marshalStrings encodes an ordered string list for a JSON column.
//
// A nil slice marshals to `null` in Go, and null is not a list. Every consumer
// — the HTTP response, a future importer, this package's own decoder — then has
// to special-case it, and one of them will forget. Emit [] and the question
// never arises. Same rule, same reason, as the taste profile's six lists.
func marshalStrings(values []string) (string, error) {
	if values == nil {
		values = []string{}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("store: encoding string list: %w", err)
	}
	return string(data), nil
}

// unmarshalStrings decodes a JSON column into a NON-NIL slice, tolerating a
// stored null or an empty string from some other writer.
func unmarshalStrings(blob string) ([]string, error) {
	if blob == "" {
		return []string{}, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(blob), &out); err != nil {
		return nil, fmt.Errorf("store: decoding string list %q: %w", blob, err)
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

func scanPlannedMeal(s interface{ Scan(...any) error }) (contract.PlannedMeal, error) {
	var (
		meal        contract.PlannedMeal
		ingredients string
		steps       string
		plannedDate string
		calendarID  sql.NullString
	)
	if err := s.Scan(&meal.ID, &meal.RecipeName, &ingredients, &steps,
		&meal.TimeMinutes, &plannedDate, &calendarID); err != nil {
		return contract.PlannedMeal{}, err
	}
	var err error
	if meal.IngredientNames, err = unmarshalStrings(ingredients); err != nil {
		return contract.PlannedMeal{}, err
	}
	if meal.Steps, err = unmarshalStrings(steps); err != nil {
		return contract.PlannedMeal{}, err
	}
	if meal.PlannedDate, err = parseTime(plannedDate); err != nil {
		return contract.PlannedMeal{}, err
	}
	// Preserved on read even though nothing on Linux writes it, so a value that
	// arrived with an import survives a read-modify-write.
	if calendarID.Valid {
		meal.CalendarEventId = &calendarID.String
	}
	return meal, nil
}

// ListPlannedMeals returns the whole meal plan, soonest first. Never nil.
func (d *DB) ListPlannedMeals(ctx context.Context) ([]contract.PlannedMeal, error) {
	return d.queryPlannedMeals(ctx, `SELECT `+plannedMealColumns+` FROM planned_meal`+plannedMealOrder)
}

// ListPlannedMealsBetween returns the meals planned in [from, to), soonest
// first. Half-open so a caller can page day by day or week by week without a
// meal landing in two windows.
func (d *DB) ListPlannedMealsBetween(ctx context.Context, from, to time.Time) ([]contract.PlannedMeal, error) {
	return d.queryPlannedMeals(ctx,
		`SELECT `+plannedMealColumns+` FROM planned_meal WHERE plannedDate >= ? AND plannedDate < ?`+plannedMealOrder,
		formatTime(from), formatTime(to))
}

// queryPlannedMeals is the shared read path.
//
// The range comparison above is a string comparison on the stored TEXT, which
// is only correct because every timestamp is written UTC and fixed width — see
// timeLayout. It is the one query that would break silently if that changed.
func (d *DB) queryPlannedMeals(ctx context.Context, query string, args ...any) ([]contract.PlannedMeal, error) {
	rows, err := d.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: listing planned meals: %w", err)
	}
	defer rows.Close()
	out := []contract.PlannedMeal{}
	for rows.Next() {
		meal, err := scanPlannedMeal(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing planned meals: %w", err)
		}
		out = append(out, meal)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing planned meals: %w", err)
	}
	return out, nil
}

// GetPlannedMeal returns one meal by id, or ErrNotFound.
func (d *DB) GetPlannedMeal(ctx context.Context, id string) (contract.PlannedMeal, error) {
	meal, err := scanPlannedMeal(d.sql.QueryRowContext(ctx,
		`SELECT `+plannedMealColumns+` FROM planned_meal WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return contract.PlannedMeal{}, fmt.Errorf("store: planned meal %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return contract.PlannedMeal{}, fmt.Errorf("store: reading planned meal %q: %w", id, err)
	}
	return meal, nil
}

// InsertPlannedMeal saves a meal, generating its id when the caller left one
// unset and defaulting an unset plannedDate to now.
//
// calendarEventId is written through but is always nil in practice: Calendar is
// not applicable on Linux. The column exists so the field set stays compatible
// with iOS, which is the only reason to keep it — see PlannedMeal.swift.
func (d *DB) InsertPlannedMeal(ctx context.Context, meal contract.PlannedMeal) (contract.PlannedMeal, error) {
	if meal.ID == "" {
		id, err := newUUID()
		if err != nil {
			return contract.PlannedMeal{}, err
		}
		meal.ID = id
	}
	if meal.PlannedDate.IsZero() {
		meal.PlannedDate = time.Now()
	}
	ingredients, err := marshalStrings(meal.IngredientNames)
	if err != nil {
		return contract.PlannedMeal{}, err
	}
	steps, err := marshalStrings(meal.Steps)
	if err != nil {
		return contract.PlannedMeal{}, err
	}
	if _, err := d.sql.ExecContext(ctx,
		`INSERT INTO planned_meal (`+plannedMealColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		meal.ID, meal.RecipeName, ingredients, steps, meal.TimeMinutes,
		formatTime(meal.PlannedDate), nullString(meal.CalendarEventId)); err != nil {
		return contract.PlannedMeal{}, fmt.Errorf("store: inserting planned meal: %w", err)
	}
	// Hand back exactly what was stored, lists included, so the caller never
	// holds a nil where the database holds [].
	if meal.IngredientNames == nil {
		meal.IngredientNames = []string{}
	}
	if meal.Steps == nil {
		meal.Steps = []string{}
	}
	return meal, nil
}

// UpdatePlannedMeal overwrites every mutable field. Returns ErrNotFound when
// the id does not exist.
func (d *DB) UpdatePlannedMeal(ctx context.Context, meal contract.PlannedMeal) error {
	if meal.PlannedDate.IsZero() {
		meal.PlannedDate = time.Now()
	}
	ingredients, err := marshalStrings(meal.IngredientNames)
	if err != nil {
		return err
	}
	steps, err := marshalStrings(meal.Steps)
	if err != nil {
		return err
	}
	res, err := d.sql.ExecContext(ctx,
		`UPDATE planned_meal
		    SET recipeName = ?, ingredientNames = ?, steps = ?, timeMinutes = ?,
		        plannedDate = ?, calendarEventId = ?
		  WHERE id = ?`,
		meal.RecipeName, ingredients, steps, meal.TimeMinutes,
		formatTime(meal.PlannedDate), nullString(meal.CalendarEventId), meal.ID)
	if err != nil {
		return fmt.Errorf("store: updating planned meal %q: %w", meal.ID, err)
	}
	return requireAffected(res, "planned meal", meal.ID)
}

// DeletePlannedMeal removes one meal. Returns ErrNotFound when it is not there.
func (d *DB) DeletePlannedMeal(ctx context.Context, id string) error {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM planned_meal WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: deleting planned meal %q: %w", id, err)
	}
	return requireAffected(res, "planned meal", id)
}
