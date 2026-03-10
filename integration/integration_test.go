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
				Type:     col.Type,
				PK:       col.IsPK,
				Nullable: col.IsNullable,
				Faker:    faker.MapColumnToFaker(driver, col),
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
		fmt.Fprintf(&sb, "  %-25s %d rows\n", tableName, len(data[tableName]))
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
			// L0
			"brands", "tags", "users", "coupons", "companies", "suppliers",
			// L1
			"categories", "addresses", "departments", "warehouses", "wishlists",
			// L2
			"products", "employees",
			// L3
			"product_tags", "orders", "projects", "inventory", "purchase_orders",
			"support_tickets", "reviews", "wishlist_items", "audit_logs",
			// L4
			"order_items", "shipments", "payments", "project_assignments",
			"purchase_order_items", "return_requests",
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
			fmt.Fprintf(&sb, "  %-25s %d\n", tc.name, tc.n)
		}
		_ = data // data available for callers; row counts come from DB
		t.Log(sb.String())
	})

	// ── FK integrity checks (existing) ─────────────────────────────────────────

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

	// ── FK integrity checks (new tables) ───────────────────────────────────────

	t.Run("FK: departments -> companies", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM departments c
			LEFT JOIN companies p ON c.company_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in departments (company_id)", orphans)
		}
	})

	t.Run("FK: departments -> head_employee (non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM departments c
			LEFT JOIN employees p ON c.head_employee_id = p.id
			WHERE c.head_employee_id IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in departments (head_employee_id)", orphans)
		}
	})

	t.Run("FK: employees -> departments", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM employees c
			LEFT JOIN departments p ON c.department_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in employees (department_id)", orphans)
		}
	})

	t.Run("FK: warehouses -> companies", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM warehouses c
			LEFT JOIN companies p ON c.company_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in warehouses (company_id)", orphans)
		}
	})

	t.Run("FK: projects -> departments", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM projects c
			LEFT JOIN departments p ON c.department_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in projects (department_id)", orphans)
		}
	})

	t.Run("FK: projects -> employees (lead)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM projects c
			LEFT JOIN employees p ON c.lead_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in projects (lead_id)", orphans)
		}
	})

	t.Run("FK: project_assignments -> projects", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM project_assignments c
			LEFT JOIN projects p ON c.project_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in project_assignments (project_id)", orphans)
		}
	})

	t.Run("FK: project_assignments -> employees", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM project_assignments c
			LEFT JOIN employees p ON c.employee_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in project_assignments (employee_id)", orphans)
		}
	})

	t.Run("FK: inventory -> products", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM inventory c
			LEFT JOIN products p ON c.product_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in inventory (product_id)", orphans)
		}
	})

	t.Run("FK: inventory -> warehouses", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM inventory c
			LEFT JOIN warehouses p ON c.warehouse_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in inventory (warehouse_id)", orphans)
		}
	})

	t.Run("FK: purchase_orders -> suppliers", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM purchase_orders c
			LEFT JOIN suppliers p ON c.supplier_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in purchase_orders (supplier_id)", orphans)
		}
	})

	t.Run("FK: purchase_orders -> employees (approved_by, non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM purchase_orders c
			LEFT JOIN employees p ON c.approved_by = p.id
			WHERE c.approved_by IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in purchase_orders (approved_by)", orphans)
		}
	})

	t.Run("FK: purchase_order_items -> purchase_orders", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM purchase_order_items c
			LEFT JOIN purchase_orders p ON c.purchase_order_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in purchase_order_items (purchase_order_id)", orphans)
		}
	})

	t.Run("FK: purchase_order_items -> products", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM purchase_order_items c
			LEFT JOIN products p ON c.product_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in purchase_order_items (product_id)", orphans)
		}
	})

	t.Run("FK: support_tickets -> users", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM support_tickets c
			LEFT JOIN users p ON c.user_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in support_tickets (user_id)", orphans)
		}
	})

	t.Run("FK: support_tickets -> employees (assigned_to, non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM support_tickets c
			LEFT JOIN employees p ON c.assigned_to = p.id
			WHERE c.assigned_to IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in support_tickets (assigned_to)", orphans)
		}
	})

	t.Run("FK: support_tickets -> orders (non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM support_tickets c
			LEFT JOIN orders p ON c.order_id = p.id
			WHERE c.order_id IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in support_tickets (order_id)", orphans)
		}
	})

	t.Run("FK: return_requests -> order_items", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM return_requests c
			LEFT JOIN order_items p ON c.order_item_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in return_requests (order_item_id)", orphans)
		}
	})

	t.Run("FK: return_requests -> employees (processed_by, non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM return_requests c
			LEFT JOIN employees p ON c.processed_by = p.id
			WHERE c.processed_by IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in return_requests (processed_by)", orphans)
		}
	})

	t.Run("FK: audit_logs -> users (non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM audit_logs c
			LEFT JOIN users p ON c.user_id = p.id
			WHERE c.user_id IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in audit_logs (user_id)", orphans)
		}
	})

	t.Run("FK: audit_logs -> employees (non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM audit_logs c
			LEFT JOIN employees p ON c.employee_id = p.id
			WHERE c.employee_id IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in audit_logs (employee_id)", orphans)
		}
	})

	// ── Value constraint checks ─────────────────────────────────────────────────

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

	t.Run("value constraints: employees salary positive", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM employees WHERE salary IS NOT NULL AND salary <= 0`).Scan(&bad)
		if err != nil {
			t.Fatalf("constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d employees with non-positive salary", bad)
		}
	})

	t.Run("value constraints: inventory quantity non-negative", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM inventory WHERE quantity < 0`).Scan(&bad)
		if err != nil {
			t.Fatalf("constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d inventory rows with negative quantity", bad)
		}
	})

	t.Run("value constraints: purchase_order_items quantity positive", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM purchase_order_items WHERE quantity < 1`).Scan(&bad)
		if err != nil {
			t.Fatalf("constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d purchase_order_items with quantity < 1", bad)
		}
	})

	// ── FK discovery subtest ────────────────────────────────────────────────────

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
			// existing
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
			// new
			"departments":          3, // company_id, parent_dept_id (self-ref), head_employee_id
			"employees":            2, // department_id, manager_id (self-ref)
			"warehouses":           1, // company_id
			"projects":             2, // department_id, lead_id
			"project_assignments":  2, // project_id, employee_id
			"inventory":            2, // product_id, warehouse_id
			"purchase_orders":      2, // supplier_id, approved_by
			"purchase_order_items": 2, // purchase_order_id, product_id
			"support_tickets":      3, // user_id, assigned_to, order_id
			"return_requests":      2, // order_item_id, processed_by
			"audit_logs":           2, // user_id, employee_id
		}
		for tbl, expectedFKs := range expected {
			if fksByTable[tbl] != expectedFKs {
				t.Errorf("table %s: expected %d FK(s), got %d", tbl, expectedFKs, fksByTable[tbl])
			}
		}
		t.Logf("FK discovery: %+v", fksByTable)
	})

	// ── Enum discovery subtests ─────────────────────────────────────────────────

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

	t.Run("enum discovery: employees.status", func(t *testing.T) {
		tables, err := db.Introspect(postgresDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}

		expectedValues := []string{"active", "inactive", "on_leave", "terminated"}
		for _, tbl := range tables {
			if tbl.Name != "employees" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "status" {
					continue
				}
				if len(col.EnumValues) == 0 {
					t.Error("employees.status: expected enum values, got none")
					return
				}
				got := map[string]bool{}
				for _, v := range col.EnumValues {
					got[v] = true
				}
				for _, want := range expectedValues {
					if !got[want] {
						t.Errorf("employees.status: missing expected enum value %q", want)
					}
				}
				t.Logf("employees.status enum values: %v", col.EnumValues)
			}
		}
	})

	// ── Self-ref root node subtests ─────────────────────────────────────────────

	t.Run("self-ref: categories has root nodes (NULL parent_id)", func(t *testing.T) {
		var roots int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM categories WHERE parent_id IS NULL`).Scan(&roots)
		if err != nil {
			t.Fatalf("self-ref check: %v", err)
		}
		if roots == 0 {
			t.Error("categories: expected at least one root node (NULL parent_id), got 0")
		}
		t.Logf("categories root nodes: %d", roots)
	})

	t.Run("self-ref: departments has root nodes (NULL parent_dept_id)", func(t *testing.T) {
		var roots int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM departments WHERE parent_dept_id IS NULL`).Scan(&roots)
		if err != nil {
			t.Fatalf("self-ref check: %v", err)
		}
		if roots == 0 {
			t.Error("departments: expected at least one root node (NULL parent_dept_id), got 0")
		}
		t.Logf("departments root nodes: %d", roots)
	})

	t.Run("self-ref: employees has root nodes (NULL manager_id)", func(t *testing.T) {
		var roots int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM employees WHERE manager_id IS NULL`).Scan(&roots)
		if err != nil {
			t.Fatalf("self-ref check: %v", err)
		}
		if roots == 0 {
			t.Error("employees: expected at least one root node (NULL manager_id), got 0")
		}
		t.Logf("employees root nodes: %d", roots)
	})

	// ── Deep chain subtest ──────────────────────────────────────────────────────

	t.Run("deep chain: return_requests -> order_items -> orders -> users", func(t *testing.T) {
		var broken int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM return_requests rr
			JOIN order_items oi ON rr.order_item_id = oi.id
			JOIN orders o ON oi.order_id = o.id
			JOIN users u ON o.user_id = u.id
			WHERE u.id IS NULL`).Scan(&broken)
		if err != nil {
			t.Fatalf("deep chain check: %v", err)
		}
		if broken > 0 {
			t.Errorf("deep chain broken: %d return_requests have no traceable user", broken)
		}
	})

	// ── Enum coverage subtests ───────────────────────────────────────────────────

	// ── Enum coverage subtests ───────────────────────────────────────────────────

	enumCountQuery := func(t *testing.T, table, col, val string) int {
		t.Helper()
		var n int
		q := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s = $1", table, col) //nolint:gosec
		if err := conn.QueryRowContext(context.Background(), q, val).Scan(&n); err != nil {
			t.Fatalf("enum count query %s.%s=%q: %v", table, col, val, err)
		}
		return n
	}

	t.Run("enum coverage: orders.status each value >= seedRows", func(t *testing.T) {
		for _, want := range []string{"pending", "processing", "shipped", "delivered", "cancelled"} {
			if n := enumCountQuery(t, "orders", "status", want); n < seedRows {
				t.Errorf("orders.status=%q: expected >= %d rows, got %d", want, seedRows, n)
			}
		}
	})

	t.Run("enum coverage: support_tickets.status each value >= seedRows", func(t *testing.T) {
		for _, want := range []string{"open", "in_progress", "resolved", "closed"} {
			if n := enumCountQuery(t, "support_tickets", "status", want); n < seedRows {
				t.Errorf("support_tickets.status=%q: expected >= %d rows, got %d", want, seedRows, n)
			}
		}
	})

	t.Run("enum coverage: support_tickets.priority each value >= seedRows", func(t *testing.T) {
		for _, want := range []string{"low", "medium", "high", "critical"} {
			if n := enumCountQuery(t, "support_tickets", "priority", want); n < seedRows {
				t.Errorf("support_tickets.priority=%q: expected >= %d rows, got %d", want, seedRows, n)
			}
		}
	})

	t.Run("enum coverage: employees.status each value >= seedRows", func(t *testing.T) {
		for _, want := range []string{"active", "inactive", "on_leave", "terminated"} {
			if n := enumCountQuery(t, "employees", "status", want); n < seedRows {
				t.Errorf("employees.status=%q: expected >= %d rows, got %d", want, seedRows, n)
			}
		}
	})

	// ── Constraint-aware introspection subtests ──────────────────────────────────

	t.Run("constraint: users.email detected as UNIQUE", func(t *testing.T) {
		tables, err := db.Introspect(postgresDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		for _, tbl := range tables {
			if tbl.Name != "users" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "email" {
					continue
				}
				if !col.Unique {
					t.Error("users.email: expected Unique=true, got false")
				}
				t.Logf("users.email Unique=%v", col.Unique)
				return
			}
		}
		t.Error("users.email column not found in introspection result")
	})

	t.Run("constraint: users.username detected as UNIQUE", func(t *testing.T) {
		tables, err := db.Introspect(postgresDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		for _, tbl := range tables {
			if tbl.Name != "users" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "username" {
					continue
				}
				if !col.Unique {
					t.Error("users.username: expected Unique=true, got false")
				}
				t.Logf("users.username Unique=%v", col.Unique)
				return
			}
		}
		t.Error("users.username column not found in introspection result")
	})

	t.Run("constraint: coupons.code detected as UNIQUE", func(t *testing.T) {
		tables, err := db.Introspect(postgresDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		for _, tbl := range tables {
			if tbl.Name != "coupons" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "code" {
					continue
				}
				if !col.Unique {
					t.Error("coupons.code: expected Unique=true, got false")
				}
				t.Logf("coupons.code Unique=%v", col.Unique)
				return
			}
		}
		t.Error("coupons.code column not found in introspection result")
	})

	t.Run("constraint: users.role CHECK values detected", func(t *testing.T) {
		tables, err := db.Introspect(postgresDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		for _, tbl := range tables {
			if tbl.Name != "users" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "role" {
					continue
				}
				if len(col.CheckValues) == 0 {
					t.Error("users.role: expected CheckValues to be populated, got none")
					return
				}
				got := map[string]bool{}
				for _, v := range col.CheckValues {
					got[v] = true
				}
				for _, want := range []string{"admin", "user", "guest"} {
					if !got[want] {
						t.Errorf("users.role: missing expected check value %q, got %v", want, col.CheckValues)
					}
				}
				t.Logf("users.role CheckValues=%v", col.CheckValues)
				return
			}
		}
		t.Error("users.role column not found in introspection result")
	})

	t.Run("constraint: users.email gets uuid faker (UNIQUE)", func(t *testing.T) {
		tables, err := db.Introspect(postgresDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		for _, tbl := range tables {
			if tbl.Name != "users" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "email" {
					continue
				}
				f := faker.MapColumnToFaker(postgresDriver, col)
				if f != "uuid" {
					t.Errorf("users.email (UNIQUE): expected faker %q, got %q", "uuid", f)
				}
				return
			}
		}
	})

	t.Run("constraint: users.role gets randomstring faker (CHECK)", func(t *testing.T) {
		tables, err := db.Introspect(postgresDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		for _, tbl := range tables {
			if tbl.Name != "users" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "role" {
					continue
				}
				f := faker.MapColumnToFaker(postgresDriver, col)
				if len(f) == 0 || f[:13] != "randomstring(" {
					t.Errorf("users.role (CHECK): expected randomstring(...) faker, got %q", f)
				}
				t.Logf("users.role faker=%q", f)
				return
			}
		}
	})

	t.Run("constraint: users.role values all within CHECK set after seed", func(t *testing.T) {
		var bad int
		if err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM users WHERE role NOT IN ('admin', 'user', 'guest')`).Scan(&bad); err != nil {
			t.Fatalf("CHECK constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d users with role outside CHECK constraint values", bad)
		}
	})

	t.Run("constraint: users.email all unique after seed", func(t *testing.T) {
		var total, distinct int
		if err := conn.QueryRowContext(context.Background(), `SELECT COUNT(*), COUNT(DISTINCT email) FROM users`).Scan(&total, &distinct); err != nil {
			t.Fatalf("uniqueness check: %v", err)
		}
		if total != distinct {
			t.Errorf("users.email: %d total rows but only %d distinct emails — UNIQUE violated", total, distinct)
		}
		t.Logf("users.email: %d rows, all distinct", total)
	})

	t.Run("constraint: coupons.code all unique after seed", func(t *testing.T) {
		var total, distinct int
		if err := conn.QueryRowContext(context.Background(), `SELECT COUNT(*), COUNT(DISTINCT code) FROM coupons`).Scan(&total, &distinct); err != nil {
			t.Fatalf("uniqueness check: %v", err)
		}
		if total != distinct {
			t.Errorf("coupons.code: %d total rows but only %d distinct codes — UNIQUE violated", total, distinct)
		}
		t.Logf("coupons.code: %d rows, all distinct", total)
	})

	t.Run("constraint: products.rating detected as range CHECK", func(t *testing.T) {
		tables, err := db.Introspect(postgresDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		for _, tbl := range tables {
			if tbl.Name != "products" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "rating" {
					continue
				}
				if col.CheckMin == nil || col.CheckMax == nil {
					t.Error("products.rating: expected CheckMin/CheckMax to be set for range constraint, got nil")
					return
				}
				if *col.CheckMin != 1 || *col.CheckMax != 5 {
					t.Errorf("products.rating: expected range [1,5], got [%d,%d]", *col.CheckMin, *col.CheckMax)
				}
				t.Logf("products.rating CheckMin=%d CheckMax=%d", *col.CheckMin, *col.CheckMax)
				return
			}
		}
		t.Error("products.rating column not found in introspection result")
	})

	t.Run("constraint: products.rating gets number faker (range CHECK)", func(t *testing.T) {
		tables, err := db.Introspect(postgresDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		for _, tbl := range tables {
			if tbl.Name != "products" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "rating" {
					continue
				}
				f := faker.MapColumnToFaker(postgresDriver, col)
				if f != "number(1,5)" {
					t.Errorf("products.rating (range CHECK 1-5): expected faker %q, got %q", "number(1,5)", f)
				}
				return
			}
		}
	})

	t.Run("constraint: products.rating values within range after seed", func(t *testing.T) {
		var bad int
		if err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM products WHERE rating < 1 OR rating > 5`).Scan(&bad); err != nil {
			t.Fatalf("range constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d products with rating outside range [1,5]", bad)
		}
	})

	// ── Truncate subtests ────────────────────────────────────────────────────────

	var truncateSortedTables []string

	t.Run("truncate: resolve table order", func(t *testing.T) {
		tables, err := db.Introspect(postgresDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		s := &schema.Schema{Tables: make(map[string]schema.Table, len(tables))}
		for _, tbl := range tables {
			st := schema.Table{Columns: make(map[string]schema.Column, len(tbl.Columns))}
			for _, col := range tbl.Columns {
				sc := schema.Column{Type: col.Type, PK: col.IsPK, Nullable: col.IsNullable}
				if col.FK != nil {
					sc.FK = fmt.Sprintf("%s.%s", col.FK.TableName, col.FK.ColumnName)
				}
				st.Columns[col.Name] = sc
			}
			s.Tables[tbl.Name] = st
		}
		g := graph.Build(s)
		sorted, err := g.TopologicalSort()
		if err != nil {
			t.Fatalf("topological sort: %v", err)
		}
		truncateSortedTables = sorted
	})

	t.Run("truncate: all tables empty after truncate", func(t *testing.T) {
		if len(truncateSortedTables) == 0 {
			t.Skip("table order not resolved — previous subtest failed")
		}
		if err := db.Truncate(context.Background(), conn, postgresDriver, truncateSortedTables); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		allTables := []string{
			"brands", "tags", "users", "coupons", "companies", "suppliers",
			"categories", "addresses", "departments", "warehouses", "wishlists",
			"products", "employees",
			"product_tags", "orders", "projects", "inventory", "purchase_orders",
			"support_tickets", "reviews", "wishlist_items", "audit_logs",
			"order_items", "shipments", "payments", "project_assignments",
			"purchase_order_items", "return_requests",
		}
		for _, tbl := range allTables {
			if n := countRows(t, conn, tbl); n != 0 {
				t.Errorf("table %s: expected 0 rows after truncate, got %d", tbl, n)
			}
		}
	})

	t.Run("truncate: re-seed after truncate succeeds", func(t *testing.T) {
		if len(truncateSortedTables) == 0 {
			t.Skip("table order not resolved — previous subtest failed")
		}
		buildAndSeed(t, "postgres (post-truncate)", postgresDriver, dsn, conn)
		allTables := []string{
			"brands", "tags", "users", "coupons", "companies", "suppliers",
			"categories", "addresses", "departments", "warehouses", "wishlists",
			"products", "employees",
			"product_tags", "orders", "projects", "inventory", "purchase_orders",
			"support_tickets", "reviews", "wishlist_items", "audit_logs",
			"order_items", "shipments", "payments", "project_assignments",
			"purchase_order_items", "return_requests",
		}
		for _, tbl := range allTables {
			if n := countRows(t, conn, tbl); n == 0 {
				t.Errorf("table %s has 0 rows after re-seed — expected > 0", tbl)
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
			// L0
			"brands", "tags", "users", "coupons", "companies", "suppliers",
			// L1
			"categories", "addresses", "departments", "warehouses", "wishlists",
			// L2
			"products", "employees",
			// L3
			"product_tags", "orders", "projects", "inventory", "purchase_orders",
			"support_tickets", "reviews", "wishlist_items", "audit_logs",
			// L4
			"order_items", "shipments", "payments", "project_assignments",
			"purchase_order_items", "return_requests",
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
			fmt.Fprintf(&sb, "  %-25s %d\n", tc.name, tc.n)
		}
		_ = data
		t.Log(sb.String())
	})

	// ── FK integrity checks (existing) ─────────────────────────────────────────

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

	// ── FK integrity checks (new tables) ───────────────────────────────────────

	t.Run("FK: departments -> companies", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM departments c
			LEFT JOIN companies p ON c.company_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in departments (company_id)", orphans)
		}
	})

	t.Run("FK: departments -> head_employee (non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM departments c
			LEFT JOIN employees p ON c.head_employee_id = p.id
			WHERE c.head_employee_id IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in departments (head_employee_id)", orphans)
		}
	})

	t.Run("FK: employees -> departments", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM employees c
			LEFT JOIN departments p ON c.department_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in employees (department_id)", orphans)
		}
	})

	t.Run("FK: warehouses -> companies", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM warehouses c
			LEFT JOIN companies p ON c.company_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in warehouses (company_id)", orphans)
		}
	})

	t.Run("FK: projects -> departments", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM projects c
			LEFT JOIN departments p ON c.department_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in projects (department_id)", orphans)
		}
	})

	t.Run("FK: projects -> employees (lead)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM projects c
			LEFT JOIN employees p ON c.lead_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in projects (lead_id)", orphans)
		}
	})

	t.Run("FK: project_assignments -> projects", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM project_assignments c
			LEFT JOIN projects p ON c.project_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in project_assignments (project_id)", orphans)
		}
	})

	t.Run("FK: project_assignments -> employees", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM project_assignments c
			LEFT JOIN employees p ON c.employee_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in project_assignments (employee_id)", orphans)
		}
	})

	t.Run("FK: inventory -> products", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM inventory c
			LEFT JOIN products p ON c.product_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in inventory (product_id)", orphans)
		}
	})

	t.Run("FK: inventory -> warehouses", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM inventory c
			LEFT JOIN warehouses p ON c.warehouse_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in inventory (warehouse_id)", orphans)
		}
	})

	t.Run("FK: purchase_orders -> suppliers", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM purchase_orders c
			LEFT JOIN suppliers p ON c.supplier_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in purchase_orders (supplier_id)", orphans)
		}
	})

	t.Run("FK: purchase_orders -> employees (approved_by, non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM purchase_orders c
			LEFT JOIN employees p ON c.approved_by = p.id
			WHERE c.approved_by IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in purchase_orders (approved_by)", orphans)
		}
	})

	t.Run("FK: purchase_order_items -> purchase_orders", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM purchase_order_items c
			LEFT JOIN purchase_orders p ON c.purchase_order_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in purchase_order_items (purchase_order_id)", orphans)
		}
	})

	t.Run("FK: purchase_order_items -> products", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM purchase_order_items c
			LEFT JOIN products p ON c.product_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in purchase_order_items (product_id)", orphans)
		}
	})

	t.Run("FK: support_tickets -> users", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM support_tickets c
			LEFT JOIN users p ON c.user_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in support_tickets (user_id)", orphans)
		}
	})

	t.Run("FK: support_tickets -> employees (assigned_to, non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM support_tickets c
			LEFT JOIN employees p ON c.assigned_to = p.id
			WHERE c.assigned_to IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in support_tickets (assigned_to)", orphans)
		}
	})

	t.Run("FK: support_tickets -> orders (non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM support_tickets c
			LEFT JOIN orders p ON c.order_id = p.id
			WHERE c.order_id IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in support_tickets (order_id)", orphans)
		}
	})

	t.Run("FK: return_requests -> order_items", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM return_requests c
			LEFT JOIN order_items p ON c.order_item_id = p.id
			WHERE p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in return_requests (order_item_id)", orphans)
		}
	})

	t.Run("FK: return_requests -> employees (processed_by, non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM return_requests c
			LEFT JOIN employees p ON c.processed_by = p.id
			WHERE c.processed_by IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in return_requests (processed_by)", orphans)
		}
	})

	t.Run("FK: audit_logs -> users (non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM audit_logs c
			LEFT JOIN users p ON c.user_id = p.id
			WHERE c.user_id IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in audit_logs (user_id)", orphans)
		}
	})

	t.Run("FK: audit_logs -> employees (non-null)", func(t *testing.T) {
		var orphans int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM audit_logs c
			LEFT JOIN employees p ON c.employee_id = p.id
			WHERE c.employee_id IS NOT NULL AND p.id IS NULL`).Scan(&orphans)
		if err != nil {
			t.Fatalf("FK check: %v", err)
		}
		if orphans > 0 {
			t.Errorf("found %d orphaned rows in audit_logs (employee_id)", orphans)
		}
	})

	// ── Value constraint checks ─────────────────────────────────────────────────

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

	t.Run("value constraints: employees salary positive", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM employees WHERE salary IS NOT NULL AND salary <= 0`).Scan(&bad)
		if err != nil {
			t.Fatalf("constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d employees with non-positive salary", bad)
		}
	})

	t.Run("value constraints: inventory quantity non-negative", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM inventory WHERE quantity < 0`).Scan(&bad)
		if err != nil {
			t.Fatalf("constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d inventory rows with negative quantity", bad)
		}
	})

	t.Run("value constraints: purchase_order_items quantity positive", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM purchase_order_items WHERE quantity < 1`).Scan(&bad)
		if err != nil {
			t.Fatalf("constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d purchase_order_items with quantity < 1", bad)
		}
	})

	// ── FK discovery subtest ────────────────────────────────────────────────────

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
			// existing
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
			// new
			"departments":          3, // company_id, parent_dept_id (self-ref), head_employee_id
			"employees":            2, // department_id, manager_id (self-ref)
			"warehouses":           1, // company_id
			"projects":             2, // department_id, lead_id
			"project_assignments":  2, // project_id, employee_id
			"inventory":            2, // product_id, warehouse_id
			"purchase_orders":      2, // supplier_id, approved_by
			"purchase_order_items": 2, // purchase_order_id, product_id
			"support_tickets":      3, // user_id, assigned_to, order_id
			"return_requests":      2, // order_item_id, processed_by
			"audit_logs":           2, // user_id, employee_id
		}
		for tbl, expectedFKs := range expected {
			if fksByTable[tbl] != expectedFKs {
				t.Errorf("table %s: expected %d FK(s), got %d", tbl, expectedFKs, fksByTable[tbl])
			}
		}
		t.Logf("FK discovery: %+v", fksByTable)
	})

	// ── Enum discovery subtests ─────────────────────────────────────────────────

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

	t.Run("enum discovery: employees.status", func(t *testing.T) {
		tables, err := db.Introspect(mysqlDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}

		expectedValues := []string{"active", "inactive", "on_leave", "terminated"}
		for _, tbl := range tables {
			if tbl.Name != "employees" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "status" {
					continue
				}
				if len(col.EnumValues) == 0 {
					t.Error("employees.status: expected enum values, got none")
					return
				}
				got := map[string]bool{}
				for _, v := range col.EnumValues {
					got[v] = true
				}
				for _, want := range expectedValues {
					if !got[want] {
						t.Errorf("employees.status: missing expected enum value %q", want)
					}
				}
				t.Logf("employees.status enum values: %v", col.EnumValues)
			}
		}
	})

	// ── Self-ref root node subtests ─────────────────────────────────────────────

	t.Run("self-ref: categories has root nodes (NULL parent_id)", func(t *testing.T) {
		var roots int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM categories WHERE parent_id IS NULL`).Scan(&roots)
		if err != nil {
			t.Fatalf("self-ref check: %v", err)
		}
		if roots == 0 {
			t.Error("categories: expected at least one root node (NULL parent_id), got 0")
		}
		t.Logf("categories root nodes: %d", roots)
	})

	t.Run("self-ref: departments has root nodes (NULL parent_dept_id)", func(t *testing.T) {
		var roots int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM departments WHERE parent_dept_id IS NULL`).Scan(&roots)
		if err != nil {
			t.Fatalf("self-ref check: %v", err)
		}
		if roots == 0 {
			t.Error("departments: expected at least one root node (NULL parent_dept_id), got 0")
		}
		t.Logf("departments root nodes: %d", roots)
	})

	t.Run("self-ref: employees has root nodes (NULL manager_id)", func(t *testing.T) {
		var roots int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM employees WHERE manager_id IS NULL`).Scan(&roots)
		if err != nil {
			t.Fatalf("self-ref check: %v", err)
		}
		if roots == 0 {
			t.Error("employees: expected at least one root node (NULL manager_id), got 0")
		}
		t.Logf("employees root nodes: %d", roots)
	})

	// ── Deep chain subtest ──────────────────────────────────────────────────────

	t.Run("deep chain: return_requests -> order_items -> orders -> users", func(t *testing.T) {
		var broken int
		err := conn.QueryRowContext(context.Background(), `
			SELECT COUNT(*) FROM return_requests rr
			JOIN order_items oi ON rr.order_item_id = oi.id
			JOIN orders o ON oi.order_id = o.id
			JOIN users u ON o.user_id = u.id
			WHERE u.id IS NULL`).Scan(&broken)
		if err != nil {
			t.Fatalf("deep chain check: %v", err)
		}
		if broken > 0 {
			t.Errorf("deep chain broken: %d return_requests have no traceable user", broken)
		}
	})

	// ── Enum coverage subtests ───────────────────────────────────────────────────

	enumCountQueryMy := func(t *testing.T, table, col, val string) int {
		t.Helper()
		var n int
		q := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s = ?", table, col) //nolint:gosec
		if err := conn.QueryRowContext(context.Background(), q, val).Scan(&n); err != nil {
			t.Fatalf("enum count query %s.%s=%q: %v", table, col, val, err)
		}
		return n
	}

	t.Run("enum coverage: orders.status each value >= seedRows", func(t *testing.T) {
		for _, want := range []string{"pending", "processing", "shipped", "delivered", "cancelled"} {
			if n := enumCountQueryMy(t, "orders", "status", want); n < seedRows {
				t.Errorf("orders.status=%q: expected >= %d rows, got %d", want, seedRows, n)
			}
		}
	})

	t.Run("enum coverage: support_tickets.status each value >= seedRows", func(t *testing.T) {
		for _, want := range []string{"open", "in_progress", "resolved", "closed"} {
			if n := enumCountQueryMy(t, "support_tickets", "status", want); n < seedRows {
				t.Errorf("support_tickets.status=%q: expected >= %d rows, got %d", want, seedRows, n)
			}
		}
	})

	t.Run("enum coverage: support_tickets.priority each value >= seedRows", func(t *testing.T) {
		for _, want := range []string{"low", "medium", "high", "critical"} {
			if n := enumCountQueryMy(t, "support_tickets", "priority", want); n < seedRows {
				t.Errorf("support_tickets.priority=%q: expected >= %d rows, got %d", want, seedRows, n)
			}
		}
	})

	t.Run("enum coverage: employees.status each value >= seedRows", func(t *testing.T) {
		for _, want := range []string{"active", "inactive", "on_leave", "terminated"} {
			if n := enumCountQueryMy(t, "employees", "status", want); n < seedRows {
				t.Errorf("employees.status=%q: expected >= %d rows, got %d", want, seedRows, n)
			}
		}
	})

	// ── Constraint-aware introspection subtests ──────────────────────────────────

	t.Run("constraint: users.email detected as UNIQUE", func(t *testing.T) {
		tables, err := db.Introspect(mysqlDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		for _, tbl := range tables {
			if tbl.Name != "users" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "email" {
					continue
				}
				if !col.Unique {
					t.Error("users.email: expected Unique=true, got false")
				}
				t.Logf("users.email Unique=%v", col.Unique)
				return
			}
		}
		t.Error("users.email column not found in introspection result")
	})

	t.Run("constraint: users.username detected as UNIQUE", func(t *testing.T) {
		tables, err := db.Introspect(mysqlDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		for _, tbl := range tables {
			if tbl.Name != "users" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "username" {
					continue
				}
				if !col.Unique {
					t.Error("users.username: expected Unique=true, got false")
				}
				t.Logf("users.username Unique=%v", col.Unique)
				return
			}
		}
		t.Error("users.username column not found in introspection result")
	})

	t.Run("constraint: coupons.code detected as UNIQUE", func(t *testing.T) {
		tables, err := db.Introspect(mysqlDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		for _, tbl := range tables {
			if tbl.Name != "coupons" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "code" {
					continue
				}
				if !col.Unique {
					t.Error("coupons.code: expected Unique=true, got false")
				}
				t.Logf("coupons.code Unique=%v", col.Unique)
				return
			}
		}
		t.Error("coupons.code column not found in introspection result")
	})

	t.Run("constraint: users.role CHECK values detected", func(t *testing.T) {
		tables, err := db.Introspect(mysqlDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		for _, tbl := range tables {
			if tbl.Name != "users" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "role" {
					continue
				}
				if len(col.CheckValues) == 0 {
					t.Error("users.role: expected CheckValues to be populated, got none")
					return
				}
				got := map[string]bool{}
				for _, v := range col.CheckValues {
					got[v] = true
				}
				for _, want := range []string{"admin", "user", "guest"} {
					if !got[want] {
						t.Errorf("users.role: missing expected check value %q, got %v", want, col.CheckValues)
					}
				}
				t.Logf("users.role CheckValues=%v", col.CheckValues)
				return
			}
		}
		t.Error("users.role column not found in introspection result")
	})

	t.Run("constraint: users.role gets randomstring faker (CHECK)", func(t *testing.T) {
		tables, err := db.Introspect(mysqlDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		for _, tbl := range tables {
			if tbl.Name != "users" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "role" {
					continue
				}
				f := faker.MapColumnToFaker(mysqlDriver, col)
				if len(f) == 0 || f[:13] != "randomstring(" {
					t.Errorf("users.role (CHECK): expected randomstring(...) faker, got %q", f)
				}
				t.Logf("users.role faker=%q", f)
				return
			}
		}
	})

	t.Run("constraint: users.role values all within CHECK set after seed", func(t *testing.T) {
		var bad int
		if err := conn.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM users WHERE role NOT IN ('admin', 'user', 'guest')").Scan(&bad); err != nil {
			t.Fatalf("CHECK constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d users with role outside CHECK constraint values", bad)
		}
	})

	t.Run("constraint: users.email all unique after seed", func(t *testing.T) {
		var total, distinct int
		if err := conn.QueryRowContext(context.Background(), "SELECT COUNT(*), COUNT(DISTINCT email) FROM users").Scan(&total, &distinct); err != nil {
			t.Fatalf("uniqueness check: %v", err)
		}
		if total != distinct {
			t.Errorf("users.email: %d total rows but only %d distinct emails — UNIQUE violated", total, distinct)
		}
		t.Logf("users.email: %d rows, all distinct", total)
	})

	t.Run("constraint: coupons.code all unique after seed", func(t *testing.T) {
		var total, distinct int
		if err := conn.QueryRowContext(context.Background(), "SELECT COUNT(*), COUNT(DISTINCT code) FROM coupons").Scan(&total, &distinct); err != nil {
			t.Fatalf("uniqueness check: %v", err)
		}
		if total != distinct {
			t.Errorf("coupons.code: %d total rows but only %d distinct codes — UNIQUE violated", total, distinct)
		}
		t.Logf("coupons.code: %d rows, all distinct", total)
	})

	t.Run("constraint: products.rating detected as range CHECK", func(t *testing.T) {
		tables, err := db.Introspect(mysqlDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		for _, tbl := range tables {
			if tbl.Name != "products" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "rating" {
					continue
				}
				if col.CheckMin == nil || col.CheckMax == nil {
					t.Error("products.rating: expected CheckMin/CheckMax to be set for range constraint, got nil")
					return
				}
				if *col.CheckMin != 1 || *col.CheckMax != 5 {
					t.Errorf("products.rating: expected range [1,5], got [%d,%d]", *col.CheckMin, *col.CheckMax)
				}
				t.Logf("products.rating CheckMin=%d CheckMax=%d", *col.CheckMin, *col.CheckMax)
				return
			}
		}
		t.Error("products.rating column not found in introspection result")
	})

	t.Run("constraint: products.rating gets number faker (range CHECK)", func(t *testing.T) {
		tables, err := db.Introspect(mysqlDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		for _, tbl := range tables {
			if tbl.Name != "products" {
				continue
			}
			for _, col := range tbl.Columns {
				if col.Name != "rating" {
					continue
				}
				f := faker.MapColumnToFaker(mysqlDriver, col)
				if f != "number(1,5)" {
					t.Errorf("products.rating (range CHECK 1-5): expected faker %q, got %q", "number(1,5)", f)
				}
				return
			}
		}
	})

	t.Run("constraint: products.rating values within range after seed", func(t *testing.T) {
		var bad int
		if err := conn.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM products WHERE rating < 1 OR rating > 5").Scan(&bad); err != nil {
			t.Fatalf("range constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d products with rating outside range [1,5]", bad)
		}
	})

	// ── Truncate subtests ────────────────────────────────────────────────────────

	var truncateSortedTablesMy []string

	t.Run("truncate: resolve table order", func(t *testing.T) {
		tables, err := db.Introspect(mysqlDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		s := &schema.Schema{Tables: make(map[string]schema.Table, len(tables))}
		for _, tbl := range tables {
			st := schema.Table{Columns: make(map[string]schema.Column, len(tbl.Columns))}
			for _, col := range tbl.Columns {
				sc := schema.Column{Type: col.Type, PK: col.IsPK, Nullable: col.IsNullable}
				if col.FK != nil {
					sc.FK = fmt.Sprintf("%s.%s", col.FK.TableName, col.FK.ColumnName)
				}
				st.Columns[col.Name] = sc
			}
			s.Tables[tbl.Name] = st
		}
		g := graph.Build(s)
		sorted, err := g.TopologicalSort()
		if err != nil {
			t.Fatalf("topological sort: %v", err)
		}
		truncateSortedTablesMy = sorted
	})

	t.Run("truncate: all tables empty after truncate", func(t *testing.T) {
		if len(truncateSortedTablesMy) == 0 {
			t.Skip("table order not resolved — previous subtest failed")
		}
		if err := db.Truncate(context.Background(), conn, mysqlDriver, truncateSortedTablesMy); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		allTables := []string{
			"brands", "tags", "users", "coupons", "companies", "suppliers",
			"categories", "addresses", "departments", "warehouses", "wishlists",
			"products", "employees",
			"product_tags", "orders", "projects", "inventory", "purchase_orders",
			"support_tickets", "reviews", "wishlist_items", "audit_logs",
			"order_items", "shipments", "payments", "project_assignments",
			"purchase_order_items", "return_requests",
		}
		for _, tbl := range allTables {
			if n := countRows(t, conn, tbl); n != 0 {
				t.Errorf("table %s: expected 0 rows after truncate, got %d", tbl, n)
			}
		}
	})

	t.Run("truncate: re-seed after truncate succeeds", func(t *testing.T) {
		if len(truncateSortedTablesMy) == 0 {
			t.Skip("table order not resolved — previous subtest failed")
		}
		buildAndSeed(t, "mysql (post-truncate)", mysqlDriver, dsn, conn)
		allTables := []string{
			"brands", "tags", "users", "coupons", "companies", "suppliers",
			"categories", "addresses", "departments", "warehouses", "wishlists",
			"products", "employees",
			"product_tags", "orders", "projects", "inventory", "purchase_orders",
			"support_tickets", "reviews", "wishlist_items", "audit_logs",
			"order_items", "shipments", "payments", "project_assignments",
			"purchase_order_items", "return_requests",
		}
		for _, tbl := range allTables {
			if n := countRows(t, conn, tbl); n == 0 {
				t.Errorf("table %s has 0 rows after re-seed — expected > 0", tbl)
			}
		}
	})

	t.Run("teardown", func(t *testing.T) {
		execScript(t, conn, "schema_mysql.sql") // re-run drops everything
	})
}
