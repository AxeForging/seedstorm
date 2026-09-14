//go:build integration

package integration_test

import (
	"fmt"
	"testing"

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
			report := decodeJSON[compare.Report](t, runBin(t, args...))
			rows := map[string]compare.Row{}
			for _, r := range report.Rows {
				rows[r.Table] = r
			}
			fresh := rows["fresh"]
			if fresh.Source == nil || fresh.Source.Rows != 30 {
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
