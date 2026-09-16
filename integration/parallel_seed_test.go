//go:build integration

package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Concurrent writers must produce exactly what one writer does on the 28-table
// schema (deep FK chains, junctions, self-references, near-cycles): the database
// enforces every foreign key, so a child written before its parent fails the run.
func TestSeed_ConcurrentWritersMatchSequentialAndKeepForeignKeys(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			dsn, conn := e.scratchDB(t, "ss_parallel")
			e.schema(t, conn)
			schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
			runBin(t, "introspect", "--db", e.name, "--dsn", dsn, "--out", schemaPath)

			seed := func(workers string) map[string]int {
				stderr := runBinStderr(t, "seed", "--db", e.name, "--dsn", dsn, "--schema", schemaPath,
					"--rows", "400", "--batch-size", "50", "--truncate", "--yes", "--workers", workers)
				if !strings.Contains(stderr, "Table written") {
					t.Fatalf("workers=%s: no per-table progress in the log:\n%s", workers, stderr)
				}
				return tableCounts(t, e, conn)
			}
			sequential := seed("1")
			concurrent := seed("8")
			for table, n := range sequential {
				// Enum coverage adds a random number of rows on top of --rows, so
				// only tables without it must match exactly.
				if n < 400 || concurrent[table] < 400 {
					t.Errorf("%s: 1 writer wrote %d rows, 8 writers %d, want at least 400 each", table, n, concurrent[table])
				}
				if n == 400 && concurrent[table] != 400 {
					t.Errorf("%s: 8 writers wrote %d rows, 1 writer wrote exactly 400", table, concurrent[table])
				}
			}
		})
	}
}

// A profile's ignored tables are never written; a table that needs an ignored,
// empty parent stops the run with both names instead of failing on an insert.
func TestSeed_ProfileIgnoreListIsHonoured(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			dsn, conn := e.scratchDB(t, "ss_ignore")
			e.schema(t, conn)
			dir := t.TempDir()
			schemaPath := filepath.Join(dir, "schema.yaml")
			runBin(t, "introspect", "--db", e.name, "--dsn", dsn, "--out", schemaPath)

			leaf := filepath.Join(dir, "leaf.yaml")
			writeProfile(t, leaf, "name: leaf\nignore: [AUDIT_*]\n")
			runBin(t, "seed", "--db", e.name, "--dsn", dsn, "--schema", schemaPath, "--rows", "5", "--profile", leaf)
			counts := tableCounts(t, e, conn)
			if counts["audit_logs"] != 0 || counts["users"] == 0 || counts["orders"] == 0 {
				t.Fatalf("after ignoring audit_*: audit_logs=%d users=%d orders=%d", counts["audit_logs"], counts["users"], counts["orders"])
			}

			// users now has rows: ignoring it is fine, children reference them.
			parent := filepath.Join(dir, "parent.yaml")
			writeProfile(t, parent, "name: parent\nignore: [users]\n")
			before := counts["users"]
			runBin(t, "seed", "--db", e.name, "--dsn", dsn, "--schema", schemaPath, "--rows", "5", "--profile", parent)
			after := tableCounts(t, e, conn)
			if after["users"] != before || after["orders"] <= counts["orders"] {
				t.Fatalf("ignoring populated users: users %d -> %d, orders %d -> %d", before, after["users"], counts["orders"], after["orders"])
			}

			// With users emptied, the same profile must refuse before writing.
			empty, emptyConn := e.scratchDB(t, "ss_ignore_empty")
			e.schema(t, emptyConn)
			_, stderr, err := runBinResult(t, "seed", "--db", e.name, "--dsn", empty, "--schema", schemaPath, "--rows", "5", "--profile", parent)
			if err == nil || !strings.Contains(stderr, "references ignored table users, which is empty") {
				t.Fatalf("seed needing an empty ignored parent: err=%v\n%s", err, stderr)
			}
			for table, n := range tableCounts(t, e, emptyConn) {
				if n != 0 {
					t.Errorf("refused run still wrote %d rows into %s", n, table)
				}
			}
		})
	}
}

func writeProfile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// runBinStderr runs the binary like runBin and returns its log output.
func runBinStderr(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, err := runBinResult(t, args...)
	if err != nil {
		t.Fatalf("seedstorm %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
	}
	return stderr
}
