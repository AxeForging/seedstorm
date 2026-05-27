package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseTableRows(t *testing.T) {
	got, err := parseTableRows([]string{"users=2, orders=4", "items=7"})
	if err != nil {
		t.Fatalf("parseTableRows: %v", err)
	}
	want := map[string]int{"users": 2, "orders": 4, "items": 7}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseTableRows = %+v, want %+v", got, want)
	}
}

func TestParseTableRowsRejectsInvalidValues(t *testing.T) {
	for _, value := range [][]string{{"users"}, {"users=0"}, {"users=nope"}, {"=2"}} {
		if _, err := parseTableRows(value); err == nil {
			t.Fatalf("parseTableRows(%v) expected error", value)
		}
	}
}

func TestSeedDryRunRejectsHardMultiTableCycleBeforeConnecting(t *testing.T) {
	schemaPath := writeTempSchema(t, `
tables:
  a:
    columns:
      id:
        type: integer
        pk: true
      b_id:
        type: integer
        fk: b.id
  b:
    columns:
      id:
        type: integer
        pk: true
      a_id:
        type: integer
        fk: a.id
`)

	err := seedCmd().Run(context.Background(), []string{
		"seed",
		"--schema", schemaPath,
		"--dsn", "postgres://invalid/unused",
		"--dry-run",
	})
	if err == nil {
		t.Fatal("expected hard multi-table cycle to fail before DB connection")
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Fatalf("error should name cycle tables, got: %v", err)
	}
}

func TestGenerateCommandHandlesHardSelfReference(t *testing.T) {
	schemaPath := writeTempSchema(t, `
tables:
  employees:
    columns:
      id:
        type: integer
        pk: true
      manager_id:
        type: integer
        fk: employees.id
`)
	outPath := filepath.Join(t.TempDir(), "data.json")

	err := generateCmd().Run(context.Background(), []string{
		"generate",
		"--schema", schemaPath,
		"--rows", "3",
		"--self-ref-depth", "2",
		"--format", "json",
		"--out", outPath,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	out, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(out), "employees") || !strings.Contains(string(out), "manager_id") {
		t.Fatalf("output should include generated hard self-reference data, got %s", string(out))
	}
}

func TestGenerateCommandAppliesTableRows(t *testing.T) {
	schemaPath := writeTempSchema(t, `
tables:
  users:
    columns:
      id:
        type: integer
        pk: true
      name:
        type: varchar
        faker: name
  orders:
    columns:
      id:
        type: integer
        pk: true
      user_id:
        type: integer
        fk: users.id
`)
	outPath := filepath.Join(t.TempDir(), "data.json")

	err := generateCmd().Run(context.Background(), []string{
		"generate",
		"--schema", schemaPath,
		"--rows", "2",
		"--table-rows", "orders=5",
		"--format", "json",
		"--out", outPath,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	out, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	var data map[string][]map[string]any
	if err := json.Unmarshal(out, &data); err != nil {
		t.Fatalf("json output: %v\n%s", err, string(out))
	}
	if got := len(data["users"]); got != 2 {
		t.Fatalf("users rows = %d, want default 2", got)
	}
	if got := len(data["orders"]); got != 5 {
		t.Fatalf("orders rows = %d, want override 5", got)
	}
}

func writeTempSchema(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	return path
}
