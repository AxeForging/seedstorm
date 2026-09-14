package faker

import (
	"fmt"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

func usersSchema() *schema.Schema {
	return &schema.Schema{Tables: map[string]schema.Table{
		"users": {Columns: map[string]schema.Column{
			"id":     {Type: "integer", PK: true},
			"email":  {Type: "varchar", DDLType: "varchar(20)", Faker: "email"},
			"status": {Type: "varchar", Faker: "randomstring(active,banned)"},
			"age":    {Type: "integer", Faker: "number(18,90)"},
		}},
	}}
}

func TestGenerate_OverridesRewriteColumnsAndSeeTheAutoValue(t *testing.T) {
	autos := make([]interface{}, 0)
	opts := DefaultGenerateOptions()
	opts.Overrides = Overrides{"users": {
		"email": func(row int, auto interface{}) (interface{}, error) {
			autos = append(autos, auto)
			return fmt.Sprintf("ss+%d@x.io", row+1), nil
		},
	}}
	s := usersSchema()
	data, err := GenerateWithOptions(s, []string{"users"}, 3, 0, nil, "pgx", opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(data["users"]) < 3 {
		t.Fatalf("rows = %d", len(data["users"]))
	}
	for i, row := range data["users"] {
		if want := fmt.Sprintf("ss+%d@x.io", i+1); row["email"] != want {
			t.Fatalf("row %d email = %v, want %s", i, row["email"], want)
		}
	}
	for _, a := range autos {
		if s, ok := a.(string); !ok || !strings.Contains(s, "@") {
			t.Fatalf("auto value = %#v, want the generated email", a)
		}
	}
}

func TestGenerate_RowOffsetContinuesTheOverrideIndex(t *testing.T) {
	opts := DefaultGenerateOptions()
	opts.RowOffset = map[string]int{"users": 5000}
	opts.Overrides = Overrides{"users": {
		"email": func(row int, _ interface{}) (interface{}, error) { return fmt.Sprintf("u%d", row), nil },
	}}
	data, err := GenerateFilteredWithOptions(usersSchema(), []string{"users"}, []string{"users"}, 0, 0, map[string]int{"users": 2}, nil, "pgx", opts)
	if err != nil {
		t.Fatal(err)
	}
	if data["users"][0]["email"] != "u5000" || data["users"][1]["email"] != "u5001" {
		t.Fatalf("emails = %v, %v", data["users"][0]["email"], data["users"][1]["email"])
	}
}

func TestGenerate_OverrideOnEnumColumnDisablesEnumTopUp(t *testing.T) {
	s := usersSchema()
	constant := func(int, interface{}) (interface{}, error) { return "active", nil }

	plain, err := GenerateWithOptions(s, []string{"users"}, 2, 0, nil, "pgx", DefaultGenerateOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(plain["users"]) != 4 {
		t.Fatalf("baseline rows = %d, want 4 (2 per enum value)", len(plain["users"]))
	}

	opts := DefaultGenerateOptions()
	opts.Overrides = Overrides{"users": {"status": constant}}
	ruled, err := GenerateWithOptions(s, []string{"users"}, 2, 0, nil, "pgx", opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(ruled["users"]) != 2 {
		t.Fatalf("rows with a status rule = %d, want exactly 2", len(ruled["users"]))
	}
}

func TestGenerate_OverrideErrorNamesTableAndColumn(t *testing.T) {
	opts := DefaultGenerateOptions()
	opts.Overrides = Overrides{"users": {
		"age": func(int, interface{}) (interface{}, error) { return "forty", nil },
	}}
	_, err := GenerateWithOptions(usersSchema(), []string{"users"}, 1, 0, nil, "pgx", opts)
	if err == nil || !strings.Contains(err.Error(), "users.age") || !strings.Contains(err.Error(), "forty") {
		t.Fatalf("err = %v, want it to name users.age and the bad value", err)
	}
}

func TestCoerceValue(t *testing.T) {
	varchar5 := schema.Column{Type: "varchar", DDLType: "varchar(5)"}
	cases := []struct {
		name    string
		col     schema.Column
		in      interface{}
		want    interface{}
		wantErr bool
	}{
		{"numeric string to int", schema.Column{Type: "integer"}, "42", int64(42), false},
		{"decimal string to float", schema.Column{Type: "numeric"}, "4.5", 4.5, false},
		{"garbage into int fails", schema.Column{Type: "bigint"}, "4x", nil, true},
		{"native int passes", schema.Column{Type: "integer"}, 3, 3, false},
		{"bool parse", schema.Column{Type: "boolean"}, "true", true, false},
		{"bool garbage fails", schema.Column{Type: "bool"}, "yes please", nil, true},
		{"string truncated to length", varchar5, "abcdefgh", "abcde", false},
		{"number stringified for text", schema.Column{Type: "text"}, 12, "12", false},
		{"nil stays nil", schema.Column{Type: "integer"}, nil, nil, false},
		{"mysql boolean tinyint takes true", schema.Column{Type: "tinyint"}, "true", true, false},
		{"mysql boolean tinyint takes false", schema.Column{Type: "tinyint"}, "FALSE", false, false},
		{"tinyint still takes numbers", schema.Column{Type: "tinyint"}, "3", int64(3), false},
		{"bit takes true", schema.Column{Type: "bit"}, "true", true, false},
		{"bit takes 0", schema.Column{Type: "bit"}, "0", false, false},
		{"integer does not take true", schema.Column{Type: "integer"}, "true", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CoerceValue(c.col, c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Fatalf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestCatalog_MatchesEngine(t *testing.T) {
	seen := map[string]bool{}
	for _, g := range Catalog() {
		seen[g.Name] = true
		if !ValidFaker(g.Expr) {
			t.Errorf("catalog expr %q is not accepted by ValidFaker", g.Expr)
		}
		if _, err := Evaluate(g.Expr); err != nil {
			t.Errorf("Evaluate(%q): %v", g.Expr, err)
		}
	}
	for name := range knownFakers {
		if name != uniqueSequenceFaker && !seen[name] {
			t.Errorf("engine generator %q missing from catalog", name)
		}
	}
	for name := range knownParamFakers {
		if !seen[name] {
			t.Errorf("engine generator %q missing from catalog", name)
		}
	}
}

func TestEvaluate_RejectsUnknownAndPatterns(t *testing.T) {
	for _, expr := range []string{"", "emial", "sequence", "nope(1)"} {
		if _, err := Evaluate(expr); err == nil {
			t.Errorf("Evaluate(%q) = nil error, want unknown generator", expr)
		}
	}
	v, err := Evaluate("numerify(AB-###)")
	if err != nil {
		t.Fatal(err)
	}
	if s := v.(string); len(s) != 6 || !strings.HasPrefix(s, "AB-") || strings.Contains(s, "#") {
		t.Fatalf("numerify = %q", s)
	}
}
