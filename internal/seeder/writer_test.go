package seeder

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// recordingDriver logs every INSERT with its table, first key and timing, and
// lets a test slow down or refuse inserts per table. It stands in for the
// database, the one external boundary of the writer.
type recordingDriver struct{}

var (
	recordingOnce sync.Once
	recordingMu   sync.Mutex
	recordings    = map[string]*recording{}
)

type insertEvent struct {
	table      string
	firstKey   interface{}
	start, end time.Time
}

type recording struct {
	mu     sync.Mutex
	events []insertEvent
	delay  map[string]time.Duration
	fail   func(table string, n int) error // n: 1-based insert number for the table
	perTab map[string]int
	active map[string]int
	// overlapSelf records a table that ever had two inserts in flight.
	overlapSelf map[string]bool
}

func openRecording(t *testing.T, delay map[string]time.Duration, fail func(table string, n int) error) (*sql.DB, *recording) {
	t.Helper()
	recordingOnce.Do(func() { sql.Register("seeder_recording", recordingDriver{}) })
	rec := &recording{delay: delay, fail: fail, perTab: map[string]int{}, active: map[string]int{}, overlapSelf: map[string]bool{}}
	recordingMu.Lock()
	recordings[t.Name()] = rec
	recordingMu.Unlock()
	conn, err := sql.Open("seeder_recording", t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, rec
}

func (recordingDriver) Open(name string) (driver.Conn, error) {
	recordingMu.Lock()
	defer recordingMu.Unlock()
	return &recordingConn{rec: recordings[name]}, nil
}

type recordingConn struct{ rec *recording }

func (c *recordingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not implemented")
}
func (c *recordingConn) Close() error              { return nil }
func (c *recordingConn) Begin() (driver.Tx, error) { return nil, errors.New("not implemented") }

func (c *recordingConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return emptyRows{}, nil
}

func (c *recordingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(query, "INSERT INTO ") {
		return driver.RowsAffected(0), nil
	}
	table := strings.Trim(strings.Fields(strings.TrimPrefix(query, "INSERT INTO "))[0], `"`+"`")
	r := c.rec
	r.mu.Lock()
	r.perTab[table]++
	n := r.perTab[table]
	r.active[table]++
	if r.active[table] > 1 {
		r.overlapSelf[table] = true
	}
	ev := insertEvent{table: table, start: time.Now()}
	if len(args) > 0 {
		ev.firstKey = args[0].Value
	}
	r.mu.Unlock()

	var err error
	select {
	case <-time.After(r.delay[table]):
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err == nil && r.fail != nil {
		err = r.fail(table, n)
	}
	r.mu.Lock()
	r.active[table]--
	ev.end = time.Now()
	if err == nil {
		r.events = append(r.events, ev)
	}
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func (r *recording) inserts(table string) []insertEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []insertEvent
	for _, e := range r.events {
		if e.table == table {
			out = append(out, e)
		}
	}
	return out
}

func lastEnd(events []insertEvent) time.Time {
	var t time.Time
	for _, e := range events {
		if e.end.After(t) {
			t = e.end
		}
	}
	return t
}

func firstStart(events []insertEvent) time.Time {
	t := events[0].start
	for _, e := range events {
		if e.start.Before(t) {
			t = e.start
		}
	}
	return t
}

// usersPostsAudit: posts references users (one NOT NULL, one nullable FK to
// reviewers); audit references nothing.
func usersPostsAudit() *schema.Schema {
	return &schema.Schema{Tables: map[string]schema.Table{
		"users":     {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}, "name": {Type: "varchar", Faker: "word"}}},
		"reviewers": {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}}},
		"audit":     {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}, "note": {Type: "varchar", Faker: "word"}}},
		"posts": {Columns: map[string]schema.Column{
			"id":          {Type: "integer", PK: true},
			"user_id":     {Type: "integer", FK: "users.id"},
			"reviewer_id": {Type: "integer", FK: "reviewers.id", Nullable: true},
		}},
	}}
}

