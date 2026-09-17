//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/db"
)

// slowQuery is a read that runs for many seconds on either engine without
// sleeping (MySQL's SLEEP swallows interrupts instead of failing).
func slowQuery(driver string) string {
	if driver == postgresDriver {
		return `SELECT COUNT(*) FROM generate_series(1, 400000000)`
	}
	return `SELECT COUNT(*) FROM information_schema.COLUMNS a, information_schema.COLUMNS b, information_schema.COLUMNS c`
}

// Every read seedstorm runs against a database it only reads is refused by the
// server if it tries to write, whatever the code does.
func TestReadScope_ServerRefusesWrites(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			_, conn := e.scratchDB(t, "ss_readscope_ro")
			execSQL(t, conn, `CREATE TABLE notes (id INT PRIMARY KEY)`)
			err := db.ReadOnce(context.Background(), conn, e.driver, db.ReadLimits{}, func(ctx context.Context, q db.Querier) error {
				_, err := q.ExecContext(ctx, `INSERT INTO notes (id) VALUES (1)`)
				return err
			})
			if err == nil {
				t.Fatal("an INSERT inside a read scope succeeded")
			}
			if n := countRows(t, conn, "notes"); n != 0 {
				t.Fatalf("notes has %d rows", n)
			}
		})
	}
}

// A statement timeout is enforced by the server and reported as timed out;
// the connection is usable afterwards and the limit does not leak into it.
func TestReadScope_StatementTimeoutIsServerSide(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			_, conn := e.scratchDB(t, "ss_readscope_timeout")
			conn.SetMaxOpenConns(1) // the next read gets the same server session
			start := time.Now()
			err := db.ReadOnce(context.Background(), conn, e.driver, db.ReadLimits{StatementTimeout: 500 * time.Millisecond}, func(ctx context.Context, q db.Querier) error {
				var n int64
				return q.QueryRowContext(ctx, slowQuery(e.driver)).Scan(&n)
			})
			if got := db.ReadOutcomeOf(context.Background(), err); got != db.OutcomeTimedOut {
				t.Fatalf("outcome = %s (err %v), want timed out", got, err)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Fatalf("timed-out read took %s", d)
			}
			// The same session runs an ordinary read without inheriting the limit.
			var one int
			if err := conn.QueryRowContext(context.Background(), `SELECT 1`).Scan(&one); err != nil || one != 1 {
				t.Fatalf("connection unusable after a timeout: %v", err)
			}
			if e.driver == mysqlDriver {
				var v int64
				if err := conn.QueryRowContext(context.Background(), `SELECT @@SESSION.max_execution_time`).Scan(&v); err != nil || v != 0 {
					t.Fatalf("max_execution_time leaked into the pool: %d (%v)", v, err)
				}
			}
		})
	}
}

// A read waiting behind a table lock (an ALTER, a LOCK TABLE) gives up after
// the lock timeout instead of queueing and holding up every writer behind it.
func TestReadScope_LockTimeoutStopsWaitingBehindALock(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			_, conn := e.scratchDB(t, "ss_readscope_lock")
			execSQL(t, conn, `CREATE TABLE ledger (id INT PRIMARY KEY)`)
			holder, err := conn.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Close()
			lock := `LOCK TABLES ledger WRITE`
			unlock := `UNLOCK TABLES`
			if e.driver == postgresDriver {
				if _, err := holder.ExecContext(context.Background(), `BEGIN`); err != nil {
					t.Fatal(err)
				}
				lock, unlock = `LOCK TABLE ledger IN ACCESS EXCLUSIVE MODE`, `ROLLBACK`
			}
			if _, err := holder.ExecContext(context.Background(), lock); err != nil {
				t.Fatal(err)
			}
			defer holder.ExecContext(context.Background(), unlock) //nolint:errcheck

			start := time.Now()
			counts, failed := db.CountTablesWithin(context.Background(), conn, e.driver, []string{"ledger"}, db.ReadLimits{LockTimeout: time.Second}, nil)
			if _, ok := counts["ledger"]; ok {
				t.Fatal("ledger counted while an exclusive lock was held")
			}
			if got := db.ReadOutcomeOf(context.Background(), failed["ledger"]); got != db.OutcomeLocked {
				t.Fatalf("outcome = %s (%v), want locked", got, failed["ledger"])
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Fatalf("locked read waited %s", d)
			}
		})
	}
}

// Cancelling a MySQL read must stop the query on the server. The driver only
// closes its socket, and the server keeps running the query: this test first
// shows that, then that ReadOnce kills it.
func TestReadScope_MySQLCancelKillsTheServerQuery(t *testing.T) {
	e := mysqlEngine()
	_, conn := e.scratchDB(t, "ss_readscope_kill")
	running := func(marker string) bool {
		var n int
		_ = conn.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE INFO LIKE ? AND INFO NOT LIKE '%PROCESSLIST%'`, "%"+marker+"%").Scan(&n)
		return n > 0
	}
	query := func(marker string) string {
		return "SELECT /* " + marker + " */ " + strings.TrimPrefix(slowQuery(mysqlDriver), "SELECT ")
	}
	waitRunning := func(marker string) {
		deadline := time.Now().Add(5 * time.Second)
		for !running(marker) {
			if time.Now().After(deadline) {
				t.Fatalf("query %s never started", marker)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	t.Run("driver alone leaves the query running", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- conn.QueryRowContext(ctx, query("ss_plain_cancel")).Scan(new(int64)) }()
		waitRunning("ss_plain_cancel")
		cancel()
		<-done
		time.Sleep(500 * time.Millisecond)
		if !running("ss_plain_cancel") {
			t.Skip("this server stopped the query on disconnect; the KILL below is still required elsewhere")
		}
		killMarker(t, conn, "ss_plain_cancel")
	})

	t.Run("ReadOnce kills it", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- db.ReadOnce(ctx, conn, mysqlDriver, db.ReadLimits{}, func(ctx context.Context, q db.Querier) error {
				return q.QueryRowContext(ctx, query("ss_scope_cancel")).Scan(new(int64))
			})
		}()
		waitRunning("ss_scope_cancel")
		cancel()
		err := <-done
		if got := db.ReadOutcomeOf(ctx, err); got != db.OutcomeCancelled {
			t.Fatalf("outcome = %s (%v), want cancelled", got, err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for running("ss_scope_cancel") {
			if time.Now().After(deadline) {
				killMarker(t, conn, "ss_scope_cancel")
				t.Fatal("the query still runs on the server 3s after cancelling")
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
}

func killMarker(t *testing.T, conn *sql.DB, marker string) {
	t.Helper()
	rows, err := conn.QueryContext(context.Background(), `SELECT ID FROM information_schema.PROCESSLIST WHERE INFO LIKE ? AND INFO NOT LIKE '%PROCESSLIST%'`, "%"+marker+"%")
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		_, _ = conn.ExecContext(context.Background(), `KILL QUERY `+strconv.FormatInt(id, 10))
	}
}
