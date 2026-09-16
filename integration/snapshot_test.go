//go:build integration

package integration_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/compare"
)

// seedSnapshotSource fills a scratch database with the shared schema and a few
// rows per table, returning its DSN, connection and per-table counts.
func seedSnapshotSource(t *testing.T, e engine, name string, rows string) (string, map[string]int) {
	t.Helper()
	dsn, conn := e.scratchDB(t, name)
	e.schema(t, conn)
	schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
	runBin(t, "introspect", "--db", e.name, "--dsn", dsn, "--out", schemaPath)
	runBin(t, "seed", "--db", e.name, "--dsn", dsn, "--schema", schemaPath, "--rows", rows)
	return dsn, tableCounts(t, e, conn)
}

func readSnapshotFile(t *testing.T, path string) compare.Snapshot {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := compare.ParseSnapshot(data)
	if err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, data)
	}
	return snap
}

func snapshotRows(snap compare.Snapshot) map[string]int {
	out := make(map[string]int, len(snap.Tables))
	for name, st := range snap.Tables {
		out[name] = int(st.Rows)
	}
	return out
}

func writeSnapshotFixture(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSnapshot_BinaryEndToEnd exports a database's counts to a file and uses
// the file, with no source connection, to compare and mirror a target.
func TestSnapshot_BinaryEndToEnd(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			srcDSN, source := seedSnapshotSource(t, e, "ss_snap_src", "5")
			tgtDSN, tgt := e.scratchDB(t, "ss_snap_tgt")
			runBin(t, "clone-schema", "--source-db", e.name, "--source-dsn", srcDSN, "--target-db", e.name, "--target-dsn", tgtDSN)
			dir := t.TempDir()
			yamlPath := filepath.Join(dir, "counts.yaml")
			jsonPath := filepath.Join(dir, "counts.json")
			target := []string{"--target-db", e.name, "--target-dsn", tgtDSN}

			t.Run("snapshot writes yaml and json files and stdout", func(t *testing.T) {
				runBin(t, "snapshot", "--db", e.name, "--dsn", srcDSN, "--out", yamlPath)
				runBin(t, "snapshot", "--db", e.name, "--dsn", srcDSN, "--format", "json", "--out", jsonPath)
				raw, _ := os.ReadFile(yamlPath)
				if !strings.HasPrefix(string(raw), "kind: seedstorm.table-counts\nversion: 1\n") {
					t.Errorf("yaml file does not start with the envelope:\n%s", raw)
				}
				fromYAML, fromJSON := readSnapshotFile(t, yamlPath), readSnapshotFile(t, jsonPath)
				if !reflect.DeepEqual(snapshotRows(fromYAML), source) {
					t.Errorf("yaml snapshot rows = %v, database has %v", snapshotRows(fromYAML), source)
				}
				if !reflect.DeepEqual(snapshotRows(fromJSON), source) {
					t.Errorf("json snapshot rows = %v, database has %v", snapshotRows(fromJSON), source)
				}
				if cols := fromYAML.Tables["users"].Columns; len(cols) == 0 {
					t.Errorf("users columns missing from snapshot: %+v", fromYAML.Tables["users"])
				}
				stdout := runBin(t, "snapshot", "--db", e.name, "--dsn", srcDSN, "--format", "json", "--counts", "estimate")
				snap, err := compare.ParseSnapshot([]byte(stdout))
				if err != nil {
					t.Fatalf("stdout snapshot: %v\n%s", err, stdout)
				}
				if snap.CountMode != compare.CountEstimate || len(snap.Tables) != len(source) {
					t.Errorf("stdout snapshot mode=%s tables=%d, want estimate and %d", snap.CountMode, len(snap.Tables), len(source))
				}
			})

			t.Run("compare from a snapshot file reports per-table deltas", func(t *testing.T) {
				for _, path := range []string{yamlPath, jsonPath} {
					args := append([]string{"compare", "--format", "json", "--source-snapshot", path}, target...)
					report := decodeJSON[compare.Report](t, runBin(t, args...))
					if len(report.Rows) != len(source) {
						t.Fatalf("%s: %d rows, want %d", path, len(report.Rows), len(source))
					}
					for _, row := range report.Rows {
						want := compare.StatusDiffers
						if source[row.Table] == 0 {
							want = compare.StatusSame
						}
						if row.Status != want || row.Delta != -int64(source[row.Table]) || row.Target == nil || row.Target.Rows != 0 {
							t.Errorf("%s %s: status=%s delta=%d target=%+v, want %s delta %d", filepath.Base(path), row.Table, row.Status, row.Delta, row.Target, want, -source[row.Table])
						}
					}
				}
			})

			t.Run("source flags are validated", func(t *testing.T) {
				_, stderr, err := runBinResult(t, append([]string{"compare"}, target...)...)
				if err == nil || !strings.Contains(stderr, "a source is required: pass --source-dsn") {
					t.Errorf("no source: err=%v stderr=%s", err, stderr)
				}
				_, stderr, err = runBinResult(t, append([]string{"compare", "--source-dsn", srcDSN, "--source-snapshot", yamlPath}, target...)...)
				if err == nil || !strings.Contains(stderr, "either --source-dsn or --source-snapshot") {
					t.Errorf("both sources: err=%v stderr=%s", err, stderr)
				}
			})

			t.Run("malformed snapshot files fail with a readable error", func(t *testing.T) {
				cases := map[string]string{
					"profile.yaml": "name: eval\nrules:\n  - column: email\n    value: x\n",
					"broken.json":  `{"kind": "seedstorm.table-counts", "version": 1, "tables": {`,
					"future.yaml":  "kind: seedstorm.table-counts\nversion: 9\ntables: {users: 1}\n",
					"dupes.yaml":   "tables:\n  users: 1\n  users: 2\n",
				}
				want := map[string]string{
					"profile.yaml": "looks like a seed profile",
					"broken.json":  "not valid JSON or YAML",
					"future.yaml":  "unsupported snapshot version 9 (supported: 1)",
					"dupes.yaml":   "lists the same name twice",
				}
				for name, body := range cases {
					path := writeSnapshotFixture(t, dir, name, body)
					for _, command := range []string{"compare", "mirror"} {
						_, stderr, err := runBinResult(t, append([]string{command, "--source-snapshot", path}, target...)...)
						if err == nil || !strings.Contains(stderr, want[name]) || !strings.Contains(stderr, name) {
							t.Errorf("%s %s: err=%v, want stderr to name the file and contain %q:\n%s", command, name, err, want[name], stderr)
						}
					}
				}
				_, stderr, err := runBinResult(t, append([]string{"compare", "--source-snapshot", filepath.Join(dir, "missing.yaml")}, target...)...)
				if err == nil || !strings.Contains(stderr, "missing.yaml") {
					t.Errorf("missing file: err=%v stderr=%s", err, stderr)
				}
				for table, n := range tableCounts(t, e, tgt) {
					if n != 0 {
						t.Errorf("a rejected snapshot still wrote %d rows into %s", n, table)
					}
				}
			})

			t.Run("mirror from a snapshot fills the target to the snapshot counts", func(t *testing.T) {
				args := append([]string{"mirror", "--format", "json", "--source-snapshot", jsonPath}, target...)
				stdout, stderr, err := runBinResult(t, args...)
				if err != nil {
					t.Fatalf("mirror: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
				}
				if !strings.Contains(stderr, "cannot check that source and target are different databases") {
					t.Errorf("mirror from a snapshot did not warn that the same-database check was skipped:\n%s", stderr)
				}
				out := decodeJSON[mirrorOutput](t, stdout)
				if len(out.Result.Problems()) != 0 {
					t.Fatalf("problems: %+v", out.Result.Problems())
				}
				assertCounts(t, tableCounts(t, e, tgt), source, 1)

				report := decodeJSON[compare.Report](t, runBin(t, append([]string{"compare", "--format", "json", "--source-snapshot", yamlPath}, target...)...))
				if report.Totals.Differs != 0 || report.Totals.Same != len(source) {
					t.Errorf("after mirror: totals %+v, want every table the same", report.Totals)
				}
			})

			t.Run("hand-written minimal snapshot with other-case names mirrors", func(t *testing.T) {
				handDSN, hand := e.scratchDB(t, "ss_snap_hand")
				runBin(t, "clone-schema", "--source-db", e.name, "--source-dsn", srcDSN, "--target-db", e.name, "--target-dsn", handDSN)
				path := writeSnapshotFixture(t, dir, "hand.yaml", "tables:\n  USERS: 7\n  Brands: 3\n")
				handTarget := []string{"--target-db", e.name, "--target-dsn", handDSN}

				report := decodeJSON[compare.Report](t, runBin(t, append([]string{"compare", "--format", "json", "--source-snapshot", path}, handTarget...)...))
				matched := map[string]string{}
				for _, row := range report.Rows {
					if row.Status == compare.StatusDiffers {
						matched[row.Table] = row.TargetTable
					}
				}
				if matched["USERS"] != "users" || matched["Brands"] != "brands" {
					t.Errorf("case-insensitive matches = %v (rows %+v)", matched, report.Rows)
				}

				runBin(t, append([]string{"mirror", "--source-snapshot", path}, handTarget...)...)
				if n := countRows(t, hand, "users"); n != 7 {
					t.Errorf("users = %d, want 7", n)
				}
				if n := countRows(t, hand, "brands"); n != 3 {
					t.Errorf("brands = %d, want 3", n)
				}
				if n := countRows(t, hand, "orders"); n != 0 {
					t.Errorf("orders = %d, want 0: tables absent from the snapshot must be left alone", n)
				}
			})
		})
	}
}

