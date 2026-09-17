//go:build integration

package integration_test

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/compare"
)

// Estimated counts must never be silently wrong or missing: a table the planner
// has no statistics for (never analyzed) or whose cached estimate says 0 is
// counted exactly instead, and every estimated number is flagged as such.
func TestCompareEstimates_FallBackToExactCountsWhenStatisticsAreMissing(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			srcDSN, src := e.scratchDB(t, "ss_est_src")
			tgtDSN, tgt := e.scratchDB(t, "ss_est_tgt")
			ddl := "CREATE TABLE fresh (id INTEGER PRIMARY KEY, name VARCHAR(20))"
			execSQL(t, src, ddl)
			execSQL(t, tgt, ddl)
			execSQL(t, src, "CREATE TABLE never_used (id INTEGER PRIMARY KEY)")
			execSQL(t, tgt, "CREATE TABLE never_used (id INTEGER PRIMARY KEY)")
			for i := 1; i <= 30; i++ {
				execSQL(t, src, fmt.Sprintf("INSERT INTO fresh (id, name) VALUES (%d, 'row')", i))
			}
			args := []string{
				"compare", "--counts", "estimate", "--format", "json",
				"--source-db", e.name, "--source-dsn", srcDSN, "--target-db", e.name, "--target-dsn", tgtDSN,
			}
			if e.driver == postgresDriver {
				// Statistics that have landed are used, and flagged as an estimate.
				waitForPostgresLiveTuples(t, src, "fresh", 30)
				landed := decodeJSON[compare.Report](t, runBin(t, args...))
				for _, r := range landed.Rows {
					if r.Table == "fresh" && (r.Source == nil || r.Source.Rows != 30 || !r.Source.Estimated) {
						t.Fatalf("fresh with statistics = %+v, want an estimate of 30", r.Source)
					}
				}
				// Then make them really missing.
				execSQL(t, src, "SELECT pg_stat_reset_single_table_counters('fresh'::regclass)")
				waitForPostgresLiveTuples(t, src, "fresh", 0)
			}
			report := decodeJSON[compare.Report](t, runBin(t, args...))
			rows := map[string]compare.Row{}
			for _, r := range report.Rows {
				rows[r.Table] = r
			}
			fresh := rows["fresh"]
			// Postgres statistics were reset above, so its count must be exact;
			// MySQL's fresh InnoDB estimate is already right.
			if fresh.Source == nil || fresh.Source.Rows != 30 || (e.driver == postgresDriver && fresh.Source.Estimated) {
				t.Fatalf("fresh source = %+v, want 30 rows (estimate missing or zero must be counted exactly)", fresh.Source)
			}
			if never := rows["never_used"]; never.Source == nil || never.Source.Rows != 0 || never.Source.Estimated {
				t.Fatalf("never_used source = %+v, want an exact 0", never.Source)
			}

			// A mirror planned from estimates must not skip the table.
			runBin(t, "mirror", "--counts", "estimate",
				"--source-db", e.name, "--source-dsn", srcDSN, "--target-db", e.name, "--target-dsn", tgtDSN)
			if n := countRows(t, tgt, "fresh"); n != 30 {
				t.Fatalf("target fresh = %d rows after mirror --counts estimate, want 30", n)
			}
		})
	}
}

// waitForPostgresLiveTuples waits until the statistics report rows live
// tuples for table. Postgres reports inserts asynchronously: reading right
// after them can see a partial count (27 of 30 on a CI runner).
func waitForPostgresLiveTuples(t *testing.T, conn *sql.DB, table string, rows int64) {
	t.Helper()
	var n int64
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if _, err := conn.Exec("SELECT pg_stat_clear_snapshot()"); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow("SELECT n_live_tup FROM pg_stat_user_tables WHERE relname = $1", table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == rows {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("statistics for %s report %d rows, want %d", table, n, rows)
		}
	}
}
