//go:build integration

package integration_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// `--seed` promises reproducible data. Two runs with the same seed must write
// byte-identical files: same table order, same values, dates included. This
// failed before: table order followed Go map iteration, date ranges ended at
// the current second, and pool sampling used an unseeded source.
func TestGenerate_SameSeedWritesIdenticalData(t *testing.T) {
	e := postgresEngine()
	dsn, conn := e.scratchDB(t, "ss_repro")
	for _, stmt := range wideSchemaDDL(60, e.driver) {
		execSQL(t, conn, stmt)
	}
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.yaml")
	runBin(t, "introspect", "--db", e.name, "--dsn", dsn, "--out", schemaPath)

	generate := func(name, seed string) []byte {
		out := filepath.Join(dir, name)
		runBin(t, "generate", "--schema", schemaPath, "--db", e.name, "--rows", "40", "--format", "sql", "--seed", seed, "--out", out)
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	first, second := generate("a.sql", "42"), generate("b.sql", "42")
	if !bytes.Equal(first, second) {
		t.Fatalf("same --seed produced different output (%d vs %d bytes); first difference at byte %d", len(first), len(second), firstDiff(first, second))
	}
	if other := generate("c.sql", "43"); bytes.Equal(first, other) {
		t.Fatal("a different --seed produced identical output")
	}
}

func firstDiff(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}