// TestSnapshot_CrossEngine uses a Postgres snapshot to compare and mirror a
// MySQL database with the same tables.
func TestSnapshot_CrossEngine(t *testing.T) {
	pg, my := postgresEngine(), mysqlEngine()
	srcDSN, source := seedSnapshotSource(t, pg, "ss_snap_xsrc", "4")
	tgtDSN, tgt := my.scratchDB(t, "ss_snap_xtgt")
	my.schema(t, tgt)

	path := filepath.Join(t.TempDir(), "pg.yaml")
	runBin(t, "snapshot", "--db", "postgres", "--dsn", srcDSN, "--out", path)
	target := []string{"--target-db", "mysql", "--target-dsn", tgtDSN}

	report := decodeJSON[compare.Report](t, runBin(t, append([]string{"compare", "--format", "json", "--source-snapshot", path}, target...)...))
	if report.Source.DBType != "pgx" || report.Target.DBType != "mysql" {
		t.Errorf("report engines = %s -> %s", report.Source.DBType, report.Target.DBType)
	}
	if report.Totals.SourceOnly != 0 || report.Totals.TargetOnly != 0 {
		t.Fatalf("tables did not match across engines: %+v", report.Totals)
	}

	runBin(t, append([]string{"mirror", "--source-snapshot", path}, target...)...)
	assertCounts(t, tableCounts(t, my, tgt), source, 1)
}
