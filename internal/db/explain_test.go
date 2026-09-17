package db

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

// A write that fails because the database's disk is full, or because the
// database dropped the connection (a crash, a restart, a full WAL disk), says
// so in words, keeping the original error.
func TestExplain_AddsWhatTheFailureMeans(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"postgres disk full", &pgconn.PgError{Code: "53100", Message: "could not extend file"}, "disk is full"},
		{"mysql table full", &mysql.MySQLError{Number: 1114, Message: "The table 'blobs' is full"}, "disk is full"},
		{"no space left", errors.New("write /var/lib/mysql/ibdata1: no space left on device"), "disk is full"},
		{"connection reset", fmt.Errorf("insert into blobs failed: %w", errors.New("read tcp 127.0.0.1:1->127.0.0.1:2: read: connection reset by peer")), "closed the connection"},
		{"bad conn", driver.ErrBadConn, "closed the connection"},
		{"postgres recovering after a crash", &pgconn.PgError{Code: "57P03", Message: "the database system is in recovery mode"}, "closed the connection"},
		{"eof", io.ErrUnexpectedEOF, "closed the connection"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Explain(c.err)
			if !strings.Contains(got.Error(), c.want) || !errors.Is(got, c.err) {
				t.Fatalf("Explain = %q (is original: %v), want it to mention %q", got, errors.Is(got, c.err), c.want)
			}
		})
	}
	plain := errors.New(`duplicate key value violates unique constraint "blobs_pkey"`)
	if Explain(plain) != plain || Explain(nil) != nil {
		t.Fatal("errors with nothing to add must pass through unchanged")
	}
}
