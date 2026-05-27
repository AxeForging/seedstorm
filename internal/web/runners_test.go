package web

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

type testJobControl struct{}

func (testJobControl) Write(p []byte) (int, error) { return len(p), nil }
func (testJobControl) Phase(string)                {}
func (testJobControl) Progress(int, int, string)   {}

func TestTableRowCounts(t *testing.T) {
	data := map[string][]map[string]any{
		"users": {
			{"id": 1},
			{"id": 2},
		},
		"orders": {
			{"id": 10},
		},
	}

	counts, total := tableRowCounts(data, []string{"users", "orders", "missing"})

	want := map[string]int{"users": 2, "orders": 1, "missing": 0}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("counts = %+v, want %+v", counts, want)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
}

func TestCleanTableRowsKeepsPositiveOverrides(t *testing.T) {
	got := cleanTableRows(map[string]int{
		"users":  4,
		"orders": 0,
		"":       9,
		"items":  -1,
	})

	want := map[string]int{"users": 4}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanTableRows = %+v, want %+v", got, want)
	}
}

func TestCleanTableRowsReturnsNilForEmptyInput(t *testing.T) {
	if got := cleanTableRows(nil); got != nil {
		t.Fatalf("cleanTableRows(nil) = %+v, want nil", got)
	}
	if got := cleanTableRows(map[string]int{"users": 0}); got != nil {
		t.Fatalf("cleanTableRows(non-positive only) = %+v, want nil", got)
	}
}

func TestRunSeedDryRunAppliesTableRowOverrides(t *testing.T) {
	srv, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := &Session{
		DBType: "pgx",
		schema: runnerRowCountSchema(),
	}

	result, err := srv.runSeed(context.Background(), sess, SeedRequest{
		Rows:      2,
		BatchSize: 100,
		DryRun:    true,
		TableRows: map[string]int{"orders": 4},
	}, testJobControl{})
	if err != nil {
		t.Fatalf("runSeed: %v", err)
	}
	if got := result["totalRows"]; got != 6 {
		t.Fatalf("totalRows = %v, want 6", got)
	}
	counts, ok := result["tableCounts"].(map[string]int)
	if !ok {
		t.Fatalf("tableCounts type = %T, want map[string]int", result["tableCounts"])
	}
	if counts["users"] != 2 || counts["orders"] != 4 {
		t.Fatalf("tableCounts = %+v, want users=2 orders=4", counts)
	}
	if result["output"] == "" {
		t.Fatalf("dry-run output should include generated SQL")
	}
}

func TestRunGenerateAppliesTableRowOverridesWithoutBreakingDefaults(t *testing.T) {
	srv, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := &Session{
		DBType: "pgx",
		schema: runnerRowCountSchema(),
	}

	result, err := srv.runGenerate(context.Background(), sess, GenerateRequest{
		Rows:      3,
		Format:    "yaml",
		TableRows: map[string]int{"orders": 1},
	}, testJobControl{})
	if err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	if got := result["totalRows"]; got != 4 {
		t.Fatalf("totalRows = %v, want 4", got)
	}
	counts, ok := result["tableCounts"].(map[string]int)
	if !ok {
		t.Fatalf("tableCounts type = %T, want map[string]int", result["tableCounts"])
	}
	if counts["users"] != 3 || counts["orders"] != 1 {
		t.Fatalf("tableCounts = %+v, want users=3 orders=1", counts)
	}
}

func TestRunSeedDryRunHandlesHardSelfReference(t *testing.T) {
	srv, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := &Session{
		DBType: "pgx",
		schema: hardSelfReferenceSchema(),
	}

	depth := 2
	result, err := srv.runSeed(context.Background(), sess, SeedRequest{
		Rows:         3,
		BatchSize:    100,
		SelfRefDepth: &depth,
		DryRun:       true,
	}, testJobControl{})
	if err != nil {
		t.Fatalf("runSeed: %v", err)
	}
	if got := result["totalRows"]; got != 3 {
		t.Fatalf("totalRows = %v, want 3", got)
	}
	output, _ := result["output"].(string)
	if output == "" || !containsAll(output, "employees", "manager_id") {
		t.Fatalf("dry-run output should include self-referential insert SQL, got %q", output)
	}
}

func TestRunGenerateHandlesHardSelfReference(t *testing.T) {
	srv, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := &Session{
		DBType: "pgx",
		schema: hardSelfReferenceSchema(),
	}

	depth := 2
	result, err := srv.runGenerate(context.Background(), sess, GenerateRequest{
		Rows:         3,
		SelfRefDepth: &depth,
		Format:       "yaml",
	}, testJobControl{})
	if err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	if got := result["totalRows"]; got != 3 {
		t.Fatalf("totalRows = %v, want 3", got)
	}
	output, _ := result["output"].(string)
	if output == "" || !containsAll(output, "employees", "manager_id") {
		t.Fatalf("generated output should include self-referential values, got %q", output)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}

func runnerRowCountSchema() *schema.Schema {
	return &schema.Schema{
		Tables: map[string]schema.Table{
			"users": {
				Columns: map[string]schema.Column{
					"id":   {Type: "integer", PK: true},
					"name": {Type: "varchar", Faker: "name"},
				},
			},
			"orders": {
				Columns: map[string]schema.Column{
					"id":      {Type: "integer", PK: true},
					"user_id": {Type: "integer", FK: "users.id"},
				},
			},
		},
	}
}

func hardSelfReferenceSchema() *schema.Schema {
	return &schema.Schema{
		Tables: map[string]schema.Table{
			"employees": {
				Columns: map[string]schema.Column{
					"id":         {Type: "integer", PK: true},
					"manager_id": {Type: "integer", FK: "employees.id"},
				},
			},
		},
	}
}
