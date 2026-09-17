package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

// ReadLimits bound a read seedstorm runs against a database it must not
// disturb: the server enforces both timeouts.
type ReadLimits struct {
	// StatementTimeout ends a statement that runs longer (0: no limit).
	StatementTimeout time.Duration
	// LockTimeout ends a statement waiting this long for a lock, so a read
	// never queues behind DDL and holds up every writer queued behind it
	// (0: wait as the server does).
	LockTimeout time.Duration
	// Concurrency is how many statements run at once (<= 1: one).
	Concurrency int
}

// DefaultCountLimits are the limits of row counts: no statement timeout (a
// long exact count is what the user asked for) but never wait behind a lock.
var DefaultCountLimits = ReadLimits{LockTimeout: 2 * time.Second}

// Querier is what a read runs statements on.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ReadOnce runs fn in its own short read-only transaction: the server refuses
// any write, the transaction ends as soon as fn returns (a long-lived snapshot
// would hold back vacuum or purge), and the limits apply on the server. On
// MySQL a cancelled ctx also kills the query on the server: the driver only
// closes its connection, and the server would keep running it.
func ReadOnce(ctx context.Context, conn *sql.DB, dbType string, lim ReadLimits, fn func(ctx context.Context, q Querier) error) error {
	if dbType == "mysql" {
		return readOnceMySQL(ctx, conn, lim, fn)
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // after Commit this is a no-op
	if lim.StatementTimeout > 0 {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", lim.StatementTimeout.Milliseconds())); err != nil {
			return err
		}
	}
	if lim.LockTimeout > 0 {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL lock_timeout = %d", lim.LockTimeout.Milliseconds())); err != nil {
			return err
		}
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func readOnceMySQL(ctx context.Context, conn *sql.DB, lim ReadLimits, fn func(ctx context.Context, q Querier) error) error {
	c, err := conn.Conn(ctx)
	if err != nil {
		return err
	}
	var id int64
	if err := c.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		_ = c.Close()
		return err
	}
	// Session limits are reset before the connection returns to the pool, or
	// the connection is discarded: later statements (seeding's own scans) must
	// never inherit them.
	limited := lim.StatementTimeout > 0 || lim.LockTimeout > 0
	defer func() {
		if limited {
			reset, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, rerr := c.ExecContext(reset, "SET SESSION max_execution_time = DEFAULT, lock_wait_timeout = DEFAULT")
			cancel()
			if rerr != nil {
				_ = c.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
		_ = c.Close()
	}()
	if lim.StatementTimeout > 0 {
		if _, err := c.ExecContext(ctx, fmt.Sprintf("SET SESSION max_execution_time = %d", lim.StatementTimeout.Milliseconds())); err != nil {
			return err
		}
	}
	if lim.LockTimeout > 0 {
		secs := max(1, int64((lim.LockTimeout+time.Second-1)/time.Second))
		if _, err := c.ExecContext(ctx, fmt.Sprintf("SET SESSION lock_wait_timeout = %d, innodb_lock_wait_timeout = %d", secs, secs)); err != nil {
			return err
		}
	}

	finished := make(chan struct{})
	var killer sync.WaitGroup
	killer.Add(1)
	go func() {
		defer killer.Done()
		select {
		case <-finished:
		case <-ctx.Done():
			kill, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = conn.ExecContext(kill, fmt.Sprintf("KILL QUERY %d", id))
		}
	}()
	defer func() {
		close(finished)
		killer.Wait()
	}()

	tx, err := c.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // after Commit this is a no-op
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ReadOutcome is how a bounded read ended.
type ReadOutcome string

const (
	OutcomeOK        ReadOutcome = "ok"
	OutcomeTimedOut  ReadOutcome = "timed out"
	OutcomeLocked    ReadOutcome = "locked"
	OutcomeCancelled ReadOutcome = "cancelled"
	OutcomeFailed    ReadOutcome = "failed"
)

// ReadOutcomeOf classifies a read's error. ctx is the read's context: a
// Postgres statement cancel is a timeout unless ctx was cancelled.
func ReadOutcomeOf(ctx context.Context, err error) ReadOutcome {
	if err == nil {
		return OutcomeOK
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return OutcomeCancelled
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "57014": // query_canceled: statement_timeout, or a user cancel
			if ctx.Err() != nil {
				return OutcomeCancelled
			}
			return OutcomeTimedOut
		case "55P03": // lock_not_available: lock_timeout
			return OutcomeLocked
		case "40001":
			// A hot standby cancels reads that conflict with replication.
			if strings.Contains(pgErr.Message, "conflict with recovery") {
				return OutcomeCancelled
			}
		}
	}
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) {
		switch myErr.Number {
		case 3024: // query execution was interrupted, maximum statement execution time exceeded
			return OutcomeTimedOut
		case 1317: // query execution was interrupted (KILL QUERY)
			return OutcomeCancelled
		case 1205: // lock wait timeout exceeded (metadata or row locks)
			return OutcomeLocked
		}
	}
	if ctx.Err() != nil {
		return OutcomeCancelled
	}
	return OutcomeFailed
}
