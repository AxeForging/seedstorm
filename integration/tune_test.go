//go:build integration

package integration_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// `seedstorm tune` recommends writers and generators from the database's
// detected limits plus what the user says about its size, with reasons; seed
// can use the recommendation directly with --workers auto.
func TestTune_CLIRecommendsAndSeedUsesAuto(t *testing.T) {
	e := postgresEngine()
	dsn, conn := e.scratchDB(t, "ss_tune_cli")
	execSQL(t, conn, `CREATE TABLE notes (id INT PRIMARY KEY, body TEXT)`)

	out := runBin(t, "tune", "--db", "postgres", "--dsn", dsn, "--rows", "1000",
		"--vcpu", "1", "--memory-mb", "629", "--storage", "network-ssd", "--storage-gb", "10", "--iops", "300")
	for _, want := range []string{"Writers", "2", "Generators", "vCPU", "connections", "depend on the database's load"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tune output lacks %q:\n%s", want, out)
		}
	}

	schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
	runBin(t, "introspect", "--db", "postgres", "--dsn", dsn, "--out", schemaPath)
	_, stderr, err := runBinResult(t, "seed", "--db", "postgres", "--dsn", dsn, "--schema", schemaPath, "--rows", "30", "--workers", "auto")
	if err != nil {
		t.Fatalf("seed --workers auto: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "Writers chosen for this database") {
		t.Fatalf("seed did not say which writers it chose:\n%s", stderr)
	}
	if n := countRows(t, conn, "notes"); n != 30 {
		t.Fatalf("notes = %d rows", n)
	}

	if _, stderr, err := runBinResult(t, "seed", "--db", "postgres", "--dsn", dsn, "--schema", schemaPath, "--rows", "1", "--workers", "many"); err == nil || !strings.Contains(stderr, "--workers") {
		t.Fatalf("an invalid --workers was accepted: %v\n%s", err, stderr)
	}
}
