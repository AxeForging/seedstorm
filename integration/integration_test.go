//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/schema"
)

const (
	postgresDSN    = "postgres://seedstorm:seedstorm@localhost:5432/testdb"
	mysqlDSN       = "seedstorm:seedstorm@tcp(localhost:3306)/testdb"
	postgresDriver = "pgx"
	mysqlDriver    = "mysql"
	seedRows       = 25
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
// It prints a summary at the end (not per-row during insert).
func buildAndSeed(t *testing.T, label, driver, dsn string, conn *sql.DB) map[string][]map[string]interface{} {
	t.Helper()

	// 1. Introspect
	tables, err := db.Introspect(driver, dsn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("introspect returned no tables")
	}

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

	// 4. Generate data
	data, err := faker.Generate(s, sortedTables, seedRows, 0, conn)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// 5. Insert data — track timing and totals for summary
	start := time.Now()
	totalRows := 0
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
		totalRows += len(rows)
	}
	elapsed := time.Since(start)

	// Summary log — one block at the end, no per-row noise
	var sb strings.Builder
	fmt.Fprintf(&sb, "=== Seed Summary (%s) ===\n", label)
	for _, tableName := range sortedTables {
		fmt.Fprintf(&sb, "  %-20s %d rows\n", tableName, len(data[tableName]))
	}
	fmt.Fprintf(&sb, "  Total: %d rows across %d tables (%.2fs)", totalRows, len(sortedTables), elapsed.Seconds())
	t.Log(sb.String())

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
		data = buildAndSeed(t, "postgres", postgresDriver, dsn, conn)
	})

	t.Run("row counts", func(t *testing.T) {
		allTables := []string{
			"brands", "categories", "tags", "users", "coupons",
			"addresses", "products", "product_tags", "orders", "wishlists",
			"order_items", "shipments", "payments", "reviews", "wishlist_items",
		}
		type tableCount struct {
			name string
			n    int
		}
		counts := make([]tableCount, 0, len(allTables))
		for _, tbl := range allTables {
			n := countRows(t, conn, tbl)
			if n == 0 {
				t.Errorf("table %s has 0 rows — expected > 0", tbl)
			}
			counts = append(counts, tableCount{tbl, n})
		}
		// Print once as a clean summary
		var sb strings.Builder
		sb.WriteString("=== Row Counts ===\n")
		for _, tc := range counts {
			fmt.Fprintf(&sb, "  %-20s %d\n", tc.name, tc.n)
		}
		_ = data // data available for callers; row counts come from DB
		t.Log(sb.String())
	})

	// FK integrity checks — one subtest per relationship (17 total)

	t.Run("FK: addresses -> users", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM addresses c
			LEFT JOIN users p ON c.user_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in addresses (user_id)", orphans)
		}
	})

	t.Run("FK: products -> categories", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM products c
			LEFT JOIN categories p ON c.category_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in products (category_id)", orphans)
		}
	})

	t.Run("FK: products -> brands", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM products c
			LEFT JOIN brands p ON c.brand_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in products (brand_id)", orphans)
		}
	})

	t.Run("FK: product_tags -> products", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM product_tags c
			LEFT JOIN products p ON c.product_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in product_tags (product_id)", orphans)
		}
	})

	t.Run("FK: product_tags -> tags", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM product_tags c
			LEFT JOIN tags p ON c.tag_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in product_tags (tag_id)", orphans)
		}
	})

	t.Run("FK: orders -> users", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM orders c
			LEFT JOIN users p ON c.user_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in orders (user_id)", orphans)
		}
	})

	t.Run("FK: orders -> addresses (non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM orders c
			LEFT JOIN addresses p ON c.address_id = p.id
			WHERE c.address_id IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in orders (address_id)", orphans)
		}
	})

	t.Run("FK: orders -> coupons (non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM orders c
			LEFT JOIN coupons p ON c.coupon_id = p.id
			WHERE c.coupon_id IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in orders (coupon_id)", orphans)
		}
	})

	t.Run("FK: order_items -> orders", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM order_items c
			LEFT JOIN orders p ON c.order_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in order_items (order_id)", orphans)
		}
	})

	t.Run("FK: order_items -> products", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM order_items c
			LEFT JOIN products p ON c.product_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in order_items (product_id)", orphans)
		}
	})

	t.Run("FK: shipments -> orders", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM shipments c
			LEFT JOIN orders p ON c.order_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in shipments (order_id)", orphans)
		}
	})

	t.Run("FK: payments -> orders", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM payments c
			LEFT JOIN orders p ON c.order_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in payments (order_id)", orphans)
		}
	})

	t.Run("FK: reviews -> users", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM reviews c
			LEFT JOIN users p ON c.user_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in reviews (user_id)", orphans)
		}
	})

	t.Run("FK: reviews -> products", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM reviews c
			LEFT JOIN products p ON c.product_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in reviews (product_id)", orphans)
		}
	})

	t.Run("FK: wishlists -> users", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM wishlists c
			LEFT JOIN users p ON c.user_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in wishlists (user_id)", orphans)
		}
	})

	t.Run("FK: wishlist_items -> wishlists", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM wishlist_items c
			LEFT JOIN wishlists p ON c.wishlist_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in wishlist_items (wishlist_id)", orphans)
		}
	})

	t.Run("FK: wishlist_items -> products", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM wishlist_items c
			LEFT JOIN products p ON c.product_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in wishlist_items (product_id)", orphans)
		}
	})

	// Value constraint checks

	t.Run("value constraints: reviews rating 1-5", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM reviews WHERE rating < 1 OR rating > 5`).Scan(&bad)
		if err != nil {
			t.Fatalf("constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d reviews with rating outside 1-5", bad)
		}
	})

	t.Run("value constraints: products price positive", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM products WHERE price <= 0`).Scan(&bad)
		if err != nil {
			t.Fatalf("constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d products with non-positive price", bad)
		}
	})

	t.Run("value constraints: order_items quantity positive", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM order_items WHERE quantity < 1`).Scan(&bad)
		if err != nil {
			t.Fatalf("constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d order_items with quantity < 1", bad)
		}
	})

	// FK discovery subtest

	t.Run("FK discovery: all relationships detected", func(t *testing.T) {
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

		expected := map[string]int{
			"addresses":      1, // user_id
			"products":       2, // category_id, brand_id
			"product_tags":   2, // product_id, tag_id
			"orders":         3, // user_id, address_id, coupon_id
			"order_items":    2, // order_id, product_id
			"shipments":      1, // order_id
			"payments":       1, // order_id
			"reviews":        2, // user_id, product_id
			"wishlists":      1, // user_id
			"wishlist_items": 2, // wishlist_id, product_id
			"categories":     1, // parent_id (self-ref)
		}
		for tbl, expectedFKs := range expected {
			if fksByTable[tbl] != expectedFKs {
				t.Errorf("table %s: expected %d FK(s), got %d", tbl, expectedFKs, fksByTable[tbl])
			}
		}
		t.Logf("FK discovery: %+v", fksByTable)
	})

	// Enum discovery subtest

	t.Run("enum discovery: orders.status", func(t *testing.T) {
		tables, err := db.Introspect(postgresDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}

		expectedValues := []string{"pending", "processing", "shipped", "delivered", "cancelled"}
		for _, tbl := range tables {
			if tbl.Name != "orders" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "status" {
					continue
				}
				if len(col.EnumValues) == 0 {
					t.Error("orders.status: expected enum values, got none")
					return
				}
				got := map[string]bool{}
				for _, v := range col.EnumValues {
					got[v] = true
				}
				for _, want := range expectedValues {
					if !got[want] {
						t.Errorf("orders.status: missing expected enum value %q", want)
					}
				}
				t.Logf("orders.status enum values: %v", col.EnumValues)
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
		data = buildAndSeed(t, "mysql", mysqlDriver, dsn, conn)
	})

	t.Run("row counts", func(t *testing.T) {
		allTables := []string{
			"brands", "categories", "tags", "users", "coupons",
			"addresses", "products", "product_tags", "orders", "wishlists",
			"order_items", "shipments", "payments", "reviews", "wishlist_items",
		}
		type tableCount struct {
			name string
			n    int
		}
		counts := make([]tableCount, 0, len(allTables))
		for _, tbl := range allTables {
			n := countRows(t, conn, tbl)
			if n == 0 {
				t.Errorf("table %s has 0 rows — expected > 0", tbl)
			}
			counts = append(counts, tableCount{tbl, n})
		}
		var sb strings.Builder
		sb.WriteString("=== Row Counts ===\n")
		for _, tc := range counts {
			fmt.Fprintf(&sb, "  %-20s %d\n", tc.name, tc.n)
		}
		_ = data
		t.Log(sb.String())
	})

	// FK integrity checks — one subtest per relationship (17 total)

	t.Run("FK: addresses -> users", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM addresses c
			LEFT JOIN users p ON c.user_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in addresses (user_id)", orphans)
		}
	})

	t.Run("FK: products -> categories", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM products c
			LEFT JOIN categories p ON c.category_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in products (category_id)", orphans)
		}
	})

	t.Run("FK: products -> brands", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM products c
			LEFT JOIN brands p ON c.brand_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in products (brand_id)", orphans)
		}
	})

	t.Run("FK: product_tags -> products", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM product_tags c
			LEFT JOIN products p ON c.product_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in product_tags (product_id)", orphans)
		}
	})

	t.Run("FK: product_tags -> tags", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM product_tags c
			LEFT JOIN tags p ON c.tag_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in product_tags (tag_id)", orphans)
		}
	})

	t.Run("FK: orders -> users", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM orders c
			LEFT JOIN users p ON c.user_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in orders (user_id)", orphans)
		}
	})

	t.Run("FK: orders -> addresses (non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM orders c
			LEFT JOIN addresses p ON c.address_id = p.id
			WHERE c.address_id IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in orders (address_id)", orphans)
		}
	})

	t.Run("FK: orders -> coupons (non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM orders c
			LEFT JOIN coupons p ON c.coupon_id = p.id
			WHERE c.coupon_id IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in orders (coupon_id)", orphans)
		}
	})

	t.Run("FK: order_items -> orders", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM order_items c
			LEFT JOIN orders p ON c.order_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in order_items (order_id)", orphans)
		}
	})

	t.Run("FK: order_items -> products", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM order_items c
			LEFT JOIN products p ON c.product_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in order_items (product_id)", orphans)
		}
	})

	t.Run("FK: shipments -> orders", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM shipments c
			LEFT JOIN orders p ON c.order_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in shipments (order_id)", orphans)
		}
	})

	t.Run("FK: payments -> orders", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM payments c
			LEFT JOIN orders p ON c.order_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in payments (order_id)", orphans)
		}
	})

	t.Run("FK: reviews -> users", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM reviews c
			LEFT JOIN users p ON c.user_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in reviews (user_id)", orphans)
		}
	})

	t.Run("FK: reviews -> products", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM reviews c
			LEFT JOIN products p ON c.product_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in reviews (product_id)", orphans)
		}
	})

	t.Run("FK: wishlists -> users", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM wishlists c
			LEFT JOIN users p ON c.user_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in wishlists (user_id)", orphans)
		}
	})

	t.Run("FK: wishlist_items -> wishlists", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM wishlist_items c
			LEFT JOIN wishlists p ON c.wishlist_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in wishlist_items (wishlist_id)", orphans)
		}
	})

	t.Run("FK: wishlist_items -> products", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM wishlist_items c
			LEFT JOIN products p ON c.product_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in wishlist_items (product_id)", orphans)
		}
	})

	// Value constraint checks

	t.Run("value constraints: reviews rating 1-5", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM reviews WHERE rating < 1 OR rating > 5`).Scan(&bad)
		if err != nil {
			t.Fatalf("constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d reviews with rating outside 1-5", bad)
		}
	})

	t.Run("value constraints: products price positive", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM products WHERE price <= 0`).Scan(&bad)
		if err != nil {
			t.Fatalf("constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d products with non-positive price", bad)
		}
	})

	t.Run("value constraints: order_items quantity positive", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM order_items WHERE quantity < 1`).Scan(&bad)
		if err != nil {
			t.Fatalf("constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d order_items with quantity < 1", bad)
		}
	})

	// FK discovery subtest

	t.Run("FK discovery: all relationships detected", func(t *testing.T) {
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
			"addresses":      1, // user_id
			"products":       2, // category_id, brand_id
			"product_tags":   2, // product_id, tag_id
			"orders":         3, // user_id, address_id, coupon_id
			"order_items":    2, // order_id, product_id
			"shipments":      1, // order_id
			"payments":       1, // order_id
			"reviews":        2, // user_id, product_id
			"wishlists":      1, // user_id
			"wishlist_items": 2, // wishlist_id, product_id
			"categories":     1, // parent_id (self-ref)
		}
		for tbl, expectedFKs := range expected {
			if fksByTable[tbl] != expectedFKs {
				t.Errorf("table %s: expected %d FK(s), got %d", tbl, expectedFKs, fksByTable[tbl])
			}
		}
		t.Logf("FK discovery: %+v", fksByTable)
	})

	// Enum discovery subtest

	t.Run("enum discovery: orders.status", func(t *testing.T) {
		tables, err := db.Introspect(mysqlDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}

		expectedValues := []string{"pending", "processing", "shipped", "delivered", "cancelled"}
		for _, tbl := range tables {
			if tbl.Name != "orders" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "status" {
					continue
				}
				if len(col.EnumValues) == 0 {
					t.Error("orders.status: expected enum values, got none")
					return
				}
				got := map[string]bool{}
				for _, v := range col.EnumValues {
					got[v] = true
				}
				for _, want := range expectedValues {
					if !got[want] {
						t.Errorf("orders.status: missing expected enum value %q", want)
					}
				}
				t.Logf("orders.status enum values: %v", col.EnumValues)
			}
		}
	})

	t.Run("teardown", func(t *testing.T) {
		execScript(t, conn, "schema_mysql.sql") // re-run drops everything
	})
}
