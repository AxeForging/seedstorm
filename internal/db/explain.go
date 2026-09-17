package db

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

// Explain adds what a database failure means when the driver's text does not
// say it plainly: a full disk, or a connection the database dropped. Other
// errors are returned unchanged.
func Explain(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case isDiskFull(err):
		return fmt.Errorf("%w (the database's disk is full: free space or grow the disk, then fill the remaining tables)", err)
	case isConnectionLost(err):
		return fmt.Errorf("%w (the database closed the connection: it may have restarted, crashed, or run out of disk for its logs)", err)
	}
	return err
}

func isDiskFull(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "53100" || pgErr.Code == "53000") {
		return true
	}
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) && (myErr.Number == 1114 || myErr.Number == 1021) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "no space left on device")
}

func isConnectionLost(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (strings.HasPrefix(pgErr.Code, "08") || pgErr.Code == "57P01" || pgErr.Code == "57P02" || pgErr.Code == "57P03") {
		// connection exceptions; admin shutdown, crash shutdown, cannot connect now
		return true
	}
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, mysql.ErrInvalidConn) {
		return true
	}
	low := strings.ToLower(err.Error())
	for _, s := range []string{"connection reset by peer", "broken pipe", "unexpected eof", "server closed the connection", "terminating connection", "conn closed", "failed to receive message"} {
		if strings.Contains(low, s) {
			return true
		}
	}
	return false
}
