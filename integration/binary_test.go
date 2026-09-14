//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// These helpers drive the real seedstorm binary against throwaway databases, so
// scenario tests exercise exactly what a user runs rather than package internals.

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// seedstormBin builds the CLI once per test process and returns its path.
func seedstormBin(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "seedstorm-bin-")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "seedstorm")
		cmd := exec.Command("go", "build", "-o", binPath, "../cmd/seedstorm")
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatalf("build seedstorm: %v", binErr)
	}
	return binPath
}

// runBin runs the binary and fails the test on a non-zero exit. It returns
// stdout; stderr (where logs go) is attached to failures.
func runBin(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, err := runBinResult(t, args...)
	if err != nil {
		t.Fatalf("seedstorm %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

// runBinResult runs the binary and returns its output and exit error without
// failing, for tests that assert on a refusal.
func runBinResult(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(seedstormBin(t), append([]string{"--no-color"}, args...)...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = strings.NewReader("")
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// engine describes how to reach one database server and create scratch
// databases on it.
type engine struct {
	name    string // postgres | mysql (CLI --db value)
	driver  string
	adminDB func(t *testing.T) *sql.DB
	dsnFor  func(dbName string) string
	schema  func(t *testing.T, conn *sql.DB)
}

func postgresEngine() engine {
	host := envOrDefault("SEEDSTORM_PG_HOST", "localhost")
	port := envOrDefault("SEEDSTORM_PG_PORT", "5432")
	dsnFor := func(name string) string {
		return fmt.Sprintf("postgres://seedstorm:seedstorm@%s:%s/%s?sslmode=disable", host, port, name)
	}
	return engine{
		name:   "postgres",
		driver: postgresDriver,
		adminDB: func(t *testing.T) *sql.DB {
			return openDB(t, postgresDriver, dsnFor("testdb"))
		},
		dsnFor: dsnFor,
		schema: func(t *testing.T, conn *sql.DB) { execScript(t, conn, "schema_postgres.sql") },
	}
}

func mysqlEngine() engine {
	host := envOrDefault("SEEDSTORM_MYSQL_HOST", "localhost")
	port := envOrDefault("SEEDSTORM_MYSQL_PORT", "3306")
	return engine{
		name:   "mysql",
		driver: mysqlDriver,
		adminDB: func(t *testing.T) *sql.DB {
			// The app user only owns testdb; creating scratch databases needs root.
			return openDB(t, mysqlDriver, fmt.Sprintf("root:%s@tcp(%s:%s)/", envOrDefault("SEEDSTORM_MYSQL_ROOT_PASSWORD", "root"), host, port))
		},
		dsnFor: func(name string) string {
			return fmt.Sprintf("seedstorm:seedstorm@tcp(%s:%s)/%s?parseTime=true&multiStatements=true", host, port, name)
		},
		schema: func(t *testing.T, conn *sql.DB) { execMySQLSchema(t, conn, "schema_mysql.sql") },
	}
}

// scratchDB creates an empty database on the engine, grants the app user access,
// and drops it when the test ends.
func (e engine) scratchDB(t *testing.T, name string) (string, *sql.DB) {
	t.Helper()
	admin := e.adminDB(t)
	defer admin.Close()
	ctx := context.Background()
	drop := func(conn *sql.DB) {
		switch e.driver {
		case postgresDriver:
			_, _ = conn.ExecContext(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name))
		default:
			_, _ = conn.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name))
		}
	}
	drop(admin)
	stmts := []string{fmt.Sprintf(`CREATE DATABASE %s`, name)}
	if e.driver == mysqlDriver {
		stmts = []string{
			fmt.Sprintf("CREATE DATABASE `%s`", name),
			fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO 'seedstorm'@'%%'", name),
		}
	}
	for _, stmt := range stmts {
		if _, err := admin.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	dsn := e.dsnFor(name)
	conn := openDB(t, e.driver, dsn)
	t.Cleanup(func() {
		_ = conn.Close()
		cleanup := e.adminDB(t)
		defer cleanup.Close()
		drop(cleanup)
	})
	return dsn, conn
}

// tableCounts reads COUNT(*) for every base table in the database.
func tableCounts(t *testing.T, e engine, conn *sql.DB) map[string]int {
	t.Helper()
	query := `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`
	if e.driver == mysqlDriver {
		query = `SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'`
	}
	rows, err := conn.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	_ = rows.Close()
	out := make(map[string]int, len(names))
	for _, n := range names {
		quoted := `"` + n + `"`
		if e.driver == mysqlDriver {
			quoted = "`" + n + "`"
		}
		out[n] = countRows(t, conn, quoted)
	}
	return out
}

func engines() []engine { return []engine{postgresEngine(), mysqlEngine()} }
