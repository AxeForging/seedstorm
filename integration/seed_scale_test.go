//go:build integration

package integration_test

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// scaleDDL is a parent with a UNIQUE column and a child with a foreign key.
func scaleDDL(e engine) []string {
	if e.driver == postgresDriver {
		return []string{
			`CREATE TABLE accounts (id BIGINT PRIMARY KEY, email VARCHAR(120) NOT NULL UNIQUE, name VARCHAR(80) NOT NULL)`,
			`CREATE TABLE orders (id BIGINT PRIMARY KEY, account_id BIGINT NOT NULL, total NUMERIC(10,2) NOT NULL, note VARCHAR(200), FOREIGN KEY (account_id) REFERENCES accounts(id))`,
		}
	}
	return []string{
		"CREATE TABLE accounts (id BIGINT PRIMARY KEY, email VARCHAR(120) NOT NULL UNIQUE, name VARCHAR(80) NOT NULL)",
		"CREATE TABLE orders (id BIGINT PRIMARY KEY, account_id BIGINT NOT NULL, total DECIMAL(10,2) NOT NULL, note VARCHAR(200), FOREIGN KEY (account_id) REFERENCES accounts(id))",
	}
}

// runBinPeakRSS runs the binary like runBin and returns its peak resident
// memory in megabytes. It reads the process's own high-water mark (VmHWM, reset
// at exec) while it runs: rusage's maxrss also counts the test process's memory
// at fork, which made every run under -race report the test binary's 212MB.
func runBinPeakRSS(t *testing.T, args ...string) int64 {
	t.Helper()
	cmd := exec.Command(seedstormBin(t), append([]string{"--no-color"}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdin = strings.NewReader("")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	statusPath := fmt.Sprintf("/proc/%d/status", cmd.Process.Pid)
	var peakKB int64
	stop := make(chan struct{})
	polled := make(chan struct{})
	go func() {
		defer close(polled)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			if kb := readHighWaterKB(statusPath); kb > peakKB {
				peakKB = kb // VmHWM only grows: the last read before exit holds the peak
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	err := cmd.Wait()
	close(stop)
	<-polled
	if err != nil {
		t.Fatalf("seedstorm %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	if peakKB == 0 {
		t.Skip("process memory is only readable from /proc on Linux")
	}
	return peakKB / 1024
}

// readHighWaterKB returns VmHWM from a /proc status file, 0 when unreadable
// (the process already exited).
func readHighWaterKB(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				kb, _ := strconv.ParseInt(fields[0], 10, 64)
				return kb
			}
		}
	}
	return 0
}

// Seeding 300k rows used to hold every generated row in memory at once (about
// 1KB per row). Rows now stream in chunks, so the peak stays well under what
// the whole data set would need.
func TestSeed_LargeRunsStreamWithFlatMemory(t *testing.T) {
	const accounts, orders, limitMB = 100_000, 200_000, 150
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			dsn, conn := e.scratchDB(t, "ss_scale")
			for _, stmt := range scaleDDL(e) {
				execSQL(t, conn, stmt)
			}
			schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
			runBin(t, "introspect", "--db", e.name, "--dsn", dsn, "--out", schemaPath)

			peak := runBinPeakRSS(t, "seed", "--db", e.name, "--dsn", dsn, "--schema", schemaPath,
				"--table-rows", fmt.Sprintf("accounts=%d,orders=%d", accounts, orders))
			if peak > limitMB {
				t.Errorf("seed of %d rows peaked at %dMB, limit %dMB", accounts+orders, peak, limitMB)
			}
			assertScaleData(t, conn, accounts, orders)

			execSQL(t, conn, "DELETE FROM orders")
			peak = runBinPeakRSS(t, "gaps", "--db", e.name, "--dsn", dsn, "--schema", schemaPath,
				"--fill", "--yes", "--rows", fmt.Sprint(orders))
			if peak > limitMB {
				t.Errorf("gaps --fill of %d rows peaked at %dMB, limit %dMB", orders, peak, limitMB)
			}
			assertScaleData(t, conn, accounts, orders)
			t.Logf("peak memory %dMB", peak)
		})
	}
}

func assertScaleData(t *testing.T, conn *sql.DB, accounts, orders int) {
	t.Helper()
	checks := []struct {
		name, query string
		want        int
	}{
		{"accounts", "SELECT COUNT(*) FROM accounts", accounts},
		{"orders", "SELECT COUNT(*) FROM orders", orders},
		{"distinct emails", "SELECT COUNT(DISTINCT email) FROM accounts", accounts},
		{"orphan orders", "SELECT COUNT(*) FROM orders o LEFT JOIN accounts a ON a.id = o.account_id WHERE a.id IS NULL", 0},
	}
	for _, c := range checks {
		var got int
		if err := conn.QueryRow(c.query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s = %d, want %d", c.name, got, c.want)
		}
	}
	// Children must spread over the parents, not pile onto a few of them.
	var referenced int
	if err := conn.QueryRow("SELECT COUNT(DISTINCT account_id) FROM orders").Scan(&referenced); err != nil {
		t.Fatal(err)
	}
	if referenced < accounts/2 {
		t.Errorf("orders reference %d of %d accounts", referenced, accounts)
	}
}

