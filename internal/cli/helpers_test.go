package cli

import (
	"strings"
	"testing"
)

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
