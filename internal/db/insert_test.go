package db

import (
	"strings"
	"testing"
)

func TestBuildInsert_postgres_quotesAndPlaceholders(t *testing.T) {
	row := map[string]interface{}{"id": 1, "name": "foo"}
	query, vals := BuildInsert("users", row, "pgx")
	if !strings.Contains(query, `"users"`) {
		t.Errorf("table should be quoted: %s", query)
	}
	if !strings.Contains(query, `"id"`) || !strings.Contains(query, `"name"`) {
		t.Errorf("columns should be quoted: %s", query)
	}
	if !strings.Contains(query, "$1") || !strings.Contains(query, "$2") {
		t.Errorf("postgres should use $N placeholders: %s", query)
	}
	if len(vals) != 2 {
		t.Errorf("expected 2 values, got %d", len(vals))
	}
}

func TestBuildInsert_mysql_backticks(t *testing.T) {
	row := map[string]interface{}{"order": "test"} // reserved word
	query, _ := BuildInsert("order", row, "mysql")
	if !strings.Contains(query, "`order`") {
		t.Errorf("mysql should use backtick quoting: %s", query)
	}
	if !strings.Contains(query, "?") {
		t.Errorf("mysql should use ? placeholders: %s", query)
	}
}

func TestBuildBatchInsert_multipleRows(t *testing.T) {
	rows := []map[string]interface{}{
		{"id": 1, "name": "a"},
		{"id": 2, "name": "b"},
		{"id": 3, "name": "c"},
	}
	query, vals := BuildBatchInsert("items", rows, "pgx")
	// Should have 3 value tuples
	count := strings.Count(query, "(")
	// One for columns, three for VALUES
	if count != 4 { // INSERT INTO "items" ("id", "name") VALUES ($1, $2), ($3, $4), ($5, $6)
		t.Errorf("expected 4 opening parens, got %d in: %s", count, query)
	}
	if len(vals) != 6 {
		t.Errorf("expected 6 values, got %d", len(vals))
	}
	if !strings.Contains(query, "$6") {
		t.Errorf("last placeholder should be $6: %s", query)
	}
}

func TestBuildBatchInsert_emptyRows(t *testing.T) {
	query, vals := BuildBatchInsert("items", nil, "pgx")
	if query != "" || vals != nil {
		t.Error("empty rows should return empty query")
	}
}

func TestBuildBatchInsert_deterministicColumnOrder(t *testing.T) {
	row := map[string]interface{}{"zebra": 1, "alpha": 2, "middle": 3}
	// Run many times — columns should always be alphabetical
	for i := 0; i < 20; i++ {
		query, _ := BuildBatchInsert("t", []map[string]interface{}{row}, "pgx")
		alphaIdx := strings.Index(query, `"alpha"`)
		middleIdx := strings.Index(query, `"middle"`)
		zebraIdx := strings.Index(query, `"zebra"`)
		if alphaIdx > middleIdx || middleIdx > zebraIdx {
			t.Fatalf("columns not sorted: %s", query)
		}
	}
}

func TestBuildInsert_nilValue(t *testing.T) {
	row := map[string]interface{}{"id": 1, "deleted_at": nil}
	_, vals := BuildInsert("items", row, "pgx")
	foundNil := false
	for _, v := range vals {
		if v == nil {
			foundNil = true
		}
	}
	if !foundNil {
		t.Error("nil values should be passed through to the VALUES list")
	}
}
