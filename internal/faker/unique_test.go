package faker

import (
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// accountsSchema mirrors Keycloak's user_entity: UNIQUE (realm_id, username)
// and UNIQUE (realm_id, email_constraint) on top of a string primary key.
func accountsSchema() *schema.Schema {
	return &schema.Schema{Tables: map[string]schema.Table{
		"accounts": {
			Columns: map[string]schema.Column{
				"id":       {Type: "character varying", DDLType: "character varying(36)", PK: true},
				"realm_id": {Type: "character varying", Faker: "word"},
				"handle":   {Type: "character varying", Faker: "randomstring(a,b,c,d)"},
				"note":     {Type: "text", Faker: "sentence"},
			},
			Unique: [][]string{{"realm_id", "handle"}},
		},
	}}
}

func tupleCounts(rows []map[string]interface{}, cols ...string) map[string]int {
	counts := map[string]int{}
	for _, row := range rows {
		parts := make([]string, len(cols))
		for i, c := range cols {
			parts[i] = keyValue("text", row[c])
		}
		counts[strings.Join(parts, "|")]++
	}
	return counts
}

func TestGenerate_MultiColumnUniqueRegeneratesAFreeColumn(t *testing.T) {
	s := accountsSchema()
	acc := s.Tables["accounts"]
	handle := acc.Columns["handle"]
	handle.Faker = "numerify(###)" // 1000 values: duplicates in 400 rows are near certain, all breakable
	acc.Columns["handle"] = handle
	s.Tables["accounts"] = acc
	opts := DefaultGenerateOptions()
	// A rule pins realm_id, so only handle can make rows distinct.
	opts.Overrides = Overrides{"accounts": {
		"realm_id": func(int, interface{}) (interface{}, error) { return "realm-a", nil },
	}}

	data, err := GenerateFilteredWithOptions(s, []string{"accounts"}, []string{"accounts"}, 0, 0, map[string]int{"accounts": 400}, nil, "pgx", opts)
	if err != nil {
		t.Fatal(err)
	}
	for tuple, n := range tupleCounts(data["accounts"], "realm_id", "handle") {
		if n > 1 {
			t.Fatalf("tuple %s appears %d times", tuple, n)
		}
	}
	if len(data["accounts"]) != 400 {
		t.Fatalf("rows = %d, want 400 (duplicates broken by regenerating handle)", len(data["accounts"]))
	}
}

func TestGenerate_MultiColumnUniqueDropsRowsThatCannotBeMadeDistinct(t *testing.T) {
	var warnings []GenerationWarning
	opts := DefaultGenerateOptions()
	opts.OnWarning = func(w GenerationWarning) { warnings = append(warnings, w) }
	// realm_id fixed by a rule and handle limited to 4 values: at most 4 rows fit.
	opts.Overrides = Overrides{"accounts": {
		"realm_id": func(int, interface{}) (interface{}, error) { return "realm-a", nil },
	}}
	data, err := GenerateFilteredWithOptions(accountsSchema(), []string{"accounts"}, []string{"accounts"}, 0, 0, map[string]int{"accounts": 20}, nil, "pgx", opts)
	if err != nil {
		t.Fatal(err)
	}
	rows := data["accounts"]
	if len(rows) == 0 || len(rows) > 4 {
		t.Fatalf("rows = %d, want between 1 and 4 distinct (realm_id, handle) pairs", len(rows))
	}
	for tuple, n := range tupleCounts(rows, "realm_id", "handle") {
		if n > 1 {
			t.Fatalf("tuple %s appears %d times", tuple, n)
		}
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Reason, "UNIQUE (realm_id, handle)") || warnings[0].Generated != len(rows) {
		t.Fatalf("warnings = %+v", warnings)
	}
}

func TestGenerate_DroppedRowsLeaveNoDanglingChildReferences(t *testing.T) {
	s := accountsSchema()
	s.Tables["sessions"] = schema.Table{Columns: map[string]schema.Column{
		"id":         {Type: "integer", PK: true},
		"account_id": {Type: "character varying", FK: "accounts.id"},
	}}
	opts := DefaultGenerateOptions()
	opts.Overrides = Overrides{"accounts": {
		"realm_id": func(int, interface{}) (interface{}, error) { return "realm-a", nil },
	}}
	data, err := GenerateFilteredWithOptions(s, []string{"accounts", "sessions"}, []string{"accounts", "sessions"}, 0, 0, map[string]int{"accounts": 20, "sessions": 50}, nil, "pgx", opts)
	if err != nil {
		t.Fatal(err)
	}
	kept := map[interface{}]bool{}
	for _, row := range data["accounts"] {
		kept[row["id"]] = true
	}
	for _, row := range data["sessions"] {
		if !kept[row["account_id"]] {
			t.Fatalf("session references dropped account %v", row["account_id"])
		}
	}
}

func TestEnforceUniqueGroups_TreatsStoredTuplesAsTaken(t *testing.T) {
	tbl := accountsSchema().Tables["accounts"]
	group := []string{"realm_id", "handle"}
	stored := map[string]*keySet{
		groupID(group): keysOf(uniqueTupleKey(tbl, group, map[string]interface{}{"realm_id": "r", "handle": "a"})),
	}
	rows := []map[string]interface{}{
		{"id": "x1", "realm_id": "r", "handle": "a"}, // collides with the stored row
		{"id": "x2", "realm_id": "r", "handle": "b"},
	}
	kept, _ := enforceUniqueGroups(rows, tbl, nil, stored)
	seenX2 := false
	for _, row := range kept {
		if row["realm_id"] == "r" && row["handle"] == "a" {
			t.Fatalf("kept a tuple that is already stored: %v", row)
		}
		if row["id"] == "x2" {
			seenX2 = row["handle"] == "b"
		}
	}
	if !seenX2 {
		t.Fatalf("the non-colliding row must be kept unchanged: %v", kept)
	}
}

func TestBuildSchema_RecordsMultiColumnUniqueGroups(t *testing.T) {
	sc := BuildSchema("pgx", []db.Table{{
		Name:    "user_entity",
		Columns: []db.Column{{Name: "id", Type: "varchar", IsPK: true}, {Name: "realm_id", Type: "varchar"}, {Name: "username", Type: "varchar"}},
		Indexes: []db.Index{
			{Name: "uk_realm_username", Columns: []string{"realm_id", "username"}, Unique: true},
			{Name: "idx_realm", Columns: []string{"realm_id", "id"}},
		},
	}})
	got := sc.Tables["user_entity"].Unique
	if len(got) != 1 || strings.Join(got[0], ",") != "realm_id,username" {
		t.Fatalf("unique groups = %v", got)
	}
}

func TestEnforceUniqueGroups_SingleColumnUniqueRewrittenByARule(t *testing.T) {
	tbl := schema.Table{Columns: map[string]schema.Column{
		"id":    {Type: "integer", PK: true},
		"email": {Type: "character varying", Faker: "uuid", Unique: true},
	}}
	stored := map[string]*keySet{"email": keysOf(uniqueTupleKey(tbl, []string{"email"}, map[string]interface{}{"email": "u1@x.io"}))}
	rule := map[string]ColumnOverride{"email": func(int, interface{}) (interface{}, error) { return nil, nil }}
	rows := []map[string]interface{}{{"id": 1, "email": "u1@x.io"}, {"id": 2, "email": "u2@x.io"}, {"id": 3, "email": "u2@x.io"}}
	kept, dropped := enforceUniqueGroups(rows, tbl, rule, stored)
	if len(kept) != 1 || kept[0]["id"] != 2 || dropped["UNIQUE (email)"] != 2 {
		t.Fatalf("kept = %v dropped = %v", kept, dropped)
	}
}
