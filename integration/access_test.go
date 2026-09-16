//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/db"
)

const accessDB = "ss_acc_scope"

var accessTables = []string{
	`CREATE TABLE ss_acc_widgets (id INTEGER PRIMARY KEY, name VARCHAR(40))`,
	`CREATE TABLE ss_acc_orders (id INTEGER PRIMARY KEY, widget_id INTEGER NOT NULL, FOREIGN KEY (widget_id) REFERENCES ss_acc_widgets(id))`,
	`CREATE TABLE ss_acc_audit (id INTEGER PRIMARY KEY, body VARCHAR(80))`,
}

var (
	fullTable = db.TableAccess{Select: true, Insert: true, Update: true, Delete: true, Truncate: true}
	readTable = db.TableAccess{Select: true}
)

func inspect(t *testing.T, conn *sql.DB, driver string) db.Access {
	t.Helper()
	acc, err := db.InspectAccess(context.Background(), conn, driver)
	if err != nil {
		t.Fatalf("InspectAccess: %v", err)
	}
	return acc
}

func assertTables(t *testing.T, acc db.Access, want map[string]db.TableAccess) {
	t.Helper()
	if !reflect.DeepEqual(acc.Tables, want) {
		t.Fatalf("tables = %+v\nwant   %+v", acc.Tables, want)
	}
}

func TestAccess_Postgres(t *testing.T) {
	e := postgresEngine()
	_, owner := e.scratchDB(t, accessDB)
	for _, stmt := range accessTables {
		execSQL(t, owner, stmt)
	}
	ctx := context.Background()

	t.Run("owner superuser has full access", func(t *testing.T) {
		acc := inspect(t, owner, e.driver)
		if acc.User != "seedstorm" || acc.Database != accessDB || !acc.Superuser || !acc.CreateTables {
			t.Fatalf("owner access = %+v", acc)
		}
		assertTables(t, acc, map[string]db.TableAccess{"ss_acc_widgets": fullTable, "ss_acc_orders": fullTable, "ss_acc_audit": fullTable})
	})

	// A limited login role plus a group role it inherits UPDATE from.
	const user, group = "ss_acc_limited", "ss_acc_group"
	dropRoles := func() {
		for _, r := range []string{user, group} {
			var exists bool
			_ = owner.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, r).Scan(&exists)
			if exists {
				_, _ = owner.ExecContext(ctx, `DROP OWNED BY `+r)
				_, _ = owner.ExecContext(ctx, `DROP ROLE `+r)
			}
		}
	}
	dropRoles()
	t.Cleanup(dropRoles)
	execSQL(t, owner, fmt.Sprintf(`
		CREATE ROLE %[2]s NOLOGIN;
		CREATE ROLE %[1]s LOGIN PASSWORD 'ss_acc_pw' INHERIT;
		GRANT CONNECT ON DATABASE %[3]s TO %[1]s;
		GRANT USAGE ON SCHEMA public TO %[1]s;
		GRANT SELECT ON ALL TABLES IN SCHEMA public TO %[1]s;
		GRANT INSERT ON ss_acc_orders TO %[1]s;
		GRANT UPDATE ON ss_acc_audit TO %[2]s;
		GRANT %[2]s TO %[1]s`, user, group, accessDB))

	limitedDSN := strings.Replace(e.dsnFor(accessDB), "seedstorm:seedstorm@", user+":ss_acc_pw@", 1)
	limited := openDB(t, e.driver, limitedDSN)
	defer limited.Close()

	t.Run("limited role reports exactly its grants", func(t *testing.T) {
		acc := inspect(t, limited, e.driver)
		if acc.User != user {
			t.Fatalf("user = %q, want %q", acc.User, user)
		}
		if acc.Superuser || acc.CreateTables {
			t.Fatalf("limited role must not be superuser or create tables: %+v", acc)
		}
		assertTables(t, acc, map[string]db.TableAccess{
			"ss_acc_widgets": readTable,
			"ss_acc_orders":  {Select: true, Insert: true},
			"ss_acc_audit":   {Select: true, Update: true}, // inherited from the group role
		})
		if len(acc.Notes) != 0 {
			t.Fatalf("unexpected notes: %v", acc.Notes)
		}
	})

	t.Run("report matches what the server enforces", func(t *testing.T) {
		if _, err := limited.ExecContext(ctx, `INSERT INTO ss_acc_widgets (id, name) VALUES (1, 'x')`); err == nil {
			t.Fatal("insert into ss_acc_widgets should be denied")
		}
		if _, err := limited.ExecContext(ctx, `TRUNCATE ss_acc_audit`); err == nil {
			t.Fatal("truncate should be denied")
		}
		if _, err := limited.ExecContext(ctx, `CREATE TABLE ss_acc_new (id INTEGER)`); err == nil {
			t.Fatal("create table should be denied")
		}
	})

	t.Run("without schema usage no table privilege is usable", func(t *testing.T) {
		// PUBLIC holds USAGE on schema public by default; remove both paths.
		execSQL(t, owner, `REVOKE USAGE ON SCHEMA public FROM PUBLIC; REVOKE USAGE ON SCHEMA public FROM `+user)
		acc := inspect(t, limited, e.driver)
		for name, ta := range acc.Tables {
			if ta != (db.TableAccess{}) {
				t.Fatalf("table %s = %+v, want no access without USAGE", name, ta)
			}
		}
		if !strings.Contains(strings.Join(acc.Notes, "\n"), "USAGE") {
			t.Fatalf("expected a USAGE note, got %v", acc.Notes)
		}
	})
}

