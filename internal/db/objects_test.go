package db

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestStripMySQLDefiner(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "view with algorithm and backtick definer on host %",
			in:   "CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`%` SQL SECURITY DEFINER VIEW `v` AS select 1 AS `x`",
			want: "CREATE ALGORITHM=UNDEFINED SQL SECURITY DEFINER VIEW `v` AS select 1 AS `x`",
		},
		{
			name: "function with quoted definer",
			in:   "CREATE DEFINER='app'@'10.0.%' FUNCTION `f`() RETURNS int\n    DETERMINISTIC\nRETURN 42",
			want: "CREATE FUNCTION `f`() RETURNS int\n    DETERMINISTIC\nRETURN 42",
		},
		{
			name: "unquoted definer",
			in:   "CREATE DEFINER=root@localhost PROCEDURE `p`() BEGIN SELECT 1; END",
			want: "CREATE PROCEDURE `p`() BEGIN SELECT 1; END",
		},
		{
			name: "spaces around equals and at",
			in:   "CREATE DEFINER = `root` @ `%` TRIGGER `t` BEFORE INSERT ON `x` FOR EACH ROW SET NEW.a = 1",
			want: "CREATE TRIGGER `t` BEFORE INSERT ON `x` FOR EACH ROW SET NEW.a = 1",
		},
		{
			name: "current_user with parens",
			in:   "CREATE DEFINER=CURRENT_USER() TRIGGER `t` BEFORE INSERT ON `x` FOR EACH ROW SET NEW.a = 1",
			want: "CREATE TRIGGER `t` BEFORE INSERT ON `x` FOR EACH ROW SET NEW.a = 1",
		},
		{
			name: "user name containing escaped backtick and at sign",
			in:   "CREATE DEFINER=`we``ird@x`@`%` PROCEDURE `p`() SELECT 1",
			want: "CREATE PROCEDURE `p`() SELECT 1",
		},
		{
			name: "no definer present is unchanged",
			in:   "CREATE ALGORITHM=MERGE SQL SECURITY INVOKER VIEW `v` AS select 1",
			want: "CREATE ALGORITHM=MERGE SQL SECURITY INVOKER VIEW `v` AS select 1",
		},
		{
			name: "definer text inside the body is not touched",
			in:   "CREATE PROCEDURE `p`() SELECT 'DEFINER=`root`@`%`'",
			want: "CREATE PROCEDURE `p`() SELECT 'DEFINER=`root`@`%`'",
		},
		{
			name: "only the header definer is stripped",
			in:   "CREATE DEFINER=`root`@`%` PROCEDURE `p`() SELECT 'DEFINER=`x`@`y` '",
			want: "CREATE PROCEDURE `p`() SELECT 'DEFINER=`x`@`y` '",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripMySQLDefiner(tt.in); got != tt.want {
				t.Fatalf("StripMySQLDefiner()\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestStripMySQLSchemaQualifier(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		schema string
		want   string
	}{
		{
			name:   "qualified columns and tables",
			in:     "select `src`.`t`.`id` AS `id` from `src`.`t`",
			schema: "src",
			want:   "select `t`.`id` AS `id` from `t`",
		},
		{
			name:   "identifiers containing the schema name as a substring stay",
			in:     "select `src_x`.`id` AS `my_src` from `src_x` join `xsrc`.`t`",
			schema: "src",
			want:   "select `src_x`.`id` AS `my_src` from `src_x` join `xsrc`.`t`",
		},
		{
			name:   "schema name used as an alias not followed by a dot stays",
			in:     "select 1 AS `src` from `src`.`t`",
			schema: "src",
			want:   "select 1 AS `src` from `t`",
		},
		{
			name:   "string literals are untouched",
			in:     "select '`src`.`t`' AS `s`, 'it''s `src`.' AS `q`, 'a\\'`src`.' AS `r` from `src`.`t`",
			schema: "src",
			want:   "select '`src`.`t`' AS `s`, 'it''s `src`.' AS `q`, 'a\\'`src`.' AS `r` from `t`",
		},
		{
			name:   "schema with escaped backtick",
			in:     "select * from `we``ird`.`t`",
			schema: "we`ird",
			want:   "select * from `t`",
		},
		{
			name:   "other schemas keep their qualifier",
			in:     "select * from `other`.`t` join `src`.`u`",
			schema: "src",
			want:   "select * from `other`.`t` join `u`",
		},
		{
			name:   "unterminated quote does not panic",
			in:     "select `src",
			schema: "src",
			want:   "select `src",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripMySQLSchemaQualifier(tt.in, tt.schema); got != tt.want {
				t.Fatalf("StripMySQLSchemaQualifier()\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestParseCloneObjects(t *testing.T) {
	tests := []struct {
		in      string
		want    CloneObjects
		wantErr bool
	}{
		{in: "", want: CloneObjects{}},
		{in: "all", want: CloneObjects{Views: true, Routines: true, Triggers: true}},
		{in: " Views , TRIGGERS ", want: CloneObjects{Views: true, Triggers: true}},
		{in: "routines,", want: CloneObjects{Routines: true}},
		{in: "views,all", want: CloneObjects{Views: true, Routines: true, Triggers: true}},
		{in: "tables", wantErr: true},
		{in: "views,sequences", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseCloneObjects(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func sampleObjects() []DBObject {
	return []DBObject{
		{Kind: ObjectTrigger, Name: "trg_a", Table: "items", Create: "CREATE TRIGGER trg_a BEFORE INSERT ON public.items FOR EACH ROW EXECUTE FUNCTION set_a()"},
		{Kind: ObjectView, Name: "v_top", Create: `CREATE VIEW "v_top" AS SELECT 1`},
		{Kind: ObjectFunction, Name: "set_a", Create: "CREATE OR REPLACE FUNCTION public.set_a()\n RETURNS trigger"},
		{Kind: ObjectMaterializedView, Name: "m_stats", Create: `CREATE MATERIALIZED VIEW "m_stats" AS SELECT 1 WITH NO DATA`},
		{Kind: ObjectProcedure, Name: "p_add", Args: "a integer, b text", Create: "CREATE OR REPLACE PROCEDURE public.p_add(a integer, b text)"},
		{Kind: ObjectView, Name: "v_base", Create: `CREATE VIEW "v_base" AS SELECT 1`},
	}
}

func TestBuildCloneDDL_noObjectsMatchesBuildSchemaDDL(t *testing.T) {
	tables := []Table{{Name: "users", Columns: []Column{{Name: "id", Type: "integer", IsPK: true}}}}
	for _, dbType := range []string{"pgx", "mysql"} {
		for _, drop := range []bool{false, true} {
			want, err := BuildSchemaDDL(tables, dbType, drop)
			if err != nil {
				t.Fatal(err)
			}
			got, err := BuildCloneDDL(tables, ObjectSet{Skipped: []SkippedObject{{Kind: ObjectView, Name: "hidden"}}}, dbType, drop)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s drop=%v: got %#v want %#v", dbType, drop, got, want)
			}
		}
	}
}

func TestBuildCloneDDL_ordersObjectsAroundTables(t *testing.T) {
	tables := []Table{{Name: "items", Columns: []Column{{Name: "id", Type: "integer", IsPK: true}}}}
	got, err := BuildCloneDDL(tables, ObjectSet{Objects: sampleObjects()}, "pgx", true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`DROP TRIGGER IF EXISTS "trg_a" ON "items"`,
		`DROP VIEW IF EXISTS "v_top" CASCADE`,
		`DROP MATERIALIZED VIEW IF EXISTS "m_stats" CASCADE`,
		`DROP VIEW IF EXISTS "v_base" CASCADE`,
		`DROP FUNCTION IF EXISTS "set_a"() CASCADE`,
		`DROP PROCEDURE IF EXISTS "p_add"(a integer, b text) CASCADE`,
		`DROP TABLE IF EXISTS "items" CASCADE`,
		"CREATE TABLE \"items\" (\n  \"id\" INTEGER NOT NULL,\n  PRIMARY KEY (\"id\")\n)",
		"CREATE OR REPLACE FUNCTION public.set_a()\n RETURNS trigger",
		"CREATE OR REPLACE PROCEDURE public.p_add(a integer, b text)",
		`CREATE VIEW "v_top" AS SELECT 1`,
		`CREATE MATERIALIZED VIEW "m_stats" AS SELECT 1 WITH NO DATA`,
		`CREATE VIEW "v_base" AS SELECT 1`,
		"CREATE TRIGGER trg_a BEFORE INSERT ON public.items FOR EACH ROW EXECUTE FUNCTION set_a()",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("statements:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestBuildCloneDDL_mysqlDropsWithoutDropExistingAreOmitted(t *testing.T) {
	tables := []Table{{Name: "items", Columns: []Column{{Name: "id", Type: "integer", IsPK: true}}}}
	got, err := BuildCloneDDL(tables, ObjectSet{Objects: sampleObjects()}, "mysql", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range got {
		if strings.HasPrefix(stmt, "DROP") {
			t.Fatalf("unexpected drop without dropExisting: %q", stmt)
		}
	}
	if len(got) != 1+len(sampleObjects()) {
		t.Fatalf("got %d statements: %#v", len(got), got)
	}
}

func TestBuildObjectDropDDL_mysql(t *testing.T) {
	got := BuildObjectDropDDL(sampleObjects(), "mysql")
	want := []string{
		"DROP TRIGGER IF EXISTS `trg_a`",
		"DROP VIEW IF EXISTS `v_top`",
		"DROP VIEW IF EXISTS `m_stats`",
		"DROP VIEW IF EXISTS `v_base`",
		"DROP FUNCTION IF EXISTS `set_a`",
		"DROP PROCEDURE IF EXISTS `p_add`",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestDDLProgressLabel(t *testing.T) {
	tests := []struct {
		stmt string
		want string
	}{
		{"CREATE TABLE \"users\" (\n  id INT\n)", "create users"},
		{`DROP TABLE IF EXISTS "users" CASCADE`, "drop users"},
		{"DROP TABLE IF EXISTS `users`", "drop users"},
		{`CREATE INDEX "idx" ON "users" ("id")`, "index idx"},
		{"CREATE OR REPLACE FUNCTION public.add_one(a integer)\n RETURNS integer", "function add_one"},
		{"CREATE OR REPLACE PROCEDURE public.bump()\n LANGUAGE plpgsql", "procedure bump"},
		{"CREATE PROCEDURE `bump`()\nBEGIN SELECT 1; END", "procedure bump"},
		{"CREATE VIEW \"v_orders\" AS\n SELECT 1", "view v_orders"},
		{"CREATE MATERIALIZED VIEW \"m\" AS SELECT 1 WITH NO DATA", "materialized view m"},
		{"CREATE ALGORITHM=UNDEFINED SQL SECURITY DEFINER VIEW `v` AS select 1", "view v"},
		{"CREATE ALGORITHM = MERGE DEFINER = `root`@`%` SQL SECURITY INVOKER VIEW `v2` AS select 1", "view v2"},
		{"CREATE TRIGGER trg BEFORE INSERT ON public.items FOR EACH ROW EXECUTE FUNCTION f()", "trigger trg"},
		{"CREATE CONSTRAINT TRIGGER ctrg AFTER INSERT ON public.items", "trigger ctrg"},
		{`CREATE FUNCTION "odd.name"() RETURNS int`, "function odd.name"},
		{`DROP FUNCTION IF EXISTS "set_a"(integer) CASCADE`, "drop function set_a"},
		{`DROP MATERIALIZED VIEW IF EXISTS "m" CASCADE`, "drop materialized view m"},
		{"DROP TRIGGER IF EXISTS `t`", "drop trigger t"},
		{"DROP VIEW", "drop"},
		{"SET FOREIGN_KEY_CHECKS=0", "set"},
		{"   ", "DDL"},
	}
	for _, tt := range tests {
		if got := ddlProgressLabel(tt.stmt); got != tt.want {
			t.Errorf("ddlProgressLabel(%q) = %q, want %q", tt.stmt, got, tt.want)
		}
	}
}

func TestIsViewAndRoutineCreate(t *testing.T) {
	if !isViewCreate("CREATE ALGORITHM=UNDEFINED SQL SECURITY DEFINER VIEW `v` AS select 1") {
		t.Error("mysql view not detected")
	}
	if isViewCreate(`CREATE TABLE "view" (id int)`) {
		t.Error("table named view detected as view")
	}
	if isViewCreate(`DROP VIEW IF EXISTS "v"`) {
		t.Error("drop detected as create")
	}
	if !isRoutineCreate("CREATE OR REPLACE FUNCTION public.f()") || !isRoutineCreate("CREATE PROCEDURE `p`() SELECT 1") {
		t.Error("routine not detected")
	}
	if isRoutineCreate("CREATE TRIGGER t BEFORE INSERT ON x FOR EACH ROW EXECUTE FUNCTION f()") {
		t.Error("trigger detected as routine")
	}
}

func TestExecInPasses_resolvesDependenciesInAnyOrder(t *testing.T) {
	// c depends on b, b depends on a; given in reverse order.
	deps := map[string]string{"c": "b", "b": "a", "a": ""}
	created := map[string]bool{}
	var order []string
	exec := func(stmt string) error {
		if dep := deps[stmt]; dep != "" && !created[dep] {
			return errors.New("missing " + dep)
		}
		created[stmt] = true
		return nil
	}
	err := execInPasses([]string{"c", "b", "a"}, exec, func(stmt string) { order = append(order, stmt) })
	if err != nil {
		t.Fatalf("execInPasses: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"a", "b", "c"}) {
		t.Fatalf("done order = %v", order)
	}
}

func TestExecInPasses_reportsUnresolvableStatements(t *testing.T) {
	attempts := 0
	exec := func(stmt string) error {
		attempts++
		if stmt == "ok" {
			return nil
		}
		return errors.New("broken " + stmt)
	}
	var done []string
	err := execInPasses([]string{"x", "ok", "y"}, exec, func(stmt string) { done = append(done, stmt) })
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"broken x", "broken y"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
	if !reflect.DeepEqual(done, []string{"ok"}) {
		t.Fatalf("done = %v", done)
	}
	// Pass 1: 3 attempts; pass 2: 2 attempts with no progress, then stop.
	if attempts != 5 {
		t.Fatalf("attempts = %d, want 5", attempts)
	}
}

func TestExecInPasses_empty(t *testing.T) {
	if err := execInPasses(nil, func(string) error { return errors.New("never") }, func(string) {}); err != nil {
		t.Fatal(err)
	}
}
