package cli

import (
	"strings"
	"testing"
)

// --- Tests from quote-identifiers branch ---

func TestBuildInsert_postgres_quotesIdentifiers(t *testing.T) {
	row := map[string]interface{}{
		"id":   1,
		"name": "Alice",
	}
	query, values := buildInsert("user", row, "pgx")

	// Table name must be quoted
	if !strings.Contains(query, `"user"`) {
		t.Errorf("expected quoted table name \"user\" in query, got: %s", query)
	}
	// Column names must be quoted
	if !strings.Contains(query, `"id"`) {
		t.Errorf("expected quoted column \"id\" in query, got: %s", query)
	}
	if !strings.Contains(query, `"name"`) {
		t.Errorf("expected quoted column \"name\" in query, got: %s", query)
	}
	// Must use $N placeholders
	if !strings.Contains(query, "$1") || !strings.Contains(query, "$2") {
		t.Errorf("expected $1, $2 placeholders in query, got: %s", query)
	}
	if len(values) != 2 {
		t.Errorf("expected 2 values, got %d", len(values))
	}
}

func TestBuildInsert_mysql_quotesIdentifiers(t *testing.T) {
	row := map[string]interface{}{
		"id":    1,
		"order": "shipped",
	}
	query, values := buildInsert("order", row, "mysql")

	// Table name must be backtick-quoted
	if !strings.Contains(query, "`order`") {
		t.Errorf("expected backtick-quoted table name in query, got: %s", query)
	}
	// Column names must be backtick-quoted
	if !strings.Contains(query, "`id`") {
		t.Errorf("expected backtick-quoted column `id` in query, got: %s", query)
	}
	// Must use ? placeholders
	if !strings.Contains(query, "?") {
		t.Errorf("expected ? placeholders in query, got: %s", query)
	}
	if len(values) != 2 {
		t.Errorf("expected 2 values, got %d", len(values))
	}
}

func TestBuildInsert_noUnquotedTableName(t *testing.T) {
	// Ensure reserved words are not left unquoted
	row := map[string]interface{}{
		"check": "value",
	}
	query, _ := buildInsert("group", row, "pgx")

	// The query should not contain unquoted "group" as a table name.
	// INSERT INTO "group" is correct, INSERT INTO group is not.
	if strings.Contains(query, "INSERT INTO group ") {
		t.Errorf("table name 'group' is not quoted in query: %s", query)
	}
	if !strings.Contains(query, `INSERT INTO "group"`) {
		t.Errorf("expected INSERT INTO \"group\" in query, got: %s", query)
	}
}

