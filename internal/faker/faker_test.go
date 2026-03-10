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

func TestTopUpEnumCoverage_topsUpToMinRows(t *testing.T) {
	tbl := makeEnumTable(map[string][]string{
		"status": {"pending", "active", "closed"},
	})
	// 2 rows for "pending", 0 for "active" and "closed"; minRows = 3
	data := map[string][]map[string]interface{}{
		"items": {
			{"id": 1, "status": "pending"},
			{"id": 2, "status": "pending"},
		},
	}
	pks := map[string][]interface{}{"items": {1, 2}}
	enumCols := findAllEnumColumns(tbl)

	if err := topUpEnumCoverage(data, pks, tbl, "items", enumCols, 3); err != nil {
		t.Fatalf("topUpEnumCoverage: %v", err)
	}

	counts := map[string]int{}
	for _, row := range data["items"] {
		if v, ok := row["status"].(string); ok {
			counts[v]++
		}
	}
	for _, want := range []string{"pending", "active", "closed"} {
		if counts[want] < 3 {
			t.Errorf("status=%q: expected >= 3 rows, got %d", want, counts[want])
		}
	}
	// pending had 2 → +1; active had 0 → +3; closed had 0 → +3; total = 2+1+3+3 = 9
	if len(data["items"]) != 9 {
		t.Errorf("expected 9 rows, got %d", len(data["items"]))
	}
}

func TestTopUpEnumCoverage_noOpWhenAllMeetMinRows(t *testing.T) {
	tbl := makeEnumTable(map[string][]string{
		"status": {"a", "b", "c"},
	})
	data := map[string][]map[string]interface{}{
		"t": {
			{"id": 1, "status": "a"},
			{"id": 2, "status": "a"},
			{"id": 3, "status": "b"},
			{"id": 4, "status": "b"},
			{"id": 5, "status": "c"},
			{"id": 6, "status": "c"},
		},
	}
	pks := map[string][]interface{}{"t": {1, 2, 3, 4, 5, 6}}
	enumCols := findAllEnumColumns(tbl)

	if err := topUpEnumCoverage(data, pks, tbl, "t", enumCols, 2); err != nil {
		t.Fatalf("topUpEnumCoverage: %v", err)
	}
	if len(data["t"]) != 6 {
		t.Errorf("expected 6 rows (no top-up needed), got %d", len(data["t"]))
	}
}

func TestTopUpEnumCoverage_multipleEnumColumns(t *testing.T) {
	tbl := makeEnumTable(map[string][]string{
		"status":   {"open", "closed"},
		"priority": {"low", "high"},
	})
	// Both rows have only "open" status and "low" priority; minRows = 2
	data := map[string][]map[string]interface{}{
		"tickets": {
			{"id": 1, "status": "open", "priority": "low"},
			{"id": 2, "status": "open", "priority": "low"},
		},
	}
	pks := map[string][]interface{}{"tickets": {1, 2}}
	enumCols := findAllEnumColumns(tbl)

	if err := topUpEnumCoverage(data, pks, tbl, "tickets", enumCols, 2); err != nil {
		t.Fatalf("topUpEnumCoverage: %v", err)
	}

	counts := map[string]map[string]int{
		"status":   {},
		"priority": {},
	}
	for _, row := range data["tickets"] {
		if v, ok := row["status"].(string); ok {
			counts["status"][v]++
		}
		if v, ok := row["priority"].(string); ok {
			counts["priority"][v]++
		}
	}
	for _, want := range []string{"open", "closed"} {
		if counts["status"][want] < 2 {
			t.Errorf("status=%q: expected >= 2 rows, got %d", want, counts["status"][want])
		}
	}
	for _, want := range []string{"low", "high"} {
		if counts["priority"][want] < 2 {
			t.Errorf("priority=%q: expected >= 2 rows, got %d", want, counts["priority"][want])
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
	const wantRows = 5
	data, err := Generate(s, []string{"orders"}, wantRows, 0, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	counts := map[string]int{}
	for _, row := range data["orders"] {
		if v, ok := row["status"].(string); ok {
			counts[v]++
		}
	}
	for _, want := range []string{"pending", "processing", "shipped", "delivered", "cancelled"} {
		if counts[want] < wantRows {
			t.Errorf("status=%q: expected >= %d rows, got %d", want, wantRows, counts[want])
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
	const wantRows = 3
	data, err := Generate(s, []string{"tickets"}, wantRows, 0, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	statusCounts := map[string]int{}
	priorityCounts := map[string]int{}
	for _, row := range data["tickets"] {
		if v, ok := row["status"].(string); ok {
			statusCounts[v]++
		}
		if v, ok := row["priority"].(string); ok {
			priorityCounts[v]++
		}
	}
	for _, want := range []string{"open", "in_progress", "resolved", "closed"} {
		if statusCounts[want] < wantRows {
			t.Errorf("status=%q: expected >= %d rows, got %d", want, wantRows, statusCounts[want])
		}
	}
	for _, want := range []string{"low", "medium", "high", "critical"} {
		if priorityCounts[want] < wantRows {
			t.Errorf("priority=%q: expected >= %d rows, got %d", want, wantRows, priorityCounts[want])
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
