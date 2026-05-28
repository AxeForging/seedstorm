package db

import (
	"strings"
	"testing"
)

func TestBuildSchemaDDL_createsTablesBeforeForeignKeys(t *testing.T) {
	tables := []Table{
		{
			Name: "orders",
			Columns: []Column{
				{Name: "id", Type: "integer", IsPK: true},
				{Name: "user_id", Type: "integer", FK: &ForeignKey{TableName: "users", ColumnName: "id"}},
			},
		},
		{
			Name: "users",
			Columns: []Column{
				{Name: "id", Type: "integer", IsPK: true},
				{Name: "email", Type: "varchar", Unique: true},
			},
		},
	}
	stmts, err := BuildSchemaDDL(tables, "pgx", false)
	if err != nil {
		t.Fatalf("BuildSchemaDDL: %v", err)
	}
	joined := strings.Join(stmts, "\n")
	createIdx := strings.Index(joined, `CREATE TABLE "orders"`)
	fkIdx := strings.Index(joined, `ALTER TABLE "orders" ADD FOREIGN KEY ("user_id") REFERENCES "users" ("id")`)
	if createIdx < 0 || fkIdx < 0 {
		t.Fatalf("missing create statements:\n%s", joined)
	}
	if createIdx > fkIdx {
		t.Fatalf("FK should be added after CREATE TABLE statements:\n%s", joined)
	}
}

func TestBuildSchemaDDL_includesBarrierDropStatements(t *testing.T) {
	tables := []Table{{Name: "users", Columns: []Column{{Name: "id", Type: "integer", IsPK: true}}}}
	stmts, err := BuildSchemaDDL(tables, "mysql", true)
	if err != nil {
		t.Fatalf("BuildSchemaDDL: %v", err)
	}
	if len(stmts) < 4 {
		t.Fatalf("statements = %#v", stmts)
	}
	if stmts[0] != "SET FOREIGN_KEY_CHECKS=0" {
		t.Fatalf("first stmt = %q", stmts[0])
	}
	if !strings.HasPrefix(stmts[1], "DROP TABLE IF EXISTS `users`") {
		t.Fatalf("drop stmt = %q", stmts[1])
	}
	if stmts[2] != "SET FOREIGN_KEY_CHECKS=1" {
		t.Fatalf("third stmt = %q", stmts[2])
	}
}

func TestBuildSchemaDDL_rejectsCrossUnsupportedDriver(t *testing.T) {
	if _, err := BuildSchemaDDL(nil, "sqlite", false); err == nil {
		t.Fatal("expected unsupported driver error")
	}
}

func TestBuildSchemaDDL_supportsHardCycles(t *testing.T) {
	tables := []Table{
		{Name: "a", Columns: []Column{{Name: "id", Type: "integer", IsPK: true}, {Name: "b_id", Type: "integer", FK: &ForeignKey{TableName: "b", ColumnName: "id"}}}},
		{Name: "b", Columns: []Column{{Name: "id", Type: "integer", IsPK: true}, {Name: "a_id", Type: "integer", FK: &ForeignKey{TableName: "a", ColumnName: "id"}}}},
	}
	stmts, err := BuildSchemaDDL(tables, "pgx", false)
	if err != nil {
		t.Fatalf("BuildSchemaDDL should support cyclic FKs via ALTER TABLE: %v", err)
	}
	ddl := strings.Join(stmts, "\n")
	for _, want := range []string{
		`ALTER TABLE "a" ADD FOREIGN KEY ("b_id") REFERENCES "b" ("id")`,
		`ALTER TABLE "b" ADD FOREIGN KEY ("a_id") REFERENCES "a" ("id")`,
	} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("missing %q in:\n%s", want, ddl)
		}
	}
}

func TestBuildSchemaDDL_checkConstraints(t *testing.T) {
	min, max := int64(1), int64(5)
	tables := []Table{{
		Name: "tickets",
		Columns: []Column{
			{Name: "id", Type: "integer", IsPK: true},
			{Name: "status", Type: "varchar", CheckValues: []string{"new", "closed"}},
			{Name: "rating", Type: "integer", CheckMin: &min, CheckMax: &max},
		},
	}}
	stmts, err := BuildSchemaDDL(tables, "pgx", false)
	if err != nil {
		t.Fatalf("BuildSchemaDDL: %v", err)
	}
	ddl := strings.Join(stmts, "\n")
	for _, want := range []string{
		`CHECK ("status" IN ('new', 'closed'))`,
		`CHECK ("rating" BETWEEN 1 AND 5)`,
	} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("missing %q in:\n%s", want, ddl)
		}
	}
}

func TestBuildSchemaDDL_postgresEnumValuesBecomeCheck(t *testing.T) {
	tables := []Table{{
		Name: "orders",
		Columns: []Column{
			{Name: "id", Type: "integer", IsPK: true},
			{Name: "status", Type: "order_status", EnumValues: []string{"new", "done"}},
		},
	}}
	stmts, err := BuildSchemaDDL(tables, "pgx", false)
	if err != nil {
		t.Fatalf("BuildSchemaDDL: %v", err)
	}
	ddl := strings.Join(stmts, "\n")
	if !strings.Contains(ddl, `"status" TEXT NOT NULL CHECK ("status" IN ('new', 'done'))`) {
		t.Fatalf("Postgres enum values should be preserved as a CHECK over TEXT:\n%s", ddl)
	}
}

func TestBuildSchemaDDL_mysqlColumnConstraintOrder(t *testing.T) {
	tables := []Table{{
		Name: "users",
		Columns: []Column{
			{Name: "id", Type: "integer", IsPK: true},
			{Name: "email", Type: "varchar", IsNullable: false, Unique: true},
			{Name: "status", Type: "varchar", IsNullable: false, CheckValues: []string{"active", "blocked"}},
		},
	}}
	stmts, err := BuildSchemaDDL(tables, "mysql", false)
	if err != nil {
		t.Fatalf("BuildSchemaDDL: %v", err)
	}
	ddl := strings.Join(stmts, "\n")
	for _, want := range []string{
		"`email` VARCHAR(255) NOT NULL UNIQUE",
		"`status` VARCHAR(255) NOT NULL CHECK (`status` IN ('active', 'blocked'))",
	} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("missing %q in:\n%s", want, ddl)
		}
	}
}