func TestNormalizeDBType(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"postgres", "pgx"},
		{"postgresql", "pgx"},
		{"mysql", "mysql"},
		{"pgx", "pgx"},
	}
	for _, tt := range tests {
		got := normalizeDBType(tt.in)
		if got != tt.want {
			t.Errorf("normalizeDBType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// --- Tests from batch-inserts branch ---

func TestBuildInsert_Postgres(t *testing.T) {
	row := map[string]interface{}{
		"name": "Alice",
		"age":  30,
		"id":   1,
	}
	query, values := buildInsert("users", row, "pgx")

	// Columns must be sorted alphabetically: age, id, name
	if !strings.Contains(query, "INSERT INTO") {
		t.Fatalf("expected INSERT INTO, got: %s", query)
	}
	if len(values) != 3 {
		t.Fatalf("expected 3 values, got %d", len(values))
	}
	// Verify Postgres-style placeholders
	if !strings.Contains(query, "$1") || !strings.Contains(query, "$2") || !strings.Contains(query, "$3") {
		t.Fatalf("expected $1,$2,$3 placeholders, got: %s", query)
	}
}

func TestBuildInsert_MySQL(t *testing.T) {
	row := map[string]interface{}{
		"name": "Bob",
		"age":  25,
	}
	query, values := buildInsert("users", row, "mysql")

	if !strings.Contains(query, "?, ?") {
		t.Fatalf("expected ? placeholders for MySQL, got: %s", query)
	}
	if len(values) != 2 {
		t.Fatalf("expected 2 values, got %d", len(values))
	}
}

func TestBuildInsert_DeterministicColumnOrder(t *testing.T) {
	row := map[string]interface{}{
		"zebra":    1,
		"alpha":    2,
		"middle":   3,
		"beta":     4,
		"zeppelin": 5,
	}

	// Run multiple times to ensure ordering is always deterministic.
	for i := 0; i < 50; i++ {
		query, _ := buildInsert("test_table", row, "pgx")
		// Columns should always be: alpha, beta, middle, zebra, zeppelin
		idx_alpha := strings.Index(query, "alpha")
		idx_beta := strings.Index(query, "beta")
		idx_middle := strings.Index(query, "middle")
		idx_zebra := strings.Index(query, "zebra")
		idx_zeppelin := strings.Index(query, "zeppelin")

		if idx_alpha > idx_beta || idx_beta > idx_middle || idx_middle > idx_zebra || idx_zebra > idx_zeppelin {
			t.Fatalf("iteration %d: columns not in alphabetical order: %s", i, query)
		}
	}
}

func TestBuildInsert_NilValues(t *testing.T) {
	row := map[string]interface{}{
		"name":  "Alice",
		"email": nil,
	}
	query, values := buildInsert("users", row, "pgx")

	if !strings.Contains(query, "INSERT INTO") {
		t.Fatalf("unexpected query: %s", query)
	}
	// email sorts before name, so values[0] should be nil
	if values[0] != nil {
		t.Fatalf("expected nil for email, got %v", values[0])
	}
	if values[1] != "Alice" {
		t.Fatalf("expected Alice for name, got %v", values[1])
	}
}

func TestBuildBatchInsert_Postgres_MultipleRows(t *testing.T) {
	rows := []map[string]interface{}{
		{"id": 1, "name": "Alice"},
		{"id": 2, "name": "Bob"},
		{"id": 3, "name": "Charlie"},
	}
	query, values := buildBatchInsert("users", rows, "pgx")

	// Should have 6 values total (2 cols * 3 rows)
	if len(values) != 6 {
		t.Fatalf("expected 6 values, got %d", len(values))
	}

	// Should have 3 value tuples
	count := strings.Count(query, "(")
	// One for column list, three for value tuples = 4 total open parens
	if count != 4 {
		t.Fatalf("expected 4 open parens (1 col list + 3 tuples), got %d in: %s", count, query)
	}

	// Should have Postgres placeholders $1 through $6
	if !strings.Contains(query, "$1") || !strings.Contains(query, "$6") {
		t.Fatalf("expected $1..$6 placeholders, got: %s", query)
	}

	// Values should be in column-sorted order (id, name) for each row
	expected := []interface{}{1, "Alice", 2, "Bob", 3, "Charlie"}
	for i, v := range expected {
		if values[i] != v {
			t.Fatalf("values[%d]: expected %v, got %v", i, v, values[i])
		}
	}
}

func TestBuildBatchInsert_MySQL_MultipleRows(t *testing.T) {
	rows := []map[string]interface{}{
		{"a": 1, "b": "x"},
		{"a": 2, "b": "y"},
	}
	query, values := buildBatchInsert("test", rows, "mysql")

	// MySQL uses ? placeholders
	if strings.Contains(query, "$") {
		t.Fatalf("MySQL should not have $ placeholders, got: %s", query)
	}
	questionMarks := strings.Count(query, "?")
	if questionMarks != 4 {
		t.Fatalf("expected 4 ? placeholders, got %d in: %s", questionMarks, query)
	}
	if len(values) != 4 {
		t.Fatalf("expected 4 values, got %d", len(values))
	}
}

func TestBuildBatchInsert_SingleRow(t *testing.T) {
	rows := []map[string]interface{}{
		{"col1": "val1", "col2": "val2"},
	}
	query, values := buildBatchInsert("t", rows, "pgx")

	if len(values) != 2 {
		t.Fatalf("expected 2 values, got %d", len(values))
	}
	// Should produce a valid single-tuple INSERT
	if !strings.Contains(query, "VALUES ($1, $2)") {
		t.Fatalf("expected single-tuple VALUES clause, got: %s", query)
	}
}

func TestBuildBatchInsert_PartialBatch(t *testing.T) {
	// Simulate 3 rows which would be a partial batch of size 100
	rows := []map[string]interface{}{
		{"x": 1},
		{"x": 2},
		{"x": 3},
	}
	query, values := buildBatchInsert("t", rows, "pgx")

	if len(values) != 3 {
		t.Fatalf("expected 3 values, got %d", len(values))
	}
	// 3 value tuples
	tupleCount := strings.Count(query, "($")
	if tupleCount != 3 {
		t.Fatalf("expected 3 value tuples, got %d in: %s", tupleCount, query)
	}
}

func TestBuildBatchInsert_EmptyRows(t *testing.T) {
	query, values := buildBatchInsert("t", nil, "pgx")
	if query != "" {
		t.Fatalf("expected empty query for nil rows, got: %s", query)
	}
	if values != nil {
		t.Fatalf("expected nil values, got: %v", values)
	}
}

func TestBuildBatchInsert_DeterministicColumnOrder(t *testing.T) {
	rows := []map[string]interface{}{
		{"z": 1, "a": 2, "m": 3},
		{"z": 4, "a": 5, "m": 6},
	}

	for i := 0; i < 50; i++ {
		query, _ := buildBatchInsert("t", rows, "pgx")
		idx_a := strings.Index(query, "a")
		idx_m := strings.Index(query, "m")
		idx_z := strings.Index(query, "z")
		if idx_a > idx_m || idx_m > idx_z {
			t.Fatalf("iteration %d: columns not in alphabetical order: %s", i, query)
		}
	}
}

func TestBuildBatchInsert_NilValues(t *testing.T) {
	rows := []map[string]interface{}{
		{"a": nil, "b": "hello"},
		{"a": 42, "b": nil},
	}
	_, values := buildBatchInsert("t", rows, "pgx")

	if len(values) != 4 {
		t.Fatalf("expected 4 values, got %d", len(values))
	}
	// Sorted columns: a, b. Row 1: nil, "hello". Row 2: 42, nil.
	if values[0] != nil {
		t.Fatalf("values[0]: expected nil, got %v", values[0])
	}
	if values[1] != "hello" {
		t.Fatalf("values[1]: expected hello, got %v", values[1])
	}
	if values[2] != 42 {
		t.Fatalf("values[2]: expected 42, got %v", values[2])
	}
	if values[3] != nil {
		t.Fatalf("values[3]: expected nil, got %v", values[3])
	}
}
