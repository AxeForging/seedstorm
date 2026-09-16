package db

import (
	"errors"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

// IsTransient reports errors a concurrent writer may simply retry: the database
// chose the statement as a deadlock victim or gave up waiting for a lock. The
// rows were not written, and the same statement can succeed on a second try.
func IsTransient(err error) bool {
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) {
		// 1213 deadlock found, 1205 lock wait timeout exceeded.
		return myErr.Number == 1213 || myErr.Number == 1205
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// 40P01 deadlock_detected, 40001 serialization_failure.
		return pgErr.Code == "40P01" || pgErr.Code == "40001"
	}
	return false
}
