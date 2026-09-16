package seeder

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// SeedOptions describes a plain seed run: seed and gaps --fill in the CLI, TUI
// and web UI.
type SeedOptions struct {
	// Rows per table, unless TableRows gives one (see faker.TableRowCount).
	Rows      int
	EnumRows  int
	TableRows map[string]int
	// BatchSize is the most rows per INSERT; SplitBatches may send fewer.
	BatchSize int
	// ChunkRows bounds rows generated at once (0: DefaultChunkRows).
	ChunkRows int
	// Workers is how many connections write at once. 0 or 1 writes each chunk
	// before the next is generated; more writes unrelated tables (and pieces of
	// one table) concurrently while generation continues. See writer.
	Workers int
	// DryRun generates rows and hands them to OnRows without writing.
	DryRun   bool
	Generate faker.GenerateOptions
	// OnRows sees every chunk before it is written (dry-run output, previews).
	OnRows func(table string, rows []map[string]interface{}) error
	// OnTableStart announces a table before its first row is generated.
	OnTableStart func(table string) error
	// OnTable reports each table once all its rows are written.
	OnTable func(p Progress)
	// OnProgress reports every written chunk (or piece of one) with table and
	// run totals. Callbacks never run concurrently with each other.
	OnProgress func(p Progress)
}

// SeedResult counts the rows seeded per table.
type SeedResult struct {
	Counts map[string]int `json:"counts"`
	Total  int            `json:"total"`
}

// Seed generates rows for tables (in FK order) chunk by chunk and writes each
// chunk as it is generated, so memory stays flat for any row count. Unlike Fill
// it is strict: the first refused insert stops the run with an error, like seed
// and gaps always have. preload lists every table whose stored keys foreign keys
// may reference. conn may be nil for a dry run that should not read the database.
func Seed(ctx context.Context, conn *sql.DB, dbType string, sc *schema.Schema, preload, tables []string, opts SeedOptions) (SeedResult, error) {
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	stream, err := faker.NewStream(sc, preload, tables, conn, dbType, opts.Generate.Overrides)
	if err != nil {
		return SeedResult{Counts: map[string]int{}}, fmt.Errorf("data generation failed: %w", err)
	}
	tally := newSeedTally(tables, opts)
	if opts.DryRun || conn == nil || opts.Workers <= 1 {
		return seedSequential(ctx, conn, dbType, stream, tables, opts, tally)
	}
	return seedConcurrent(ctx, conn, dbType, sc, stream, tables, opts, tally)
}

func seedSequential(ctx context.Context, conn *sql.DB, dbType string, stream *faker.Stream, tables []string, opts SeedOptions, tally *seedTally) (SeedResult, error) {
	for _, tableName := range tables {
		if opts.OnTableStart != nil {
			if err := opts.OnTableStart(tableName); err != nil {
				return tally.result(), err
			}
		}
		count, overridden := faker.TableRowCount(tableName, opts.Rows, opts.TableRows)
		err := stream.GenerateChunks(tableName, count, opts.EnumRows, overridden, chunkSize(opts.ChunkRows), opts.Generate, func(rows []map[string]interface{}) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if opts.OnRows != nil {
				if err := opts.OnRows(tableName, rows); err != nil {
					return err
				}
			}
			if !opts.DryRun {
				if err := insertStrict(ctx, conn, dbType, tableName, rows, opts.BatchSize); err != nil {
					return err
				}
			}
			tally.written(tableName, len(rows))
			return nil
		})
		if err != nil {
			return tally.result(), err
		}
		tally.done(tableName)
	}
	return tally.result(), nil
}

func seedConcurrent(ctx context.Context, conn *sql.DB, dbType string, sc *schema.Schema, stream *faker.Stream, tables []string, opts SeedOptions, tally *seedTally) (SeedResult, error) {
	chunk := chunkSize(opts.ChunkRows)
	w := newWriter(ctx, conn, dbType, opts.BatchSize, opts.Workers, 2*chunk)
	w.onWritten = tally.written
	w.onDone = tally.done

	run := make(map[string]bool, len(tables))
	for _, t := range tables {
		run[t] = true
	}
	writers := make(map[string]*tableWriter, len(tables))
	var genErr error
	for _, tableName := range tables {
		if opts.OnTableStart != nil {
			if genErr = opts.OnTableStart(tableName); genErr != nil {
				break
			}
		}
		names, selfRef := referencedTables(sc, tableName, run)
		var parents []*tableWriter
		for _, p := range names {
			// A parent opened later (FK ordering disabled) is not waited for:
			// its rows may not exist yet whatever the writer does.
			if pw, ok := writers[p]; ok {
				parents = append(parents, pw)
			}
		}
		tw := w.open(tableName, parents, selfRef)
		writers[tableName] = tw
		count, overridden := faker.TableRowCount(tableName, opts.Rows, opts.TableRows)
		genErr = stream.GenerateChunks(tableName, count, opts.EnumRows, overridden, chunk, opts.Generate, func(rows []map[string]interface{}) error {
			if opts.OnRows != nil {
				if err := opts.OnRows(tableName, rows); err != nil {
					return err
				}
			}
			return tw.submit(rows)
		})
		tw.close()
		if genErr != nil {
			break
		}
	}
	if genErr != nil {
		w.abort(genErr)
	}
	if err := w.wait(); err != nil {
		return tally.result(), err
	}
	return tally.result(), nil
}

// seedTally counts written rows and turns them into progress callbacks.
type seedTally struct {
	mu        sync.Mutex
	opts      SeedOptions
	index     map[string]int
	requested map[string]int64
	counts    map[string]int
	total     int64
	wanted    int64
	tables    int
}

func newSeedTally(tables []string, opts SeedOptions) *seedTally {
	t := &seedTally{opts: opts, index: map[string]int{}, requested: map[string]int64{}, counts: map[string]int{}, tables: len(tables)}
	for i, name := range tables {
		t.index[name] = i + 1
		n, _ := faker.TableRowCount(name, opts.Rows, opts.TableRows)
		t.requested[name] = int64(n)
		t.wanted += int64(n)
	}
	return t
}

func (t *seedTally) progress(table string) Progress {
	inserted := int64(t.counts[table])
	// Enum coverage can add rows beyond the requested count.
	requested := max(t.requested[table], inserted)
	return Progress{
		Table: table, TableIndex: t.index[table], Tables: t.tables,
		Inserted: inserted, Requested: requested,
		RowsDone: t.total, RowsTotal: max(t.wanted, t.total),
	}
}

func (t *seedTally) written(table string, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counts[table] += rows
	t.total += int64(rows)
	if t.opts.OnProgress != nil {
		t.opts.OnProgress(t.progress(table))
	}
}

func (t *seedTally) done(table string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.counts[table]; !ok {
		t.counts[table] = 0
	}
	if t.opts.OnTable != nil {
		p := t.progress(table)
		p.Requested = p.Inserted
		t.opts.OnTable(p)
	}
}

func (t *seedTally) result() SeedResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	res := SeedResult{Counts: make(map[string]int, len(t.counts))}
	for name, n := range t.counts {
		res.Counts[name] = n
		res.Total += n
	}
	return res
}
