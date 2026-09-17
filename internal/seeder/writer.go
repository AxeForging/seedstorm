package seeder

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faultinject"
	"github.com/AxeForging/seedstorm/internal/runerr"
	"github.com/AxeForging/seedstorm/internal/safego"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// DefaultWorkers is how many connections the CLI, TUI and web UI write on at
// once. The engine itself defaults to one (strictly sequential) when unset.
const DefaultWorkers = 4

// approxRowMemory converts a row cap into a memory budget when chunks are not
// bounded by bytes.
const approxRowMemory = 1 << 10

// maxTransientRetries bounds retries of a statement the database refused only
// because of a lock conflict between concurrent writers.
const maxTransientRetries = 3

// writer inserts the chunks of a run on up to workers connections at once while
// the caller keeps generating. Generation stays single-threaded (the stream is
// stateful and cheap next to the database); only writes are concurrent.
//
// Ordering rules keep every insert valid:
//   - a table's rows are written only after every table it references in this
//     run has finished writing, nullable foreign keys included;
//   - a table that references itself writes its chunks one at a time, in the
//     order they were generated, so parents rows land before their children;
//   - anything else writes concurrently, including pieces of one table.
//
// Tables wait in their own dispatcher goroutine, never inside a worker, so a
// child waiting for a parent cannot starve the workers the parent needs.
// Memory stays bounded: submit blocks while too much row memory is queued.
type writer struct {
	ctx     context.Context
	cancel  context.CancelCauseFunc
	conn    *sql.DB
	dbType  string
	batch   int
	workers int

	work    chan func()
	running sync.WaitGroup
	tables  sync.WaitGroup
	budget  *rowBudget
	// minRowCharge is the least a queued row costs against the budget, so
	// narrow rows cannot fill it with several chunks' worth of rows.
	minRowCharge int

	// hooks run one at a time, so callers need no locking of their own.
	hookMu    sync.Mutex
	onWritten func(table string, rows int)
	onDone    func(table string)
}

func newWriter(ctx context.Context, conn *sql.DB, dbType string, batch, workers, maxQueuedBytes int) *writer {
	wctx, cancel := context.WithCancelCause(ctx)
	w := &writer{
		ctx: wctx, cancel: cancel, conn: conn, dbType: dbType, batch: batch, workers: workers,
		work:   make(chan func()),
		budget: newRowBudget(maxQueuedBytes),
	}
	for i := 0; i < workers; i++ {
		w.running.Add(1)
		go func() {
			defer w.running.Done()
			for task := range w.work {
				// A panic in a task fails the run, not the process; the
				// task's own defers still release its bookkeeping.
				if err := safego.Run("writer", func() error { task(); return nil }); err != nil {
					w.abort(err)
				}
			}
		}()
	}
	return w
}

// queued is a piece of a chunk waiting for a worker, with the memory it holds.
type queued struct {
	rows []map[string]interface{}
	size int
}

// tableWriter queues one table's chunks.
type tableWriter struct {
	w       *writer
	name    string
	parents []*tableWriter
	ordered bool

	mu     sync.Mutex
	queue  []queued
	closed bool
	wake   chan struct{}
	done   chan struct{}
}

// open starts a table. parents are tables of this run it references; they must
// have been opened before it.
func (w *writer) open(name string, parents []*tableWriter, ordered bool) *tableWriter {
	tw := &tableWriter{w: w, name: name, parents: parents, ordered: ordered, wake: make(chan struct{}, 1), done: make(chan struct{})}
	w.tables.Add(1)
	go func() {
		if err := safego.Run("dispatch "+name, func() error { tw.dispatch(); return nil }); err != nil {
			w.abort(runerr.At(runerr.PhaseWrite, name, err))
		}
	}()
	return tw
}

// submit queues rows for writing, blocking while the writer holds too many
// queued rows. It fails once the run has failed or been cancelled.
func (tw *tableWriter) submit(rows []map[string]interface{}) error {
	for _, piece := range tw.pieces(rows) {
		size := max(db.RowsMemory(piece), len(piece)*tw.w.minRowCharge)
		if err := tw.w.budget.acquire(tw.w.ctx, size); err != nil {
			return context.Cause(tw.w.ctx)
		}
		tw.mu.Lock()
		tw.queue = append(tw.queue, queued{rows: piece, size: size})
		tw.mu.Unlock()
		tw.signal()
	}
	return nil
}

// pieces splits a chunk so a table without self-references spreads over every
// worker. A self-referencing table keeps its chunk whole and in order.
func (tw *tableWriter) pieces(rows []map[string]interface{}) [][]map[string]interface{} {
	if tw.ordered || tw.w.workers <= 1 || len(rows) <= tw.w.batch {
		return [][]map[string]interface{}{rows}
	}
	size := (len(rows) + tw.w.workers - 1) / tw.w.workers
	if size < tw.w.batch {
		size = tw.w.batch
	}
	var out [][]map[string]interface{}
	for start := 0; start < len(rows); start += size {
		end := min(start+size, len(rows))
		out = append(out, rows[start:end])
	}
	return out
}

// close marks that no more rows will be submitted for the table.
func (tw *tableWriter) close() {
	tw.mu.Lock()
	tw.closed = true
	tw.mu.Unlock()
	tw.signal()
}

func (tw *tableWriter) signal() {
	select {
	case tw.wake <- struct{}{}:
	default:
	}
}

