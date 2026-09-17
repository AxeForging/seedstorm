//go:build integration

package integration_test

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// degreeStats reads children per parent for orders.account_id.
type degreeStats struct {
	parents, withChildren, children, max int64
	avg, zeroShare                       float64
}

func orderDegrees(t *testing.T, conn *sql.DB) degreeStats {
	t.Helper()
	var s degreeStats
	if err := conn.QueryRow("SELECT COUNT(*) FROM accounts").Scan(&s.parents); err != nil {
		t.Fatal(err)
	}
	var maxC sql.NullInt64
	if err := conn.QueryRow("SELECT COUNT(*), COALESCE(SUM(c), 0), MAX(c) FROM (SELECT account_id, COUNT(*) AS c FROM orders GROUP BY account_id) per_parent").Scan(&s.withChildren, &s.children, &maxC); err != nil {
		t.Fatal(err)
	}
	s.max = maxC.Int64
	if s.withChildren > 0 {
		s.avg = float64(s.children) / float64(s.withChildren)
	}
	if s.parents > 0 {
		s.zeroShare = float64(s.parents-s.withChildren) / float64(s.parents)
	}
	return s
}

const shapedProfile = `name: shaped
relationships:
  orders.account_id:
    min: 1
    avg: 4
    max: 25
    zeroShare: 0.3
    histogram:
      - {min: 1, max: 1, parents: 900}
      - {min: 2, max: 3, parents: 1100}
      - {min: 4, max: 7, parents: 1300}
      - {min: 8, max: 25, parents: 200}
`

// A profile's relationships shape the seeded foreign key: row counts derived
// from the parents, no parent above max, average and zero share on target,
// and the achieved shape logged next to the target.
func TestShapedSeed_BinaryFollowsTheProfile(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			dsn, conn := e.scratchDB(t, "ss_shaped")
			for _, stmt := range scaleDDL(e) {
				execSQL(t, conn, stmt)
			}
			dir := t.TempDir()
			schemaPath := filepath.Join(dir, "schema.yaml")
			profilePath := filepath.Join(dir, "profile.yaml")
			if err := os.WriteFile(profilePath, []byte(shapedProfile), 0o600); err != nil {
				t.Fatal(err)
			}
			runBin(t, "introspect", "--db", e.name, "--dsn", dsn, "--out", schemaPath)
			_, stderr, err := runBinResult(t, "seed", "--db", e.name, "--dsn", dsn, "--schema", schemaPath,
				"--profile", profilePath, "--table-rows", "accounts=5000", "--shape-rows", "--rows", "10")
			if err != nil {
				t.Fatalf("shaped seed: %v\n%s", err, stderr)
			}
			for _, want := range []string{"Relationship shapes applied", "Rows derived from relationship shapes", "Relationship shape (target → table now)"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr lacks %q:\n%s", want, stderr)
				}
			}
			got := orderDegrees(t, conn)
			// 5,000 accounts × 0.7 with children × 4 = 14,000 orders.
			if got.children != 14_000 || got.max > 25 || math.Abs(got.avg-4) > 0.05 || math.Abs(got.zeroShare-0.3) > 0.005 {
				t.Fatalf("achieved %+v, want 14000 orders, max ≤ 25, avg 4, zero share 0.3", got)
			}
		})
	}
}

