package db

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsTransient_OnlyLockConflictsAreRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"mysql deadlock", &mysql.MySQLError{Number: 1213}, true},
		{"mysql lock wait timeout", &mysql.MySQLError{Number: 1205}, true},
		{"mysql wrapped deadlock", fmt.Errorf("insert into t: %w", &mysql.MySQLError{Number: 1213}), true},
		{"mysql duplicate key", &mysql.MySQLError{Number: 1062}, false},
		{"mysql fk violation", &mysql.MySQLError{Number: 1452}, false},
		{"pg deadlock", &pgconn.PgError{Code: "40P01"}, true},
		{"pg serialization failure", &pgconn.PgError{Code: "40001"}, true},
		{"pg unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"plain error", errors.New("deadlock"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsTransient(c.err); got != c.want {
				t.Fatalf("IsTransient(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
