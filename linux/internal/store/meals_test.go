package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IniTechInc/dinnerDecider/linux/internal/contract"
)

func TestPlannedMealCRUDRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	when := time.Date(2026, 9, 20, 18, 30, 0, 0, time.UTC)

	meal, err := db.InsertPlannedMeal(ctx, contract.PlannedMeal{
		RecipeName:      "Shakshuka",
		IngredientNames: []string{"Eggs", "Tomatoes", "Paprika"},
		Steps:           []string{"Fry the onions.", "Add the tomatoes.", "Crack in the eggs."},
		TimeMinutes:     35,
		PlannedDate:     when,
	})
	if err != nil {
		t.Fatalf("InsertPlannedMeal: %v", err)
	}
	if meal.ID == "" {
		t.Fatal("InsertPlannedMeal did not generate an id")
	}

	got, err := db.GetPlannedMeal(ctx, meal.ID)
	if err != nil {
		t.Fatalf("GetPlannedMeal: %v", err)
	}
	if got.RecipeName != "Shakshuka" || got.TimeMinutes != 35 || !got.PlannedDate.Equal(when) {
		t.Errorf("scalars = %+v", got)
	}
	// Order is part of the value: steps are numbered in the UI.
	if len(got.Steps) != 3 || got.Steps[0] != "Fry the onions." || got.Steps[2] != "Crack in the eggs." {
		t.Errorf("steps = %v", got.Steps)
	}
	if len(got.IngredientNames) != 3 || got.IngredientNames[1] != "Tomatoes" {
		t.Errorf("ingredientNames = %v", got.IngredientNames)
	}

	got.TimeMinutes = 40
	got.Steps = append(got.Steps, "Serve with bread.")
	if err := db.UpdatePlannedMeal(ctx, got); err != nil {
		t.Fatalf("UpdatePlannedMeal: %v", err)
	}
	after, _ := db.GetPlannedMeal(ctx, meal.ID)
	if after.TimeMinutes != 40 || len(after.Steps) != 4 {
		t.Errorf("after update = %+v", after)
	}

	if err := db.DeletePlannedMeal(ctx, meal.ID); err != nil {
		t.Fatalf("DeletePlannedMeal: %v", err)
	}
	if _, err := db.GetPlannedMeal(ctx, meal.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
	if err := db.DeletePlannedMeal(ctx, meal.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
	if err := db.UpdatePlannedMeal(ctx, got); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of a deleted meal = %v, want ErrNotFound", err)
	}
}

// A caller-supplied id is respected, so an importer can preserve identity.
func TestInsertPlannedMealKeepsACallerSuppliedID(t *testing.T) {
	db := openTestDB(t)
	meal, err := db.InsertPlannedMeal(context.Background(), contract.PlannedMeal{
		ID: "11111111-2222-4333-8444-555555555555", RecipeName: "Toast", TimeMinutes: 2,
	})
	if err != nil {
		t.Fatalf("InsertPlannedMeal: %v", err)
	}
	if meal.ID != "11111111-2222-4333-8444-555555555555" {
		t.Errorf("id = %q; a caller-supplied id was overwritten", meal.ID)
	}
	if meal.PlannedDate.IsZero() {
		t.Error("plannedDate was not defaulted to now")
	}
}

// Nil lists must be stored as [] and come back as [], never as null and never
// as nil: the same trap as the taste profile's six lists.
func TestPlannedMealNilListsBecomeEmptyArrays(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	meal, err := db.InsertPlannedMeal(ctx, contract.PlannedMeal{RecipeName: "Water", TimeMinutes: 0})
	if err != nil {
		t.Fatalf("InsertPlannedMeal: %v", err)
	}
	if meal.IngredientNames == nil || meal.Steps == nil {
		t.Error("InsertPlannedMeal returned a nil list; it marshals to JSON null")
	}

	var ingredients, steps string
	if err := db.SQL().QueryRow(
		`SELECT ingredientNames, steps FROM planned_meal WHERE id = ?`, meal.ID).
		Scan(&ingredients, &steps); err != nil {
		t.Fatalf("reading columns: %v", err)
	}
	if ingredients != "[]" || steps != "[]" {
		t.Errorf("stored columns = %q / %q, want [] / []", ingredients, steps)
	}

	got, _ := db.GetPlannedMeal(ctx, meal.ID)
	if got.IngredientNames == nil || got.Steps == nil {
		t.Errorf("read back nil lists: %+v", got)
	}
}

