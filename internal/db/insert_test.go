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

func rowsOf(n, cols int, value interface{}) []map[string]interface{} {
	rows := make([]map[string]interface{}, n)
	for i := range rows {
		row := make(map[string]interface{}, cols)
		for c := 0; c < cols; c++ {
			row["c"+strings.Repeat("x", c)] = value
		}
		rows[i] = row
	}
	return rows
}

func batchSizes(batches [][]map[string]interface{}) []int {
	out := make([]int, len(batches))
	for i, b := range batches {
		out[i] = len(b)
	}
	return out
}

func TestSplitBatches_KeepsEveryRowInOrder(t *testing.T) {
	rows := rowsOf(10, 2, 1)
	for i := range rows {
		rows[i]["id"] = i
	}
	batches := SplitBatches(rows, 4)
	if got := batchSizes(batches); len(got) != 3 || got[0] != 4 || got[1] != 4 || got[2] != 2 {
		t.Fatalf("batch sizes = %v, want [4 4 2]", got)
	}
	i := 0
	for _, b := range batches {
		for _, row := range b {
			if row["id"] != i {
				t.Fatalf("row %v out of order, want id %d", row["id"], i)
			}
			i++
		}
	}
	if len(SplitBatches(nil, 4)) != 0 {
		t.Fatal("no rows must give no batches")
	}
}

// Postgres and MySQL both reject a statement with more than 65535 placeholders:
// a wide table cannot take thousands of rows per INSERT.
func TestSplitBatches_StaysUnderThePlaceholderLimit(t *testing.T) {
	// Empty strings weigh almost nothing, so only the placeholder limit can
	// stop 200,000 placeholders from going out in one statement.
	rows := rowsOf(5000, 40, "")
	for _, b := range SplitBatches(rows, 5000) {
		if params := len(b) * 40; params > maxPlaceholders {
			t.Fatalf("batch of %d rows uses %d placeholders", len(b), params)
		}
	}
}

// MySQL 5.7 refuses packets over 4MB by default, so long text values must
// shrink the batch.
func TestSplitBatches_StaysUnderTheByteBudget(t *testing.T) {
	rows := rowsOf(1000, 2, strings.Repeat("p", 20_000)) // ~40KB per row
	batches := SplitBatches(rows, 1000)
	if len(batches) < 2 {
		t.Fatalf("40MB of rows went into %d batch", len(batches))
	}
	for _, b := range batches {
		if len(b) > 1 && len(b)*40_000 > maxBatchBytes {
			t.Fatalf("batch of %d rows carries ~%dKB", len(b), len(b)*40)
		}
	}
	huge := rowsOf(3, 1, strings.Repeat("p", 2*maxBatchBytes))
	if got := batchSizes(SplitBatches(huge, 10)); len(got) != 3 {
		t.Fatalf("oversized rows batch sizes = %v, want each row alone", got)
	}
}