// Mirroring with --shape-like-source gives the empty target the source's
// skew, not uniform picks.
func TestShapedSeed_MirrorShapesLikeTheSource(t *testing.T) {
	e := postgresEngine()
	srcDSN, src := e.scratchDB(t, "ss_shaped_src")
	tgtDSN, tgt := e.scratchDB(t, "ss_shaped_tgt")
	for _, stmt := range scaleDDL(e) {
		execSQL(t, src, stmt)
		execSQL(t, tgt, stmt)
	}
	// 2,000 accounts; only the first 200 have orders, 1 to 40 each.
	execSQL(t, src, `INSERT INTO accounts SELECT g, 'a' || g || '@x.test', 'n' FROM generate_series(1, 2000) g`)
	execSQL(t, src, `INSERT INTO orders SELECT row_number() OVER (), a, 1, NULL FROM generate_series(1, 200) a, generate_series(1, 1 + (a * 7) % 40) k`)
	execSQL(t, src, "ANALYZE")
	source := orderDegrees(t, src)

	_, stderr, err := runBinResult(t, "mirror", "--source-dsn", srcDSN, "--target-dsn", tgtDSN, "--shape-like-source", "--scan-unindexed")
	if err != nil {
		t.Fatalf("mirror --shape-like-source: %v\n%s", err, stderr)
	}
	got := orderDegrees(t, tgt)
	if got.children != source.children || got.parents != source.parents {
		t.Fatalf("volumes %+v, source %+v", got, source)
	}
	if got.max > source.max || math.Abs(got.zeroShare-source.zeroShare) > 0.01 || math.Abs(got.avg-source.avg) > 0.2 {
		t.Fatalf("target shape %+v does not follow source %+v\n%s", got, source, stderr)
	}
	if !strings.Contains(stderr, "Relationship shape (target → table now)") {
		t.Errorf("no achieved-shape line:\n%s", stderr)
	}
}

// Shaping keeps the streaming memory bound of an unshaped seed.
func TestShapedSeed_MemoryStaysBounded(t *testing.T) {
	const accounts, limitMB = 100_000, 150
	e := postgresEngine()
	dsn, conn := e.scratchDB(t, "ss_shaped_scale")
	for _, stmt := range scaleDDL(e) {
		execSQL(t, conn, stmt)
	}
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.yaml")
	profilePath := filepath.Join(dir, "profile.yaml")
	profile := "relationships:\n  orders.account_id: {min: 1, avg: 3, max: 12, zeroShare: 0.25}\n"
	if err := os.WriteFile(profilePath, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	runBin(t, "introspect", "--db", e.name, "--dsn", dsn, "--out", schemaPath)
	peak := runBinPeakRSS(t, "seed", "--db", e.name, "--dsn", dsn, "--schema", schemaPath, "--profile", profilePath,
		"--table-rows", fmt.Sprintf("accounts=%d", accounts), "--shape-rows")
	if peak > limitMB {
		t.Errorf("shaped seed peaked at %dMB, limit %dMB", peak, limitMB)
	}
	got := orderDegrees(t, conn)
	if got.children != 225_000 || got.max > 12 {
		t.Fatalf("achieved %+v", got)
	}
	t.Logf("shaped seed of %d rows peaked at %dMB", accounts+got.children, peak)
}

// Real schemas hold keys that cannot be shaped (junction keys, foreign keys
// inside a primary key, self-references). A mirror shaped like its source must
// account for every key it measured: the ones it shapes, and the ones it names
// with a reason. Silently dropping them looked like shaping did nothing
// (Keycloak: 38 of 67 keys).
func TestShapedSeed_MirrorNamesEveryKeyItCannotShape(t *testing.T) {
	e := postgresEngine()
	srcDSN, src := e.scratchDB(t, "ss_shapes_report_src")
	tgtDSN, _ := e.scratchDB(t, "ss_shapes_report_tgt")
	e.schema(t, src)
	schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
	runBin(t, "introspect", "--db", e.name, "--dsn", srcDSN, "--out", schemaPath)
	runBin(t, "seed", "--db", e.name, "--dsn", srcDSN, "--schema", schemaPath, "--rows", "60")
	runBin(t, "clone-schema", "--source-dsn", srcDSN, "--target-dsn", tgtDSN)

	_, stderr, err := runBinResult(t, "mirror", "--source-dsn", srcDSN, "--target-dsn", tgtDSN,
		"--shape-like-source", "--scan-unindexed", "--log-level", "info")
	if err != nil {
		t.Fatalf("mirror --shape-like-source: %v\n%s", err, stderr)
	}

	measured := countOf(t, stderr, `Relationships measured.*relationships=(\d+)`)
	shaped := countOf(t, stderr, `Relationship shapes applied.*relationships=(\d+)`)
	named := len(regexp.MustCompile(`Relationship not shaped: (\S+): (.+)`).FindAllString(stderr, -1))
	if measured == 0 || shaped == 0 || named == 0 {
		t.Fatalf("measured %d, shaped %d, named %d — this schema must exercise both paths:\n%s", measured, shaped, named, stderr)
	}
	if shaped+named != measured {
		t.Fatalf("%d keys measured but %d shaped + %d named: some were dropped without a word", measured, shaped, named)
	}
	// The schema's own unshapeable shapes: a self-reference and a foreign key
	// inside a composite primary key.
	for _, want := range []string{"categories.parent_id: self-references", "metric_snapshots.source_id: key columns"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("no report for %q:\n%s", want, stderr)
		}
	}
}

