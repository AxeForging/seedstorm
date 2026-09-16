// Package seeder inserts generated rows into a live database resiliently. It is
// the execution engine behind mirror runs in the CLI, TUI and web UI: tables are
// filled one at a time in fixed-size chunks from one faker.Stream (the database
// is read once per table and memory stays flat however many rows are written),
// Postgres chunks go through COPY, failures degrade from a batch to single rows,
// and a table that cannot make progress is reported and skipped instead of
// retried forever.
package seeder

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// Defaults for Options.
const (
	// DefaultBatchSize is the most rows per INSERT; db.SplitBatches sends fewer
	// when the parameter limit or byte budget would be exceeded.
	DefaultBatchSize      = 1000
	DefaultMaxRowFailures = 25
	// DefaultChunkRows is how many rows are generated and held in memory at once.
	DefaultChunkRows = 20_000
	// maxZeroRounds is how many consecutive rounds may generate nothing before a
	// table is abandoned. One empty round happens by chance on small chunks.
	maxZeroRounds = 3
)

// Table outcome statuses.
const (
	StatusOK      = "ok"
	StatusPartial = "partial"
	StatusFailed  = "failed"
)

// Options tunes a fill.
type Options struct {
	BatchSize int
	// ChunkRows bounds rows generated in memory at once (0: DefaultChunkRows).
	ChunkRows int
	// Workers is how many connections write one chunk at once (0 or 1: one).
	// Tables still fill one after another: each reads its parents' stored keys.
	Workers int
	// StopOnError aborts the run at the first failed insert instead of
	// degrading to row-by-row and moving on.
	StopOnError bool
	// MaxRowFailures is how many consecutive single-row failures abandon a table.
	MaxRowFailures int
	// Generate carries value-rule overrides and self-reference depth.
	Generate faker.GenerateOptions
	// OnProgress reports inserted/requested rows for the current table.
	OnProgress func(p Progress)
}

// Progress is one progress tick: the table that just advanced, and the run.
type Progress struct {
	Table      string
	TableIndex int // 1-based position in the run order
	Tables     int
	Inserted   int64
	Requested  int64
	// RowsDone and RowsTotal count rows across every table of the run.
	RowsDone  int64
	RowsTotal int64
}

