//go:build integration && loadsim

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/db"
)

// Assertion 7: the read safeguards behave on the smallest managed instance as
// they do on an unconstrained server.
func TestLoadsim_ReadSafeguardsOnTheSmallestInstance(t *testing.T) {
	p := loadsimProfiles["cloudsql-micro"]
	for _, driver := range []string{postgresDriver, mysqlDriver} {
		t.Run(driver, func(t *testing.T) {
			requireHeadroom(t, 2048)
			d := startLoadsimDB(t, driver, p, false)
			execSQL(t, d.conn, `CREATE TABLE ledger (id INT PRIMARY KEY)`)

			err := db.ReadOnce(context.Background(), d.conn, driver, db.ReadLimits{}, func(ctx context.Context, q db.Querier) error {
				_, err := q.ExecContext(ctx, `INSERT INTO ledger (id) VALUES (1)`)
				return err
			})
			if err == nil || countRows(t, d.conn, "ledger") != 0 {
				t.Fatalf("write inside a read scope: err=%v rows=%d", err, countRows(t, d.conn, "ledger"))
			}

			start := time.Now()
			err = db.ReadOnce(context.Background(), d.conn, driver, db.ReadLimits{StatementTimeout: 500 * time.Millisecond}, func(ctx context.Context, q db.Querier) error {
				return q.QueryRowContext(ctx, slowQuery(driver)).Scan(new(int64))
			})
			if got := db.ReadOutcomeOf(context.Background(), err); got != db.OutcomeTimedOut {
				t.Fatalf("outcome = %s (%v), want timed out", got, err)
			}
			if d := time.Since(start); d > 10*time.Second {
				t.Fatalf("timeout took %s on the micro profile", d)
			}
			if d.oomKilled(t) {
				t.Fatal("the database was OOM-killed")
			}
		})
	}
}
