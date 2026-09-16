//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// TestCloneSchema_Objects drives `clone-schema` with object flags against both
// engines: views (including a view built on another view whose name sorts
// first), a function, a procedure and a trigger must all work on the clone.
func TestCloneSchema_Objects(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			if e.driver == mysqlDriver {
				allowMySQLRoutineCreation(t, e)
			}
			srcDSN, src := e.scratchDB(t, "ss_obj_src")
			createCloneObjectsSource(t, e, src)

			tgtDSN, tgt := e.scratchDB(t, "ss_obj_tgt")
			if e.driver == mysqlDriver {
				// Routine bodies contain ';' — prove they run as single
				// statements without the driver's multiStatements mode.
				tgtDSN = strings.Replace(tgtDSN, "&multiStatements=true", "", 1)
			}
			_, stderr := runBinBoth(t, "clone-schema", "--source-db", e.name, "--source-dsn", srcDSN,
				"--target-db", e.name, "--target-dsn", tgtDSN, "--views", "--routines", "--triggers")

			wantObjects := []string{"view:order_totals", "view:a_big_spenders", "function:add_tax", "procedure:promote_customer", "trigger:trg_orders_status"}
			if e.driver == postgresDriver {
				wantObjects = append(wantObjects, "function:orders_default_status", "materialized view:mv_totals")
			}
			got := cloneObjectNames(t, e, tgt)
			for _, want := range wantObjects {
				if !got[want] {
					t.Errorf("target is missing %s (has %v)", want, got)
				}
			}
			assertClonedObjectsWork(t, e, tgt)

			if e.driver == mysqlDriver {
				// hidden_proc is owned by root; the app user cannot read its body.
				if got["procedure:hidden_proc"] {
					t.Error("hidden_proc should not be cloned without a visible body")
				}
				if !strings.Contains(stderr, "hidden_proc") || !strings.Contains(stderr, "Object not cloned") {
					t.Errorf("expected a skipped warning for hidden_proc, stderr:\n%s", stderr)
				}
			}

			t.Run("drop-existing re-clone is repeatable", func(t *testing.T) {
				for i := 0; i < 2; i++ {
					runBin(t, "clone-schema", "--source-db", e.name, "--source-dsn", srcDSN,
						"--target-db", e.name, "--target-dsn", tgtDSN, "--drop-existing", "--objects", "all")
				}
				got := cloneObjectNames(t, e, tgt)
				for _, want := range wantObjects {
					if !got[want] {
						t.Errorf("after re-clone target is missing %s", want)
					}
				}
				assertClonedObjectsWork(t, e, tgt)
			})

			t.Run("without flags no objects are cloned", func(t *testing.T) {
				plainDSN, plain := e.scratchDB(t, "ss_obj_plain")
				runBin(t, "clone-schema", "--source-db", e.name, "--source-dsn", srcDSN,
					"--target-db", e.name, "--target-dsn", plainDSN)
				if got := cloneObjectNames(t, e, plain); len(got) != 0 {
					t.Fatalf("plain clone created objects: %v", got)
				}
				if n := len(tableCounts(t, e, plain)); n != 2 {
					t.Fatalf("plain clone tables = %d, want 2", n)
				}
			})

			t.Run("dry-run prints object DDL only when requested", func(t *testing.T) {
				out := runBin(t, "clone-schema", "--source-db", e.name, "--source-dsn", srcDSN,
					"--target-db", e.name, "--target-dsn", tgtDSN, "--dry-run", "--objects", "views,routines,triggers")
				for _, want := range []string{"CREATE TABLE", "order_totals", "a_big_spenders", "add_tax", "promote_customer", "trg_orders_status"} {
					if !strings.Contains(out, want) {
						t.Errorf("dry-run output missing %q:\n%s", want, out)
					}
				}
				if strings.Contains(out, "DEFINER=") {
					t.Errorf("dry-run output still carries a DEFINER clause:\n%s", out)
				}
				if strings.Contains(out, "ss_obj_src") {
					t.Errorf("dry-run output references the source schema:\n%s", out)
				}
				plainOut := runBin(t, "clone-schema", "--source-db", e.name, "--source-dsn", srcDSN,
					"--target-db", e.name, "--target-dsn", tgtDSN, "--dry-run")
				if strings.Contains(plainOut, "add_tax") || strings.Contains(plainOut, "VIEW") {
					t.Errorf("dry-run without object flags printed object DDL:\n%s", plainOut)
				}
			})

			t.Run("unknown object kind is rejected", func(t *testing.T) {
				_, stderr, err := runBinResult(t, "clone-schema", "--source-db", e.name, "--source-dsn", srcDSN,
					"--target-db", e.name, "--target-dsn", tgtDSN, "--dry-run", "--objects", "views,sequences")
				if err == nil {
					t.Fatal("expected failure for unknown object kind")
				}
				if !strings.Contains(stderr, "sequences") {
					t.Fatalf("error should name the bad kind, stderr:\n%s", stderr)
				}
			})
		})
	}
}

