//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
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
	execSQL(t, conn, string(raw))
}

func execSQL(t *testing.T, conn *sql.DB, body string) {
	t.Helper()
	// Split on ; and execute each statement
	for _, stmt := range strings.Split(body, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := conn.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec statement [%.80s...]: %v", stmt, err)
		}
	}
}

// execMySQLSchema loads schema_mysql.sql, downgrading any 8.0-only DDL shapes
// when the server doesn't support them, and executes the result. This keeps a
// single source of truth for the modern schema while still exercising the
// same table structure on MySQL 5.7.
func execMySQLSchema(t *testing.T, conn *sql.DB, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read script %s: %v", path, err)
	}
	sql := string(raw)
	// DEFAULT (<expr>) — functional column defaults — requires MySQL 8.0.13+.
	// We key off CHECK_CONSTRAINTS support (8.0.16+) as the single
	// "is this a modern MySQL?" probe for the test matrix.
	if !mysqlHasCheckSupport(t, conn) {
		sql = stripMySQLFunctionalDefaults(sql)
	}
	execSQL(t, conn, sql)
}

// stripMySQLFunctionalDefaults removes `DEFAULT (<expr>)` clauses from a DDL
// script. seedstorm provides a value for every column at INSERT time, so the
// default itself is never exercised during seeding — dropping it on older
// MySQL preserves table shape without changing seeding behaviour.
func stripMySQLFunctionalDefaults(sqlText string) string {
	return reMySQLFuncDefault.ReplaceAllString(sqlText, "")
}

// Matches the two functional defaults used in the test schema. Kept explicit
// rather than a generic `DEFAULT (...)` matcher because balanced parens (e.g.
// `UUID()` inside the outer `DEFAULT (...)`) would require a non-regular
// matcher — and an explicit allow-list makes it obvious what we're downgrading.
var reMySQLFuncDefault = regexp.MustCompile(`(?i)\s+DEFAULT\s+\(UUID\(\)\)`)

// mysqlHasCheckSupport reports whether this MySQL server exposes
// information_schema.CHECK_CONSTRAINTS (MySQL 8.0.16+). Used to skip
// CHECK-dependent subtests on older servers where seedstorm can't introspect
// the constraint even though the DDL parses successfully.
func mysqlHasCheckSupport(t *testing.T, conn *sql.DB) bool {
	t.Helper()
	var one int
	err := conn.QueryRowContext(context.Background(), `
		SELECT 1
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = 'information_schema'
		  AND TABLE_NAME   = 'CHECK_CONSTRAINTS'
		LIMIT 1`).Scan(&one)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("probe CHECK_CONSTRAINTS support: %v", err)
	}
	return true
}