// countOf reads the first capture of pattern in out as a number, 0 when absent.
func countOf(t *testing.T, out, pattern string) int {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(out)
	if len(m) < 2 {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("unreadable count in %q", m[0])
	}
	return n
}

// A parent table larger than the key pool (500k) is held as a sample that
// rotates during the run. The shape then lands close but not exact: a parent
// caught in two samples can pass max, and parents never sampled raise the
// share without children. The run says the pool is smaller than the table.
func TestShapedSeed_ParentsAboveThePoolLimitStayClose(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 2.2M rows")
	}
	const parents, children = 600_000, 1_620_000 // 600k × 0.9 with children × 3
	e := postgresEngine()
	dsn, conn := e.scratchDB(t, "ss_shaped_big")
	execSQL(t, conn, `
		CREATE TABLE customers (id BIGINT PRIMARY KEY, name TEXT);
		CREATE TABLE orders (id BIGINT PRIMARY KEY, customer_id BIGINT NOT NULL REFERENCES customers (id));
		CREATE INDEX orders_customer ON orders (customer_id)`)
	dir := t.TempDir()
	schemaPath, profilePath := filepath.Join(dir, "schema.yaml"), filepath.Join(dir, "profile.yaml")
	if err := os.WriteFile(profilePath, []byte("version: 2\nname: big\nrelationships:\n  orders.customer_id: {min: 1, avg: 3, max: 8, zeroShare: 0.1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runBin(t, "introspect", "--db", e.name, "--dsn", dsn, "--out", schemaPath)
	_, stderr, err := runBinResult(t, "seed", "--db", e.name, "--dsn", dsn, "--schema", schemaPath, "--profile", profilePath,
		"--table-rows", fmt.Sprintf("customers=%d,orders=%d", parents, children), "--log-level", "warn")
	if err != nil {
		t.Fatalf("seed: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "larger than the key pool") {
		t.Errorf("the run never said the parent table is larger than the pool:\n%s", stderr)
	}

	var withChildren, total, maxC int64
	if err := conn.QueryRow(`SELECT COUNT(*), COALESCE(SUM(c), 0), MAX(c) FROM (SELECT customer_id, COUNT(*) c FROM orders GROUP BY customer_id) per`).Scan(&withChildren, &total, &maxC); err != nil {
		t.Fatal(err)
	}
	avg := float64(total) / float64(withChildren)
	zeroShare := float64(parents-withChildren) / float64(parents)
	t.Logf("600k parents: avg %.2f (target 3) max %d (target 8) without children %.0f%% (target 10%%)", avg, maxC, zeroShare*100)
	if total != children {
		t.Fatalf("%d children, want %d", total, children)
	}
	// Close, not exact: rotation is what keeps a huge parent table covered.
	if avg < 2.5 || avg > 4 {
		t.Errorf("average %.2f is not near the target 3", avg)
	}
	if maxC > 2*8 {
		t.Errorf("max %d is more than twice the target 8: rounds are not holding the shape", maxC)
	}
	if zeroShare > 0.35 {
		t.Errorf("%.0f%% of parents have no children, target 10%%: the sample is not rotating", zeroShare*100)
	}
}
