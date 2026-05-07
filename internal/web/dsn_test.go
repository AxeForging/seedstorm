package web

import (
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
