package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// sharedSchemaDir is shared/schema relative to linux/internal/contract.
const sharedSchemaDir = "../../../shared/schema"

// TestTypesJSONTags pins every JSON tag on every contract type, in declaration
// order, against the property names in shared/schema/*.schema.json.
//
// This is the boring-but-fatal class: a typo renaming timeMinutes to
// time_minutes breaks the wire and nothing else notices.
func TestTypesJSONTags(t *testing.T) {
	cases := []struct {
		name string
		typ  any
		want []string
	}{
		{"IdentifiedItem", IdentifiedItem{}, []string{
			"name", "brand", "category", "confidence",
		}},
		{"InventoryItem", InventoryItem{}, []string{
			"name", "brand", "categoryRaw", "quantity", "dateAdded",
			"sourcePhotoID", "isStaple",
		}},
		{"ShoppingListItem", ShoppingListItem{}, []string{
			"name", "isChecked", "dateAdded", "isManual",
		}},
		{"IngredientLine", IngredientLine{}, []string{
			"name", "hasIt",
		}},
		{"RecipeSuggestion", RecipeSuggestion{}, []string{
			"name", "ingredients", "steps", "timeMinutes", "missingItems",
		}},
		{"RecipeBundleResponse", RecipeBundleResponse{}, []string{
			"makeNow", "almostThere",
		}},
		{"PlannedMeal", PlannedMeal{}, []string{
			"id", "recipeName", "ingredientNames", "steps", "timeMinutes",
			"plannedDate", "calendarEventId",
		}},
		{"TasteProfile", TasteProfile{}, []string{
			"favoriteFoods", "favoriteFoodsOther",
			"dislikedFoods", "dislikedFoodsOther",
			"cuisinesLoved", "cuisinesLovedOther",
			"cuisinesAvoided", "cuisinesAvoidedOther",
			"allergies", "allergiesOther",
			"texturesAvoided", "texturesAvoidedOther",
			"spiceLevel",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := jsonTags(t, tc.typ)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("json tags:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestTypesJSONTagsMatchSharedSchemaProperties cross-checks the same tags
// against the schema files themselves, so the literals above cannot drift away
// from shared/ without a failure.
func TestTypesJSONTagsMatchSharedSchemaProperties(t *testing.T) {
	cases := []struct {
		name    string
		typ     any
		file    string
		pointer []string // "" for the root schema, else $defs path
	}{
		{"IdentifiedItem", IdentifiedItem{}, "identified-item.schema.json", nil},
		{"InventoryItem", InventoryItem{}, "inventory-item.schema.json", nil},
		{"ShoppingListItem", ShoppingListItem{}, "shopping-list-item.schema.json", nil},
		{"TasteProfile", TasteProfile{}, "taste-profile.schema.json", nil},
		{"RecipeBundleResponse", RecipeBundleResponse{}, "recipe.schema.json", nil},
		{"IngredientLine", IngredientLine{}, "recipe.schema.json", []string{"$defs", "IngredientLine"}},
		{"RecipeSuggestion", RecipeSuggestion{}, "recipe.schema.json", []string{"$defs", "RecipeSuggestion"}},
		{"PlannedMeal", PlannedMeal{}, "recipe.schema.json", []string{"$defs", "PlannedMeal"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := readSchemaNode(t, tc.file, tc.pointer)
			props, ok := node["properties"].(map[string]any)
			if !ok {
				t.Fatalf("%s: no properties object", tc.file)
			}
			want := make(map[string]bool, len(props))
			for k := range props {
				want[k] = true
			}
			got := make(map[string]bool)
			for _, tag := range jsonTags(t, tc.typ) {
				got[tag] = true
			}
			for k := range want {
				if !got[k] {
					t.Errorf("schema property %q has no Go field", k)
				}
			}
			for k := range got {
				if !want[k] {
					t.Errorf("Go json tag %q is not a schema property", k)
				}
			}
		})
	}
}

// TestScanPhaseHasSixValues pins the six phases and their wire strings.
// `failed` is shipped iOS behavior, not a Linux invention: reusing `empty` for
// it blames the user's photo for our failure.
func TestScanPhaseHasSixValues(t *testing.T) {
	all := []ScanPhase{Idle, Scanning, Finished, Empty, Failed, Cancelled}
	want := []string{"idle", "scanning", "finished", "empty", "failed", "cancelled"}

	if len(all) != 6 {
		t.Fatalf("want 6 scan phases, got %d", len(all))
	}
	for i, p := range all {
		if p.String() != want[i] {
			t.Errorf("phase %d: got %q want %q", i, p.String(), want[i])
		}
	}
	if Failed == Empty {
		t.Fatal("Failed and Empty must stay distinct")
	}

	// The wire form is the raw string, never an ordinal.
	b, err := json.Marshal(struct {
		Phase ScanPhase `json:"phase"`
	}{Phase: Failed})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `{"phase":"failed"}` {
		t.Errorf("got %s", b)
	}
}

// TestUnmarshalIdentifiedItemMissingRequiredKeysIsNull is decode rule 1.
// encoding/json silently zero-fills a missing key; Swift refuses the input.
func TestUnmarshalIdentifiedItemMissingRequiredKeysIsNull(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"missing all but name", `{"name": "Milk"}`},
		{"missing name", `{"category": "dairy", "confidence": 0.9}`},
		{"missing category", `{"name": "Milk", "confidence": 0.9}`},
		{"missing confidence", `{"name": "Milk", "category": "dairy"}`},
		{"explicit null name", `{"name": null, "category": "dairy", "confidence": 0.9}`},
		{"explicit null category", `{"name": "Milk", "category": null, "confidence": 0.9}`},
		{"explicit null confidence", `{"name": "Milk", "category": "dairy", "confidence": null}`},
		{"empty object", `{}`},
		{"json null", `null`},
		{"malformed", `{ not: valid json }`},
		{"empty input", ``},
		{"wrong type for confidence", `{"name": "Milk", "category": "dairy", "confidence": "high"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if item, ok := UnmarshalIdentifiedItem([]byte(tc.input)); ok {
				t.Fatalf("want no value, got %+v", item)
			}
		})
	}

	// The guard is real: a plain json.Unmarshal accepts the same input.
	var naive IdentifiedItem
	if err := json.Unmarshal([]byte(`{"name": "Milk"}`), &naive); err != nil {
		t.Fatalf("precondition: plain Unmarshal should succeed, got %v", err)
	}
	if naive.Category != "" || naive.Confidence != 0 {
		t.Fatal("precondition: plain Unmarshal should zero-fill")
	}
}

// TestUnmarshalIdentifiedItemBrandIsThreeWay is decode rule 2. Absent and
// explicit null both land on nil, and neither is the empty string.
func TestUnmarshalIdentifiedItemBrandIsThreeWay(t *testing.T) {
	absent, ok := UnmarshalIdentifiedItem([]byte(`{"name": "Eggs", "category": "dairy", "confidence": 0.8}`))
	if !ok {
		t.Fatal("absent brand must still decode")
	}
	if absent.Brand != nil {
		t.Errorf("absent brand: want nil, got %q", *absent.Brand)
	}

	explicitNull, ok := UnmarshalIdentifiedItem([]byte(`{"name": "Eggs", "brand": null, "category": "dairy", "confidence": 0.8}`))
	if !ok {
		t.Fatal("null brand must still decode")
	}
	if explicitNull.Brand != nil {
		t.Errorf("null brand: want nil, got %q", *explicitNull.Brand)
	}

	emptyString, ok := UnmarshalIdentifiedItem([]byte(`{"name": "Eggs", "brand": "", "category": "dairy", "confidence": 0.8}`))
	if !ok {
		t.Fatal(`brand "" must decode`)
	}
	if emptyString.Brand == nil {
		t.Fatal(`brand "" must NOT collapse to nil: it is a third, distinct value`)
	}
	if *emptyString.Brand != "" {
		t.Errorf(`want "", got %q`, *emptyString.Brand)
	}

	present, ok := UnmarshalIdentifiedItem([]byte(`{"name": "Whole Milk", "brand": "Horizon", "category": "dairy", "confidence": 0.92}`))
	if !ok {
		t.Fatal("present brand must decode")
	}
	if present.Brand == nil || *present.Brand != "Horizon" {
		t.Errorf("want Horizon, got %v", present.Brand)
	}
	if present.Name != "Whole Milk" || present.Category != "dairy" || present.Confidence != 0.92 {
		t.Errorf("unexpected item: %+v", present)
	}
}

// TestUnmarshalIdentifiedItemsEmptyIsNotFailure pins the nil-slice trap: an
// empty array is a real, successful "saw nothing" answer.
func TestUnmarshalIdentifiedItemsEmptyIsNotFailure(t *testing.T) {
	items, ok := UnmarshalIdentifiedItems([]byte(`[]`))
	if !ok {
		t.Fatal("[] must decode successfully")
	}
	if items == nil {
		t.Fatal("[] must produce a NON-NIL empty slice; nil means parse failure")
	}
	if len(items) != 0 {
		t.Fatalf("want 0 items, got %d", len(items))
	}
}

func TestUnmarshalIdentifiedItemsRejects(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"json null", `null`},
		{"object when array expected", `{"name": "Milk", "brand": null, "category": "dairy", "confidence": 0.9}`},
		{"truncated", `[{"name": "Milk"},`},
		{"empty input", ``},
		{"one bad element fails the whole array", `[{"name": "Milk", "category": "dairy", "confidence": 0.9}, {"name": "Eggs"}]`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, ok := UnmarshalIdentifiedItems([]byte(tc.input))
			if ok {
				t.Fatalf("want no value, got %+v", items)
			}
			if items != nil {
				t.Fatal("a failed decode must return a nil slice")
			}
		})
	}
}

func TestUnmarshalIdentifiedItemsMulti(t *testing.T) {
	const input = `[{"name": "Milk", "brand": "Horizon", "category": "dairy", "confidence": 0.9},
	 {"name": "Eggs", "brand": null, "category": "dairy", "confidence": 0.85}]`

	items, ok := UnmarshalIdentifiedItems([]byte(input))
	if !ok {
		t.Fatal("want a value")
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].Brand == nil || *items[0].Brand != "Horizon" {
		t.Errorf("item 0 brand: %v", items[0].Brand)
	}
	if items[1].Brand != nil {
		t.Errorf("item 1 brand: want nil, got %q", *items[1].Brand)
	}
}

// TestTypesValidateAgainstSharedSchemas marshals a representative instance of
// each type and validates it against the real schema file.
func TestTypesValidateAgainstSharedSchemas(t *testing.T) {
	brand := "Horizon"
	photoID := "photo-1"
	when := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		value   any
		file    string
		pointer string
	}{
		{"IdentifiedItem", IdentifiedItem{
			Name: "Whole Milk", Brand: &brand, Category: "dairy", Confidence: 0.92,
		}, "identified-item.schema.json", ""},
		{"IdentifiedItem/null brand", IdentifiedItem{
			Name: "Eggs", Category: "dairy", Confidence: 0.8,
		}, "identified-item.schema.json", ""},
		{"InventoryItem", InventoryItem{
			Name: "Whole Milk", Brand: &brand, CategoryRaw: "dairy", Quantity: 2,
			DateAdded: when, SourcePhotoID: &photoID, IsStaple: false,
		}, "inventory-item.schema.json", ""},
		{"ShoppingListItem", ShoppingListItem{
			Name: "Butter", IsChecked: false, DateAdded: when, IsManual: true,
		}, "shopping-list-item.schema.json", ""},
		{"RecipeBundleResponse", RecipeBundleResponse{
			MakeNow: []RecipeSuggestion{{
				Name:         "Scrambled Eggs",
				Ingredients:  []IngredientLine{{Name: "Eggs", HasIt: true}},
				Steps:        []string{"Beat the eggs.", "Cook low and slow."},
				TimeMinutes:  10,
				MissingItems: []string{},
			}},
			AlmostThere: []RecipeSuggestion{},
		}, "recipe.schema.json", ""},
		{"PlannedMeal", PlannedMeal{
			ID:              "6f1a1f6c-6a1e-4b2f-9a3d-1f2e3d4c5b6a",
			RecipeName:      "Scrambled Eggs",
			IngredientNames: []string{"Eggs"},
			Steps:           []string{"Beat the eggs."},
			TimeMinutes:     10,
			PlannedDate:     when,
		}, "recipe.schema.json", "#/$defs/PlannedMeal"},
		{"TasteProfile", TasteProfile{
			FavoriteFoods: []string{}, DislikedFoods: []string{},
			CuisinesLoved: []string{}, CuisinesAvoided: []string{},
			Allergies: []string{"peanuts"}, TexturesAvoided: []string{},
			SpiceLevel: 3,
		}, "taste-profile.schema.json", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sch := compileSharedSchema(t, tc.file, tc.pointer)
			if err := sch.Validate(marshalToAny(t, tc.value)); err != nil {
				t.Fatalf("instance does not satisfy %s%s:\n%v", tc.file, tc.pointer, err)
			}
		})
	}
}

// TestZeroTasteProfileIsNotSchemaValid records the write rule the store must
// honour: nil Go slices marshal to JSON null, and null is not an array. A blob
// written that way is unreadable by iOS, silently losing the profile including
// allergies. Always write [] for the six lists.
func TestZeroTasteProfileIsNotSchemaValid(t *testing.T) {
	sch := compileSharedSchema(t, "taste-profile.schema.json", "")
	if err := sch.Validate(marshalToAny(t, TasteProfile{})); err == nil {
		t.Fatal("a zero TasteProfile marshals its lists as null and must NOT validate")
	}
}

// --- helpers ---

func jsonTags(t *testing.T, v any) []string {
	t.Helper()
	typ := reflect.TypeOf(v)
	if typ.Kind() != reflect.Struct {
		t.Fatalf("not a struct: %T", v)
	}
	tags := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		field := typ.Field(i)
		tag, ok := field.Tag.Lookup("json")
		if !ok {
			t.Fatalf("%s.%s has no json tag", typ.Name(), field.Name)
		}
		if tag == "" || tag == "-" {
			t.Fatalf("%s.%s has json tag %q", typ.Name(), field.Name, tag)
		}
		if name, _, found := strings.Cut(tag, ","); found {
			t.Fatalf("%s.%s json tag %q carries options; the contract types take none (omitempty would drop required keys)", typ.Name(), field.Name, name)
		}
		tags = append(tags, tag)
	}
	return tags
}

func readSchemaFile(t *testing.T, file string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(sharedSchemaDir, file))
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return doc
}

func readSchemaNode(t *testing.T, file string, pointer []string) map[string]any {
	t.Helper()
	node := readSchemaFile(t, file)
	for _, key := range pointer {
		next, ok := node[key].(map[string]any)
		if !ok {
			t.Fatalf("%s: no node at %q", file, key)
		}
		node = next
	}
	return node
}

func compileSharedSchema(t *testing.T, file, pointer string) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join(sharedSchemaDir, file)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("%s: not a JSON object", file)
	}
	id, ok := obj["$id"].(string)
	if !ok {
		t.Fatalf("%s: no $id", file)
	}

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(id, doc); err != nil {
		t.Fatalf("add %s: %v", file, err)
	}
	sch, err := compiler.Compile(id + pointer)
	if err != nil {
		t.Fatalf("compile %s%s: %v", file, pointer, err)
	}
	return sch
}

func marshalToAny(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	return inst
}
