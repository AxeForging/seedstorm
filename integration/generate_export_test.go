//go:build integration

package integration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const scaleSchemaYAML = `tables:
  accounts:
    columns:
      id: {type: bigint, pk: true}
      email: {type: varchar, faker: uuid, unique: true}
      name: {type: varchar, faker: name}
  orders:
    columns:
      id: {type: bigint, pk: true}
      account_id: {type: bigint, fk: accounts.id}
      total: {type: numeric, faker: "price(1,500)"}
      note: {type: varchar, faker: sentence, nullable: true}
`

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func lineCount(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

// generate and export used to build the whole document in memory: 300k rows
// took 1.6GB to generate and 2.1GB to export. Both now stream.
func TestGenerateExport_LargeFilesStreamWithFlatMemory(t *testing.T) {
	const limitMB = 150
	schemaPath := writeFile(t, "schema.yaml", scaleSchemaYAML)
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "data.yaml")

	peak := runBinPeakRSS(t, "generate", "--schema", schemaPath, "--table-rows", "accounts=100000,orders=200000", "--out", dataPath)
	if peak > limitMB {
		t.Errorf("generate of 300k rows peaked at %dMB, limit %dMB", peak, limitMB)
	}

	sqlPath := filepath.Join(dir, "data.sql")
	peak = runBinPeakRSS(t, "export", "--data", dataPath, "--format", "sql", "--batch-size", "1000", "--out", sqlPath)
	if peak > limitMB {
		t.Errorf("export of 300k rows peaked at %dMB, limit %dMB", peak, limitMB)
	}
	if n := lineCount(t, sqlPath); n != 300 {
		t.Errorf("SQL export has %d statements, want 300 (1000 rows each)", n)
	}

	csvPath := filepath.Join(dir, "data.csv")
	runBinPeakRSS(t, "export", "--data", dataPath, "--format", "csv", "--out", csvPath)
	if n := lineCount(t, csvPath); n != 300_002 {
		t.Errorf("CSV export has %d lines, want 300,000 rows and 2 headers", n)
	}
	t.Logf("export peak %dMB", peak)
}

// SQL written by generate and export must load: values are literals, escaped
// the way each engine reads them.
func TestExport_SQLLoadsIntoEachEngineWithExactValues(t *testing.T) {
	values := []string{
		"O'Brien",
		`C:\path\to\new`,
		"line1\nline2",
		"semi;colon -- not a comment",
		"ünïcödé ✓",
		"",
		"'); DROP TABLE tricky; --",
	}
	var doc strings.Builder
	doc.WriteString("tricky:\n")
	for i, v := range values {
		fmt.Fprintf(&doc, "- id: %d\n  txt: %q\n  flag: %t\n  amount: %d.25\n  happened: \"2024-02-29 13:04:05\"\n  note: null\n", i+1, v, i%2 == 0, i)
	}
	dataPath := writeFile(t, "data.yaml", doc.String())

	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			_, conn := e.scratchDB(t, "ss_export_sql")
			ddl := "CREATE TABLE tricky (id BIGINT PRIMARY KEY, txt TEXT NOT NULL, flag BOOLEAN NOT NULL, amount DECIMAL(10,2) NOT NULL, happened TIMESTAMP NOT NULL, note VARCHAR(20))"
			if e.driver == mysqlDriver {
				// MySQL 5.7 defaults to latin1, which cannot store the unicode value.
				ddl += " DEFAULT CHARSET=utf8mb4"
			}
			execSQL(t, conn, ddl)
			script := runBin(t, "export", "--data", dataPath, "--format", "sql", "--db", e.name, "--batch-size", "3")
			if _, err := conn.Exec(script); err != nil {
				t.Fatalf("exported SQL does not run: %v\n%s", err, script)
			}
			rows, err := conn.Query("SELECT id, txt, flag, amount, note FROM tricky ORDER BY id")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			n := 0
			for rows.Next() {
				var id int
				var txt, amount string
				var flag bool
				var note *string
				if err := rows.Scan(&id, &txt, &flag, &amount, &note); err != nil {
					t.Fatal(err)
				}
				i := id - 1
				if txt != values[i] || flag != (i%2 == 0) || amount != fmt.Sprintf("%d.25", i) || note != nil {
					t.Errorf("row %d = %q %v %s %v, want %q %v %d.25 NULL", id, txt, flag, amount, note, values[i], i%2 == 0, i)
				}
				n++
			}
			if n != len(values) {
				t.Fatalf("%d rows loaded, want %d", n, len(values))
			}

			// generate --format sql loads too, parents before children.
			execSQL(t, conn, "DROP TABLE tricky")
			for _, stmt := range scaleDDL(e) {
				execSQL(t, conn, stmt)
			}
			genSQL := runBin(t, "generate", "--schema", writeFile(t, "schema.yaml", scaleSchemaYAML), "--db", e.name, "--format", "sql", "--table-rows", "accounts=300,orders=900")
			if _, err := conn.Exec(genSQL); err != nil {
				t.Fatalf("generated SQL does not run: %v", err)
			}
			assertScaleData(t, conn, 300, 900)
		})
	}
}