func (tw *tableWriter) dispatch() {
	w := tw.w
	defer w.tables.Done()
	defer close(tw.done)
	for _, p := range tw.parents {
		select {
		case <-p.done:
		case <-w.ctx.Done():
			tw.discard()
			return
		}
	}
	var pending sync.WaitGroup
	for {
		item, ok := tw.next()
		if !ok {
			break
		}
		finished := make(chan struct{})
		pending.Add(1)
		task := func() {
			defer pending.Done()
			defer close(finished)
			tw.write(item)
		}
		select {
		case w.work <- task:
		case <-w.ctx.Done():
			pending.Done()
			w.budget.release(item.size)
			continue
		}
		if tw.ordered {
			<-finished
		}
	}
	pending.Wait()
	if w.ctx.Err() == nil {
		w.hookMu.Lock()
		if w.onDone != nil {
			w.onDone(tw.name)
		}
		w.hookMu.Unlock()
	}
}

// next waits for the next queued chunk. It reports false once the table is
// closed and drained, or the run was cancelled (queued rows are dropped).
func (tw *tableWriter) next() (queued, bool) {
	for {
		tw.mu.Lock()
		if len(tw.queue) > 0 {
			item := tw.queue[0]
			tw.queue[0] = queued{}
			tw.queue = tw.queue[1:]
			tw.mu.Unlock()
			return item, true
		}
		closed := tw.closed
		tw.mu.Unlock()
		if closed {
			return queued{}, false
		}
		select {
		case <-tw.wake:
		case <-tw.w.ctx.Done():
			tw.discard()
			return queued{}, false
		}
	}
}

// discard drops queued rows after a failure, returning their budget.
func (tw *tableWriter) discard() {
	tw.mu.Lock()
	pending := tw.queue
	tw.queue = nil
	tw.mu.Unlock()
	for _, item := range pending {
		tw.w.budget.release(item.size)
	}
}

func (tw *tableWriter) write(item queued) {
	w := tw.w
	rows := item.rows
	defer w.budget.release(item.size)
	if w.ctx.Err() != nil {
		return
	}
	err := safego.Run("write "+tw.name, func() error {
		if err := faultinject.Hit(w.ctx, "write", tw.name); err != nil {
			return err
		}
		return insertStrict(w.ctx, w.conn, w.dbType, tw.name, rows, w.batch)
	})
	if err != nil {
		if w.ctx.Err() == nil {
			w.cancel(runerr.At(runerr.PhaseWrite, tw.name, err))
		}
		return
	}
	w.hookMu.Lock()
	if w.onWritten != nil {
		w.onWritten(tw.name, len(rows))
	}
	w.hookMu.Unlock()
}

// abort stops the run with err unless it already failed.
func (w *writer) abort(err error) {
	if w.ctx.Err() == nil {
		w.cancel(err)
	}
}

// wait blocks until every opened table has finished (or the run failed), stops
// the workers and returns the first error.
func (w *writer) wait() error {
	w.tables.Wait()
	close(w.work)
	w.running.Wait()
	err := context.Cause(w.ctx)
	w.cancel(nil)
	return err
}

// rowBudget bounds the memory of generated rows waiting to be written.
type rowBudget struct {
	mu      sync.Mutex
	used    int
	limit   int
	changed chan struct{}
}

func newRowBudget(limit int) *rowBudget {
	return &rowBudget{limit: limit, changed: make(chan struct{})}
}

// acquire reserves n rows. A reservation larger than the limit is granted when
// nothing else is held, so one big chunk can never block forever.
func (b *rowBudget) acquire(ctx context.Context, n int) error {
	for {
		b.mu.Lock()
		if b.used == 0 || b.used+n <= b.limit {
			b.used += n
			b.mu.Unlock()
			return nil
		}
		ch := b.changed
		b.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (b *rowBudget) release(n int) {
	b.mu.Lock()
	b.used -= n
	close(b.changed)
	b.changed = make(chan struct{})
	b.mu.Unlock()
}

// insertStrict writes rows through COPY on Postgres, or batched INSERTs, and
// fails at the first refused batch. A statement refused only because of a lock
// conflict with another writer is retried: nothing of it was written.
func insertStrict(ctx context.Context, conn *sql.DB, dbType, tableName string, rows []map[string]interface{}, batchSize int) error {
	if db.CopyRows(ctx, conn, dbType, tableName, rows) == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// COPY is all or nothing, so nothing was written: batches report the
	// database's own error for the offending rows.
	for _, batch := range db.SplitBatches(rows, batchSize) {
		query, values := db.BuildBatchInsert(tableName, batch, dbType)
		err := retryTransient(ctx, func() error {
			_, err := conn.ExecContext(ctx, query, values...)
			return err
		})
		if err != nil {
			return fmt.Errorf("insert into %s failed: %w", tableName, err)
		}
	}
	return nil
}

func retryTransient(ctx context.Context, fn func() error) error {
	err := fn()
	for attempt := 1; attempt <= maxTransientRetries && err != nil && db.IsTransient(err); attempt++ {
		select {
		case <-time.After(time.Duration(attempt) * 50 * time.Millisecond):
		case <-ctx.Done():
			return err
		}
		err = fn()
	}
	return err
}

// referencedTables returns the tables of run that table references through a
// foreign key (nullable ones included), and whether it references itself.
func referencedTables(sc *schema.Schema, table string, run map[string]bool) (parents []string, selfRef bool) {
	seen := map[string]bool{}
	for _, colName := range sortedColumns(sc.Tables[table]) {
		parent, _, ok := strings.Cut(sc.Tables[table].Columns[colName].FK, ".")
		if !ok {
			continue
		}
		if parent == table {
			selfRef = true
			continue
		}
		if run[parent] && !seen[parent] {
			seen[parent] = true
			parents = append(parents, parent)
		}
	}
	return parents, selfRef
}

func sortedColumns(t schema.Table) []string {
	out := make([]string, 0, len(t.Columns))
	for name := range t.Columns {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
