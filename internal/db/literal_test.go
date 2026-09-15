package db

import (
	"strings"
	"testing"
	"time"
)

func TestSQLLiteral_EscapesPerDialect(t *testing.T) {
	when := time.Date(2024, 2, 29, 13, 4, 5, 123000000, time.FixedZone("CET", 3600))
	cases := []struct {
		name  string
		value interface{}
		pg    string
		mysql string
	}{
		{"null", nil, "NULL", "NULL"},
		{"plain string", "hello", "'hello'", "'hello'"},
		{"quote", "O'Brien", "'O''Brien'", "'O''Brien'"},
		{"backslash is literal on Postgres but escapes on MySQL", `C:\dir\n`, `'C:\dir\n'`, `'C:\\dir\\n'`},
		{"newline and unicode", "line1\nünï", "'line1\nünï'", "'line1\nünï'"},
		{"int", 42, "42", "42"},
		{"negative int64", int64(-7), "-7", "-7"},
		{"uint64", uint64(18446744073709551615), "18446744073709551615", "18446744073709551615"},
		{"float", 12.5, "12.5", "12.5"},
		{"bool", true, "TRUE", "TRUE"},
		{"time in UTC", when, "'2024-02-29 12:04:05.123+00'", "'2024-02-29 12:04:05.123'"},
		{"bytes", []byte{0xde, 0xad}, `'\xdead'`, "X'dead'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sqlLiteral(c.value, "pgx"); got != c.pg {
				t.Errorf("postgres: %s, want %s", got, c.pg)
			}
			if got := sqlLiteral(c.value, "mysql"); got != c.mysql {
				t.Errorf("mysql: %s, want %s", got, c.mysql)
			}
		})
	}
}

func TestRenderInsert_WritesRunnableStatementsWithValues(t *testing.T) {
	rows := []map[string]interface{}{
		{"id": 1, "name": "a'b", "note": nil},
		{"id": 2, "name": "c"}, // a missing column is NULL, not a shifted value
	}
	got := RenderInsert("users", rows, "pgx")
	want := `INSERT INTO "users" ("id", "name", "note") VALUES (1, 'a''b', NULL), (2, 'c', NULL);`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if my := RenderInsert("order", rows[:1], "mysql"); !strings.HasPrefix(my, "INSERT INTO `order` (`id`, `name`, `note`) VALUES (1, 'a''b', NULL)") {
		t.Fatalf("mysql: %s", my)
	}
	if strings.Contains(got, "$1") || RenderInsert("users", nil, "pgx") != "" {
		t.Fatal("rendered SQL must carry values, and no rows render nothing")
	}
}