// TableResult is the outcome for one table. Rejected counts insert attempts
// the database refused; rejected rows are regenerated with fresh values, so a
// table can finish OK despite rejections. Missing is what never made it in.
type TableResult struct {
	Table     string `json:"table"`
	Requested int64  `json:"requested"`
	Inserted  int64  `json:"inserted"`
	Rejected  int64  `json:"rejected"`
	Missing   int64  `json:"missing"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

// Result is the outcome of a fill.
type Result struct {
	Tables   []TableResult `json:"tables"`
	Inserted int64         `json:"inserted"`
	Missing  int64         `json:"missing"`
	// Sequences lists Postgres sequences moved past the inserted ids.
	Sequences []db.SequenceAdjustment `json:"sequences,omitempty"`
	// SequenceError explains why sequences could not be advanced.
	SequenceError string `json:"sequenceError,omitempty"`
}

// Problems returns tables that did not receive every requested row.
func (r Result) Problems() []TableResult {
	var out []TableResult
	for _, t := range r.Tables {
		if t.Status != StatusOK {
			out = append(out, t)
		}
	}
	return out
}

// Fill inserts counts[table] new rows into each table of order, which must be
// in FK-safe (topological) order. It returns an error only when the run must
// stop: the context was cancelled, or StopOnError was set and a write failed.
// Per-table problems are reported in Result.
func Fill(ctx context.Context, conn *sql.DB, dbType string, sc *schema.Schema, order []string, counts map[string]int, opts Options) (res Result, err error) {
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	if opts.MaxRowFailures <= 0 {
		opts.MaxRowFailures = DefaultMaxRowFailures
	}
	// Explicit ids leave Postgres sequences behind; move them forward for every
	// table that received rows, including when the run stops early.
	defer func() {
		var touched []string
		for _, t := range res.Tables {
			if t.Inserted > 0 {
				touched = append(touched, t.Table)
			}
		}
		syncCtx := context.WithoutCancel(ctx)
		adjusted, syncErr := db.SyncSequences(syncCtx, conn, dbType, touched)
		res.Sequences = adjusted
		if syncErr != nil {
			res.SequenceError = syncErr.Error()
		}
	}()
	run := runPosition{}
	for _, t := range order {
		if counts[t] > 0 {
			run.tables++
			run.rowsTotal += int64(counts[t])
		}
	}
	for _, tableName := range order {
		want := counts[tableName]
		if want <= 0 {
			continue
		}
		run.index++
		tr, fillErr := fillTable(ctx, conn, dbType, sc, tableName, want, run, opts)
		run.rowsDone += tr.Inserted
		res.Tables = append(res.Tables, tr)
		res.Inserted += tr.Inserted
		res.Missing += tr.Missing
		if fillErr != nil {
			return res, fillErr
		}
	}
	return res, nil
}

// runPosition is where a fill is: the table being filled and rows so far.
type runPosition struct {
	index, tables       int
	rowsDone, rowsTotal int64
}

func fillTable(ctx context.Context, conn *sql.DB, dbType string, sc *schema.Schema, tableName string, want int, run runPosition, opts Options) (TableResult, error) {
	tr := TableResult{Table: tableName, Requested: int64(want)}
	if _, ok := sc.Tables[tableName]; !ok {
		return finishStuck(tr, "table is not in the schema", opts)
	}
	chunk := chunkSize(opts.ChunkRows)
	preload := preloadTables(sc, tableName)
	_, selfRef := referencedTables(sc, tableName, nil)
	// Parents are complete by now (tables run in FK order), so their pools and
	// this table's stored keys are read once and kept for every chunk.
	stream, err := faker.NewStream(sc, preload, []string{tableName}, conn, dbType, opts.Generate.Overrides)
	if err != nil {
		return finishStuck(tr, "read existing rows: "+err.Error(), opts)
	}
	genOpts := opts.Generate
	var warned string
	userWarn := genOpts.OnWarning
	genOpts.OnWarning = func(w faker.GenerationWarning) {
		warned = w.Reason
		if userWarn != nil {
			userWarn(w)
		}
	}

	zeroRounds := 0
	// refusedStreak counts rows refused since the last round that inserted any.
	refusedStreak := 0
	for tr.Inserted < tr.Requested {
		if err := ctx.Err(); err != nil {
			tr.Error = "cancelled"
			return finish(tr), err
		}
		n := chunk
		if remaining := int(tr.Requested - tr.Inserted); remaining < n {
			n = remaining
		}
		data, err := stream.Generate([]string{tableName}, n, 0, map[string]int{tableName: n}, genOpts)
		if err != nil {
			return finishStuck(tr, "generate: "+err.Error(), opts)
		}
		rows := data[tableName]
		if len(rows) == 0 {
			// Keys or UNIQUE groups can run out for good, or a small chunk can
			// lose every row to collisions by chance: only repeated empty rounds
			// mean there is nothing left to generate.
			zeroRounds++
			if zeroRounds >= maxZeroRounds {
				reason := "generator produced no rows"
				if warned != "" {
					reason = "no more rows possible: " + warned
				}
				return finishStuck(tr, reason, opts)
			}
			continue
		}
		inserted, rejected, lastErr, err := insertRowsConcurrently(ctx, conn, dbType, tableName, rows, opts, selfRef)
		tr.Inserted += int64(inserted)
		tr.Rejected += int64(rejected)
		if lastErr != "" {
			tr.Error = lastErr
		}
		if opts.OnProgress != nil {
			opts.OnProgress(Progress{
				Table: tableName, TableIndex: run.index, Tables: run.tables, Inserted: tr.Inserted, Requested: tr.Requested,
				RowsDone: run.rowsDone + tr.Inserted, RowsTotal: run.rowsTotal,
			})
		}
		if err != nil {
			return finish(tr), err
		}
		if inserted > 0 {
			refusedStreak = 0
		} else {
			refusedStreak += max(rejected, 1)
		}
		switch {
		case refusedStreak >= opts.MaxRowFailures:
			// A long run of refused rows, not a few unlucky small rounds: at a 50%
			// rejection rate 25 refusals in a row happen once in 30 million.
			return finishStuck(tr, tr.Error, opts)
		case tr.Rejected > rejectBudget(tr.Requested, opts.MaxRowFailures):
			return finishStuck(tr, "too many rejected rows, last: "+tr.Error, opts)
		}
	}
	return finish(tr), nil
}

// rejectBudget bounds how many refused rows a table may accumulate while its
// rejected rows are regenerated. Each round retries only the shortfall, so even a
// 90% rejection rate converges well inside ten times the requested volume; a
// round that inserts nothing is handled separately as stuck.
func rejectBudget(requested int64, maxRowFailures int) int64 {
	return 10*requested + int64(maxRowFailures)*4
}

func finish(tr TableResult) TableResult {
	tr.Missing = tr.Requested - tr.Inserted
	switch {
	case tr.Missing <= 0:
		tr.Missing = 0
		tr.Status = StatusOK
	case tr.Inserted == 0:
		tr.Status = StatusFailed
	default:
		tr.Status = StatusPartial
	}
	return tr
}

// finishStuck closes a table that cannot make further progress.
func finishStuck(tr TableResult, reason string, opts Options) (TableResult, error) {
	tr.Error = strings.TrimSpace(reason)
	tr = finish(tr)
	return tr, stopErr(opts, tr)
}

func stopErr(opts Options, tr TableResult) error {
	if !opts.StopOnError {
		return nil
	}
	return fmt.Errorf("%s: %s", tr.Table, tr.Error)
}

// insertRowsConcurrently splits a chunk over opts.Workers connections. Rows of
// a self-referencing table stay in one ordered piece: a row may reference an
// earlier row of the same chunk.
func insertRowsConcurrently(ctx context.Context, conn *sql.DB, dbType, tableName string, rows []map[string]interface{}, opts Options, selfRef bool) (inserted, rejected int, lastErr string, err error) {
	if opts.Workers <= 1 || selfRef || len(rows) <= opts.BatchSize {
		return insertRows(ctx, conn, dbType, tableName, rows, opts)
	}
	size := max((len(rows)+opts.Workers-1)/opts.Workers, opts.BatchSize)
	type outcome struct {
		inserted, rejected int
		lastErr            string
		err                error
	}
	var pieces [][]map[string]interface{}
	for start := 0; start < len(rows); start += size {
		pieces = append(pieces, rows[start:min(start+size, len(rows))])
	}
	results := make([]outcome, len(pieces))
	var wg sync.WaitGroup
	for i, piece := range pieces {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o := &results[i]
			o.inserted, o.rejected, o.lastErr, o.err = insertRows(ctx, conn, dbType, tableName, piece, opts)
		}()
	}
	wg.Wait()
	for _, o := range results {
		inserted += o.inserted
		rejected += o.rejected
		if o.lastErr != "" {
			lastErr = o.lastErr
		}
		if err == nil {
			err = o.err
		}
	}
	return inserted, rejected, lastErr, err
}

// insertRows writes rows in batches. A failed batch is retried row by row so one
// bad row does not sink its neighbours; MaxRowFailures consecutive row failures
// abandon the rest of the chunk.
func insertRows(ctx context.Context, conn *sql.DB, dbType, tableName string, rows []map[string]interface{}, opts Options) (inserted, rejected int, lastErr string, err error) {
	// Postgres takes a whole chunk through COPY. COPY is atomic, so when the
	// database refuses any row the chunk is retried below with batched INSERTs
	// and row-by-row fallback, which isolate the bad rows.
	copyErr := db.CopyRows(ctx, conn, dbType, tableName, rows)
	if copyErr == nil {
		return len(rows), 0, "", nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return 0, 0, copyErr.Error(), ctxErr
	}
	for _, batch := range db.SplitBatches(rows, opts.BatchSize) {
		query, values := db.BuildBatchInsert(tableName, batch, dbType)
		_, execErr := conn.ExecContext(ctx, query, values...)
		if execErr == nil {
			inserted += len(batch)
			continue
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return inserted, rejected, execErr.Error(), ctxErr
		}
		lastErr = execErr.Error()
		if opts.StopOnError {
			return inserted, rejected + len(batch), lastErr, fmt.Errorf("insert into %s: %w", tableName, execErr)
		}
		consecutive := 0
		for _, row := range batch {
			q, v := db.BuildInsert(tableName, row, dbType)
			if _, rowErr := conn.ExecContext(ctx, q, v...); rowErr != nil {
				if errors.Is(rowErr, context.Canceled) || ctx.Err() != nil {
					return inserted, rejected, rowErr.Error(), ctx.Err()
				}
				rejected++
				consecutive++
				lastErr = rowErr.Error()
				if consecutive >= opts.MaxRowFailures {
					return inserted, rejected, lastErr, nil
				}
				continue
			}
			consecutive = 0
			inserted++
		}
	}
	return inserted, rejected, lastErr, nil
}

func chunkSize(configured int) int {
	if configured > 0 {
		return configured
	}
	return DefaultChunkRows
}

// preloadTables is the table plus every table it references, which is all the
// generator needs to read PK pools for (not the whole database).
func preloadTables(sc *schema.Schema, tableName string) []string {
	out := []string{tableName}
	seen := map[string]bool{tableName: true}
	for _, col := range sc.Tables[tableName].Columns {
		parent, _, ok := strings.Cut(col.FK, ".")
		if !ok || seen[parent] {
			continue
		}
		if _, exists := sc.Tables[parent]; !exists {
			continue
		}
		seen[parent] = true
		out = append(out, parent)
	}
	return out
}
