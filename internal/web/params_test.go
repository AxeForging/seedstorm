package web

import (
	"strings"
	"testing"
)

func TestNormalizeParams_dropsBlankNamesAndTrims(t *testing.T) {
	got := normalizeParams([]Param{
		{Name: "  tls ", Value: " skip-verify "},
		{Name: "   ", Value: "orphan"},
		{Name: "", Value: ""},
		{Name: "timeout", Value: "10s"},
	})
	if len(got) != 2 {
		t.Fatalf("params = %+v, want 2 entries", got)
	}
	if got[0] != (Param{Name: "tls", Value: "skip-verify"}) {
		t.Fatalf("first param = %+v", got[0])
	}
	if got[1] != (Param{Name: "timeout", Value: "10s"}) {
		t.Fatalf("second param = %+v", got[1])
	}
}

func TestValidateParamNames(t *testing.T) {
	cases := []struct {
		name    string
		param   string
		wantErr bool
	}{
		{"plain", "tls", false},
		{"camel", "allowCleartextPasswords", false},
		{"underscored", "foreign_key_checks", false},
		{"dotted", "session.timeout", false},
		{"leading digit", "1tls", true},
		{"embedded equals", "tls=true", true},
		{"embedded ampersand", "tls&x", true},
		{"embedded space", "tls mode", true},
		{"empty", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateParamNames([]Param{{Name: c.param, Value: "v"}})
			if c.wantErr && err == nil {
				t.Fatalf("param %q: expected error", c.param)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("param %q: unexpected error: %v", c.param, err)
			}
		})
	}
}

func TestValidateParams_mysql(t *testing.T) {
	cases := []struct {
		name        string
		param       Param
		wantIssues  int
		wantLevel   string
		wantSuggest *Param
		wantContain string
	}{
		{
			name:       "known driver option is silent",
			param:      Param{Name: "allowCleartextPasswords", Value: "1"},
			wantIssues: 0,
		},
		{
			name:        "jdbc public key retrieval is an error with no replacement",
			param:       Param{Name: "allowPublicKeyRetrieval", Value: "true"},
			wantIssues:  1,
			wantLevel:   "error",
			wantContain: "Remove it",
		},
		{
			name:        "useSSL=false maps to tls=false",
			param:       Param{Name: "useSSL", Value: "false"},
			wantIssues:  1,
			wantLevel:   "error",
			wantSuggest: &Param{Name: "tls", Value: "false"},
		},
		{
			name:        "useSSL=true maps to tls=true",
			param:       Param{Name: "useSSL", Value: "true"},
			wantIssues:  1,
			wantLevel:   "error",
			wantSuggest: &Param{Name: "tls", Value: "true"},
		},
		{
			name:        "serverTimezone keeps its value under the Go name",
			param:       Param{Name: "serverTimezone", Value: "UTC"},
			wantIssues:  1,
			wantLevel:   "error",
			wantSuggest: &Param{Name: "loc", Value: "UTC"},
		},
		{
			name:        "unknown name is a warning about SET",
			param:       Param{Name: "foreign_key_checks", Value: "0"},
			wantIssues:  1,
			wantLevel:   "warn",
			wantContain: "SET foreign_key_checks = 0",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			issues := validateParams("mysql", []Param{c.param})
			if len(issues) != c.wantIssues {
				t.Fatalf("issues = %+v, want %d", issues, c.wantIssues)
			}
			if c.wantIssues == 0 {
				return
			}
			if issues[0].Level != c.wantLevel {
				t.Fatalf("level = %q, want %q", issues[0].Level, c.wantLevel)
			}
			if c.wantSuggest != nil {
				if issues[0].Suggest == nil || *issues[0].Suggest != *c.wantSuggest {
					t.Fatalf("suggest = %+v, want %+v", issues[0].Suggest, c.wantSuggest)
				}
			}
			if c.wantContain != "" && !strings.Contains(issues[0].Message, c.wantContain) {
				t.Fatalf("message %q missing %q", issues[0].Message, c.wantContain)
			}
		})
	}
}

func TestValidateParams_postgres(t *testing.T) {
	if issues := validateParams("postgres", []Param{{Name: "connect_timeout", Value: "5"}}); len(issues) != 0 {
		t.Fatalf("known param flagged: %+v", issues)
	}
	issues := validateParams("postgres", []Param{{Name: "search_path", Value: "app"}})
	if len(issues) != 1 || issues[0].Level != "warn" {
		t.Fatalf("GUC should warn, got %+v", issues)
	}
	if !strings.Contains(issues[0].Message, "runtime parameter") {
		t.Fatalf("message %q should explain runtime parameters", issues[0].Message)
	}
	issues = validateParams("postgres", []Param{{Name: "currentSchema", Value: "app"}})
	if len(issues) != 1 || issues[0].Level != "error" {
		t.Fatalf("JDBC param should error, got %+v", issues)
	}
	if issues[0].Suggest == nil || *issues[0].Suggest != (Param{Name: "search_path", Value: "app"}) {
		t.Fatalf("suggest = %+v, want search_path=app", issues[0].Suggest)
	}
	issues = validateParams("postgres", []Param{{Name: "allowPublicKeyRetrieval", Value: "true"}})
	if len(issues) != 1 || issues[0].Level != "error" {
		t.Fatalf("MySQL-ism should error on postgres, got %+v", issues)
	}
}

