//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/schema"
)

const (
	postgresDSN = "postgres://seedstorm:seedstorm@localhost:5432/testdb"
	mysqlDSN    = "seedstorm:seedstorm@tcp(localhost:3306)/testdb"

	postgresDriver = "pgx"
	mysqlDriver    = "mysql"

	seedRows = 20
)

// ── helpers ────────────────────────────────────────────────────────────────────

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func openDB(t *testing.T, driver, dsn string) *sql.DB {
	t.Helper()
	conn, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatalf("open %s: %v", driver, err)
	}
	if err := conn.PingContext(context.Background()); err != nil {
		t.Fatalf("ping %s: %v — is docker compose running? (make dev-up)", driver, err)
	}
	return conn
}

func execScript(t *testing.T, conn *sql.DB, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read script %s: %v", path, err)
	}
	// Split on ; and execute each statement
	stmts := strings.Split(string(raw), ";")
	for _, stmt := range stmts {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := conn.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec statement [%.80s...]: %v", stmt, err)
		}
	}
}

func countRows(t *testing.T, conn *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(context.Background(), fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n); err != nil { //nolint:gosec
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// buildAndSeed runs the full introspect → build schema → generate → seed pipeline.
func buildAndSeed(t *testing.T, driver, dsn string, conn *sql.DB) map[string][]map[string]interface{} {
	t.Helper()

	// 1. Introspect
	tables, err := db.Introspect(driver, dsn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("introspect returned no tables")
	}
	t.Logf("discovered %d tables", len(tables))

	// 2. Build schema with faker mappings
	s := &schema.Schema{Tables: make(map[string]schema.Table, len(tables))}
	for _, tbl := range tables {
		st := schema.Table{Columns: make(map[string]schema.Column, len(tbl.Columns))}
		for _, col := range tbl.Columns {
			sc := schema.Column{
				Type:  col.Type,
				PK:    col.IsPK,
				Faker: faker.MapColumnToFaker(driver, col),
			}
			if col.FK != nil {
				sc.FK = fmt.Sprintf("%s.%s", col.FK.TableName, col.FK.ColumnName)
			}
			st.Columns[col.Name] = sc
		}
		s.Tables[tbl.Name] = st
	}

	// 3. Topological sort
	g := graph.Build(s)
	sortedTables, err := g.TopologicalSort()
	if err != nil {
		t.Fatalf("topological sort: %v", err)
	}
	t.Logf("seed order: %s", strings.Join(sortedTables, " → "))

	// 4. Generate data
	data, err := faker.Generate(s, sortedTables, seedRows, 0, conn)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// 5. Insert data
	for _, tableName := range sortedTables {
		rows := data[tableName]
		for _, row := range rows {
			cols := make([]string, 0, len(row))
			placeholders := make([]string, 0, len(row))
			vals := make([]interface{}, 0, len(row))
			i := 1
			for colName, val := range row {
				cols = append(cols, colName)
				if driver == postgresDriver {
					placeholders = append(placeholders, fmt.Sprintf("$%d", i))
				} else {
					placeholders = append(placeholders, "?")
				}
				vals = append(vals, val)
				i++
			}
			query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
				tableName, strings.Join(cols, ", "), strings.Join(placeholders, ", "))
			if _, err := conn.ExecContext(context.Background(), query, vals...); err != nil {
				t.Fatalf("insert into %s: %v\nquery: %s", tableName, err, query)
			}
		}
		t.Logf("  ✓ %s: %d rows", tableName, len(rows))
	}

	return data
}

// ── PostgreSQL ─────────────────────────────────────────────────────────────────

func TestPostgresIntegration(t *testing.T) {
	dsn := envOrDefault("POSTGRES_DSN", postgresDSN)
	conn := openDB(t, postgresDriver, dsn)
	defer conn.Close()

	t.Run("setup schema", func(t *testing.T) {
		execScript(t, conn, "schema_postgres.sql")
	})

	var data map[string][]map[string]interface{}

	t.Run("introspect and seed", func(t *testing.T) {
		data = buildAndSeed(t, postgresDriver, dsn, conn)
	})

	t.Run("row counts", func(t *testing.T) {
		expectedTables := []string{"users", "categories", "products", "addresses", "orders", "order_items", "reviews"}
		for _, tbl := range expectedTables {
			n := countRows(t, conn, tbl)
			if n == 0 {
				t.Errorf("table %s has 0 rows — expected > 0", tbl)
			}
			t.Logf("  %s: %d rows in DB, %d generated", tbl, n, len(data[tbl]))
		}
	})

	t.Run("FK integrity: orders reference valid users", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM orders o
			LEFT JOIN users u ON o.user_id = u.id
			WHERE u.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orders with invalid user_id", orphans)
		}
	})

	t.Run("FK integrity: order_items reference valid orders and products", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM order_items oi
			LEFT JOIN orders o   ON oi.order_id   = o.id
			LEFT JOIN products p ON oi.product_id = p.id
			WHERE o.id IS NULL OR p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d order_items with broken FKs", orphans)
		}
	})

	t.Run("FK integrity: products reference valid categories", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM products p
			LEFT JOIN categories c ON p.category_id = c.id
			WHERE c.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d products with invalid category_id", orphans)
		}
	})

	t.Run("FK integrity: reviews reference valid users and products", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM reviews r
			LEFT JOIN users    u ON r.user_id    = u.id
			LEFT JOIN products p ON r.product_id = p.id
			WHERE u.id IS NULL OR p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d reviews with broken FKs", orphans)
		}
	})

	t.Run("introspect discovers all FK relationships", func(t *testing.T) {
		tables, err := db.Introspect(postgresDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}

		fksByTable := map[string]int{}
		for _, tbl := range tables {
			for _, col := range tbl.Columns {
				if col.FK != nil {
					fksByTable[tbl.Name]++
				}
			}
		}

		// Verify expected FK counts
		expected := map[string]int{
			"addresses":   1, // user_id
			"products":    1, // category_id
			"orders":      2, // user_id, shipping_addr
			"order_items": 2, // order_id, product_id
			"reviews":     2, // user_id, product_id
		}
		for tbl, expectedFKs := range expected {
			if fksByTable[tbl] != expectedFKs {
				t.Errorf("table %s: expected %d FK(s), got %d", tbl, expectedFKs, fksByTable[tbl])
			}
		}
		t.Logf("FK discovery: %+v", fksByTable)
	})

	t.Run("introspect discovers enum values", func(t *testing.T) {
		tables, err := db.Introspect(postgresDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}

		for _, tbl := range tables {
			if tbl.Name != "orders" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name == "status" {
					if len(col.EnumValues) == 0 {
						t.Error("orders.status: expected enum values, got none")
					} else {
						t.Logf("orders.status enum values: %v", col.EnumValues)
					}
				}
			}
		}
	})

	t.Run("teardown", func(t *testing.T) {
		execScript(t, conn, "schema_postgres.sql") // re-run drops everything
	})
}

