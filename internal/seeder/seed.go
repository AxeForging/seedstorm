package seeder

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/faultinject"
	"github.com/AxeForging/seedstorm/internal/runerr"
	"github.com/AxeForging/seedstorm/internal/safego"
	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/AxeForging/seedstorm/internal/tuning"
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
	// GenWorkers is how many tables generate at once (0 or 1: one). It needs
	// Workers > 1, and is ignored for dry runs, when OnRows is set (callers
	// print chunks in order) and when Reproducible is set. See generateTables.
	GenWorkers int
	// OnNotice receives decisions the run made that the user should know, such
	// as fewer writers because the server has few free connections.
	OnNotice func(msg string)
	// Reproducible keeps generation on one goroutine so a seeded run repeats
	// exactly: concurrent tables would draw from the random source in any order.
	Reproducible bool
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
	opts.Generate.ChunkBytes = chunkBytesOrDefault(opts.Generate.ChunkBytes)
	stream, err := faker.NewStreamContext(ctx, sc, preload, tables, conn, dbType, opts.Generate.Overrides)
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
	releases := newPoolReleases(stream.Schema(), tables)
	for _, tableName := range tables {
		if opts.OnTableStart != nil {
			if err := opts.OnTableStart(tableName); err != nil {
				return tally.result(), err
			}
		}
		count, overridden := faker.TableRowCount(tableName, opts.Rows, opts.TableRows)
		err := safego.Run("generate "+tableName, func() error {
			if err := faultinject.Hit(ctx, "generate", tableName); err != nil {
				return err
			}
			return stream.GenerateChunks(tableName, count, opts.EnumRows, overridden, chunkSize(opts.ChunkRows), opts.Generate, func(rows []map[string]interface{}) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if opts.OnRows != nil {
					if err := opts.OnRows(tableName, rows); err != nil {
						return err
					}
				}
				if !opts.DryRun {
					err := safego.Run("write "+tableName, func() error {
						if err := faultinject.Hit(ctx, "write", tableName); err != nil {
							return err
						}
						return insertStrict(ctx, conn, dbType, tableName, rows, opts.BatchSize)
					})
					if err != nil {
						return runerr.At(runerr.PhaseWrite, tableName, err)
					}
				}
				tally.written(tableName, len(rows))
				return nil
			})
		})
		if err != nil {
			return tally.result(), runerr.At(runerr.PhaseGenerate, tableName, err)
		}
		releases.generated(stream, tableName)
		tally.done(tableName)
	}
	return tally.result(), nil
}