// The driver error strings below are copied verbatim from
// go-sql-driver/mysql errors.go and from MySQL/Postgres server responses. If a
// dependency bump changes them, this test is the tripwire.
func TestParamHintFromError(t *testing.T) {
	cases := []struct {
		name       string
		dbType     string
		msg        string
		wantAdd    *Param
		wantRemove string
		wantNil    bool
	}{
		{
			name:    "cleartext password plugin",
			dbType:  "mysql",
			msg:     "this user requires clear text authentication. If you still want to use it, please add 'allowCleartextPasswords=1' to your DSN",
			wantAdd: &Param{Name: "allowCleartextPasswords", Value: "1"},
		},
		{
			name:    "old password plugin",
			dbType:  "mysql",
			msg:     "this user requires old password authentication. If you still want to use it, please add 'allowOldPasswords=1' to your DSN. See also https://github.com/go-sql-driver/mysql/wiki/old_passwords",
			wantAdd: &Param{Name: "allowOldPasswords", Value: "1"},
		},
		{
			name:       "unknown system variable comes from a bogus param",
			dbType:     "mysql",
			msg:        "Error 1193 (HY000): Unknown system variable 'allowPublicKeyRetrieval'",
			wantRemove: "allowPublicKeyRetrieval",
		},
		{
			name:       "unknown postgres GUC",
			dbType:     "postgres",
			msg:        `ERROR: unrecognized configuration parameter "currentSchema" (SQLSTATE 42704)`,
			wantRemove: "currentSchema",
		},
		{
			name:    "mysql server without TLS",
			dbType:  "mysql",
			msg:     "TLS requested but server does not support TLS",
			wantAdd: &Param{Name: "allowFallbackToPlaintext", Value: "true"},
		},
		{
			name:    "postgres server without TLS",
			dbType:  "postgres",
			msg:     "server refused TLS connection",
			wantAdd: &Param{Name: "sslmode", Value: "disable"},
		},
		{
			name:    "access denied carries no parameter advice",
			dbType:  "mysql",
			msg:     "Error 1045 (28000): Access denied for user 'app'@'172.17.0.1' (using password: YES)",
			wantNil: true,
		},
		{
			name:    "connection refused carries no parameter advice",
			dbType:  "postgres",
			msg:     "failed to connect to `host=127.0.0.1 user=app database=app`: dial error: connection refused",
			wantNil: true,
		},
		{
			name:    "empty message",
			dbType:  "mysql",
			msg:     "   ",
			wantNil: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hint := paramHintFromError(c.dbType, c.msg)
			if c.wantNil {
				if hint != nil {
					t.Fatalf("hint = %+v, want nil", hint)
				}
				return
			}
			if hint == nil {
				t.Fatal("hint = nil, want a suggestion")
			}
			if c.wantAdd != nil {
				if hint.Add == nil || *hint.Add != *c.wantAdd {
					t.Fatalf("add = %+v, want %+v", hint.Add, c.wantAdd)
				}
			}
			if c.wantRemove != "" && hint.Remove != c.wantRemove {
				t.Fatalf("remove = %q, want %q", hint.Remove, c.wantRemove)
			}
			if hint.Note == "" {
				t.Fatal("hint has no note to show the user")
			}
		})
	}
}

func TestParamCatalog(t *testing.T) {
	mysql := paramCatalog("mysql")
	if mysql.Driver != "mysql" {
		t.Fatalf("driver = %q", mysql.Driver)
	}
	if !contains(mysql.Known, "allowCleartextPasswords") || !contains(mysql.Known, "tls") {
		t.Fatalf("mysql catalog missing driver options: %v", mysql.Known)
	}
	if contains(mysql.Known, "sslmode") {
		t.Fatal("mysql catalog should not offer the postgres sslmode option")
	}
	if _, ok := mysql.Translations["allowpublickeyretrieval"]; !ok {
		t.Fatal("mysql catalog should translate allowPublicKeyRetrieval")
	}
	if len(mysql.Suggestions) == 0 || mysql.UnknownNote == "" {
		t.Fatal("mysql catalog should carry suggestions and an explanation")
	}

	pg := paramCatalog("postgres")
	if !contains(pg.Known, "sslmode") || contains(pg.Known, "allowCleartextPasswords") {
		t.Fatalf("postgres catalog is wrong: %v", pg.Known)
	}
	// An unspecified driver is treated as postgres, matching normalizeDriver.
	if paramCatalog("").Driver != "postgres" {
		t.Fatal("empty driver should default to postgres")
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
