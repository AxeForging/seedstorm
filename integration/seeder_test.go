//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/seeder"
)

// hostileDDL builds tables real apps have and seedstorm cannot model: a CHECK
// that rejects about half of generated rows, a CHECK nothing satisfies (with a
// dependent child), and a junction whose key space is smaller than requested.
var hostileDDL = []string{
	`CREATE TABLE even_stock (id INTEGER PRIMARY KEY, qty INTEGER NOT NULL, CONSTRAINT even_qty CHECK (MOD(qty, 2) = 0))`,
	`CREATE TABLE impossible (id INTEGER PRIMARY KEY, label VARCHAR(20) NOT NULL, CONSTRAINT never CHECK (1 = 0))`,
	// Table-level FOREIGN KEY: MySQL silently ignores column-level REFERENCES.
	`CREATE TABLE impossible_child (id INTEGER PRIMARY KEY, impossible_id INTEGER NOT NULL, FOREIGN KEY (impossible_id) REFERENCES impossible(id))`,
	`CREATE TABLE left_side (id INTEGER PRIMARY KEY)`,
	`CREATE TABLE right_side (id INTEGER PRIMARY KEY)`,
	`CREATE TABLE pairs (left_id INTEGER NOT NULL, right_id INTEGER NOT NULL, PRIMARY KEY (left_id, right_id), FOREIGN KEY (left_id) REFERENCES left_side(id), FOREIGN KEY (right_id) REFERENCES right_side(id))`,
	`CREATE TABLE plain_notes (id INTEGER PRIMARY KEY, body VARCHAR(40))`,
}

func TestSeederFill_RecoversFromRejectionsAndReportsWhatItCannotDo(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			dsn, conn := e.scratchDB(t, "ss_hostile")
			if e.driver == mysqlDriver && !mysqlHasCheckSupport(t, conn) {
				t.Skip("CHECK constraints are not enforced before MySQL 8.0.16")
			}
			for _, stmt := range hostileDDL {
				execSQL(t, conn, stmt)
			}
			tables, err := db.Introspect(e.driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			sc := faker.BuildSchema(e.driver, tables)
			order, err := graph.Build(sc).TopologicalSort()
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{
				"even_stock": 200, "impossible": 50, "impossible_child": 20,
				"left_side": 2, "right_side": 2, "pairs": 10, "plain_notes": 30,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			start := time.Now()
			res, err := seeder.Fill(ctx, conn, e.driver, sc, order, counts, seeder.Options{BatchSize: 25})
			if err != nil {
				t.Fatalf("Fill returned a run-stopping error: %v", err)
			}
			if elapsed := time.Since(start); elapsed > 30*time.Second {
				t.Errorf("Fill took %s; a stuck table must be abandoned, not retried forever", elapsed)
			}
			byTable := map[string]seeder.TableResult{}
			for _, tr := range res.Tables {
				byTable[tr.Table] = tr
			}

			even := byTable["even_stock"]
			if even.Status != seeder.StatusOK || even.Inserted != 200 || even.Rejected == 0 {
				t.Errorf("even_stock = %+v, want all 200 inserted after regenerating rejected rows", even)
			}
			if n := countRows(t, conn, "even_stock"); n != 200 {
				t.Errorf("even_stock has %d rows in the database", n)
			}

			impossible := byTable["impossible"]
			if impossible.Status != seeder.StatusFailed || impossible.Missing != 50 || impossible.Error == "" {
				t.Errorf("impossible = %+v, want failed with a reason", impossible)
			}
			child := byTable["impossible_child"]
			if child.Status != seeder.StatusFailed || !strings.Contains(child.Error, "impossible") {
				t.Errorf("impossible_child = %+v, want failed because its parent is empty", child)
			}

			pairs := byTable["pairs"]
			if pairs.Status != seeder.StatusPartial || pairs.Inserted != 4 || !strings.Contains(pairs.Error, "no more rows possible") {
				t.Errorf("pairs = %+v, want partial with the 4 possible key combinations", pairs)
			}

			if notes := byTable["plain_notes"]; notes.Status != seeder.StatusOK || notes.Inserted != 30 {
				t.Errorf("plain_notes = %+v, an unrelated table must still be filled", notes)
			}
			if res.Missing != 50+20+6 {
				t.Errorf("missing = %d, want 76", res.Missing)
			}
		})
	}
}

func TestSeederFill_StopOnErrorAbortsAtTheFirstRejection(t *testing.T) {
	e := postgresEngine()
	dsn, conn := e.scratchDB(t, "ss_hostile_stop")
	for _, stmt := range hostileDDL[1:3] {
		execSQL(t, conn, stmt)
	}
	tables, err := db.Introspect(e.driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	sc := faker.BuildSchema(e.driver, tables)
	_, err = seeder.Fill(context.Background(), conn, e.driver, sc, []string{"impossible", "impossible_child"},
		map[string]int{"impossible": 5, "impossible_child": 5}, seeder.Options{StopOnError: true})
	if err == nil || !strings.Contains(err.Error(), "impossible") {
		t.Fatalf("err = %v, want the first rejection to stop the run", err)
	}
}