// runBinBoth runs the binary, fails on a non-zero exit, and returns stdout and
// stderr (warnings are logged to stderr).
func runBinBoth(t *testing.T, args ...string) (string, string) {
	t.Helper()
	stdout, stderr, err := runBinResult(t, args...)
	if err != nil {
		t.Fatalf("seedstorm %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout, stderr
}

// allowMySQLRoutineCreation enables log_bin_trust_function_creators. MySQL 8
// has binary logging on by default, and then only SUPER may create functions or
// triggers; the app user the tests (and users) connect as is not SUPER. The
// setting is global and left on: flipping it back would race other tests that
// share the dev server.
func allowMySQLRoutineCreation(t *testing.T, e engine) {
	t.Helper()
	admin := e.adminDB(t)
	defer admin.Close()
	if _, err := admin.ExecContext(context.Background(), "SET GLOBAL log_bin_trust_function_creators = 1"); err != nil {
		t.Fatalf("enable log_bin_trust_function_creators: %v", err)
	}
}

func createCloneObjectsSource(t *testing.T, e engine, conn *sql.DB) {
	t.Helper()
	var stmts []string
	if e.driver == postgresDriver {
		stmts = []string{
			`CREATE TABLE customers (id INT PRIMARY KEY, name TEXT NOT NULL, tier TEXT)`,
			`CREATE TABLE orders (id INT PRIMARY KEY, customer_id INT NOT NULL REFERENCES customers(id), amount INT NOT NULL, status TEXT)`,
			`CREATE FUNCTION add_tax(v integer) RETURNS integer LANGUAGE sql IMMUTABLE AS $$ SELECT v + 10 $$`,
			`CREATE FUNCTION orders_default_status() RETURNS trigger LANGUAGE plpgsql AS $$
			 BEGIN
			   IF NEW.status IS NULL THEN NEW.status := 'new'; END IF;
			   RETURN NEW;
			 END $$`,
			`CREATE PROCEDURE promote_customer(cid integer) LANGUAGE plpgsql AS $$
			 BEGIN UPDATE customers SET tier = 'gold' WHERE id = cid; END $$`,
			`CREATE VIEW order_totals AS SELECT customer_id, SUM(amount)::int AS total FROM orders GROUP BY customer_id`,
			`CREATE VIEW a_big_spenders AS SELECT c.name, t.total FROM customers c JOIN order_totals t ON t.customer_id = c.id WHERE t.total > 100`,
			`CREATE MATERIALIZED VIEW mv_totals AS SELECT * FROM order_totals`,
			`CREATE TRIGGER trg_orders_status BEFORE INSERT ON orders FOR EACH ROW EXECUTE FUNCTION orders_default_status()`,
		}
	} else {
		stmts = []string{
			"CREATE TABLE customers (id INT PRIMARY KEY, name VARCHAR(50) NOT NULL, tier VARCHAR(20))",
			"CREATE TABLE orders (id INT PRIMARY KEY, customer_id INT NOT NULL, amount INT NOT NULL, status VARCHAR(20), FOREIGN KEY (customer_id) REFERENCES customers(id))",
			"CREATE FUNCTION add_tax(v INT) RETURNS INT DETERMINISTIC RETURN v + 10",
			"CREATE PROCEDURE promote_customer(IN cid INT)\nBEGIN\n  UPDATE customers SET tier = 'gold' WHERE id = cid;\nEND",
			"CREATE VIEW order_totals AS SELECT customer_id, CAST(SUM(amount) AS SIGNED) AS total FROM orders GROUP BY customer_id",
			"CREATE VIEW a_big_spenders AS SELECT c.name, t.total FROM customers c JOIN order_totals t ON t.customer_id = c.id WHERE t.total > 100",
			"CREATE TRIGGER trg_orders_status BEFORE INSERT ON orders FOR EACH ROW\nBEGIN\n  IF NEW.status IS NULL THEN SET NEW.status = 'new'; END IF;\nEND",
		}
	}
	// Source rows differ from what the assertions insert on the target, so a
	// view still pointing at the source database would return the wrong data.
	stmts = append(stmts,
		"INSERT INTO customers (id, name) VALUES (1, 'Zed')",
		"INSERT INTO orders (id, customer_id, amount) VALUES (1, 1, 900)",
	)
	for _, stmt := range stmts {
		if _, err := conn.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("source setup %q: %v", stmt, err)
		}
	}
	if e.driver == mysqlDriver {
		admin := e.adminDB(t)
		defer admin.Close()
		if _, err := admin.ExecContext(context.Background(), "CREATE PROCEDURE `ss_obj_src`.`hidden_proc`() SELECT 1"); err != nil {
			t.Fatalf("create root-owned procedure: %v", err)
		}
	}
}

// assertClonedObjectsWork exercises every cloned object on a fresh data set.
func assertClonedObjectsWork(t *testing.T, e engine, conn *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{"DELETE FROM orders", "DELETE FROM customers"} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO customers (id, name) VALUES (7, 'Ada')"); err != nil {
		t.Fatalf("insert customer: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO orders (id, customer_id, amount) VALUES (70, 7, 150)"); err != nil {
		t.Fatalf("insert order: %v", err)
	}

	var status sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT status FROM orders WHERE id = 70").Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status.String != "new" {
		t.Errorf("trigger did not fire on the clone: status = %v", status)
	}

	var name string
	var total int
	if err := conn.QueryRowContext(ctx, "SELECT name, total FROM a_big_spenders").Scan(&name, &total); err != nil {
		t.Fatalf("select view-on-view: %v", err)
	}
	if name != "Ada" || total != 150 {
		t.Errorf("a_big_spenders = (%q, %d), want (Ada, 150) from target data", name, total)
	}

	var taxed int
	if err := conn.QueryRowContext(ctx, "SELECT add_tax(5)").Scan(&taxed); err != nil {
		t.Fatalf("call function: %v", err)
	}
	if taxed != 15 {
		t.Errorf("add_tax(5) = %d, want 15", taxed)
	}

	if _, err := conn.ExecContext(ctx, "CALL promote_customer(7)"); err != nil {
		t.Fatalf("call procedure: %v", err)
	}
	var tier sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT tier FROM customers WHERE id = 7").Scan(&tier); err != nil {
		t.Fatalf("read tier: %v", err)
	}
	if tier.String != "gold" {
		t.Errorf("procedure did not run: tier = %v", tier)
	}

	if e.driver == postgresDriver {
		if _, err := conn.ExecContext(ctx, "REFRESH MATERIALIZED VIEW mv_totals"); err != nil {
			t.Fatalf("refresh materialized view: %v", err)
		}
		var mvTotal int
		if err := conn.QueryRowContext(ctx, "SELECT total FROM mv_totals WHERE customer_id = 7").Scan(&mvTotal); err != nil {
			t.Fatalf("select materialized view: %v", err)
		}
		if mvTotal != 150 {
			t.Errorf("mv_totals total = %d, want 150", mvTotal)
		}
	}
}

