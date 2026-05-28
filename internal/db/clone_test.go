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

func TestBuildSchemaDDL_postgresEnumDDLTypeDoesNotRequireSourceEnumType(t *testing.T) {
	tables := []Table{{
		Name: "coupons",
		Columns: []Column{
			{Name: "id", DDLType: "integer", Type: "integer", IsPK: true},
			{Name: "discount_type", DDLType: "discount_type", Type: "discount_type", EnumValues: []string{"percentage", "fixed"}, Default: "'percentage'::discount_type"},
		},
	}}
	stmts, err := BuildSchemaDDL(tables, "pgx", false)
	if err != nil {
		t.Fatalf("BuildSchemaDDL: %v", err)
	}
	ddl := strings.Join(stmts, "\n")
	if strings.Contains(ddl, `"discount_type" discount_type`) {
		t.Fatalf("clone DDL should not require source enum type to exist:\n%s", ddl)
	}
	if !strings.Contains(ddl, `CHECK ("discount_type" IN ('percentage', 'fixed'))`) {
		t.Fatalf("expected enum to clone as TEXT with CHECK:\n%s", ddl)
	}
	if strings.Contains(ddl, "::discount_type") {
		t.Fatalf("clone DDL should not require source enum type in defaults:\n%s", ddl)
	}
	if !strings.Contains(ddl, `"discount_type" TEXT NOT NULL DEFAULT 'percentage' CHECK`) {
		t.Fatalf("expected enum default cast to be stripped:\n%s", ddl)
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

func TestBuildSchemaDDL_postgresSerialDefaultBecomesIdentity(t *testing.T) {
	tables := []Table{{
		Name: "addresses",
		Columns: []Column{
			{Name: "id", DDLType: "integer", Type: "integer", IsPK: true, AutoIncrement: true, Default: "nextval('addresses_id_seq'::regclass)"},
		},
	}}
	stmts, err := BuildSchemaDDL(tables, "pgx", false)
	if err != nil {
		t.Fatalf("BuildSchemaDDL: %v", err)
	}
	ddl := strings.Join(stmts, "\n")
	if strings.Contains(ddl, "addresses_id_seq") {
		t.Fatalf("source sequence name should not be copied into clone DDL:\n%s", ddl)
	}
	if !strings.Contains(ddl, `"id" integer GENERATED BY DEFAULT AS IDENTITY NOT NULL`) {
		t.Fatalf("serial PK should become identity column:\n%s", ddl)
	}
}

func TestBuildSchemaDDL_mysqlAutoIncrement(t *testing.T) {
	tables := []Table{{
		Name: "addresses",
		Columns: []Column{
			{Name: "id", DDLType: "int", Type: "integer", IsPK: true, AutoIncrement: true},
		},
	}}
	stmts, err := BuildSchemaDDL(tables, "mysql", false)
	if err != nil {
		t.Fatalf("BuildSchemaDDL: %v", err)
	}
	ddl := strings.Join(stmts, "\n")
	if !strings.Contains(ddl, "`id` int AUTO_INCREMENT NOT NULL") {
		t.Fatalf("auto_increment should be preserved:\n%s", ddl)
	}
}

func TestIsPostgresSerialDefault(t *testing.T) {
	if !isPostgresSerialDefault("nextval('addresses_id_seq'::regclass)") {
		t.Fatal("expected nextval default to be detected as serial")
	}
	if isPostgresSerialDefault("'pending'::text") {
		t.Fatal("ordinary string default should not be serial")
	}
}

func TestPostgresIndexQueryAvoidsArrayPositionOnInt2Vector(t *testing.T) {
	query := postgresIndexQuery()
	if strings.Contains(query, "array_position(ix.indkey") {
		t.Fatalf("Postgres index query must avoid array_position(int2vector, ...) for PG13 compatibility:\n%s", query)
	}
	if !strings.Contains(query, "FROM unnest(ix.indkey)") {
		t.Fatalf("Postgres index query should filter expression indexes through unnest(ix.indkey):\n%s", query)
	}
}

func TestBuildSchemaDDL_preservesDefaultsGeneratedIndexesAndComments(t *testing.T) {
	tables := []Table{{
		Name:    "orders",
		Comment: "order table",
		Columns: []Column{
			{Name: "id", DDLType: "integer", Type: "integer", IsPK: true},
			{Name: "status", DDLType: "varchar(20)", Type: "character varying", IsNullable: false, Default: "'new'::character varying", Comment: "workflow state"},
			{Name: "subtotal", DDLType: "numeric(10,2)", Type: "numeric", IsNullable: false, Default: "0"},
			{Name: "tax", DDLType: "numeric(10,2)", Type: "numeric", IsNullable: false, Default: "0"},
			{Name: "total", DDLType: "numeric(10,2)", Type: "numeric", Generated: "(subtotal + tax)"},
		},
		Indexes: []Index{
			{Name: "idx_orders_status_subtotal", Columns: []string{"status", "subtotal"}},
			{Name: "uq_orders_status_total", Columns: []string{"status", "total"}, Unique: true},
		},
	}}
	stmts, err := BuildSchemaDDL(tables, "pgx", false)
	if err != nil {
		t.Fatalf("BuildSchemaDDL: %v", err)
	}
	ddl := strings.Join(stmts, "\n")
	for _, want := range []string{
		`"status" varchar(20) NOT NULL DEFAULT 'new'::character varying`,
		`"subtotal" numeric(10,2) NOT NULL DEFAULT 0`,
		`"total" numeric(10,2) GENERATED ALWAYS AS ((subtotal + tax)) STORED`,
		`CREATE INDEX "idx_orders_status_subtotal" ON "orders" ("status", "subtotal")`,
		`CREATE UNIQUE INDEX "uq_orders_status_total" ON "orders" ("status", "total")`,
		`COMMENT ON TABLE "orders" IS 'order table'`,
		`COMMENT ON COLUMN "orders"."status" IS 'workflow state'`,
	} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("missing %q in:\n%s", want, ddl)
		}
	}
}

