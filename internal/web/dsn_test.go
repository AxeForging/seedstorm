package web

import (
	"net/url"
	"strings"
	"testing"
)

func TestBuildDSN_postgres(t *testing.T) {
	driver, dsn, err := buildDSN(ConnectionInfo{
		DBType: "postgres",
		Host:   "db",
		Port:   5433,
		DBName: "app",
		User:   "u",
		SSL:    "require",
	}, "p@ss/word")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if driver != "pgx" {
		t.Fatalf("driver = %q, want pgx", driver)
	}
	if !strings.HasPrefix(dsn, "postgres://u:") {
		t.Fatalf("missing user/scheme: %q", dsn)
	}
	if !strings.Contains(dsn, "@db:5433/app") {
		t.Fatalf("missing host/port/db: %q", dsn)
	}
	if !strings.Contains(dsn, "sslmode=require") {
		t.Fatalf("missing sslmode: %q", dsn)
	}
	// Ensure password special chars are URL-escaped (not present raw).
	if strings.Contains(dsn, "p@ss/word") {
		t.Fatalf("password not escaped: %q", dsn)
	}
}

func TestBuildDSN_postgresDefaults(t *testing.T) {
	_, dsn, err := buildDSN(ConnectionInfo{
		DBType: "postgres",
		DBName: "x",
		User:   "u",
	}, "p")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(dsn, "@localhost:5432/x") {
		t.Fatalf("host/port defaults missing: %q", dsn)
	}
	if !strings.Contains(dsn, "sslmode=disable") {
		t.Fatalf("sslmode default missing: %q", dsn)
	}
}

func TestBuildDSN_mysql(t *testing.T) {
	driver, dsn, err := buildDSN(ConnectionInfo{
		DBType: "mysql",
		Host:   "127.0.0.1",
		Port:   3306,
		DBName: "app",
		User:   "root",
	}, "secret")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if driver != "mysql" {
		t.Fatalf("driver = %q, want mysql", driver)
	}
	if !strings.HasPrefix(dsn, "root:secret@tcp(127.0.0.1:3306)/app") {
		t.Fatalf("dsn = %q", dsn)
	}
	if !strings.Contains(dsn, "parseTime=true") {
		t.Fatalf("missing parseTime: %q", dsn)
	}
}

func TestBuildDSN_unsupported(t *testing.T) {
	_, _, err := buildDSN(ConnectionInfo{DBType: "sqlite"}, "")
	if err == nil {
		t.Fatal("expected error for unsupported db type")
	}
}

func TestBuildRawDSN_postgresDisplayInfo(t *testing.T) {
	driver, dsn, info, err := buildRawDSN("postgres", "postgres://alice:secret@db.example:5439/targetdb?sslmode=require", nil)
	if err != nil {
		t.Fatalf("buildRawDSN: %v", err)
	}
	if driver != "pgx" {
		t.Fatalf("driver = %q, want pgx", driver)
	}
	if dsn != "postgres://alice:secret@db.example:5439/targetdb?sslmode=require" {
		t.Fatalf("dsn changed: %q", dsn)
	}
	if info.DBName != "targetdb" || info.User != "alice" || info.Host != "db.example" || info.Port != 5439 || info.SSL != "require" {
		t.Fatalf("display info = %+v", info)
	}
}

func TestBuildRawDSN_mysqlDisplayInfoAndParams(t *testing.T) {
	driver, dsn, info, err := buildRawDSN("mysql", "bob:secret@tcp(mysql.example:3310)/targetdb", nil)
	if err != nil {
		t.Fatalf("buildRawDSN: %v", err)
	}
	if driver != "mysql" {
		t.Fatalf("driver = %q, want mysql", driver)
	}
	if !strings.Contains(dsn, "parseTime=true") || !strings.Contains(dsn, "multiStatements=true") {
		t.Fatalf("mysql params not added: %q", dsn)
	}
	if info.DBName != "targetdb" || info.User != "bob" || info.Host != "mysql.example" || info.Port != 3310 {
		t.Fatalf("display info = %+v", info)
	}
}

// ── extra driver parameters ────────────────────────────────────────────────