// withDeadline fails the test instead of hanging when the writer deadlocks.
func withDeadline(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func TestSeed_ConcurrentWritesParentsBeforeChildrenAndUnrelatedTablesOverlap(t *testing.T) {
	conn, rec := openRecording(t, map[string]time.Duration{"users": 40 * time.Millisecond, "reviewers": 40 * time.Millisecond}, nil)
	// audit is generated right after users, while users' pieces are in flight.
	order := []string{"users", "audit", "reviewers", "posts"}
	res, err := Seed(withDeadline(t, 20*time.Second), conn, "mysql", usersPostsAudit(), order, order, SeedOptions{
		Rows: 3000, BatchSize: 500, ChunkRows: 1000, Workers: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 12000 || res.Counts["posts"] != 3000 || res.Counts["audit"] != 3000 {
		t.Fatalf("result = %+v", res)
	}
	users, reviewers, audit, posts := rec.inserts("users"), rec.inserts("reviewers"), rec.inserts("audit"), rec.inserts("posts")
	if len(posts) == 0 || len(users) == 0 || len(audit) == 0 || len(reviewers) == 0 {
		t.Fatalf("missing inserts: users=%d reviewers=%d audit=%d posts=%d", len(users), len(reviewers), len(audit), len(posts))
	}
	if firstStart(posts).Before(lastEnd(users)) {
		t.Fatalf("posts started writing at %v before users finished at %v", firstStart(posts), lastEnd(users))
	}
	// The nullable FK is waited for too: its values point at generated reviewers.
	if firstStart(posts).Before(lastEnd(reviewers)) {
		t.Fatalf("posts started writing before reviewers (nullable FK parent) finished")
	}
	if !firstStart(audit).Before(lastEnd(users)) {
		t.Fatalf("audit never overlapped users: writes were not concurrent")
	}
	if !rec.overlapSelf["users"] {
		t.Fatalf("pieces of users were never written concurrently")
	}
}

func TestSeed_SelfReferencingTableWritesChunksInOrderOneAtATime(t *testing.T) {
	sc := &schema.Schema{Tables: map[string]schema.Table{
		"categories": {Columns: map[string]schema.Column{
			"id":        {Type: "integer", PK: true},
			"parent_id": {Type: "integer", FK: "categories.id", Nullable: true},
		}},
	}}
	conn, rec := openRecording(t, map[string]time.Duration{"categories": 5 * time.Millisecond}, nil)
	res, err := Seed(withDeadline(t, 20*time.Second), conn, "mysql", sc, []string{"categories"}, []string{"categories"}, SeedOptions{
		Rows: 2000, BatchSize: 100, ChunkRows: 250, Workers: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts["categories"] != 2000 {
		t.Fatalf("counts = %+v", res.Counts)
	}
	if rec.overlapSelf["categories"] {
		t.Fatal("a self-referencing table had two inserts in flight")
	}
	events := rec.inserts("categories")
	var prev int64
	for i, e := range events {
		id, ok := e.firstKey.(int64)
		if !ok {
			t.Fatalf("insert %d first key %T %v, want int64 id", i, e.firstKey, e.firstKey)
		}
		if id <= prev {
			t.Fatalf("insert %d starts at id %d after %d: chunks written out of order", i, id, prev)
		}
		prev = id
	}
}

func TestSeed_ConcurrentFirstRefusedInsertStopsTheRunAndChildrenNeverWrite(t *testing.T) {
	conn, rec := openRecording(t, map[string]time.Duration{"users": 2 * time.Millisecond}, func(table string, n int) error {
		if table == "users" && n == 3 {
			return errCheck
		}
		return nil
	})
	order := []string{"users", "reviewers", "audit", "posts"}
	_, err := Seed(withDeadline(t, 20*time.Second), conn, "mysql", usersPostsAudit(), order, order, SeedOptions{
		Rows: 5000, BatchSize: 500, ChunkRows: 1000, Workers: 4,
	})
	if !errors.Is(err, errCheck) || !strings.Contains(err.Error(), "insert into users") {
		t.Fatalf("err = %v, want the refused users insert", err)
	}
	if got := rec.inserts("posts"); len(got) != 0 {
		t.Fatalf("posts wrote %d batches after its parent failed", len(got))
	}
}

func TestSeed_CancelledConcurrentRunReturnsWithoutHanging(t *testing.T) {
	conn, _ := openRecording(t, map[string]time.Duration{"users": 20 * time.Millisecond, "audit": 20 * time.Millisecond}, nil)
	ctx, cancel := context.WithCancel(withDeadline(t, 20*time.Second))
	time.AfterFunc(60*time.Millisecond, cancel)
	order := []string{"users", "reviewers", "audit", "posts"}
	_, err := Seed(ctx, conn, "mysql", usersPostsAudit(), order, order, SeedOptions{
		Rows: 50_000, BatchSize: 500, ChunkRows: 1000, Workers: 3,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestSeed_ProgressReportsRunTotalsForEveryWrite(t *testing.T) {
	conn, _ := openRecording(t, nil, nil)
	order := []string{"users", "reviewers", "audit", "posts"}
	var ticks []Progress
	finished := map[string]int64{}
	res, err := Seed(withDeadline(t, 20*time.Second), conn, "mysql", usersPostsAudit(), order, order, SeedOptions{
		Rows: 1200, BatchSize: 200, ChunkRows: 500, Workers: 4,
		TableRows:  map[string]int{"posts": 3000},
		OnProgress: func(p Progress) { ticks = append(ticks, p) },
		OnTable:    func(p Progress) { finished[p.Table] = p.Inserted },
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = 1200*3 + 3000
	var last int64
	for _, p := range ticks {
		if p.RowsDone < last {
			t.Fatalf("RowsDone went backwards: %d after %d", p.RowsDone, last)
		}
		if p.RowsTotal != want || p.Tables != 4 || p.TableIndex < 1 || p.TableIndex > 4 {
			t.Fatalf("tick %+v, want RowsTotal %d over 4 tables", p, want)
		}
		if p.Inserted > p.Requested {
			t.Fatalf("tick %+v: table inserted past requested", p)
		}
		last = p.RowsDone
	}
	if last != want || int64(res.Total) != want {
		t.Fatalf("last RowsDone %d, total %d, want %d", last, res.Total, want)
	}
	if fmt.Sprint(finished) != "map[audit:1200 posts:3000 reviewers:1200 users:1200]" {
		t.Fatalf("finished tables = %v", finished)
	}
}

func TestSeed_WorkersUnsetKeepsTheSequentialContract(t *testing.T) {
	conn, rec := openRecording(t, map[string]time.Duration{"users": time.Millisecond}, nil)
	order := []string{"users", "audit"}
	_, err := Seed(withDeadline(t, 20*time.Second), conn, "mysql", usersPostsAudit(), order, order, SeedOptions{Rows: 3000, BatchSize: 250})
	if err != nil {
		t.Fatal(err)
	}
	if rec.overlapSelf["users"] || !firstStart(rec.inserts("audit")).After(lastEnd(rec.inserts("users"))) {
		t.Fatal("unset Workers wrote concurrently")
	}
}

func TestRowBudget_BlocksPastLimitAndGrantsOversizeWhenIdle(t *testing.T) {
	b := newRowBudget(100)
	ctx := context.Background()
	if err := b.acquire(ctx, 500); err != nil {
		t.Fatalf("oversize reservation on an idle budget: %v", err)
	}
	got := make(chan error, 1)
	go func() { got <- b.acquire(ctx, 10) }()
	select {
	case err := <-got:
		t.Fatalf("acquire past the limit returned early: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	b.release(500)
	if err := <-got; err != nil {
		t.Fatal(err)
	}

	cctx, cancel := context.WithCancel(ctx)
	blocked := make(chan error, 1)
	go func() { blocked <- b.acquire(cctx, 95) }()
	cancel()
	if err := <-blocked; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquire = %v", err)
	}
}

func TestFill_WorkersSplitAChunkButKeepSelfReferencesWhole(t *testing.T) {
	t.Run("plain table", func(t *testing.T) {
		conn, rec := openRecording(t, map[string]time.Duration{"notes": 10 * time.Millisecond}, nil)
		res, err := Fill(withDeadline(t, 20*time.Second), conn, "mysql", notesSchema(), []string{"notes"}, map[string]int{"notes": 4000}, Options{BatchSize: 250, ChunkRows: 2000, Workers: 4})
		if err != nil {
			t.Fatal(err)
		}
		if res.Tables[0].Inserted != 4000 || res.Tables[0].Status != StatusOK {
			t.Fatalf("result = %+v", res.Tables)
		}
		if !rec.overlapSelf["notes"] {
			t.Fatal("chunk pieces were not written concurrently")
		}
	})
	t.Run("self-referencing table", func(t *testing.T) {
		sc := &schema.Schema{Tables: map[string]schema.Table{
			"nodes": {Columns: map[string]schema.Column{
				"id": {Type: "integer", PK: true}, "parent_id": {Type: "integer", FK: "nodes.id", Nullable: true},
			}},
		}}
		conn, rec := openRecording(t, map[string]time.Duration{"nodes": 2 * time.Millisecond}, nil)
		if _, err := Fill(withDeadline(t, 20*time.Second), conn, "mysql", sc, []string{"nodes"}, map[string]int{"nodes": 2000}, Options{BatchSize: 100, Workers: 4}); err != nil {
			t.Fatal(err)
		}
		if rec.overlapSelf["nodes"] {
			t.Fatal("a self-referencing table was split over concurrent writers")
		}
	})
}