// A null stored by some other writer still reads back as an empty list rather
// than as nil, so a read-modify-write cannot propagate the null.
func TestPlannedMealToleratesStoredNullLists(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if _, err := db.SQL().Exec(
		`INSERT INTO planned_meal (id, recipeName, ingredientNames, steps, timeMinutes, plannedDate)
		 VALUES ('m1', 'Imported', 'null', '', 10, ?)`, formatTime(time.Now())); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	got, err := db.GetPlannedMeal(ctx, "m1")
	if err != nil {
		t.Fatalf("GetPlannedMeal: %v", err)
	}
	if got.IngredientNames == nil || len(got.IngredientNames) != 0 {
		t.Errorf("ingredientNames = %v, want an empty non-nil slice", got.IngredientNames)
	}
	if got.Steps == nil || len(got.Steps) != 0 {
		t.Errorf("steps = %v, want an empty non-nil slice", got.Steps)
	}
}

// calendarEventId is nullable and nothing on Linux writes it — Calendar is not
// applicable here. The column is kept for schema compatibility, and a value
// that arrived with an import must survive a read-modify-write.
func TestCalendarEventIDIsNullableAndPreserved(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	meal, err := db.InsertPlannedMeal(ctx, contract.PlannedMeal{RecipeName: "Soup", TimeMinutes: 20})
	if err != nil {
		t.Fatalf("InsertPlannedMeal: %v", err)
	}
	got, _ := db.GetPlannedMeal(ctx, meal.ID)
	if got.CalendarEventId != nil {
		t.Errorf("calendarEventId = %v, want nil on Linux", *got.CalendarEventId)
	}

	imported := "EK-12345"
	got.CalendarEventId = &imported
	if err := db.UpdatePlannedMeal(ctx, got); err != nil {
		t.Fatalf("UpdatePlannedMeal: %v", err)
	}
	after, _ := db.GetPlannedMeal(ctx, meal.ID)
	if after.CalendarEventId == nil || *after.CalendarEventId != imported {
		t.Errorf("calendarEventId = %v, want it preserved", after.CalendarEventId)
	}
}

func TestListPlannedMealsIsChronologicalAndNeverNil(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	meals, err := db.ListPlannedMeals(ctx)
	if err != nil {
		t.Fatalf("ListPlannedMeals: %v", err)
	}
	if meals == nil {
		t.Error("an empty plan returned a nil slice; it marshals to JSON null")
	}

	base := time.Date(2026, 9, 20, 18, 0, 0, 0, time.UTC)
	for i, name := range []string{"Wednesday", "Monday", "Tuesday"} {
		offsets := map[string]int{"Monday": 0, "Tuesday": 24, "Wednesday": 48}
		if _, err := db.InsertPlannedMeal(ctx, contract.PlannedMeal{
			RecipeName:  name,
			TimeMinutes: 10 + i,
			PlannedDate: base.Add(time.Duration(offsets[name]) * time.Hour),
		}); err != nil {
			t.Fatalf("insert %q: %v", name, err)
		}
	}
	meals, _ = db.ListPlannedMeals(ctx)
	if len(meals) != 3 {
		t.Fatalf("len = %d, want 3", len(meals))
	}
	if meals[0].RecipeName != "Monday" || meals[1].RecipeName != "Tuesday" || meals[2].RecipeName != "Wednesday" {
		t.Errorf("order = %v %v %v", meals[0].RecipeName, meals[1].RecipeName, meals[2].RecipeName)
	}
}

// The range query compares stored TEXT, which is only correct because every
// timestamp is UTC and fixed width.
func TestListPlannedMealsBetweenIsHalfOpen(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 4; i++ {
		if _, err := db.InsertPlannedMeal(ctx, contract.PlannedMeal{
			RecipeName:  string(rune('A' + i)),
			PlannedDate: base.Add(time.Duration(i) * 24 * time.Hour),
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	// [day0, day2) is two meals: the lower bound is included, the upper is not.
	got, err := db.ListPlannedMealsBetween(ctx, base, base.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("ListPlannedMealsBetween: %v", err)
	}
	if len(got) != 2 || got[0].RecipeName != "A" || got[1].RecipeName != "B" {
		t.Errorf("window = %v", got)
	}
	// A timestamp in a non-UTC zone still selects correctly, because the
	// bounds are converted before they are compared.
	tokyo := time.FixedZone("JST", 9*3600)
	got, err = db.ListPlannedMealsBetween(ctx, base.In(tokyo), base.Add(48*time.Hour).In(tokyo))
	if err != nil {
		t.Fatalf("ListPlannedMealsBetween in another zone: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("window in another zone returned %d meals, want 2", len(got))
	}
	// An empty window is an empty non-nil slice.
	got, _ = db.ListPlannedMealsBetween(ctx, base.Add(-48*time.Hour), base)
	if got == nil || len(got) != 0 {
		t.Errorf("empty window = %v", got)
	}
}