func TestBuildDSN_postgresExtraParams(t *testing.T) {
	_, dsn, err := buildDSN(ConnectionInfo{
		DBType: "postgres",
		Host:   "db",
		Port:   5432,
		DBName: "app",
		User:   "u",
		SSL:    "disable",
		Params: []Param{
			{Name: "connect_timeout", Value: "5"},
			{Name: "application_name", Value: "seedstorm run"},
		},
	}, "pw")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn %q: %v", dsn, err)
	}
	q := u.Query()
	if q.Get("connect_timeout") != "5" {
		t.Fatalf("connect_timeout = %q in %q", q.Get("connect_timeout"), dsn)
	}
	if q.Get("application_name") != "seedstorm run" {
		t.Fatalf("application_name did not survive encoding: %q", q.Get("application_name"))
	}
	if q.Get("sslmode") != "disable" {
		t.Fatalf("default sslmode lost: %q", dsn)
	}
}

func TestBuildDSN_postgresExtraParamOverridesDefault(t *testing.T) {
	_, dsn, err := buildDSN(ConnectionInfo{
		DBType: "postgres",
		DBName: "app",
		User:   "u",
		SSL:    "disable",
		Params: []Param{{Name: "sslmode", Value: "require"}},
	}, "pw")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	u, _ := url.Parse(dsn)
	if got := u.Query()["sslmode"]; len(got) != 1 || got[0] != "require" {
		t.Fatalf("sslmode = %v, want exactly [require] in %q", got, dsn)
	}
}

func TestBuildDSN_mysqlExtraParams(t *testing.T) {
	_, dsn, err := buildDSN(ConnectionInfo{
		DBType: "mysql",
		Host:   "127.0.0.1",
		Port:   3306,
		DBName: "app",
		User:   "u",
		Params: []Param{
			{Name: "allowCleartextPasswords", Value: "1"},
			{Name: "charset", Value: "utf8mb4,utf8"},
		},
	}, "pw")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	q := mysqlQueryOf(t, dsn)
	if q["allowCleartextPasswords"] != "1" {
		t.Fatalf("param missing: %q", dsn)
	}
	// The driver reads charset verbatim, so percent-encoding it would break it.
	if q["charset"] != "utf8mb4,utf8" {
		t.Fatalf("charset was mangled: %q", q["charset"])
	}
	if q["parseTime"] != "true" || q["multiStatements"] != "true" {
		t.Fatalf("defaults lost: %q", dsn)
	}
}

func TestBuildDSN_mysqlExtraParamOverridesDefault(t *testing.T) {
	_, dsn, err := buildDSN(ConnectionInfo{
		DBType: "mysql",
		DBName: "app",
		User:   "u",
		Params: []Param{{Name: "parseTime", Value: "false"}},
	}, "pw")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strings.Count(dsn, "parseTime=") != 1 {
		t.Fatalf("parseTime appears more than once: %q", dsn)
	}
	if q := mysqlQueryOf(t, dsn); q["parseTime"] != "false" {
		t.Fatalf("parseTime = %q, want false", q["parseTime"])
	}
}

func TestBuildDSN_paramValueWithAmpersandIsEscaped(t *testing.T) {
	_, dsn, err := buildDSN(ConnectionInfo{
		DBType: "mysql",
		DBName: "app",
		User:   "u",
		Params: []Param{{Name: "connectionAttributes", Value: "a=1&b=2"}},
	}, "pw")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	q := mysqlQueryOf(t, dsn)
	if q["connectionAttributes"] != "a=1%262" && q["connectionAttributes"] != "a=1%26b=2" {
		t.Fatalf("ampersand not escaped, value = %q (dsn %q)", q["connectionAttributes"], dsn)
	}
	if _, ok := q["b"]; ok {
		t.Fatalf("value leaked into a separate parameter: %q", dsn)
	}
}

func TestBuildDSN_rejectsInvalidParamName(t *testing.T) {
	_, _, err := buildDSN(ConnectionInfo{
		DBType: "mysql",
		DBName: "app",
		User:   "u",
		Params: []Param{{Name: "tls=true&evil", Value: "x"}},
	}, "pw")
	if err == nil {
		t.Fatal("expected an error for a parameter name that would corrupt the DSN")
	}
}