func seedConcurrent(ctx context.Context, conn *sql.DB, dbType string, sc *schema.Schema, stream *faker.Stream, tables []string, opts SeedOptions, tally *seedTally) (SeedResult, error) {
	chunk := chunkSize(opts.ChunkRows)
	generators := 1
	if opts.GenWorkers > 1 && opts.OnRows == nil && !opts.Reproducible {
		// Never more generators than the CPU quota allows (a container's --cpus,
		// not the host's cores).
		generators = min(tuning.ClampGenerators(opts.GenWorkers), len(tables))
		// Every generator holds a chunk: they share one chunk's worth of memory.
		opts.Generate.ChunkBytes = max(opts.Generate.ChunkBytes/generators, minGeneratorChunkBytes)
		opts.Generate.OnWarning = serialized(opts.Generate.OnWarning)
	}
	// Queued and in-flight rows hold about one chunk of memory (by rows when
	// the byte cap is off). Two chunks peaked near 300MB on slow MySQL writes:
	// the queue stays full there and the GC keeps up to twice the live heap.
	queue := opts.Generate.ChunkBytes * generators
	if queue <= 0 {
		queue = chunk * approxRowMemory
	}
	opts.Workers = clampToServer(ctx, conn, dbType, opts.Workers, generators, opts.OnNotice)
	// The run never holds more connections than it uses: writers, generators
	// reading parent keys, and one for sequences.
	conn.SetMaxOpenConns(opts.Workers + generators + 1)
	w := newWriter(ctx, conn, dbType, opts.BatchSize, opts.Workers, queue)
	// A queued row costs at least half an average row of a full chunk, so
	// narrow rows queue at most about two chunks of rows: 300k narrow rows with
	// 4 writers peaked at 157MB on the byte budget alone, 117MB with this, at
	// the same speed (one chunk saved 10MB more but ran 7% slower).
	w.minRowCharge = queue / max(2*chunk*generators, 1)
	w.onWritten = tally.written
	w.onDone = tally.done

	run := make(map[string]bool, len(tables))
	for _, t := range tables {
		run[t] = true
	}
	// Writers open in run order before any row exists, so every table waits
	// for the tables before it that it references, whichever generates first.
	writers := make(map[string]*tableWriter, len(tables))
	for _, tableName := range tables {
		names, selfRef := referencedTables(sc, tableName, run)
		var parents []*tableWriter
		for _, p := range names {
			// A parent later in the order (FK ordering disabled, or a nullable
			// near-cycle) is not waited for: its rows may not exist yet whatever
			// the writer does, and the generator saw no keys from it.
			if pw, ok := writers[p]; ok {
				parents = append(parents, pw)
			}
		}
		writers[tableName] = w.open(tableName, parents, selfRef)
	}

	generate := func(gen *faker.Stream, tableName string) error {
		tw := writers[tableName]
		defer tw.close()
		if err := faultinject.Hit(w.ctx, "generate", tableName); err != nil {
			return err
		}
		if opts.OnTableStart != nil {
			if err := opts.OnTableStart(tableName); err != nil {
				return err
			}
		}
		count, overridden := faker.TableRowCount(tableName, opts.Rows, opts.TableRows)
		return gen.GenerateChunks(tableName, count, opts.EnumRows, overridden, chunk, opts.Generate, func(rows []map[string]interface{}) error {
			if opts.OnRows != nil {
				if err := opts.OnRows(tableName, rows); err != nil {
					return err
				}
			}
			return tw.submit(rows)
		})
	}
	genErr := generateTables(w.ctx, stream, sc, tables, generators, generate, newPoolReleases(sc, tables))
	if genErr != nil {
		w.abort(genErr)
	}
	if err := w.wait(); err != nil {
		return tally.result(), err
	}
	return tally.result(), nil
}

// minGeneratorChunkBytes keeps many generators from shrinking chunks to a
// handful of rows.
const minGeneratorChunkBytes = 4 << 20

// generateTables runs generate for every table, in order when generators is 1.
// With more, tables generate concurrently on forks of the stream, and a table
// starts only after (a) every earlier table it references has generated, so
// its key pools are final, and (b) every earlier table that references it has
// generated, so that table saw none of its keys (a nullable near-cycle stays
// NULL exactly as in order). Tables that meet neither condition overlap. The
// first error stops tables that have not started; running ones stop at their
// next chunk because ctx is cancelled by the caller.
func generateTables(ctx context.Context, stream *faker.Stream, sc *schema.Schema, tables []string, generators int, generate func(*faker.Stream, string) error, releases *poolReleases) error {
	if generators <= 1 {
		for _, tableName := range tables {
			if err := safego.Run("generate "+tableName, func() error { return generate(stream, tableName) }); err != nil {
				return runerr.At(runerr.PhaseGenerate, tableName, err)
			}
			releases.generated(stream, tableName)
		}
		return nil
	}
	deps := generationDeps(sc, tables)
	done := make([]chan struct{}, len(tables))
	for i := range done {
		done[i] = make(chan struct{})
	}
	slots := make(chan struct{}, generators)
	gctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var wg sync.WaitGroup
	for i, tableName := range tables {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(done[i])
			for _, d := range deps[i] {
				select {
				case <-done[d]:
				case <-gctx.Done():
					return
				}
			}
			select {
			case slots <- struct{}{}:
			case <-gctx.Done():
				return
			}
			defer func() { <-slots }()
			if gctx.Err() != nil {
				return
			}
			fork := stream.ForkTable(tableName)
			// Its own random source: forks sharing the global one spent their
			// time waiting on its lock (4 generators ran slower than 1).
			fork.UseOwnRandom()
			if err := safego.Run("generate "+tableName, func() error { return generate(fork, tableName) }); err != nil {
				cancel(runerr.At(runerr.PhaseGenerate, tableName, err))
				return
			}
			stream.MergeTable(fork, tableName)
			releases.generated(stream, tableName)
		}()
	}
	wg.Wait()
	if err := context.Cause(gctx); err != nil && err != context.Canceled {
		return err
	}
	return ctx.Err()
}