func TestAccess_MySQL(t *testing.T) {
	e := mysqlEngine()
	_, owner := e.scratchDB(t, accessDB)
	for _, stmt := range accessTables {
		execSQL(t, owner, stmt)
	}
	ctx := context.Background()
	admin := e.adminDB(t)
	defer admin.Close()

	t.Run("app user with ALL on the database has full access", func(t *testing.T) {
		acc := inspect(t, owner, e.driver)
		if !strings.HasPrefix(acc.User, "seedstorm@") || acc.Database != accessDB || acc.Superuser || !acc.CreateTables {
			t.Fatalf("owner access = %+v", acc)
		}
		assertTables(t, acc, map[string]db.TableAccess{"ss_acc_widgets": fullTable, "ss_acc_orders": fullTable, "ss_acc_audit": fullTable})
	})

	t.Run("root is superuser", func(t *testing.T) {
		root := openDB(t, e.driver, strings.Replace(e.dsnFor(accessDB), "seedstorm:seedstorm@", "root:"+envOrDefault("SEEDSTORM_MYSQL_ROOT_PASSWORD", "root")+"@", 1))
		defer root.Close()
		acc := inspect(t, root, e.driver)
		if !acc.Superuser || !acc.CreateTables {
			t.Fatalf("root access = %+v", acc)
		}
		assertTables(t, acc, map[string]db.TableAccess{"ss_acc_widgets": fullTable, "ss_acc_orders": fullTable, "ss_acc_audit": fullTable})
	})

	const user = "ss_acc_limited"
	cleanupUsers := func(names ...string) {
		for _, n := range names {
			_, _ = admin.ExecContext(ctx, fmt.Sprintf("DROP USER IF EXISTS '%s'@'%%'", n))
		}
	}
	cleanupUsers(user)
	t.Cleanup(func() {
		c := e.adminDB(t)
		defer c.Close()
		for _, n := range []string{user, "ss_acc_member", "ss_acc_inactive", "ss_acc_role"} {
			_, _ = c.ExecContext(ctx, fmt.Sprintf("DROP USER IF EXISTS '%s'@'%%'", n))
		}
	})
	for _, stmt := range []string{
		fmt.Sprintf("CREATE USER '%s'@'%%' IDENTIFIED BY 'ss_acc_pw'", user),
		fmt.Sprintf("GRANT SELECT ON `%s`.* TO '%s'@'%%'", accessDB, user),
		fmt.Sprintf("GRANT INSERT ON `%s`.`ss_acc_orders` TO '%s'@'%%'", accessDB, user),
	} {
		if _, err := admin.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	dsnAs := func(name string) string {
		return strings.Replace(e.dsnFor(accessDB), "seedstorm:seedstorm@", name+":ss_acc_pw@", 1)
	}

	limited := openDB(t, e.driver, dsnAs(user))
	defer limited.Close()

	t.Run("limited user reports exactly its grants", func(t *testing.T) {
		acc := inspect(t, limited, e.driver)
		if !strings.HasPrefix(acc.User, user+"@") {
			t.Fatalf("user = %q", acc.User)
		}
		if acc.Superuser || acc.CreateTables {
			t.Fatalf("limited user must not be superuser or create tables: %+v", acc)
		}
		assertTables(t, acc, map[string]db.TableAccess{
			"ss_acc_widgets": readTable,
			"ss_acc_orders":  {Select: true, Insert: true},
			"ss_acc_audit":   readTable,
		})
		if len(acc.Notes) != 0 {
			t.Fatalf("unexpected notes: %v", acc.Notes)
		}
	})

	t.Run("report matches what the server enforces", func(t *testing.T) {
		if _, err := limited.ExecContext(ctx, "INSERT INTO ss_acc_widgets (id, name) VALUES (1, 'x')"); err == nil {
			t.Fatal("insert into ss_acc_widgets should be denied")
		}
		if _, err := limited.ExecContext(ctx, "TRUNCATE TABLE ss_acc_orders"); err == nil {
			t.Fatal("truncate should be denied")
		}
		if _, err := limited.ExecContext(ctx, "CREATE TABLE ss_acc_new (id INTEGER)"); err == nil {
			t.Fatal("create table should be denied")
		}
	})

	t.Run("role privileges are expanded when the role is active", func(t *testing.T) {
		var supportsRoles int
		if err := admin.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = 'mysql' AND TABLE_NAME = 'default_roles'`).Scan(&supportsRoles); err != nil || supportsRoles == 0 {
			t.Skip("roles need MySQL 8")
		}
		for _, stmt := range []string{
			"CREATE ROLE 'ss_acc_role'@'%'",
			fmt.Sprintf("GRANT SELECT, INSERT, DROP ON `%s`.* TO 'ss_acc_role'@'%%'", accessDB),
			"CREATE USER 'ss_acc_member'@'%' IDENTIFIED BY 'ss_acc_pw'",
			"GRANT 'ss_acc_role'@'%' TO 'ss_acc_member'@'%'",
			"SET DEFAULT ROLE ALL TO 'ss_acc_member'@'%'",
			"CREATE USER 'ss_acc_inactive'@'%' IDENTIFIED BY 'ss_acc_pw'",
			"GRANT 'ss_acc_role'@'%' TO 'ss_acc_inactive'@'%'",
			// A direct grant so the user may connect to the database at all.
			fmt.Sprintf("GRANT SELECT ON `%s`.`ss_acc_audit` TO 'ss_acc_inactive'@'%%'", accessDB),
		} {
			if _, err := admin.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
		roleTable := db.TableAccess{Select: true, Insert: true, Truncate: true}

		member := openDB(t, e.driver, dsnAs("ss_acc_member"))
		defer member.Close()
		acc := inspect(t, member, e.driver)
		if len(acc.Notes) > 0 {
			// Expansion failed: the report must say so rather than guess.
			if !strings.Contains(strings.Join(acc.Notes, "\n"), "roles") {
				t.Fatalf("unexpected notes: %v", acc.Notes)
			}
			t.Logf("role expansion unavailable: %v", acc.Notes)
		} else {
			if acc.CreateTables || acc.Superuser {
				t.Fatalf("role member access = %+v", acc)
			}
			assertTables(t, acc, map[string]db.TableAccess{"ss_acc_widgets": roleTable, "ss_acc_orders": roleTable, "ss_acc_audit": roleTable})
			if _, err := member.ExecContext(ctx, "TRUNCATE TABLE ss_acc_audit"); err != nil {
				t.Fatalf("truncate with the role's DROP should work: %v", err)
			}
		}

		inactive := openDB(t, e.driver, dsnAs("ss_acc_inactive"))
		defer inactive.Close()
		acc = inspect(t, inactive, e.driver)
		assertTables(t, acc, map[string]db.TableAccess{"ss_acc_audit": readTable})
		if !strings.Contains(strings.Join(acc.Notes, "\n"), "none is active") {
			t.Fatalf("expected an inactive-role note, got %v", acc.Notes)
		}
	})
}