func TestBuildDSN_blankParamRowsAreIgnored(t *testing.T) {
	_, dsn, err := buildDSN(ConnectionInfo{
		DBType: "mysql",
		DBName: "app",
		User:   "u",
		Params: []Param{{Name: "", Value: "orphan"}, {Name: "  ", Value: ""}},
	}, "pw")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strings.Contains(dsn, "=orphan") || strings.Contains(dsn, "&&") || strings.HasSuffix(dsn, "&") {
		t.Fatalf("blank rows leaked into the dsn: %q", dsn)
	}
}

func TestBuildRawDSN_mysqlMergesExtrasOverExisting(t *testing.T) {
	_, dsn, info, err := buildRawDSN(
		"mysql",
		"bob:secret@tcp(mysql.example:3310)/targetdb?tls=false&timeout=1s",
		[]Param{{Name: "tls", Value: "skip-verify"}, {Name: "readTimeout", Value: "30s"}},
	)
	if err != nil {
		t.Fatalf("buildRawDSN: %v", err)
	}
	q := mysqlQueryOf(t, dsn)
	if q["tls"] != "skip-verify" {
		t.Fatalf("extra param did not override the raw string: %q", dsn)
	}
	if strings.Count(dsn, "tls=") != 1 {
		t.Fatalf("tls appears twice: %q", dsn)
	}
	if q["timeout"] != "1s" {
		t.Fatalf("existing param dropped: %q", dsn)
	}
	if q["readTimeout"] != "30s" {
		t.Fatalf("new param missing: %q", dsn)
	}
	if q["parseTime"] != "true" || q["multiStatements"] != "true" {
		t.Fatalf("defaults lost: %q", dsn)
	}
	if len(info.Params) != 2 {
		t.Fatalf("info should carry the extras, got %+v", info.Params)
	}
}

func TestBuildRawDSN_postgresMergesExtras(t *testing.T) {
	_, dsn, info, err := buildRawDSN(
		"postgres",
		"postgres://alice:secret@db.example:5439/targetdb?sslmode=require",
		[]Param{{Name: "sslmode", Value: "disable"}, {Name: "application_name", Value: "seedstorm"}},
	)
	if err != nil {
		t.Fatalf("buildRawDSN: %v", err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if got := q["sslmode"]; len(got) != 1 || got[0] != "disable" {
		t.Fatalf("sslmode = %v, want [disable]: %q", got, dsn)
	}
	if q.Get("application_name") != "seedstorm" {
		t.Fatalf("application_name missing: %q", dsn)
	}
	if info.SSL != "disable" {
		t.Fatalf("display info should reflect the merged sslmode, got %q", info.SSL)
	}
	if pw, _ := u.User.Password(); pw != "secret" {
		t.Fatalf("credentials lost while merging: %q", dsn)
	}
}

// A password containing '?' must not be mistaken for the start of the query
// string: go-sql-driver looks for the first '?' after the last '/'.
func TestSplitMySQLDSN_passwordWithQuestionMark(t *testing.T) {
	base, query := splitMySQLDSN("u:pa?ss@tcp(h:3306)/db?tls=true")
	if base != "u:pa?ss@tcp(h:3306)/db" {
		t.Fatalf("base = %q", base)
	}
	if query != "tls=true" {
		t.Fatalf("query = %q", query)
	}
}

func TestSplitMySQLDSN_noQuery(t *testing.T) {
	base, query := splitMySQLDSN("u:p@tcp(h:3306)/db")
	if base != "u:p@tcp(h:3306)/db" || query != "" {
		t.Fatalf("base = %q query = %q", base, query)
	}
}

// mysqlQueryOf parses the parameters back out of a MySQL DSN the same way the
// driver does, so assertions test behaviour rather than string formatting.
func mysqlQueryOf(t *testing.T, dsn string) map[string]string {
	t.Helper()
	_, query := splitMySQLDSN(dsn)
	out := map[string]string{}
	for _, p := range parseMySQLQuery(query) {
		out[p.Name] = p.Value
	}
	return out
}
