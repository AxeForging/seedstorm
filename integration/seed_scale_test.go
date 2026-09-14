//go:build integration

package integration_test

import (
	"database/sql"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
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
// memory in megabytes.
func runBinPeakRSS(t *testing.T, args ...string) int64 {
	t.Helper()
	cmd := exec.Command(seedstormBin(t), append([]string{"--no-color"}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdin = strings.NewReader("")
	if err := cmd.Run(); err != nil {
		t.Fatalf("seedstorm %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	usage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	if !ok {
		t.Skip("peak memory is not reported on this platform")
	}
	return usage.Maxrss / 1024 // kilobytes on Linux
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
