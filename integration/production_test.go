//go:build integration

package integration_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// A database marked production (--production or SEEDSTORM_PRODUCTION) is never
// written without --allow-production: the command stops before truncating or
// inserting anything. Dry runs and reads still work.
func TestProduction_CLIRefusesWritesUnlessAllowed(t *testing.T) {
	e := postgresEngine()
	dsn, conn := e.scratchDB(t, "ss_production_cli")
	execSQL(t, conn, `CREATE TABLE accounts (id INT PRIMARY KEY, name TEXT); INSERT INTO accounts VALUES (1, 'kept')`)
	schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
	runBin(t, "introspect", "--db", "postgres", "--dsn", dsn, "--out", schemaPath)

	refused := []struct {
		name string
		args []string
		env  bool
	}{
		{"seed --truncate", []string{"seed", "--db", "postgres", "--dsn", dsn, "--schema", schemaPath, "--rows", "5", "--truncate", "--yes", "--production"}, false},
		{"seed via env", []string{"seed", "--db", "postgres", "--dsn", dsn, "--schema", schemaPath, "--rows", "5"}, true},
		{"gaps --fill", []string{"gaps", "--db", "postgres", "--dsn", dsn, "--schema", schemaPath, "--fill", "--yes", "--production"}, false},
		{"clone-schema", []string{"clone-schema", "--source-db", "postgres", "--source-dsn", dsn, "--target-db", "postgres", "--target-dsn", dsn, "--production"}, false},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			if c.env {
				t.Setenv("SEEDSTORM_PRODUCTION", "true")
			}
			_, stderr, err := runBinResult(t, c.args...)
			if err == nil {
				t.Fatal("the write to a production database ran")
			}
			if !strings.Contains(stderr, "production") || !strings.Contains(stderr, "--allow-production") {
				t.Fatalf("stderr does not explain the refusal:\n%s", stderr)
			}
			if n := countRows(t, conn, "accounts"); n != 1 {
				t.Fatalf("accounts has %d rows, want the 1 it had", n)
			}
		})
	}

	t.Run("dry run is allowed", func(t *testing.T) {
		runBin(t, "seed", "--db", "postgres", "--dsn", dsn, "--schema", schemaPath, "--rows", "5", "--dry-run", "--production")
	})
	t.Run("allowed explicitly", func(t *testing.T) {
		runBin(t, "seed", "--db", "postgres", "--dsn", dsn, "--schema", schemaPath, "--rows", "5", "--production", "--allow-production")
		if n := countRows(t, conn, "accounts"); n != 6 {
			t.Fatalf("accounts has %d rows after an allowed seed, want 6", n)
		}
	})
}