// cloneObjectNames lists views, routines and triggers as "kind:name".
func cloneObjectNames(t *testing.T, e engine, conn *sql.DB) map[string]bool {
	t.Helper()
	query := `
		SELECT 'view', viewname FROM pg_views WHERE schemaname = 'public'
		UNION ALL SELECT 'materialized view', matviewname FROM pg_matviews WHERE schemaname = 'public'
		UNION ALL SELECT CASE p.prokind WHEN 'p' THEN 'procedure' ELSE 'function' END, p.proname
		  FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'public'
		UNION ALL SELECT 'trigger', tg.tgname FROM pg_trigger tg
		  JOIN pg_class c ON c.oid = tg.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		  WHERE n.nspname = 'public' AND NOT tg.tgisinternal`
	if e.driver == mysqlDriver {
		query = `
		SELECT 'view', TABLE_NAME FROM information_schema.VIEWS WHERE TABLE_SCHEMA = DATABASE()
		UNION ALL SELECT LOWER(ROUTINE_TYPE), ROUTINE_NAME FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = DATABASE()
		UNION ALL SELECT 'trigger', TRIGGER_NAME FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = DATABASE()`
	}
	rows, err := conn.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatalf("list objects: %v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			t.Fatal(err)
		}
		out[kind+":"+name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
