package faker

import (
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// makeEnumTable builds a minimal schema.Table with one or more enum columns.
func makeEnumTable(enumCols map[string][]string) schema.Table {
	cols := map[string]schema.Column{
		"id": {Type: "integer", PK: true},
	}
	for colName, vals := range enumCols {
		faker := "randomstring(" + joinVals(vals) + ")"
		cols[colName] = schema.Column{Type: "varchar", Faker: faker}
	}
	return schema.Table{Columns: cols}
}

func joinVals(vals []string) string {
	s := ""
	for i, v := range vals {
		if i > 0 {
			s += ","
		}
		s += v
	}
	return s
}

// ── findAllEnumColumns ────────────────────────────────────────────────────────

func TestFindAllEnumColumns_single(t *testing.T) {
	tbl := makeEnumTable(map[string][]string{
		"status": {"pending", "active", "closed"},
	})
	got := findAllEnumColumns(tbl)
	if len(got) != 1 {
		t.Fatalf("expected 1 enum column, got %d", len(got))
	}
	if _, ok := got["status"]; !ok {
		t.Error("expected 'status' in result")
	}
	if len(got["status"]) != 3 {
		t.Errorf("expected 3 values, got %d", len(got["status"]))
	}
}

func TestFindAllEnumColumns_multiple(t *testing.T) {
	tbl := makeEnumTable(map[string][]string{
		"status":   {"open", "closed"},
		"priority": {"low", "medium", "high"},
	})
	got := findAllEnumColumns(tbl)
	if len(got) != 2 {
		t.Fatalf("expected 2 enum columns, got %d", len(got))
	}
}

func TestFindAllEnumColumns_none(t *testing.T) {
	tbl := schema.Table{Columns: map[string]schema.Column{
		"id":   {Type: "integer", PK: true},
		"name": {Type: "varchar", Faker: "name"},
	}}
	got := findAllEnumColumns(tbl)
	if len(got) != 0 {
		t.Errorf("expected 0 enum columns, got %d", len(got))
	}
}

// ── topUpEnumCoverage ─────────────────────────────────────────────────────────

func TestTopUpEnumCoverage_fillsMissingValues(t *testing.T) {
	tbl := makeEnumTable(map[string][]string{
		"status": {"pending", "active", "closed"},
	})
	// Simulate 2 rows, both "pending" — active and closed are missing
	data := map[string][]map[string]interface{}{
		"items": {
			{"id": 1, "status": "pending"},
			{"id": 2, "status": "pending"},
		},
	}
	pks := map[string][]interface{}{"items": {1, 2}}
	enumCols := findAllEnumColumns(tbl)

	if err := topUpEnumCoverage(data, pks, tbl, "items", enumCols); err != nil {
		t.Fatalf("topUpEnumCoverage: %v", err)
	}

	got := map[string]bool{}
	for _, row := range data["items"] {
		if v, ok := row["status"].(string); ok {
			got[v] = true
		}
	}
	for _, want := range []string{"pending", "active", "closed"} {
		if !got[want] {
			t.Errorf("missing enum value %q after top-up", want)
		}
	}
	// Should have added exactly 2 rows (active + closed)
	if len(data["items"]) != 4 {
		t.Errorf("expected 4 rows (2 original + 2 top-up), got %d", len(data["items"]))
	}
}

func TestTopUpEnumCoverage_noOpWhenAllPresent(t *testing.T) {
	tbl := makeEnumTable(map[string][]string{
		"status": {"a", "b", "c"},
	})
	data := map[string][]map[string]interface{}{
		"t": {
			{"id": 1, "status": "a"},
			{"id": 2, "status": "b"},
			{"id": 3, "status": "c"},
		},
	}
	pks := map[string][]interface{}{"t": {1, 2, 3}}
	enumCols := findAllEnumColumns(tbl)

	if err := topUpEnumCoverage(data, pks, tbl, "t", enumCols); err != nil {
		t.Fatalf("topUpEnumCoverage: %v", err)
	}
	if len(data["t"]) != 3 {
		t.Errorf("expected 3 rows (no top-up needed), got %d", len(data["t"]))
	}
}

func TestTopUpEnumCoverage_multipleEnumColumns(t *testing.T) {
	tbl := makeEnumTable(map[string][]string{
		"status":   {"open", "closed"},
		"priority": {"low", "high"},
	})
	// Both rows have only "open" status and "low" priority
	data := map[string][]map[string]interface{}{
		"tickets": {
			{"id": 1, "status": "open", "priority": "low"},
			{"id": 2, "status": "open", "priority": "low"},
		},
	}
	pks := map[string][]interface{}{"tickets": {1, 2}}
	enumCols := findAllEnumColumns(tbl)

	if err := topUpEnumCoverage(data, pks, tbl, "tickets", enumCols); err != nil {
		t.Fatalf("topUpEnumCoverage: %v", err)
	}

	statusSeen := map[string]bool{}
	prioritySeen := map[string]bool{}
	for _, row := range data["tickets"] {
		if v, ok := row["status"].(string); ok {
			statusSeen[v] = true
		}
		if v, ok := row["priority"].(string); ok {
			prioritySeen[v] = true
		}
	}
	for _, want := range []string{"open", "closed"} {
		if !statusSeen[want] {
			t.Errorf("status: missing %q after top-up", want)
		}
	}
	for _, want := range []string{"low", "high"} {
		if !prioritySeen[want] {
			t.Errorf("priority: missing %q after top-up", want)
		}
	}
}

// ── Generate with enum top-up ─────────────────────────────────────────────────

func TestGenerate_enumTopUp_singleColumn(t *testing.T) {
	s := &schema.Schema{
		Tables: map[string]schema.Table{
			"orders": makeEnumTable(map[string][]string{
				"status": {"pending", "processing", "shipped", "delivered", "cancelled"},
			}),
		},
	}
	// Only 2 rows — statistically likely to miss several values
	data, err := Generate(s, []string{"orders"}, 2, 0, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	seen := map[string]bool{}
	for _, row := range data["orders"] {
		if v, ok := row["status"].(string); ok {
			seen[v] = true
		}
	}
	for _, want := range []string{"pending", "processing", "shipped", "delivered", "cancelled"} {
		if !seen[want] {
			t.Errorf("Generate: missing enum value %q in output", want)
		}
	}
}

func TestGenerate_enumTopUp_multipleColumns(t *testing.T) {
	s := &schema.Schema{
		Tables: map[string]schema.Table{
			"tickets": makeEnumTable(map[string][]string{
				"status":   {"open", "in_progress", "resolved", "closed"},
				"priority": {"low", "medium", "high", "critical"},
			}),
		},
	}
	data, err := Generate(s, []string{"tickets"}, 1, 0, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	statusSeen := map[string]bool{}
	prioritySeen := map[string]bool{}
	for _, row := range data["tickets"] {
		if v, ok := row["status"].(string); ok {
			statusSeen[v] = true
		}
		if v, ok := row["priority"].(string); ok {
			prioritySeen[v] = true
		}
	}
	for _, want := range []string{"open", "in_progress", "resolved", "closed"} {
		if !statusSeen[want] {
			t.Errorf("status: missing %q", want)
		}
	}
	for _, want := range []string{"low", "medium", "high", "critical"} {
		if !prioritySeen[want] {
			t.Errorf("priority: missing %q", want)
		}
	}
}

func TestGenerate_noEnumColumns_rowCountUnchanged(t *testing.T) {
	s := &schema.Schema{
		Tables: map[string]schema.Table{
			"users": {
				Columns: map[string]schema.Column{
					"id":    {Type: "integer", PK: true},
					"email": {Type: "varchar", Faker: "email"},
				},
			},
		},
	}
	data, err := Generate(s, []string{"users"}, 10, 0, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(data["users"]) != 10 {
		t.Errorf("expected exactly 10 rows, got %d", len(data["users"]))
	}
}