func countRows(t *testing.T, conn *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(context.Background(), fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n); err != nil { //nolint:gosec
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func assertHardSelfRefSeeded(t *testing.T, conn *sql.DB) {
	t.Helper()
	var total, nulls, orphans int
	if err := conn.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM hard_self_employees`).Scan(&total); err != nil {
		t.Fatalf("hard self-ref count: %v", err)
	}
	if total == 0 {
		t.Fatal("hard_self_employees: expected seeded rows")
	}
	if err := conn.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM hard_self_employees WHERE manager_id IS NULL`).Scan(&nulls); err != nil {
		t.Fatalf("hard self-ref null check: %v", err)
	}
	if nulls != 0 {
		t.Fatalf("hard_self_employees: found %d NULL manager_id values", nulls)
	}
	if err := conn.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM hard_self_employees c
		LEFT JOIN hard_self_employees p ON c.manager_id = p.id
		WHERE p.id IS NULL`).Scan(&orphans); err != nil {
		t.Fatalf("hard self-ref FK check: %v", err)
	}
	if orphans != 0 {
		t.Fatalf("hard_self_employees: found %d orphaned manager_id values", orphans)
	}
}

func filterTables(tables []db.Table, names ...string) []db.Table {
	want := make(map[string]bool, len(names))
	for _, name := range names {
		want[name] = true
	}
	var out []db.Table
	for _, tbl := range tables {
		if want[tbl.Name] {
			out = append(out, tbl)
		}
	}
	return out
}

func cloneSmokeSchema(t *testing.T, driver string, conn *sql.DB) {
	t.Helper()
	if driver == postgresDriver {
		execSQL(t, conn, `
			DROP TABLE IF EXISTS clone_orders;
			DROP TABLE IF EXISTS clone_users;
			CREATE TABLE clone_users (
				id integer PRIMARY KEY,
				email varchar(255) NOT NULL UNIQUE,
				status varchar(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'blocked')),
				full_label varchar(300) GENERATED ALWAYS AS (email || ':' || status) STORED
			);
			CREATE TABLE clone_orders (
				id integer PRIMARY KEY,
				user_id integer NOT NULL REFERENCES clone_users(id),
				subtotal numeric(10,2) NOT NULL DEFAULT 10.00,
				tax numeric(10,2) NOT NULL DEFAULT 0.00,
				total numeric(10,2) GENERATED ALWAYS AS (subtotal + tax) STORED,
				quantity integer NOT NULL CHECK (quantity BETWEEN 1 AND 500)
			);
			CREATE INDEX idx_clone_orders_user_total ON clone_orders(user_id, total);
			CREATE UNIQUE INDEX uq_clone_users_status_email ON clone_users(status, email);
			COMMENT ON TABLE clone_users IS 'clone source users';
			COMMENT ON COLUMN clone_users.status IS 'workflow state';
		`)
		return
	}
	execSQL(t, conn, `
		SET FOREIGN_KEY_CHECKS=0;
		DROP TABLE IF EXISTS clone_orders;
		DROP TABLE IF EXISTS clone_users;
		SET FOREIGN_KEY_CHECKS=1;
		CREATE TABLE clone_users (
			id integer PRIMARY KEY,
			email varchar(255) NOT NULL UNIQUE,
			status varchar(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'blocked')),
			full_label varchar(300) GENERATED ALWAYS AS (concat(email, ':', status)) STORED,
			UNIQUE KEY uq_clone_users_status_email (status, email)
		) COMMENT='clone source users';
		ALTER TABLE clone_users MODIFY status varchar(20) NOT NULL DEFAULT 'active' COMMENT 'workflow state';
		CREATE INDEX idx_clone_users_status ON clone_users(status);
		CREATE TABLE clone_orders (
			id integer PRIMARY KEY,
			user_id integer NOT NULL,
			subtotal decimal(10,2) NOT NULL DEFAULT 10.00,
			tax decimal(10,2) NOT NULL DEFAULT 0.00,
			total decimal(10,2) GENERATED ALWAYS AS (subtotal + tax) STORED,
			quantity integer NOT NULL CHECK (quantity BETWEEN 1 AND 500),
			FOREIGN KEY (user_id) REFERENCES clone_users(id)
		);
		CREATE INDEX idx_clone_orders_user_total ON clone_orders(user_id, total);
	`)
}

func dropCloneSmokeSchema(t *testing.T, driver string, conn *sql.DB) {
	t.Helper()
	if driver == postgresDriver {
		execSQL(t, conn, `DROP TABLE IF EXISTS clone_orders; DROP TABLE IF EXISTS clone_users;`)
		return
	}
	execSQL(t, conn, `SET FOREIGN_KEY_CHECKS=0; DROP TABLE IF EXISTS clone_orders; DROP TABLE IF EXISTS clone_users; SET FOREIGN_KEY_CHECKS=1;`)
}

func assertCloneSchemaCanSeed(t *testing.T, driver string, conn *sql.DB, tables []db.Table) {
	t.Helper()
	s := &schema.Schema{Tables: make(map[string]schema.Table, len(tables))}
	for _, tbl := range tables {
		st := schema.Table{Columns: make(map[string]schema.Column, len(tbl.Columns))}
		for _, col := range tbl.Columns {
			sc := schema.Column{
				Type:      col.Type,
				DDLType:   col.DDLType,
				PK:        col.IsPK,
				Nullable:  col.IsNullable,
				Generated: col.Generated != "",
				Faker:     faker.MapColumnToFaker(driver, col),
			}
			if col.Name == "email" {
				sc.Faker = "email"
			}
			if col.FK != nil {
				sc.FK = fmt.Sprintf("%s.%s", col.FK.TableName, col.FK.ColumnName)
			}
			st.Columns[col.Name] = sc
		}
		s.Tables[tbl.Name] = st
	}
	g := graph.Build(s)
	order, err := g.TopologicalSort()
	if err != nil {
		t.Fatalf("topological sort cloned schema: %v", err)
	}
	data, err := faker.Generate(s, order, 5, 0, conn, driver)
	if err != nil {
		t.Fatalf("generate cloned schema data: %v", err)
	}
	for _, tableName := range order {
		for _, row := range data[tableName] {
			query, values := db.BuildInsert(tableName, row, driver)
			if _, err := conn.ExecContext(context.Background(), query, values...); err != nil {
				t.Fatalf("insert cloned table %s: %v", tableName, err)
			}
		}
	}
	if got := countRows(t, conn, "clone_users"); got == 0 {
		t.Fatal("clone_users was not seeded")
	}
	if got := countRows(t, conn, "clone_orders"); got == 0 {
		t.Fatal("clone_orders was not seeded")
	}
}

func assertCloneMetadata(t *testing.T, tables []db.Table) {
	t.Helper()
	byName := make(map[string]db.Table, len(tables))
	for _, tbl := range tables {
		byName[tbl.Name] = tbl
	}
	users, ok := byName["clone_users"]
	if !ok {
		t.Fatal("clone_users missing from cloned metadata")
	}
	if users.Comment != "clone source users" {
		t.Fatalf("clone_users comment = %q", users.Comment)
	}
	var status, label db.Column
	for _, col := range users.Columns {
		switch col.Name {
		case "status":
			status = col
		case "full_label":
			label = col
		}
	}
	if status.Default == "" {
		t.Fatal("clone_users.status default was not preserved")
	}
	if status.Comment != "workflow state" {
		t.Fatalf("clone_users.status comment = %q", status.Comment)
	}
	if label.Generated == "" {
		t.Fatal("clone_users.full_label generated expression was not preserved")
	}
	if !hasIndex(users.Indexes, "uq_clone_users_status_email", true, []string{"status", "email"}) {
		t.Fatalf("multi-column unique index not preserved: %#v", users.Indexes)
	}
	orders := byName["clone_orders"]
	if !hasIndex(orders.Indexes, "idx_clone_orders_user_total", false, []string{"user_id", "total"}) {
		t.Fatalf("multi-column index not preserved: %#v", orders.Indexes)
	}
	var subtotal, total db.Column
	for _, col := range orders.Columns {
		switch col.Name {
		case "subtotal":
			subtotal = col
		case "total":
			total = col
		}
	}
	if subtotal.Default == "" {
		t.Fatal("clone_orders.subtotal default was not preserved")
	}
	if total.Generated == "" {
		t.Fatal("clone_orders.total generated expression was not preserved")
	}
}

func hasIndex(indexes []db.Index, name string, unique bool, columns []string) bool {
	for _, idx := range indexes {
		if idx.Name != name || idx.Unique != unique || len(idx.Columns) != len(columns) {
			continue
		}
		match := true
		for i := range columns {
			if idx.Columns[i] != columns[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
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
				Type:      col.Type,
				DDLType:   col.DDLType,
				PK:        col.IsPK,
				Nullable:  col.IsNullable,
				Generated: col.Generated != "",
				Faker:     faker.MapColumnToFaker(driver, col),
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
	data, err := faker.Generate(s, sortedTables, seedRows, 0, conn, driver)
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
			"brands", "tags", "users", "coupons", "companies", "suppliers", "hard_self_employees",
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

	t.Run("value constraints: short varchar values fit", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM brands WHERE country IS NOT NULL AND char_length(country) > 2`).Scan(&bad)
		if err != nil {
			t.Fatalf("length constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d rows with country length > 2", bad)
		}
	})

	t.Run("value constraints: boolean values fit", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM brands WHERE enabled NOT IN (TRUE, FALSE)`).Scan(&bad)
		if err != nil {
			t.Fatalf("boolean constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d rows with invalid boolean values", bad)
		}
	})

	t.Run("value constraints: date values fit", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM brands WHERE total IS NOT NULL AND total::text !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'`).Scan(&bad)
		if err != nil {
			t.Fatalf("date constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d rows with invalid date values", bad)
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
			"addresses":           1, // user_id
			"products":            2, // category_id, brand_id
			"product_tags":        2, // product_id, tag_id
			"orders":              3, // user_id, address_id, coupon_id
			"order_items":         2, // order_id, product_id
			"shipments":           1, // order_id
			"payments":            1, // order_id
			"reviews":             2, // user_id, product_id
			"wishlists":           1, // user_id
			"wishlist_items":      2, // wishlist_id, product_id
			"categories":          1, // parent_id (self-ref)
			"hard_self_employees": 1, // manager_id (hard self-ref)
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

	t.Run("self-ref: hard_self_employees has valid non-null managers", func(t *testing.T) {
		assertHardSelfRefSeeded(t, conn)
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
				sc := schema.Column{Type: col.Type, DDLType: col.DDLType, PK: col.IsPK, Nullable: col.IsNullable, Generated: col.Generated != ""}
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
			"brands", "tags", "users", "coupons", "companies", "suppliers", "hard_self_employees",
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
			"brands", "tags", "users", "coupons", "companies", "suppliers", "hard_self_employees",
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

func TestPostgresSchemaCloneDDL(t *testing.T) {
	dsn := envOrDefault("POSTGRES_DSN", postgresDSN)
	conn := openDB(t, postgresDriver, dsn)
	defer conn.Close()
	dropCloneSmokeSchema(t, postgresDriver, conn)
	cloneSmokeSchema(t, postgresDriver, conn)

	sourceTables, err := db.Introspect(postgresDriver, dsn)
	if err != nil {
		t.Fatalf("introspect source: %v", err)
	}
	sourceTables = filterTables(sourceTables, "clone_users", "clone_orders")
	if len(sourceTables) != 2 {
		t.Fatalf("source tables = %d, want 2", len(sourceTables))
	}
	stmts, err := db.BuildSchemaDDL(sourceTables, postgresDriver, true)
	if err != nil {
		t.Fatalf("BuildSchemaDDL: %v", err)
	}
	if err := db.ExecSchemaDDL(context.Background(), conn, postgresDriver, stmts); err != nil {
		t.Fatalf("ExecSchemaDDL: %v", err)
	}
	cloned, err := db.Introspect(postgresDriver, dsn)
	if err != nil {
		t.Fatalf("introspect cloned: %v", err)
	}
	cloned = filterTables(cloned, "clone_users", "clone_orders")
	if len(cloned) != 2 {
		t.Fatalf("cloned tables = %d, want 2", len(cloned))
	}
	assertCloneMetadata(t, cloned)
	assertCloneSchemaCanSeed(t, postgresDriver, conn, cloned)
	dropCloneSmokeSchema(t, postgresDriver, conn)
}

// ── MySQL ──────────────────────────────────────────────────────────────────────

func TestMySQLIntegration(t *testing.T) {
	dsn := envOrDefault("MYSQL_DSN", mysqlDSN)
	conn := openDB(t, mysqlDriver, dsn)
	defer conn.Close()

	t.Run("setup schema", func(t *testing.T) {
		execMySQLSchema(t, conn, "schema_mysql.sql")
	})

	var data map[string][]map[string]interface{}

	t.Run("introspect and seed", func(t *testing.T) {
		data = buildAndSeed(t, "mysql", mysqlDriver, dsn, conn)
	})

	t.Run("row counts", func(t *testing.T) {
		allTables := []string{
			// L0
			"brands", "tags", "users", "coupons", "companies", "suppliers", "hard_self_employees",
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

	t.Run("value constraints: short varchar values fit", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM brands WHERE country IS NOT NULL AND CHAR_LENGTH(country) > 2`).Scan(&bad)
		if err != nil {
			t.Fatalf("length constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d rows with country length > 2", bad)
		}
	})

	t.Run("value constraints: bit boolean values fit", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM brands WHERE enabled + 0 NOT IN (0, 1)`).Scan(&bad)
		if err != nil {
			t.Fatalf("bit boolean constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d rows with invalid bit boolean values", bad)
		}
	})

	t.Run("value constraints: date values fit", func(t *testing.T) {
		var bad int
		err := conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM brands WHERE total IS NOT NULL AND CAST(total AS CHAR) NOT REGEXP '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'`).Scan(&bad)
		if err != nil {
			t.Fatalf("date constraint check: %v", err)
		}
		if bad > 0 {
			t.Errorf("found %d rows with invalid date values", bad)
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
			"addresses":           1, // user_id
			"products":            2, // category_id, brand_id
			"product_tags":        2, // product_id, tag_id
			"orders":              3, // user_id, address_id, coupon_id
			"order_items":         2, // order_id, product_id
			"shipments":           1, // order_id
			"payments":            1, // order_id
			"reviews":             2, // user_id, product_id
			"wishlists":           1, // user_id
			"wishlist_items":      2, // wishlist_id, product_id
			"categories":          1, // parent_id (self-ref)
			"hard_self_employees": 1, // manager_id (hard self-ref)
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

	t.Run("self-ref: hard_self_employees has valid non-null managers", func(t *testing.T) {
		assertHardSelfRefSeeded(t, conn)
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

	hasCheck := mysqlHasCheckSupport(t, conn)
	skipIfNoCheck := func(t *testing.T) {
		t.Helper()
		if !hasCheck {
			t.Skip("MySQL server does not expose information_schema.CHECK_CONSTRAINTS (requires 8.0.16+)")
		}
	}

	t.Run("constraint: users.role CHECK values detected", func(t *testing.T) {
		skipIfNoCheck(t)
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
		skipIfNoCheck(t)
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
		skipIfNoCheck(t)
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
		skipIfNoCheck(t)
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
		skipIfNoCheck(t)
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
		skipIfNoCheck(t)
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
				sc := schema.Column{Type: col.Type, DDLType: col.DDLType, PK: col.IsPK, Nullable: col.IsNullable, Generated: col.Generated != ""}
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
			"brands", "tags", "users", "coupons", "companies", "suppliers", "hard_self_employees",
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
			"brands", "tags", "users", "coupons", "companies", "suppliers", "hard_self_employees",
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
		execMySQLSchema(t, conn, "schema_mysql.sql") // re-run drops everything
	})
}

func TestMySQLSchemaCloneDDL(t *testing.T) {
	dsn := envOrDefault("MYSQL_DSN", mysqlDSN)
	conn := openDB(t, mysqlDriver, dsn)
	defer conn.Close()
	dropCloneSmokeSchema(t, mysqlDriver, conn)
	cloneSmokeSchema(t, mysqlDriver, conn)

	sourceTables, err := db.Introspect(mysqlDriver, dsn)
	if err != nil {
		t.Fatalf("introspect source: %v", err)
	}
	sourceTables = filterTables(sourceTables, "clone_users", "clone_orders")
	if len(sourceTables) != 2 {
		t.Fatalf("source tables = %d, want 2", len(sourceTables))
	}
	stmts, err := db.BuildSchemaDDL(sourceTables, mysqlDriver, true)
	if err != nil {
		t.Fatalf("BuildSchemaDDL: %v", err)
	}
	if err := db.ExecSchemaDDL(context.Background(), conn, mysqlDriver, stmts); err != nil {
		t.Fatalf("ExecSchemaDDL: %v", err)
	}
	cloned, err := db.Introspect(mysqlDriver, dsn)
	if err != nil {
		t.Fatalf("introspect cloned: %v", err)
	}
	cloned = filterTables(cloned, "clone_users", "clone_orders")
	if len(cloned) != 2 {
		t.Fatalf("cloned tables = %d, want 2", len(cloned))
	}
	assertCloneMetadata(t, cloned)
	assertCloneSchemaCanSeed(t, mysqlDriver, conn, cloned)
	dropCloneSmokeSchema(t, mysqlDriver, conn)
}

// ── Gap Analysis ──────────────────────────────────────────────────────────────
//
// These tests exercise the gaps / GenerateFiltered feature end-to-end:
//   1. Partial seed: populate only L0 (root) tables.
//   2. Gap detection: GetTableRowCounts shows L1+ tables are empty.
//   3. Gap fill: GenerateFiltered seeds only empty tables, resolving FKs from
//      already-populated parents.
//   4. Verify: all tables populated, FK integrity intact.
//   5. Idempotent fill: running gap fill a second time when all tables already
//      have rows adds nothing (no gaps found → no generation).

// gapL0Tables are the root (no FK parents) tables in the 29-table test schema.
var gapL0Tables = []string{"brands", "tags", "users", "coupons", "companies", "suppliers", "hard_self_employees"}

// gapAllTables lists every table in the test schema (used for count assertions).
var gapAllTables = []string{
	"brands", "tags", "users", "coupons", "companies", "suppliers", "hard_self_employees",
	"categories", "addresses", "departments", "warehouses", "wishlists",
	"products", "employees",
	"product_tags", "orders", "projects", "inventory", "purchase_orders",
	"support_tickets", "reviews", "wishlist_items", "audit_logs",
	"order_items", "shipments", "payments", "project_assignments",
	"purchase_order_items", "return_requests",
}

// seedL0 populates only the root tables (L0) by introspecting and generating
// a schema filtered to just those tables, then inserting the rows.
func seedL0(t *testing.T, driver, dsn string, conn *sql.DB) {
	t.Helper()

	tables, err := db.Introspect(driver, dsn)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	s := &schema.Schema{Tables: make(map[string]schema.Table, len(tables))}
	for _, tbl := range tables {
		st := schema.Table{Columns: make(map[string]schema.Column, len(tbl.Columns))}
		for _, col := range tbl.Columns {
			sc := schema.Column{
				Type:      col.Type,
				DDLType:   col.DDLType,
				PK:        col.IsPK,
				Nullable:  col.IsNullable,
				Generated: col.Generated != "",
				Faker:     faker.MapColumnToFaker(driver, col),
			}
			if col.FK != nil {
				sc.FK = fmt.Sprintf("%s.%s", col.FK.TableName, col.FK.ColumnName)
			}
			st.Columns[col.Name] = sc
		}
		s.Tables[tbl.Name] = st
	}

	data, err := faker.Generate(s, gapL0Tables, seedRows, 0, conn, driver)
	if err != nil {
		t.Fatalf("generate L0: %v", err)
	}
	for _, tableName := range gapL0Tables {
		for _, row := range data[tableName] {
			cols := make([]string, 0, len(row))
			phs := make([]string, 0, len(row))
			vals := make([]interface{}, 0, len(row))
			i := 1
			for colName, val := range row {
				cols = append(cols, colName)
				if driver == postgresDriver {
					phs = append(phs, fmt.Sprintf("$%d", i))
				} else {
					phs = append(phs, "?")
				}
				vals = append(vals, val)
				i++
			}
			q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
				tableName, strings.Join(cols, ", "), strings.Join(phs, ", "))
			if _, err := conn.ExecContext(context.Background(), q, vals...); err != nil {
				t.Fatalf("L0 insert into %s: %v", tableName, err)
			}
		}
	}
	t.Logf("seedL0: inserted %d rows into %d root tables", seedRows*len(gapL0Tables), len(gapL0Tables))
}

// fillGaps runs the core GenerateFiltered → insert pipeline on only the tables
// that have 0 rows, using allSorted for FK preloading and returning the number
// of gap tables that were filled.
func fillGaps(t *testing.T, driver string, conn *sql.DB, s *schema.Schema, allSorted []string) int {
	t.Helper()

	counts, err := db.GetTableRowCounts(context.Background(), conn, driver, allSorted)
	if err != nil {
		t.Fatalf("GetTableRowCounts: %v", err)
	}

	var gapTables []string
	for _, tbl := range allSorted {
		if counts[tbl] == 0 {
			gapTables = append(gapTables, tbl)
		}
	}
	if len(gapTables) == 0 {
		return 0
	}

	data, err := faker.GenerateFiltered(s, allSorted, gapTables, seedRows, 0, conn, driver)
	if err != nil {
		t.Fatalf("GenerateFiltered: %v", err)
	}

	for _, tableName := range gapTables {
		for _, row := range data[tableName] {
			cols := make([]string, 0, len(row))
			phs := make([]string, 0, len(row))
			vals := make([]interface{}, 0, len(row))
			i := 1
			for colName, val := range row {
				cols = append(cols, colName)
				if driver == postgresDriver {
					phs = append(phs, fmt.Sprintf("$%d", i))
				} else {
					phs = append(phs, "?")
				}
				vals = append(vals, val)
				i++
			}
			q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
				tableName, strings.Join(cols, ", "), strings.Join(phs, ", "))
			if _, err := conn.ExecContext(context.Background(), q, vals...); err != nil {
				t.Fatalf("gap fill insert into %s: %v", tableName, err)
			}
		}
	}
	return len(gapTables)
}

func TestPostgresGaps(t *testing.T) {
	dsn := envOrDefault("POSTGRES_DSN", postgresDSN)
	conn := openDB(t, postgresDriver, dsn)
	defer conn.Close()

	t.Run("setup schema", func(t *testing.T) {
		execScript(t, conn, "schema_postgres.sql")
	})

	// Build full schema + sorted order once, reused across sub-tests.
	var (
		fullSchema *schema.Schema
		allSorted  []string
	)
	t.Run("build schema and sort order", func(t *testing.T) {
		tables, err := db.Introspect(postgresDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		fullSchema = &schema.Schema{Tables: make(map[string]schema.Table, len(tables))}
		for _, tbl := range tables {
			st := schema.Table{Columns: make(map[string]schema.Column, len(tbl.Columns))}
			for _, col := range tbl.Columns {
				sc := schema.Column{
					Type:      col.Type,
					DDLType:   col.DDLType,
					PK:        col.IsPK,
					Nullable:  col.IsNullable,
					Generated: col.Generated != "",
					Faker:     faker.MapColumnToFaker(postgresDriver, col),
				}
				if col.FK != nil {
					sc.FK = fmt.Sprintf("%s.%s", col.FK.TableName, col.FK.ColumnName)
				}
				st.Columns[col.Name] = sc
			}
			fullSchema.Tables[tbl.Name] = st
		}
		g := graph.Build(fullSchema)
		allSorted, err = g.TopologicalSort()
		if err != nil {
			t.Fatalf("topological sort: %v", err)
		}
	})

	t.Run("partial seed: populate L0 tables only", func(t *testing.T) {
		if fullSchema == nil {
			t.Skip("schema not built — previous subtest failed")
		}
		seedL0(t, postgresDriver, dsn, conn)
		for _, tbl := range gapL0Tables {
			if n := countRows(t, conn, tbl); n == 0 {
				t.Errorf("L0 table %s should have rows after partial seed", tbl)
			}
		}
	})

	t.Run("gap detection: L1+ tables are empty", func(t *testing.T) {
		if fullSchema == nil {
			t.Skip("schema not built — previous subtest failed")
		}
		counts, err := db.GetTableRowCounts(context.Background(), conn, postgresDriver, gapAllTables)
		if err != nil {
			t.Fatalf("GetTableRowCounts: %v", err)
		}
		l0Set := make(map[string]bool)
		for _, tbl := range gapL0Tables {
			l0Set[tbl] = true
		}
		for _, tbl := range gapAllTables {
			if l0Set[tbl] {
				if counts[tbl] == 0 {
					t.Errorf("L0 table %s should be populated", tbl)
				}
			} else {
				if counts[tbl] != 0 {
					t.Errorf("L1+ table %s should be empty before gap fill, got %d rows", tbl, counts[tbl])
				}
			}
		}
		// Count total gaps.
		gaps := 0
		for _, c := range counts {
			if c == 0 {
				gaps++
			}
		}
		t.Logf("gap detection: %d/%d tables are empty (gaps)", gaps, len(gapAllTables))
	})

	var gapsFilled int
	t.Run("gap fill: seed empty tables only", func(t *testing.T) {
		if fullSchema == nil || len(allSorted) == 0 {
			t.Skip("schema not built — previous subtest failed")
		}
		gapsFilled = fillGaps(t, postgresDriver, conn, fullSchema, allSorted)
		if gapsFilled == 0 {
			t.Error("expected at least one gap table to be filled")
		}
		t.Logf("gap fill: seeded %d previously-empty tables", gapsFilled)
	})

	t.Run("gap fill: all tables populated after fill", func(t *testing.T) {
		for _, tbl := range gapAllTables {
			if n := countRows(t, conn, tbl); n == 0 {
				t.Errorf("table %s still has 0 rows after gap fill", tbl)
			}
		}
	})

	t.Run("gap fill: L0 tables unchanged (not re-seeded)", func(t *testing.T) {
		// L0 tables should still have exactly seedRows rows (not doubled).
		for _, tbl := range gapL0Tables {
			n := countRows(t, conn, tbl)
			if n == 0 {
				t.Errorf("L0 table %s lost its rows", tbl)
			}
			// enum top-up can add rows, so we just ensure the count didn't halve
			if n < seedRows {
				t.Errorf("L0 table %s has fewer rows than expected (%d < %d)", tbl, n, seedRows)
			}
		}
	})

	t.Run("gap fill: FK integrity for filled tables", func(t *testing.T) {
		checks := []struct {
			child, parent, col string
		}{
			{"addresses", "users", "user_id"},
			{"products", "categories", "category_id"},
			{"products", "brands", "brand_id"},
			{"orders", "users", "user_id"},
			{"order_items", "orders", "order_id"},
			{"order_items", "products", "product_id"},
			{"shipments", "orders", "order_id"},
			{"payments", "orders", "order_id"},
			{"reviews", "users", "user_id"},
			{"reviews", "products", "product_id"},
		}
		for _, c := range checks {
			c := c
			t.Run(fmt.Sprintf("%s->%s", c.child, c.parent), func(t *testing.T) {
				q := fmt.Sprintf(`SELECT COUNT(*) FROM %s child
					LEFT JOIN %s parent ON child.%s = parent.id
					WHERE parent.id IS NULL AND child.%s IS NOT NULL`,
					c.child, c.parent, c.col, c.col)
				var orphans int
				if err := conn.QueryRowContext(context.Background(), q).Scan(&orphans); err != nil {
					t.Fatalf("FK check: %v", err)
				}
				if orphans > 0 {
					t.Errorf("%d orphaned rows in %s.%s after gap fill", orphans, c.child, c.col)
				}
			})
		}
	})

	t.Run("gap fill: idempotent — no gaps after full fill", func(t *testing.T) {
		if fullSchema == nil || len(allSorted) == 0 {
			t.Skip("schema not built — previous subtest failed")
		}
		counts, err := db.GetTableRowCounts(context.Background(), conn, postgresDriver, allSorted)
		if err != nil {
			t.Fatalf("GetTableRowCounts: %v", err)
		}
		for _, tbl := range allSorted {
			if counts[tbl] == 0 {
				t.Errorf("table %s is still empty — gap fill was not idempotent", tbl)
			}
		}
		// Simulate a second gap fill call: should find 0 gaps.
		var secondGaps []string
		for _, tbl := range allSorted {
			if counts[tbl] == 0 {
				secondGaps = append(secondGaps, tbl)
			}
		}
		if len(secondGaps) > 0 {
			t.Errorf("second gap scan found %d gaps: %v", len(secondGaps), secondGaps)
		}
	})

	t.Run("teardown", func(t *testing.T) {
		execScript(t, conn, "schema_postgres.sql")
	})
}

func TestMySQLGaps(t *testing.T) {
	dsn := envOrDefault("MYSQL_DSN", mysqlDSN)
	conn := openDB(t, mysqlDriver, dsn)
	defer conn.Close()

	t.Run("setup schema", func(t *testing.T) {
		execMySQLSchema(t, conn, "schema_mysql.sql")
	})

	var (
		fullSchema *schema.Schema
		allSorted  []string
	)
	t.Run("build schema and sort order", func(t *testing.T) {
		tables, err := db.Introspect(mysqlDriver, dsn)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		fullSchema = &schema.Schema{Tables: make(map[string]schema.Table, len(tables))}
		for _, tbl := range tables {
			st := schema.Table{Columns: make(map[string]schema.Column, len(tbl.Columns))}
			for _, col := range tbl.Columns {
				sc := schema.Column{
					Type:      col.Type,
					DDLType:   col.DDLType,
					PK:        col.IsPK,
					Nullable:  col.IsNullable,
					Generated: col.Generated != "",
					Faker:     faker.MapColumnToFaker(mysqlDriver, col),
				}
				if col.FK != nil {
					sc.FK = fmt.Sprintf("%s.%s", col.FK.TableName, col.FK.ColumnName)
				}
				st.Columns[col.Name] = sc
			}
			fullSchema.Tables[tbl.Name] = st
		}
		g := graph.Build(fullSchema)
		allSorted, err = g.TopologicalSort()
		if err != nil {
			t.Fatalf("topological sort: %v", err)
		}
	})

	t.Run("partial seed: populate L0 tables only", func(t *testing.T) {
		if fullSchema == nil {
			t.Skip("schema not built — previous subtest failed")
		}
		seedL0(t, mysqlDriver, dsn, conn)
		for _, tbl := range gapL0Tables {
			if n := countRows(t, conn, tbl); n == 0 {
				t.Errorf("L0 table %s should have rows after partial seed", tbl)
			}
		}
	})

	t.Run("gap detection: L1+ tables are empty", func(t *testing.T) {
		if fullSchema == nil {
			t.Skip("schema not built — previous subtest failed")
		}
		counts, err := db.GetTableRowCounts(context.Background(), conn, mysqlDriver, gapAllTables)
		if err != nil {
			t.Fatalf("GetTableRowCounts: %v", err)
		}
		l0Set := make(map[string]bool)
		for _, tbl := range gapL0Tables {
			l0Set[tbl] = true
		}
		gaps := 0
		for _, tbl := range gapAllTables {
			if l0Set[tbl] {
				if counts[tbl] == 0 {
					t.Errorf("L0 table %s should be populated", tbl)
				}
			} else {
				if counts[tbl] != 0 {
					t.Errorf("L1+ table %s should be empty before gap fill, got %d rows", tbl, counts[tbl])
				}
				gaps++
			}
		}
		t.Logf("gap detection: %d/%d tables are empty (gaps)", gaps, len(gapAllTables))
	})

	t.Run("gap fill: seed empty tables only", func(t *testing.T) {
		if fullSchema == nil || len(allSorted) == 0 {
			t.Skip("schema not built — previous subtest failed")
		}
		filled := fillGaps(t, mysqlDriver, conn, fullSchema, allSorted)
		if filled == 0 {
			t.Error("expected at least one gap table to be filled")
		}
		t.Logf("gap fill: seeded %d previously-empty tables", filled)
	})

	t.Run("gap fill: all tables populated after fill", func(t *testing.T) {
		for _, tbl := range gapAllTables {
			if n := countRows(t, conn, tbl); n == 0 {
				t.Errorf("table %s still has 0 rows after gap fill", tbl)
			}
		}
	})

	t.Run("gap fill: FK integrity for filled tables", func(t *testing.T) {
		checks := []struct {
			child, parent, col string
		}{
			{"addresses", "users", "user_id"},
			{"products", "categories", "category_id"},
			{"products", "brands", "brand_id"},
			{"orders", "users", "user_id"},
			{"order_items", "orders", "order_id"},
			{"order_items", "products", "product_id"},
			{"shipments", "orders", "order_id"},
			{"payments", "orders", "order_id"},
			{"reviews", "users", "user_id"},
			{"reviews", "products", "product_id"},
		}
		for _, c := range checks {
			c := c
			t.Run(fmt.Sprintf("%s->%s", c.child, c.parent), func(t *testing.T) {
				q := fmt.Sprintf(`SELECT COUNT(*) FROM %s child
					LEFT JOIN %s parent ON child.%s = parent.id
					WHERE parent.id IS NULL AND child.%s IS NOT NULL`,
					c.child, c.parent, c.col, c.col)
				var orphans int
				if err := conn.QueryRowContext(context.Background(), q).Scan(&orphans); err != nil {
					t.Fatalf("FK check: %v", err)
				}
				if orphans > 0 {
					t.Errorf("%d orphaned rows in %s.%s after gap fill", orphans, c.child, c.col)
				}
			})
		}
	})

	t.Run("gap fill: idempotent — no gaps after full fill", func(t *testing.T) {
		if fullSchema == nil || len(allSorted) == 0 {
			t.Skip("schema not built — previous subtest failed")
		}
		counts, err := db.GetTableRowCounts(context.Background(), conn, mysqlDriver, allSorted)
		if err != nil {
			t.Fatalf("GetTableRowCounts: %v", err)
		}
		for _, tbl := range allSorted {
			if counts[tbl] == 0 {
				t.Errorf("table %s is still empty — gap fill was not idempotent", tbl)
			}
		}
	})

	t.Run("teardown", func(t *testing.T) {
		execMySQLSchema(t, conn, "schema_mysql.sql")
	})
}
