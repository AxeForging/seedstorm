//go:build integration

package integration_test

import (
	"path/filepath"
	"testing"
)

// junctionDDL adds a pure many-to-many table (the whole PK is FKs) on top of the
// shared schema, the shape whose key enumeration restarts on every seed run.
// Table-level FOREIGN KEY clauses: MySQL ignores column-level REFERENCES.
const junctionDDL = `CREATE TABLE favorite_tags (
    user_id INTEGER NOT NULL,
    tag_id  INTEGER NOT NULL,
    PRIMARY KEY (user_id, tag_id),
    FOREIGN KEY (user_id) REFERENCES users(id),
    FOREIGN KEY (tag_id) REFERENCES tags(id)
)`

// TestSeed_TwiceWithoutTruncateAppendsRows is the regression eval for re-seeding
// a populated database: the second run must add rows instead of colliding with
// existing composite keys, UNIQUE sequences, or sparse integer ids.
func TestSeed_TwiceWithoutTruncateAppendsRows(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			dsn, conn := e.scratchDB(t, "ss_reseed")
			e.schema(t, conn)
			execSQL(t, conn, junctionDDL)
			schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
			runBin(t, "introspect", "--db", e.name, "--dsn", dsn, "--out", schemaPath)

			seed := []string{"seed", "--db", e.name, "--dsn", dsn, "--schema", schemaPath, "--rows", "6"}
			runBin(t, seed...)
			// Punch a hole in an integer id sequence so count+1 would reuse an id.
			execSQL(t, conn, "DELETE FROM audit_logs WHERE id = 2")
			first := tableCounts(t, e, conn)

			runBin(t, seed...)
			second := tableCounts(t, e, conn)

			for table, before := range first {
				if second[table] <= before {
					t.Errorf("%s did not grow on the second seed: %d -> %d", table, before, second[table])
				}
			}
		})
	}
}
