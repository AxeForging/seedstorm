//go:build integration

package integration_test

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
)

// wideSchemaDDL builds a deterministic schema of n tables shaped like a large
// product database: every table references up to three earlier ones (NOT NULL
// and nullable), every fifth table references itself, every seventh has a
// nullable FK to a later table (a near-cycle, added once all tables exist),
// and every eleventh is a junction table keyed by two parents. It is the
// stress case for FK ordering, concurrent writers and the workspace graph.
func wideSchemaDDL(n int, driver string) []string {
	r := rand.New(rand.NewSource(20260916)) //nolint:gosec // deterministic fixture
	name := func(i int) string { return fmt.Sprintf("t%03d_%s", i, wideNouns[i%len(wideNouns)]) }
	serial := "SERIAL"
	if driver == mysqlDriver {
		serial = "INTEGER AUTO_INCREMENT"
	}
	var creates, later []string
	for i := 0; i < n; i++ {
		t := name(i)
		if i%11 == 10 && i >= 2 {
			// Junction parents are regular tables: junctions have no id.
			pick := func() int {
				for {
					if p := r.Intn(i); p%11 != 10 {
						return p
					}
				}
			}
			a, b := pick(), pick()
			for b == a {
				b = pick()
			}
			creates = append(creates, fmt.Sprintf(`CREATE TABLE %s (
  left_id INTEGER NOT NULL, right_id INTEGER NOT NULL, weight INTEGER NOT NULL,
  PRIMARY KEY (left_id, right_id),
  FOREIGN KEY (left_id) REFERENCES %s(id), FOREIGN KEY (right_id) REFERENCES %s(id))`, t, name(a), name(b)))
			continue
		}
		cols := []string{"id " + serial + " PRIMARY KEY", "label VARCHAR(60) NOT NULL", "amount NUMERIC(10,2)", "created_at TIMESTAMP NULL"}
		var fks []string
		seen := map[int]bool{}
		for k := 0; k < min(i, 1+r.Intn(3)); k++ {
			p := r.Intn(i)
			if seen[p] || p%11 == 10 {
				continue
			}
			seen[p] = true
			col := fmt.Sprintf("%s_id", name(p))
			null := "NOT NULL"
			if k > 0 {
				null = "NULL"
			}
			cols = append(cols, fmt.Sprintf("%s INTEGER %s", col, null))
			fks = append(fks, fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s(id)", col, name(p)))
		}
		if i%5 == 4 {
			cols = append(cols, "parent_id INTEGER NULL")
			fks = append(fks, fmt.Sprintf("FOREIGN KEY (parent_id) REFERENCES %s(id)", t))
		}
		creates = append(creates, fmt.Sprintf("CREATE TABLE %s (\n  %s\n)", t, strings.Join(append(cols, fks...), ",\n  ")))
		if i%7 == 6 && i+3 < n && (i+3)%11 != 10 {
			later = append(later,
				fmt.Sprintf("ALTER TABLE %s ADD COLUMN later_ref_id INTEGER NULL", t),
				fmt.Sprintf("ALTER TABLE %s ADD FOREIGN KEY (later_ref_id) REFERENCES %s(id)", t, name(i+3)))
		}
	}
	return append(creates, later...)
}

var wideNouns = []string{"account", "invoice", "shipment", "product", "region", "warehouse", "ticket", "booking", "vessel", "berth", "crane", "driver", "truck", "slot", "gate", "container"}

// A 150-table schema with dense cross references seeds completely with
// concurrent writers on both engines; the database enforces every FK.
func TestSeed_WideSchemaWithCrossReferences(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			dsn, conn := e.scratchDB(t, "ss_wide150")
			for _, stmt := range wideSchemaDDL(150, e.driver) {
				execSQL(t, conn, stmt)
			}
			schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
			runBin(t, "introspect", "--db", e.name, "--dsn", dsn, "--out", schemaPath)
			runBin(t, "seed", "--db", e.name, "--dsn", dsn, "--schema", schemaPath, "--rows", "60", "--workers", "8")

			counts := tableCounts(t, e, conn)
			if len(counts) != 150 {
				t.Fatalf("tables = %d, want 150", len(counts))
			}
			for table, n := range counts {
				if n < 60 {
					t.Errorf("%s: %d rows, want at least 60", table, n)
				}
			}
			// A second concurrent run appends to every populated table.
			runBin(t, "seed", "--db", e.name, "--dsn", dsn, "--schema", schemaPath, "--rows", "20", "--workers", "8")
			for table, n := range tableCounts(t, e, conn) {
				if n <= counts[table] {
					t.Errorf("%s did not grow on the second run: %d -> %d", table, counts[table], n)
				}
			}
		})
	}
}