// ── MySQL ──────────────────────────────────────────────────────────────────────

func TestMySQLIntegration(t *testing.T) {
	dsn := envOrDefault("MYSQL_DSN", mysqlDSN)
	conn := openDB(t, mysqlDriver, dsn)
	defer conn.Close()

	t.Run("setup schema", func(t *testing.T) {
		execScript(t, conn, "schema_mysql.sql")
	})

	var data map[string][]map[string]interface{}

	t.Run("introspect and seed", func(t *testing.T) {
		data = buildAndSeed(t, mysqlDriver, dsn, conn)
	})

	t.Run("row counts", func(t *testing.T) {
		expectedTables := []string{"users", "categories", "products", "addresses", "orders", "order_items", "reviews"}
		for _, tbl := range expectedTables {
			n := countRows(t, conn, tbl)
			if n == 0 {
				t.Errorf("table %s has 0 rows — expected > 0", tbl)
			}
			t.Logf("  %s: %d rows in DB, %d generated", tbl, n, len(data[tbl]))
		}
	})

	t.Run("FK integrity: orders reference valid users", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM orders o
			LEFT JOIN users u ON o.user_id = u.id
			WHERE u.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orders with invalid user_id", orphans)
		}
	})

	t.Run("FK integrity: order_items reference valid orders and products", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM order_items oi
			LEFT JOIN orders o   ON oi.order_id   = o.id
			LEFT JOIN products p ON oi.product_id = p.id
			WHERE o.id IS NULL OR p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d order_items with broken FKs", orphans)
		}
	})

	t.Run("FK integrity: products reference valid categories", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM products p
			LEFT JOIN categories c ON p.category_id = c.id
			WHERE c.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d products with invalid category_id", orphans)
		}
	})

	t.Run("introspect discovers all FK relationships", func(t *testing.T) {
		tables, err := db.Introspect(mysqlDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}

		fksByTable := map[string]int{}
		for _, tbl := range tables {
			for _, col := range tbl.Columns {
				if col.FK != nil {
					fksByTable[tbl.Name]++
				}
			}
		}

		expected := map[string]int{
			"addresses":   1,
			"products":    1,
			"orders":      2,
			"order_items": 2,
			"reviews":     2,
		}
		for tbl, expectedFKs := range expected {
			if fksByTable[tbl] != expectedFKs {
				t.Errorf("table %s: expected %d FK(s), got %d", tbl, expectedFKs, fksByTable[tbl])
			}
		}
		t.Logf("FK discovery: %+v", fksByTable)
	})

	t.Run("introspect discovers MySQL enum values", func(t *testing.T) {
		tables, err := db.Introspect(mysqlDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}

		for _, tbl := range tables {
			if tbl.Name != "orders" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name == "status" {
					if len(col.EnumValues) == 0 {
						t.Error("orders.status: expected enum values, got none")
					} else {
						t.Logf("orders.status enum values: %v", col.EnumValues)
					}
				}
			}
		}
	})

	t.Run("teardown", func(t *testing.T) {
		execScript(t, conn, "schema_mysql.sql") // re-run drops everything
	})
}