// Postgres and MySQL cap one statement at 65,535 placeholders: 80 one-character
// columns at the default 1000 rows per INSERT would be 81,000 placeholders in
// well under a megabyte, so only the placeholder limit splits the batch.
func TestSeed_WideTableStaysUnderThePlaceholderLimit(t *testing.T) {
	const cols, rows = 80, 3000
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			dsn, conn := e.scratchDB(t, "ss_wide")
			defs := []string{"id BIGINT PRIMARY KEY"}
			for i := 0; i < cols; i++ {
				defs = append(defs, fmt.Sprintf("c%02d CHAR(1)", i))
			}
			execSQL(t, conn, "CREATE TABLE wide ("+strings.Join(defs, ", ")+")")
			schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
			runBin(t, "introspect", "--db", e.name, "--dsn", dsn, "--out", schemaPath)
			runBin(t, "seed", "--db", e.name, "--dsn", dsn, "--schema", schemaPath, "--rows", fmt.Sprint(rows))
			if n := countRows(t, conn, "wide"); n != rows {
				t.Fatalf("wide has %d rows, want %d", n, rows)
			}
		})
	}
}

// Memory used to follow row width: 20,000 rows per chunk whatever each row
// held, so ~6KB rows peaked at 320MB (377MB with 8 writers) and wider rows grew
// without bound. Chunks and the write queue are now bounded by memory. Rows here
// are ~20KB, where one old-style chunk alone is ~400MB; the bound is far below.
// (Live heap stays flat as rows grow; peak RSS varies with GC timing, so the
// limit leaves room for that but not for a row-count-sized chunk.)
func TestSeed_WideRowsStayUnderAMemoryBound(t *testing.T) {
	const limitMB = 300
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			rows, workerCounts := 30000, []string{"1", "8"}
			if e.driver == mysqlDriver {
				rows, workerCounts = 22000, []string{"8"} // MySQL writes wide rows slowly
			}
			dsn, conn := e.scratchDB(t, "ss_widerows")
			body := "TEXT"
			if e.driver == mysqlDriver {
				body = "MEDIUMTEXT" // MySQL TEXT stops at 64KB
			}
			execSQL(t, conn, "CREATE TABLE docs (id BIGINT PRIMARY KEY, title VARCHAR(80) NOT NULL, body "+body+" NOT NULL)")
			dir := t.TempDir()
			schemaPath := filepath.Join(dir, "schema.yaml")
			runBin(t, "introspect", "--db", e.name, "--dsn", dsn, "--out", schemaPath)
			profile := filepath.Join(dir, "wide.yaml")
			writeProfile(t, profile, "name: wide\ntables:\n  docs:\n    columns:\n      body: { faker: \"paragraph(130)\" }\n")

			for _, workers := range workerCounts {
				peak := runBinPeakRSS(t, "seed", "--db", e.name, "--dsn", dsn, "--schema", schemaPath,
					"--rows", fmt.Sprint(rows), "--truncate", "--yes", "--workers", workers, "--profile", profile)
				if n := countRows(t, conn, "docs"); n != rows {
					t.Fatalf("workers=%s: docs has %d rows, want %d", workers, n, rows)
				}
				var avg float64
				if err := conn.QueryRow("SELECT AVG(LENGTH(body)) FROM docs").Scan(&avg); err != nil {
					t.Fatal(err)
				}
				if avg < 15000 {
					t.Fatalf("average body is %.0f bytes: rows are not wide enough to test memory", avg)
				}
				t.Logf("workers=%s: %d rows of ~%.0f bytes, peak %dMB", workers, rows, avg, peak)
				if peak > limitMB {
					t.Errorf("workers=%s: seeding %d rows of ~%.0f bytes peaked at %dMB, limit %dMB", workers, rows, avg, peak, limitMB)
				}
			}
		})
	}
}

// Every table used to keep its generated keys (up to 500k each) until the run
// ended, even tables nothing references: 100 tables × 30k rows peaked at 272MB.
// Pools are now freed once no remaining table reads them.
func TestSeed_ManyTablesDoNotKeepEveryKeyPool(t *testing.T) {
	const tables, rows, limitMB = 100, 30000, 200
	e := postgresEngine()
	dsn, conn := e.scratchDB(t, "ss_manytables")
	for i := 0; i < tables; i++ {
		execSQL(t, conn, fmt.Sprintf("CREATE TABLE flat%03d (id BIGINT PRIMARY KEY, name VARCHAR(60) NOT NULL, email VARCHAR(80), amount NUMERIC(10,2))", i))
	}
	schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
	runBin(t, "introspect", "--db", e.name, "--dsn", dsn, "--out", schemaPath)
	peak := runBinPeakRSS(t, "seed", "--db", e.name, "--dsn", dsn, "--schema", schemaPath, "--rows", fmt.Sprint(rows), "--workers", "8")
	t.Logf("%d tables × %d rows peaked at %dMB", tables, rows, peak)
	if peak > limitMB {
		t.Errorf("seeding %d tables × %d rows peaked at %dMB, limit %dMB", tables, rows, peak, limitMB)
	}
	if n := countRows(t, conn, "flat099"); n != rows {
		t.Fatalf("flat099 has %d rows, want %d", n, rows)
	}
}
