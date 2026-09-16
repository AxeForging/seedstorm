package db

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

var (
	accNone = TableAccess{}
	accRead = TableAccess{Select: true}
	accFull = TableAccess{Select: true, Insert: true, Update: true, Delete: true, Truncate: true}
	accDML  = TableAccess{Select: true, Insert: true, Update: true, Delete: true}
)

func TestParseMySQLGrants(t *testing.T) {
	tables := []string{"orders", "users"}
	tests := []struct {
		name         string
		lines        []string
		database     string
		noWildcards  bool
		superuser    bool
		createTables bool
		want         map[string]TableAccess
		noteContains string
	}{
		{
			name:  "empty grants give nothing",
			lines: nil, database: "app",
			want: map[string]TableAccess{"orders": accNone, "users": accNone},
		},
		{
			name:  "only USAGE gives nothing",
			lines: []string{"GRANT USAGE ON *.* TO `u`@`%`"}, database: "app",
			want: map[string]TableAccess{"orders": accNone, "users": accNone},
		},
		{
			name:  "ALL PRIVILEGES on *.* is superuser with everything",
			lines: []string{"GRANT ALL PRIVILEGES ON *.* TO `root`@`%` WITH GRANT OPTION"}, database: "app",
			superuser: true, createTables: true,
			want: map[string]TableAccess{"orders": accFull, "users": accFull},
		},
		{
			name: "MySQL 8 root lists static privileges including SUPER",
			lines: []string{
				"GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, RELOAD, SUPER, CREATE ROLE ON *.* TO `root`@`%` WITH GRANT OPTION",
				"GRANT APPLICATION_PASSWORD_ADMIN,AUDIT_ADMIN,SYSTEM_USER ON *.* TO `root`@`%` WITH GRANT OPTION",
			},
			database: "app", superuser: true, createTables: true,
			want: map[string]TableAccess{"orders": accFull, "users": accFull},
		},
		{
			name:  "ALL without PRIVILEGES keyword on database",
			lines: []string{"GRANT USAGE ON *.* TO `u`@`%`", "GRANT ALL ON `app`.* TO `u`@`%`"}, database: "app",
			createTables: true,
			want:         map[string]TableAccess{"orders": accFull, "users": accFull},
		},
		{
			name:  "database-level SELECT is read-only without CREATE",
			lines: []string{"GRANT SELECT ON `app`.* TO `u`@`%`"}, database: "app",
			want: map[string]TableAccess{"orders": accRead, "users": accRead},
		},
		{
			name:  "privilege names are case-insensitive",
			lines: []string{"grant select, insert on `app`.* to `u`@`%`"}, database: "app",
			want: map[string]TableAccess{"orders": {Select: true, Insert: true}, "users": {Select: true, Insert: true}},
		},
		{
			name:  "grant on another database does not apply",
			lines: []string{"GRANT ALL PRIVILEGES ON `other`.* TO `u`@`%`", "GRANT INSERT ON `other`.`orders` TO `u`@`%`"}, database: "app",
			want: map[string]TableAccess{"orders": accNone, "users": accNone},
		},
		{
			name:  "table-level grant applies to that table only",
			lines: []string{"GRANT SELECT ON `app`.* TO `u`@`%`", "GRANT INSERT, UPDATE ON `app`.`orders` TO `u`@`%`"}, database: "app",
			want: map[string]TableAccess{"orders": {Select: true, Insert: true, Update: true}, "users": accRead},
		},
		{
			name:  "column-level grants do not grant table-level privileges",
			lines: []string{"GRANT SELECT (`id`, `email`), INSERT (`email`) ON `app`.`users` TO `u`@`%`"}, database: "app",
			want: map[string]TableAccess{"orders": accNone, "users": accNone},
		},
		{
			name:  "column-level grant mixed with table-level privilege keeps the table-level one",
			lines: []string{"GRANT SELECT, INSERT (`email`), UPDATE ON `app`.`users` TO `u`@`%`"}, database: "app",
			want: map[string]TableAccess{"orders": accNone, "users": {Select: true, Update: true}},
		},
		{
			name:  "WITH GRANT OPTION does not change privileges",
			lines: []string{"GRANT SELECT, DELETE ON `app`.* TO `u`@`%` WITH GRANT OPTION"}, database: "app",
			want: map[string]TableAccess{"orders": {Select: true, Delete: true}, "users": {Select: true, Delete: true}},
		},
		{
			name:  "role grant lines carry no privileges",
			lines: []string{"GRANT USAGE ON *.* TO `u`@`%`", "GRANT `app_rw`@`%`,`app_ro`@`%` TO `u`@`%`"}, database: "app",
			want: map[string]TableAccess{"orders": accNone, "users": accNone},
		},
		{
			name:  "TRUNCATE requires DROP",
			lines: []string{"GRANT SELECT, INSERT, UPDATE, DELETE ON `app`.* TO `u`@`%`", "GRANT DROP ON `app`.`orders` TO `u`@`%`"}, database: "app",
			want: map[string]TableAccess{"orders": accFull, "users": accDML},
		},
		{
			name:  "CREATE on the database allows creating tables",
			lines: []string{"GRANT CREATE ON `app`.* TO `u`@`%`"}, database: "app",
			createTables: true,
			want:         map[string]TableAccess{"orders": accNone, "users": accNone},
		},
		{
			name:  "global CREATE allows creating tables",
			lines: []string{"GRANT CREATE ON *.* TO `u`@`%`"}, database: "app",
			createTables: true,
			want:         map[string]TableAccess{"orders": accNone, "users": accNone},
		},
		{
			name:  "CREATE on a table does not allow creating tables",
			lines: []string{"GRANT CREATE ON `app`.`orders` TO `u`@`%`"}, database: "app",
			want: map[string]TableAccess{"orders": accNone, "users": accNone},
		},
		{
			name:  "percent wildcard database matches",
			lines: []string{"GRANT SELECT ON `app%`.* TO `u`@`%`"}, database: "app_prod",
			want: map[string]TableAccess{"orders": accRead, "users": accRead},
		},
		{
			name:  "underscore wildcard matches any single character",
			lines: []string{"GRANT SELECT ON `app_db`.* TO `u`@`%`"}, database: "appXdb",
			want: map[string]TableAccess{"orders": accRead, "users": accRead},
		},
		{
			name:  "underscore wildcard does not match two characters",
			lines: []string{"GRANT SELECT ON `app_db`.* TO `u`@`%`"}, database: "appXYdb",
			want: map[string]TableAccess{"orders": accNone, "users": accNone},
		},
		{
			name:  "escaped underscore is literal and does not match another character",
			lines: []string{"GRANT SELECT ON `app\\_db`.* TO `u`@`%`"}, database: "appXdb",
			want: map[string]TableAccess{"orders": accNone, "users": accNone},
		},
		{
			name:  "escaped underscore matches a literal underscore",
			lines: []string{"GRANT SELECT ON `app\\_db`.* TO `u`@`%`"}, database: "app_db",
			want: map[string]TableAccess{"orders": accRead, "users": accRead},
		},
		{
			name:  "wildcards are literal when partial_revokes is on",
			lines: []string{"GRANT SELECT ON `app%`.* TO `u`@`%`"}, database: "app_prod", noWildcards: true,
			want: map[string]TableAccess{"orders": accNone, "users": accNone},
		},
		{
			name:  "exact database grant wins over a wildcard grant",
			lines: []string{"GRANT ALL PRIVILEGES ON `app%`.* TO `u`@`%`", "GRANT SELECT ON `app`.* TO `u`@`%`"}, database: "app",
			want: map[string]TableAccess{"orders": accRead, "users": accRead},
		},
		{
			name:  "several matching wildcard grants add a note",
			lines: []string{"GRANT SELECT ON `a%`.* TO `u`@`%`", "GRANT INSERT ON `ap_`.* TO `u`@`%`"}, database: "app",
			want:         map[string]TableAccess{"orders": {Select: true, Insert: true}, "users": {Select: true, Insert: true}},
			noteContains: "most specific",
		},
		{
			name: "identifiers with escaped backticks",
			lines: []string{
				"GRANT SELECT ON `we``ird`.* TO `u`@`%`",
				"GRANT INSERT ON `we``ird`.`or``ders` TO `u`@`%`",
			},
			database: "we`ird",
			want:     map[string]TableAccess{"or`ders": {Select: true, Insert: true}},
		},
		{
			name:  "database named like a keyword",
			lines: []string{"GRANT SELECT ON `on to`.* TO `u`@`%`"}, database: "on to",
			want: map[string]TableAccess{"orders": accRead, "users": accRead},
		},
		{
			name:  "TABLE keyword before the object",
			lines: []string{"GRANT INSERT ON TABLE `app`.`orders` TO `u`@`%`"}, database: "app",
			want: map[string]TableAccess{"orders": {Insert: true}, "users": accNone},
		},
		{
			name:  "routine and proxy grants are ignored",
			lines: []string{"GRANT EXECUTE ON PROCEDURE `app`.`p` TO `u`@`%`", "GRANT PROXY ON ``@`` TO `u`@`%` WITH GRANT OPTION"}, database: "app",
			want: map[string]TableAccess{"orders": accNone, "users": accNone},
		},
		{
			name: "partial revoke removes global privileges for the database",
			lines: []string{
				"GRANT SELECT, INSERT, DROP, CREATE ON *.* TO `u`@`%`",
				"REVOKE INSERT, DROP, CREATE ON `app`.* FROM `u`@`%`",
			},
			database: "app",
			want:     map[string]TableAccess{"orders": accRead, "users": accRead},
		},
		{
			name: "partial revoke on another database does not apply",
			lines: []string{
				"GRANT SELECT, INSERT ON *.* TO `u`@`%`",
				"REVOKE INSERT ON `other`.* FROM `u`@`%`",
			},
			database: "app",
			want:     map[string]TableAccess{"orders": {Select: true, Insert: true}, "users": {Select: true, Insert: true}},
		},
		{
			name:  "unquoted identifiers and trailing semicolon",
			lines: []string{"GRANT SELECT ON app.* TO u@'%';"}, database: "app",
			want: map[string]TableAccess{"orders": accRead, "users": accRead},
		},
		{
			name:  "no database selected gives no create",
			lines: []string{"GRANT ALL PRIVILEGES ON `app`.* TO `u`@`%`"}, database: "",
			want: map[string]TableAccess{"orders": accNone, "users": accNone},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbls := tables
			if len(tt.want) != len(tables) {
				tbls = nil
				for n := range tt.want {
					tbls = append(tbls, n)
				}
			}
			got := parseMySQLGrants(tt.lines, tt.database, tbls, !tt.noWildcards)
			if got.Superuser != tt.superuser {
				t.Errorf("Superuser = %v, want %v", got.Superuser, tt.superuser)
			}
			if got.CreateTables != tt.createTables {
				t.Errorf("CreateTables = %v, want %v", got.CreateTables, tt.createTables)
			}
			if !reflect.DeepEqual(got.Tables, tt.want) {
				t.Errorf("Tables = %+v, want %+v", got.Tables, tt.want)
			}
			if tt.noteContains == "" && len(got.Notes) != 0 {
				t.Errorf("unexpected notes: %v", got.Notes)
			}
			if tt.noteContains != "" && !strings.Contains(strings.Join(got.Notes, "\n"), tt.noteContains) {
				t.Errorf("notes %v do not mention %q", got.Notes, tt.noteContains)
			}
		})
	}
}