func TestBuildSchemaDDL_mysqlPreservesDefaultsGeneratedIndexesAndComments(t *testing.T) {
	tables := []Table{{
		Name:    "orders",
		Comment: "order table",
		Columns: []Column{
			{Name: "id", DDLType: "int", Type: "integer", IsPK: true},
			{Name: "status", DDLType: "varchar(20)", Type: "varchar", IsNullable: false, Default: "'new'", Comment: "workflow state"},
			{Name: "subtotal", DDLType: "decimal(10,2)", Type: "decimal", IsNullable: false, Default: "0.00"},
			{Name: "tax", DDLType: "decimal(10,2)", Type: "decimal", IsNullable: false, Default: "0.00"},
			{Name: "total", DDLType: "decimal(10,2)", Type: "decimal", Generated: "`subtotal` + `tax`"},
		},
		Indexes: []Index{
			{Name: "idx_orders_status_subtotal", Columns: []string{"status", "subtotal"}},
		},
	}}
	stmts, err := BuildSchemaDDL(tables, "mysql", false)
	if err != nil {
		t.Fatalf("BuildSchemaDDL: %v", err)
	}
	ddl := strings.Join(stmts, "\n")
	for _, want := range []string{
		"`status` varchar(20) NOT NULL DEFAULT 'new' COMMENT 'workflow state'",
		"`subtotal` decimal(10,2) NOT NULL DEFAULT 0.00",
		"`total` decimal(10,2) GENERATED ALWAYS AS (`subtotal` + `tax`) STORED",
		"CREATE INDEX `idx_orders_status_subtotal` ON `orders` (`status`, `subtotal`)",
		"COMMENT='order table'",
	} {
		if !strings.Contains(ddl, want) {
			t.Fatalf("missing %q in:\n%s", want, ddl)
		}
	}
}

func TestMySQLGeneratedExpressionNormalizesEscapedStringLiterals(t *testing.T) {
	got := mysqlGeneratedExpression("concat(`email`,_utf8mb4\\':\\',`status`)")
	want := "concat(`email`,_utf8mb4':',`status`)"
	if got != want {
		t.Fatalf("mysqlGeneratedExpression() = %q, want %q", got, want)
	}
}
