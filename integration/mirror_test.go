//go:build integration

package integration_test

import (
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/seeder"
)

// evalProfile shapes values the way a load-test user would: tagged emails that
// are easy to find and delete, a fixed value, a NULL column and a row count.
const evalProfile = `name: eval
rules:
  - name: tag every email
    column: "*email*"
    template: "ss+{{seq}}.{{run}}@seedstorm.test"
  - table: suppliers
    column: phone
    setNull: true
tables:
  users:
    rows: 13
    columns:
      last_name: { value: Seeded }
`

type mirrorOutput struct {
	Plan    compare.MirrorPlan                  `json:"plan"`
	Result  seeder.Result                       `json:"result"`
	Preview map[string][]map[string]interface{} `json:"preview"`
}

func decodeJSON[T any](t *testing.T, raw string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, raw)
	}
	return v
}

func scalar(t *testing.T, conn *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// TestMirror_BinaryEndToEnd drives compare, mirror and profiles exactly as a user
// would, on both engines, and checks the databases rather than the output.
func TestMirror_BinaryEndToEnd(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			srcDSN, src := e.scratchDB(t, "ss_mirror_src")
			tgtDSN, tgt := e.scratchDB(t, "ss_mirror_tgt")
			e.schema(t, src)
			execSQL(t, src, junctionDDL)
			runBin(t, "clone-schema", "--source-db", e.name, "--source-dsn", srcDSN, "--target-db", e.name, "--target-dsn", tgtDSN)

			dir := t.TempDir()
			profilePath := filepath.Join(dir, "eval.yaml")
			if err := os.WriteFile(profilePath, []byte(evalProfile), 0o600); err != nil {
				t.Fatal(err)
			}
			schemaPath := filepath.Join(dir, "schema.yaml")
			runBin(t, "introspect", "--db", e.name, "--dsn", srcDSN, "--out", schemaPath)
			runBin(t, "seed", "--db", e.name, "--dsn", srcDSN, "--schema", schemaPath, "--rows", "5", "--profile", profilePath)

			t.Run("seed applies the profile", func(t *testing.T) {
				if n := countRows(t, src, "users"); n != 13 {
					t.Errorf("users rows = %d, want the profile's 13", n)
				}
				if bad := scalar(t, src, "SELECT COUNT(*) FROM users WHERE email NOT LIKE 'ss+%@seedstorm.test' OR last_name <> 'Seeded'"); bad != 0 {
					t.Errorf("%d users rows ignore the profile", bad)
				}
				if bad := scalar(t, src, "SELECT COUNT(*) FROM suppliers WHERE phone IS NOT NULL OR email NOT LIKE 'ss+%'"); bad != 0 {
					t.Errorf("%d suppliers rows ignore the profile", bad)
				}
			})
			source := tableCounts(t, e, src)
			endpoints := []string{"--source-db", e.name, "--source-dsn", srcDSN, "--target-db", e.name, "--target-dsn", tgtDSN}

			t.Run("compare reports every table", func(t *testing.T) {
				report := decodeJSON[compare.Report](t, runBin(t, append([]string{"compare", "--format", "json"}, endpoints...)...))
				if len(report.Rows) != len(source) {
					t.Fatalf("report has %d rows, database has %d tables", len(report.Rows), len(source))
				}
				for _, row := range report.Rows {
					if row.Target == nil || row.Target.Rows != 0 || row.Source.Rows != int64(source[row.Table]) {
						t.Errorf("%s: %+v / %+v, want source %d target 0", row.Table, row.Source, row.Target, source[row.Table])
					}
				}
				table := runBin(t, append([]string{"compare", "--only-diff"}, endpoints...)...)
				if !strings.Contains(table, "users") || !strings.Contains(table, "differs") {
					t.Errorf("table output:\n%s", table)
				}
			})

			t.Run("dry run plans everything and writes nothing", func(t *testing.T) {
				out := decodeJSON[mirrorOutput](t, runBin(t, append([]string{"mirror", "--dry-run", "--format", "json", "--profile", profilePath}, endpoints...)...))
				want := 0
				for _, n := range source {
					want += n
				}
				if out.Plan.TotalInsert != int64(want) {
					t.Errorf("plan inserts %d rows, source holds %d", out.Plan.TotalInsert, want)
				}
				if users := out.Preview["users"]; len(users) == 0 || !strings.HasPrefix(users[0]["email"].(string), "ss+") {
					t.Errorf("preview users = %v", users)
				}
				for table, n := range tableCounts(t, e, tgt) {
					if n != 0 {
						t.Errorf("dry run wrote %d rows into %s", n, table)
					}
				}
			})

			t.Run("top-up matches the source exactly", func(t *testing.T) {
				out := decodeJSON[mirrorOutput](t, runBin(t, append([]string{"mirror", "--format", "json", "--profile", profilePath}, endpoints...)...))
				if len(out.Result.Problems()) != 0 {
					t.Fatalf("problems: %+v", out.Result.Problems())
				}
				assertCounts(t, tableCounts(t, e, tgt), source, 1)
				if bad := scalar(t, tgt, "SELECT COUNT(*) FROM users WHERE email NOT LIKE 'ss+%@seedstorm.test'"); bad != 0 {
					t.Errorf("%d target users ignore the profile", bad)
				}
			})

			t.Run("second top-up at 2x appends without collisions", func(t *testing.T) {
				runBin(t, append([]string{"mirror", "--scale", "2", "--profile", profilePath}, endpoints...)...)
				assertCounts(t, tableCounts(t, e, tgt), source, 2)
				runs := scalar(t, tgt, distinctRunsQuery(e))
				if runs != 2 {
					t.Errorf("distinct {{run}} tags on target users = %d, want 2 (one per mirror run)", runs)
				}
			})

			t.Run("reset at half scale truncates and refills", func(t *testing.T) {
				runBin(t, append([]string{"mirror", "--mode", "reset", "--scale", "0.5", "--yes"}, endpoints...)...)
				assertCounts(t, tableCounts(t, e, tgt), source, 0.5)
			})

			t.Run("reset without --yes refuses", func(t *testing.T) {
				before := tableCounts(t, e, tgt)
				_, stderr, err := runBinResult(t, append([]string{"mirror", "--mode", "reset", "--scale", "3"}, endpoints...)...)
				if err == nil || !strings.Contains(stderr, "mirror aborted") {
					t.Fatalf("reset without confirmation: err=%v stderr=%s", err, stderr)
				}
				assertCounts(t, tableCounts(t, e, tgt), before, 1)
			})

			t.Run("source is never written", func(t *testing.T) {
				assertCounts(t, tableCounts(t, e, src), source, 1)
			})

			t.Run("same database as source and target is refused", func(t *testing.T) {
				_, stderr, err := runBinResult(t, "mirror", "--source-db", e.name, "--source-dsn", srcDSN, "--target-db", e.name, "--target-dsn", srcDSN)
				if err == nil || !strings.Contains(stderr, "same database") {
					t.Fatalf("err=%v stderr=%s", err, stderr)
				}
			})
		})
	}
}

// assertCounts checks got[table] == ceil(base[table] * scale) for every table.
func assertCounts(t *testing.T, got, base map[string]int, scale float64) {
	t.Helper()
	for table, n := range base {
		want := int(math.Ceil(float64(n) * scale))
		if got[table] != want {
			t.Errorf("%s: %d rows, want %d (%d x %g)", table, got[table], want, n, scale)
		}
	}
}

func distinctRunsQuery(e engine) string {
	if e.driver == mysqlDriver {
		return "SELECT COUNT(DISTINCT SUBSTRING_INDEX(SUBSTRING_INDEX(email, '@', 1), '.', -1)) FROM users"
	}
	return "SELECT COUNT(DISTINCT split_part(split_part(email, '@', 1), '.', 2)) FROM users"
}
