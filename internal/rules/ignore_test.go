package rules

import (
	"reflect"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// ignoreSchema has the table names real ignore lists target: migration
// bookkeeping, audit tables and MySQL's upper-case identifiers.
func ignoreSchema() *schema.Schema {
	id := map[string]schema.Column{"id": {Type: "integer", PK: true}}
	return &schema.Schema{Tables: map[string]schema.Table{
		"flyway_schema_history": {Columns: id},
		"FLYWAY_LOCK":           {Columns: id},
		"users":                 {Columns: id},
		"users_audit":           {Columns: id},
		"orders_audit":          {Columns: id},
		"log1":                  {Columns: id},
		"log12":                 {Columns: id},
	}}
}

func TestIgnoredTables_GlobMatching(t *testing.T) {
	cases := []struct {
		name   string
		ignore []string
		want   []IgnoredTable
		all    bool // every schema table, matched by the only glob
	}{
		{"prefix glob matches any case", []string{"flyway_*"}, []IgnoredTable{
			{Table: "FLYWAY_LOCK", Pattern: "flyway_*"},
			{Table: "flyway_schema_history", Pattern: "flyway_*"},
		}, false},
		{"suffix glob", []string{"*_audit"}, []IgnoredTable{
			{Table: "orders_audit", Pattern: "*_audit"},
			{Table: "users_audit", Pattern: "*_audit"},
		}, false},
		{"question mark is exactly one character", []string{"log?"}, []IgnoredTable{
			{Table: "log1", Pattern: "log?"},
		}, false},
		{"exact name is case-insensitive", []string{"USERS"}, []IgnoredTable{
			{Table: "users", Pattern: "USERS"},
		}, false},
		{"first matching glob is reported", []string{"users*", "*_audit"}, []IgnoredTable{
			{Table: "orders_audit", Pattern: "*_audit"},
			{Table: "users", Pattern: "users*"},
			{Table: "users_audit", Pattern: "users*"},
		}, false},
		{"star alone ignores everything", []string{"*"}, nil, true},
		{"blank glob never means everything", []string{"  ", ""}, nil, false},
		{"malformed glob matches nothing", []string{"[users"}, nil, false},
		{"no match", []string{"payments"}, nil, false},
		{"nothing ignored", nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rs := &RuleSet{Ignore: tc.ignore}
			got := rs.IgnoredTables(ignoreSchema())
			if tc.all {
				if len(got) != len(ignoreSchema().Tables) || got[0].Pattern != tc.ignore[0] {
					t.Fatalf("got %+v, want all %d tables", got, len(ignoreSchema().Tables))
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("IgnoredTables = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestIgnoredTables_NilSafe(t *testing.T) {
	var rs *RuleSet
	if got := rs.IgnoredTables(ignoreSchema()); got != nil {
		t.Errorf("nil rule set = %+v", got)
	}
	if got := rs.IgnoredSet(ignoreSchema()); got != nil {
		t.Errorf("nil rule set set = %+v", got)
	}
	if _, ok := rs.IgnorePattern("users"); ok {
		t.Error("nil rule set ignored a table")
	}
	if got := (&RuleSet{Ignore: []string{"*"}}).IgnoredTables(nil); got != nil {
		t.Errorf("nil schema = %+v", got)
	}
}

func TestIgnoredSet_KeysAreSchemaNames(t *testing.T) {
	got := (&RuleSet{Ignore: []string{"flyway_lock", "users"}}).IgnoredSet(ignoreSchema())
	want := map[string]bool{"FLYWAY_LOCK": true, "users": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IgnoredSet = %v, want %v", got, want)
	}
}

func TestValidate_IgnoreIssues(t *testing.T) {
	cases := []struct {
		name     string
		doc      string
		sc       *schema.Schema
		severity Severity
		path     string
		message  string
	}{
		{
			name:     "blank glob is an error",
			doc:      "ignore: [\"  \"]",
			severity: SeverityError, path: "ignore[0]", message: "table pattern is required",
		},
		{
			name:     "malformed glob is an error",
			doc:      "ignore: [users, \"[bad\"]",
			severity: SeverityError, path: "ignore[1]", message: `invalid table pattern "[bad"`,
		},
		{
			name:     "glob matching no table warns",
			doc:      "ignore: [payments_*]",
			sc:       ignoreSchema(),
			severity: SeverityWarning, path: "ignore[0]", message: `pattern "payments_*" matches no table in this database`,
		},
		{
			name: "ignoring a table with explicit rules warns",
			doc: `
ignore: ["*_audit"]
tables:
  USERS_AUDIT:
    rows: 5
`,
			sc:       ignoreSchema(),
			severity: SeverityWarning, path: "tables.USERS_AUDIT", message: `rules for users_audit never apply: users_audit is ignored by "*_audit"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues := parse(t, tc.doc).Validate(tc.sc)
			for _, i := range issues {
				if i.Severity == tc.severity && i.Path == tc.path && i.Message == tc.message {
					return
				}
			}
			t.Fatalf("missing %s %s %q in %v", tc.severity, tc.path, tc.message, issues)
		})
	}
}

func TestValidate_CleanIgnoreHasNoIssues(t *testing.T) {
	rs := parse(t, `
ignore: ["flyway_*", "*_audit"]
tables:
  users:
    rows: 3
`)
	if issues := rs.Validate(ignoreSchema()); len(issues) != 0 {
		t.Fatalf("issues = %v", issues)
	}
	if issues := rs.Validate(nil); len(issues) != 0 {
		t.Fatalf("structural issues = %v", issues)
	}
}

func TestMarshal_RoundTripKeepsIgnore(t *testing.T) {
	rs := parse(t, `
name: ci
ignore:
  - flyway_*
  - "*_audit"
`)
	out, err := rs.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "ignore:") {
		t.Fatalf("marshalled YAML lacks ignore:\n%s", out)
	}
	back := parse(t, string(out))
	if !reflect.DeepEqual(back.Ignore, []string{"flyway_*", "*_audit"}) {
		t.Fatalf("round trip ignore = %v", back.Ignore)
	}
	empty, err := (&RuleSet{Name: "x"}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "ignore") {
		t.Fatalf("empty ignore should be omitted:\n%s", empty)
	}
}
