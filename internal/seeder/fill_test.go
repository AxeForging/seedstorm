package seeder

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// scriptedDriver answers every query with no rows and decides each INSERT from
// a per-DSN script, so Fill's retry decisions can be tested deterministically.
type scriptedDriver struct{}

var (
	registerOnce sync.Once
	scriptsMu    sync.Mutex
	scripts      = map[string]*insertScript{}
)

type insertScript struct {
	mu      sync.Mutex
	inserts int
	decide  func(n int) error // n is the 1-based INSERT number
}

func (s *insertScript) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inserts
}

func openScripted(t *testing.T, decide func(n int) error) (*sql.DB, *insertScript) {
	t.Helper()
	registerOnce.Do(func() { sql.Register("seeder_scripted", scriptedDriver{}) })
	script := &insertScript{decide: decide}
	scriptsMu.Lock()
	scripts[t.Name()] = script
	scriptsMu.Unlock()
	conn, err := sql.Open("seeder_scripted", t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, script
}

func (scriptedDriver) Open(name string) (driver.Conn, error) {
	scriptsMu.Lock()
	defer scriptsMu.Unlock()
	return &scriptedConn{script: scripts[name]}, nil
}

type scriptedConn struct{ script *insertScript }

func (c *scriptedConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}
func (c *scriptedConn) Close() error              { return nil }
func (c *scriptedConn) Begin() (driver.Tx, error) { return nil, errors.New("not implemented") }

func (c *scriptedConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return emptyRows{}, nil
}

func (c *scriptedConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(query, "INSERT") {
		return driver.RowsAffected(0), nil
	}
	c.script.mu.Lock()
	c.script.inserts++
	n := c.script.inserts
	c.script.mu.Unlock()
	if err := c.script.decide(n); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

type emptyRows struct{}

func (emptyRows) Columns() []string         { return []string{"v"} }
func (emptyRows) Close() error              { return nil }
func (emptyRows) Next([]driver.Value) error { return io.EOF }

func notesSchema() *schema.Schema {
	return &schema.Schema{Tables: map[string]schema.Table{
		"notes": {Columns: map[string]schema.Column{
			"id":   {Type: "integer", PK: true},
			"body": {Type: "varchar", Faker: "word"},
		}},
	}}
}

var errCheck = errors.New("check constraint violated")

// A small final chunk can be rejected entirely by chance (3 rows at a 50%
// rejection rate fail together one time in eight). One empty round must not
// end the table: the regenerated rows get another chance.
func TestFill_OneFullyRejectedRoundIsRetriedNotAbandoned(t *testing.T) {
	// INSERT 1 is the 5-row batch, 2-6 its row-by-row retries: all refused.
	conn, _ := openScripted(t, func(n int) error {
		if n <= 6 {
			return errCheck
		}
		return nil
	})
	res, err := Fill(context.Background(), conn, "pgx", notesSchema(), []string{"notes"}, map[string]int{"notes": 5}, Options{BatchSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	got := res.Tables[0]
	if got.Status != StatusOK || got.Inserted != 5 || got.Rejected != 5 {
		t.Fatalf("result = %+v, want all 5 inserted after one rejected round", got)
	}
}

// With half the generated rows refused, the last remaining row is refused three
// rounds running one time in eight. Rounds are not the signal: only a long run
// of refused rows with nothing inserted means the table is stuck.
func TestFill_LastRowRefusedSeveralRoundsRunningStillFinishes(t *testing.T) {
	// Each round is one 1-row batch plus its row-by-row retry: INSERTs 1-6 are
	// three fully refused rounds.
	conn, _ := openScripted(t, func(n int) error {
		if n <= 6 {
			return errCheck
		}
		return nil
	})
	res, err := Fill(context.Background(), conn, "pgx", notesSchema(), []string{"notes"}, map[string]int{"notes": 1}, Options{BatchSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Tables[0]; got.Status != StatusOK || got.Inserted != 1 {
		t.Fatalf("result = %+v, want the row inserted once the database accepts one", got)
	}
}

func TestFill_ImpossibleTableGivesUpWithinABoundedNumberOfAttempts(t *testing.T) {
	conn, script := openScripted(t, func(int) error { return errCheck })
	res, err := Fill(context.Background(), conn, "pgx", notesSchema(), []string{"notes"}, map[string]int{"notes": 1000}, Options{BatchSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	got := res.Tables[0]
	if got.Status != StatusFailed || got.Missing != 1000 || !strings.Contains(got.Error, "check constraint") {
		t.Fatalf("result = %+v", got)
	}
	// One batch plus MaxRowFailures refused single rows is enough evidence.
	if attempts := script.count(); attempts > 1+DefaultMaxRowFailures {
		t.Fatalf("%d insert attempts against an impossible table", attempts)
	}
}

func TestFill_StopOnErrorReturnsAtFirstRejection(t *testing.T) {
	conn, script := openScripted(t, func(int) error { return errCheck })
	_, err := Fill(context.Background(), conn, "pgx", notesSchema(), []string{"notes"}, map[string]int{"notes": 10}, Options{StopOnError: true})
	if err == nil || script.count() != 1 {
		t.Fatalf("err = %v after %d inserts, want an error after exactly 1", err, script.count())
	}
}

func TestFill_CancelledContextStopsTheRun(t *testing.T) {
	conn, _ := openScripted(t, func(int) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := Fill(ctx, conn, "pgx", notesSchema(), []string{"notes"}, map[string]int{"notes": 10}, Options{})
	if !errors.Is(err, context.Canceled) || res.Tables[0].Missing != 10 {
		t.Fatalf("err = %v result = %+v", err, res)
	}
}