// poolReleases frees a table's key pool once the table and every table of the
// run that references it have generated (see faker.Stream.ReleaseTable).
type poolReleases struct {
	mu      sync.Mutex
	waiting map[string]int      // table -> referencing tables not yet generated
	readers map[string][]string // table -> tables it references
	done    map[string]bool
}

func newPoolReleases(sc *schema.Schema, tables []string) *poolReleases {
	r := &poolReleases{waiting: map[string]int{}, readers: map[string][]string{}, done: map[string]bool{}}
	run := make(map[string]bool, len(tables))
	for _, t := range tables {
		run[t] = true
	}
	for _, t := range tables {
		parents, _ := referencedTables(sc, t, run)
		r.readers[t] = parents
		for _, p := range parents {
			r.waiting[p]++
		}
	}
	return r
}

// generated records that table finished generating and releases every pool
// that nothing left in the run can read.
func (r *poolReleases) generated(stream *faker.Stream, table string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done[table] = true
	if r.waiting[table] == 0 {
		stream.ReleaseTable(table)
	}
	for _, p := range r.readers[table] {
		r.waiting[p]--
		if r.waiting[p] == 0 && r.done[p] {
			stream.ReleaseTable(p)
		}
	}
}

// generationDeps lists, per table index, the earlier tables it must wait for:
// earlier tables it references and earlier tables that reference it.
func generationDeps(sc *schema.Schema, tables []string) [][]int {
	index := make(map[string]int, len(tables))
	for i, t := range tables {
		index[t] = i
	}
	deps := make([][]int, len(tables))
	seen := make([]map[int]bool, len(tables))
	for i := range seen {
		seen[i] = map[int]bool{}
	}
	add := func(later, earlier int) {
		if !seen[later][earlier] {
			seen[later][earlier] = true
			deps[later] = append(deps[later], earlier)
		}
	}
	for i, tableName := range tables {
		for _, colName := range sortedColumns(sc.Tables[tableName]) {
			parent, _, ok := strings.Cut(sc.Tables[tableName].Columns[colName].FK, ".")
			j, inRun := index[parent]
			if !ok || !inRun || j == i {
				continue
			}
			if j < i {
				add(i, j) // i reads j's keys
			} else {
				add(j, i) // i ran first and must see none of j's keys
			}
		}
	}
	for i := range deps {
		sort.Ints(deps[i])
	}
	return deps
}

// serialized wraps a callback so concurrent generators never call it at once.
func serialized(fn func(faker.GenerationWarning)) func(faker.GenerationWarning) {
	if fn == nil {
		return nil
	}
	var mu sync.Mutex
	return func(w faker.GenerationWarning) {
		mu.Lock()
		defer mu.Unlock()
		fn(w)
	}
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

// connectionUsage reads the server's connection limit and use; a variable so
// tests can stand in for a busy server.
var connectionUsage = db.ConnectionUsage

// clampToServer lowers writers when the server lacks free connections for the
// run, and says so. Any error reading the limit leaves writers unchanged.
func clampToServer(ctx context.Context, conn *sql.DB, dbType string, writers, generators int, notice func(string)) int {
	maxConns, used, err := connectionUsage(ctx, conn, dbType)
	if err != nil {
		return writers
	}
	clamped := tuning.ClampWriters(writers, generators, maxConns, used)
	if clamped < writers && notice != nil {
		notice(fmt.Sprintf("Using %d writers instead of %d: the server has %d of %d connections in use", clamped, writers, used, maxConns))
	}
	return clamped
}