func TestMySQLGrantedRoles(t *testing.T) {
	lines := []string{
		"GRANT USAGE ON *.* TO `u`@`%`",
		"GRANT SELECT ON `app`.* TO `u`@`%`",
		"GRANT `rw`@`%`,`ro`@`localhost` TO `u`@`%` WITH ADMIN OPTION",
	}
	got := mysqlGrantedRoles(lines)
	want := []string{"`ro`@`localhost`", "`rw`@`%`"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("roles = %v, want %v", got, want)
	}
	if roles := mysqlGrantedRoles(lines[:2]); len(roles) != 0 {
		t.Fatalf("no role grants should give no roles, got %v", roles)
	}
}

func TestMySQLPatternMatch(t *testing.T) {
	tests := []struct {
		pattern, name string
		want          bool
	}{
		{"app", "app", true},
		{"app", "apps", false},
		{"app%", "app", true},
		{"app%", "application", true},
		{"%", "", true},
		{"a_p", "app", true},
		{"a_p", "ap", false},
		{`app\_db`, "app_db", true},
		{`app\_db`, "appXdb", false},
		{`100\%`, "100%", true},
		{`100\%`, "1000", false},
		{"a.b", "aXb", false}, // regexp metacharacters are literal
		{"über_", "überX", true},
	}
	for _, tt := range tests {
		if got := mysqlPatternMatch(tt.pattern, tt.name); got != tt.want {
			t.Errorf("mysqlPatternMatch(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
		}
	}
}

func TestAccess_JSONShapeIsCamelCase(t *testing.T) {
	acc := parseMySQLGrants([]string{"GRANT SELECT ON `app`.* TO `u`@`%`"}, "app", []string{"users"}, true)
	raw, err := json.Marshal(acc)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"user":"","database":"app","superuser":false,"createTables":false,"tables":{"users":{"select":true,"insert":false,"update":false,"delete":false,"truncate":false}},"notes":[]}`
	if string(raw) != want {
		t.Fatalf("json = %s\nwant %s", raw, want)
	}
}

func TestInspectAccess_RejectsUnknownDBType(t *testing.T) {
	if _, err := InspectAccess(context.Background(), nil, "sqlite"); err == nil {
		t.Fatal("expected an error for an unsupported database type")
	}
}
