//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/db"
)

// fkTargets maps "table.column" to the "table.column" its FK references.
func fkTargets(tables []db.Table) map[string]string {
	out := map[string]string{}
	for _, t := range tables {
		for _, c := range t.Columns {
			if c.FK != nil {
				out[t.Name+"."+c.Name] = c.FK.TableName + "." + c.FK.ColumnName
			}
		}
	}
	return out
}

func pkColumns(tables []db.Table) map[string][]string {
	out := map[string][]string{}
	for _, t := range tables {
		for _, c := range t.Columns {
			if c.IsPK {
				out[t.Name] = append(out[t.Name], c.Name)
			}
		}
	}
	return out
}

// Postgres FKs were read from information_schema joined by constraint name
// only: a two-column FK paired every column with every referenced column (last
// one won), two tables with a same-named constraint mixed their columns, and a
// role with only SELECT saw no constraints at all.
func TestIntrospect_PostgresConstraintsArePairedAndVisibleToReadOnlyRoles(t *testing.T) {
	e := postgresEngine()
	dsn, owner := e.scratchDB(t, "ss_introspect_fk")
	execSQL(t, owner, `
		CREATE TABLE regions (country CHAR(2), code VARCHAR(8), name TEXT, PRIMARY KEY (country, code));
		CREATE TABLE stores (id SERIAL PRIMARY KEY, country CHAR(2) NOT NULL, region VARCHAR(8) NOT NULL,
			CONSTRAINT loc_fk FOREIGN KEY (region, country) REFERENCES regions (code, country));
		CREATE TABLE brands (id SERIAL PRIMARY KEY, name TEXT);
		CREATE TABLE products (id SERIAL PRIMARY KEY, brand_id INT NOT NULL, store_id INT NOT NULL,
			CONSTRAINT loc_fk FOREIGN KEY (brand_id) REFERENCES brands (id),
			CONSTRAINT store_fk FOREIGN KEY (store_id) REFERENCES stores (id))`)

	want := map[string]string{
		"stores.country":    "regions.country",
		"stores.region":     "regions.code",
		"products.brand_id": "brands.id",
		"products.store_id": "stores.id",
	}
	check := func(t *testing.T, dsn string) {
		t.Helper()
		tables, err := db.Introspect(e.driver, dsn)
		if err != nil {
			t.Fatal(err)
		}
		got := fkTargets(tables)
		for col, target := range want {
			if got[col] != target {
				t.Errorf("FK %s -> %q, want %q (all: %v)", col, got[col], target, got)
			}
		}
		if len(got) != len(want) {
			t.Errorf("FKs = %v, want exactly %v", got, want)
		}
		pks := pkColumns(tables)
		if strings.Join(pks["regions"], ",") != "country,code" || strings.Join(pks["stores"], ",") != "id" {
			t.Errorf("PKs = %v", pks)
		}
	}

	t.Run("owner", func(t *testing.T) { check(t, dsn) })

	t.Run("select-only role", func(t *testing.T) {
		const role = "ss_introspect_reader"
		ctx := context.Background()
		drop := func() {
			var exists bool
			_ = owner.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists)
			if exists {
				_, _ = owner.ExecContext(ctx, `DROP OWNED BY `+role)
				_, _ = owner.ExecContext(ctx, `DROP ROLE `+role)
			}
		}
		drop()
		t.Cleanup(drop)
		execSQL(t, owner, fmt.Sprintf(`
			CREATE ROLE %[1]s LOGIN PASSWORD 'ss_reader_pw';
			GRANT CONNECT ON DATABASE ss_introspect_fk TO %[1]s;
			GRANT USAGE ON SCHEMA public TO %[1]s;
			GRANT SELECT ON ALL TABLES IN SCHEMA public TO %[1]s`, role))
		check(t, strings.Replace(dsn, "seedstorm:seedstorm@", role+":ss_reader_pw@", 1))
	})
}

// A foreign key may reference a UNIQUE column that is not the primary key, or
// a column of a composite key that is unique on its own.
// Seeding used to fill it with the parent's primary-key values, which the
// database refused as FK violations.
func TestSeed_ForeignKeysToNonKeyColumnsInsertValidReferences(t *testing.T) {
	for _, e := range []engine{postgresEngine(), mysqlEngine()} {
		t.Run(e.name, func(t *testing.T) {
			dsn, conn := e.scratchDB(t, "ss_seed_refcols")
			execSQL(t, conn, `
				CREATE TABLE accounts (id INT PRIMARY KEY, code VARCHAR(16) NOT NULL UNIQUE);
				CREATE TABLE ledger (id INT PRIMARY KEY, account_code VARCHAR(16) NOT NULL,
					FOREIGN KEY (account_code) REFERENCES accounts (code));
				CREATE TABLE memberships (tenant_id INT NOT NULL, member_no INT NOT NULL, PRIMARY KEY (tenant_id, member_no), UNIQUE (member_no));
				CREATE TABLE badges (id INT PRIMARY KEY, member_no INT NOT NULL,
					FOREIGN KEY (member_no) REFERENCES memberships (member_no))`)
			schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
			runBin(t, "introspect", "--db", e.name, "--dsn", dsn, "--out", schemaPath)
			runBin(t, "seed", "--db", e.name, "--dsn", dsn, "--schema", schemaPath, "--rows", "40", "--workers", "1")

			var ledger, badges int
			if err := conn.QueryRow(`SELECT COUNT(*) FROM ledger`).Scan(&ledger); err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRow(`SELECT COUNT(*) FROM badges`).Scan(&badges); err != nil {
				t.Fatal(err)
			}
			if ledger != 40 || badges != 40 {
				t.Fatalf("rows: ledger=%d badges=%d, want 40 each (the database enforces every reference)", ledger, badges)
			}
		})
	}
}
