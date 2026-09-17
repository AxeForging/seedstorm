package faker

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sync"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// Stream keeps what generation learns from the database (PK pools, stored
// keys, UNIQUE tuples, sequence positions) between calls. Loading happens once,
// so a table filled in many chunks costs one read of the database instead of
// one per chunk, and every structure it holds is bounded: pools are sampled
// (poolLimit) and key sets switch to Bloom filters (DefaultExactKeys).
type Stream struct {
	sc       *schema.Schema
	pks      map[string][]interface{}
	existing existingState
	// cursor is where junction-key enumeration stopped, per table.
	cursor map[string]int

	conn   *sql.DB
	dbType string
	// ctx stops database reads (preloads, pool redraws) when a run is cancelled.
	ctx context.Context
	// sampled marks preloaded tables whose pool is a sample of the stored keys.
	sampled map[string]bool
	// sinceDraw counts rows generated per table since its parents' samples
	// were last drawn.
	sinceDraw map[string]int
	// offset is how many rows value rules have already numbered, per table.
	offset map[string]int
	// refCols lists, per table, columns FKs reference that are not its single
	// primary key; their values are pooled under "table.column".
	refCols map[string][]string
	// gen draws random values; a fork may switch to its own (UseOwnRandom).
	gen generator
	// mu guards the maps above while forks are taken and merged (ForkTable).
	mu sync.Mutex
}

// NewStream reads existing PKs of allTables and the stored keys of
// targetTables. With a nil conn nothing is read and the stream starts empty. overrides are the value rules
// that will be applied, which decide what stored UNIQUE values must be read.
func NewStream(s *schema.Schema, allTables, targetTables []string, conn *sql.DB, dbType string, overrides Overrides) (*Stream, error) {
	return NewStreamContext(context.Background(), s, allTables, targetTables, conn, dbType, overrides)
}

// NewStreamContext is NewStream whose database reads stop when ctx ends.
func NewStreamContext(ctx context.Context, s *schema.Schema, allTables, targetTables []string, conn *sql.DB, dbType string, overrides Overrides) (*Stream, error) {
	if err := CheckSeedable(s, targetTables, overrides); err != nil {
		return nil, err
	}
	g := &Stream{
		sc: s, pks: make(map[string][]interface{}), cursor: make(map[string]int),
		conn: conn, dbType: dbType, ctx: ctx, sampled: make(map[string]bool), sinceDraw: make(map[string]int), offset: make(map[string]int),
		gen: defaultGen, refCols: referencedColumns(s),
	}
	for table, cols := range g.refCols {
		for _, col := range cols {
			g.pks[referencePoolKey(table, col)] = []interface{}{}
		}
	}
	preloaded := make(map[string]bool, len(targetTables))
	if conn != nil {
		if err := g.gen.queryExistingPKs(ctx, conn, allTables, s.Tables, g.pks, dbType, g.sampled); err != nil {
			return nil, err
		}
		if err := g.queryReferencedValues(allTables); err != nil {
			return nil, err
		}
		for _, t := range allTables {
			preloaded[t] = true
		}
	} else {
		// Nothing is stored: every table's pool is complete from the start.
		for _, t := range targetTables {
			preloaded[t] = true
		}
	}
	var err error
	if g.existing, err = loadExistingState(ctx, conn, targetTables, s.Tables, dbType, preloaded, overrides); err != nil {
		return nil, err
	}
	return g, nil
}

// Generate produces rows for targetTables (in topological order). Calling it
// again continues where the previous call left off: new keys, UNIQUE values
// and sequences never repeat those of earlier calls.
func (g *Stream) Generate(targetTables []string, rows, enumRows int, tableRows map[string]int, opts GenerateOptions) (map[string][]map[string]interface{}, error) {
	data := make(map[string][]map[string]interface{}, len(targetTables))
	for _, tableName := range targetTables {
		data[tableName] = nil
		count, hasRowOverride := TableRowCount(tableName, rows, tableRows)
		err := g.GenerateChunks(tableName, count, enumRows, hasRowOverride, 0, opts, func(chunk []map[string]interface{}) error {
			data[tableName] = append(data[tableName], chunk...)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return data, nil
}

// TableRowCount resolves how many rows a table gets: its tableRows entry when
// positive, else rows. overridden reports an entry exists at all, which turns
// off enum coverage rows for that table.
func TableRowCount(tableName string, rows int, tableRows map[string]int) (count int, overridden bool) {
	n, overridden := tableRows[tableName]
	if n > 0 {
		return n, overridden
	}
	return rows, overridden
}

// GenerateChunks produces one table's rows and hands them to emit at most chunk
// rows at a time (0: a single chunk), so memory holds one chunk however large
// the table. Row counts, enum coverage, junction keys, UNIQUE values and
// {{seq}} match a single Generate call. emit sees final rows: rules applied,
// UNIQUE enforced, self-references resolved.
func (g *Stream) GenerateChunks(tableName string, rows, enumRows int, hasRowOverride bool, chunk int, opts GenerateOptions, emit func([]map[string]interface{}) error) error {
	opts = normalizeOptions(opts)
	table := g.sc.Tables[tableName]
	if len(opts.Shapes) > 0 {
		if g.gen.shapes == nil {
			g.gen.shapes = newShaper()
		}
		total := rows
		if enumCol, enumVals := findEnumColumn(withoutColumns(table, opts.Overrides[tableName])); enumCol != "" && enumRows > 0 && !hasRowOverride {
			total = len(enumVals) * enumRows
		}
		if t, ok := opts.ShapeTotals[tableName]; ok && t > 0 {
			total = t
		}
		g.gen.shapes.beginTable(g.sc, tableName, total, opts)
	}
	if chunk <= 0 {
		chunk = math.MaxInt
	}
	keys := g.existing.keysFor(tableName)
	trackKeys := keys != nil && pkColumnCount(table) > 0
	var buf []map[string]interface{}
	preloaded := len(g.pks[tableName])
	// limit is the row count of the chunk being built. With ChunkBytes the
	// first chunk holds at most firstChunkRows; rows are measured once final
	// (value rules can make them much wider than generated) and later chunks
	// are sized from that. Tables no larger than firstChunkRows chunk exactly
	// as without ChunkBytes.
	limit, rowMemory := chunk, 0
	if opts.ChunkBytes > 0 {
		limit = min(chunk, firstChunkRows)
	}

	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		out, err := g.finalize(tableName, table, buf, preloaded, opts)
		buf = nil
		if err != nil {
			return err
		}
		if opts.ChunkBytes > 0 && len(out) > 0 {
			sample := out[:min(len(out), chunkSampleRows)]
			rowMemory = max(1, db.RowsMemory(sample)/len(sample))
		}
		if err := emit(out); err != nil {
			return err
		}
		preloaded = len(g.pks[tableName])
		return nil
	}
	// add generates n raw rows through gen in pieces that fit the chunk,
	// flushing full chunks. It stops early when gen produces fewer rows than
	// asked (a finite key space).
	add := func(n int, gen func(piece map[string][]map[string]interface{}, k int) error, seenRows func([]map[string]interface{})) error {
		for n > 0 {
			if len(buf) == 0 {
				// Parent samples change only between chunks, never under a buffer
				// whose rows already reference them.
				if err := g.redrawParents(tableName, table); err != nil {
					return err
				}
				preloaded = len(g.pks[tableName])
				if rowMemory > 0 {
					limit = chunkLimit(chunk, opts.ChunkBytes, rowMemory)
				}
			}
			k := min(n, limit-len(buf))
			piece := map[string][]map[string]interface{}{tableName: nil}
			if err := gen(piece, k); err != nil {
				return fmt.Errorf("table %s: %w", tableName, err)
			}
			got := piece[tableName]
			if trackKeys {
				for _, row := range got {
					keys.Add(compositePKKey(row, table))
				}
			}
			if seenRows != nil {
				seenRows(got)
			}
			buf = append(buf, got...)
			n -= len(got)
			if len(buf) >= limit {
				if err := flush(); err != nil {
					return err
				}
			}
			if len(got) < k {
				return nil
			}
		}
		return nil
	}

	// Columns rewritten by a value rule no longer carry their enum values, so
	// enum coverage must not add rows for them.
	enumView := withoutColumns(table, opts.Overrides[tableName])
	enumCol, enumVals := findEnumColumn(enumView)
	var scratch map[string][]map[string]interface{}
	_, _, composite, err := g.gen.enumerateCompositeFKPKRows(scratch, g.pks, table, tableName, 0, keys, g.cursor[tableName])
	switch {
	case err != nil:
		return fmt.Errorf("table %s: %w", tableName, err)
	case enumCol != "" && enumRows > 0 && !hasRowOverride:
		for _, v := range enumVals {
			vals := []string{v}
			err := add(enumRows, func(piece map[string][]map[string]interface{}, k int) error {
				return g.gen.generateEnumRows(piece, g.pks, table, tableName, enumCol, vals, k, keys)
			}, nil)
			if err != nil {
				return err
			}
		}
	case composite:
		generated := 0
		err := add(rows, func(piece map[string][]map[string]interface{}, k int) error {
			n, next, _, err := g.gen.enumerateCompositeFKPKRows(piece, g.pks, table, tableName, k, keys, g.cursor[tableName])
			g.cursor[tableName] = next
			generated += n
			return err
		}, nil)
		if err != nil {
			return err
		}
		if generated < rows && opts.OnWarning != nil {
			opts.OnWarning(GenerationWarning{Table: tableName, Requested: rows, Generated: generated, Reason: "finite FK primary-key combinations"})
		}
	default:
		// Guarantee every enum value appears at least the requested row count,
		// independently per column, counting across every chunk.
		enumCols := findAllEnumColumns(enumView)
		topUp := len(enumCols) > 0 && !hasRowOverride
		var counts map[string]map[string]int
		count := func(rows []map[string]interface{}) {
			if topUp {
				counts = countEnumValues(counts, enumCols, rows)
			}
		}
		err := add(rows, func(piece map[string][]map[string]interface{}, k int) error {
			return g.gen.generateStandardRows(piece, g.pks, table, tableName, k, keys)
		}, count)
		if err != nil {
			return err
		}
		if topUp {
			counts = countEnumValues(counts, enumCols, nil)
			err := enumTopUp(enumCols, rows, counts, func(col, val string, n int) error {
				vals := []string{val}
				return add(n, func(piece map[string][]map[string]interface{}, k int) error {
					return g.gen.generateEnumRows(piece, g.pks, table, tableName, col, vals, k, keys)
				}, count)
			})
			if err != nil {
				return fmt.Errorf("table %s enum top-up: %w", tableName, err)
			}
		}
	}
	return flush()
}

// finalize turns raw rows into insertable ones: UNIQUE sequences, value rules,
// multi-column UNIQUE enforcement and self-references, then records what the
// rows used so later chunks avoid it.
func (g *Stream) finalize(tableName string, table schema.Table, rows []map[string]interface{}, preloaded int, opts GenerateOptions) ([]map[string]interface{}, error) {
	existing := g.existing
	assignUniqueSequences(rows, table, existing.seqStartFor(tableName))
	existing.advanceSequences(table, tableName, len(rows))
	offset := opts.RowOffset[tableName] + g.offset[tableName]
	g.offset[tableName] += len(rows)
	if err := applyOverrides(rows, table, tableName, opts.Overrides[tableName], offset); err != nil {
		return nil, err
	}
	// Multi-column UNIQUE groups are checked once values are final (rules
	// included); dropped rows leave the PK pool before children or
	// self-references can point at them.
	if kept, dropped := g.gen.enforceUniqueGroups(rows, table, opts.Overrides[tableName], existing.uniqueFor(tableName)); len(dropped) > 0 {
		requested := len(rows)
		g.gen.shapes.returnRows(tableName, droppedRows(rows, kept))
		rows = kept
		rebuildPKPool(g.pks, tableName, table, preloaded, kept)
		if opts.OnWarning != nil {
			opts.OnWarning(GenerationWarning{Table: tableName, Requested: requested, Generated: len(kept), Reason: droppedSummary(dropped)})
		}
	}
	if err := backfillSelfReferences(rows, table, tableName, opts.SelfRefDepth); err != nil {
		return nil, fmt.Errorf("table %s self-reference backfill: %w", tableName, err)
	}
	existing.record(table, tableName, rows)
	g.recordReferencedValues(tableName, rows)
	g.sinceDraw[tableName] += len(rows)
	g.pks[tableName] = capPool(g.pks[tableName], poolLimit, g.gen.rnd)
	return rows, nil
}

// redrawParents replaces sampled parent pools with a fresh sample once a table
// has generated poolLimit rows from the current one, so children of a large
// parent spread over all of it instead of the first sample. Junction tables
// keep their pools: their enumeration cursor depends on them.
func (g *Stream) redrawParents(tableName string, table schema.Table) error {
	if g.conn == nil || g.sinceDraw[tableName] < poolLimit || keyIsAllForeign(table) {
		return nil
	}
	g.sinceDraw[tableName] = 0
	drawn := map[string]bool{}
	for _, colName := range sortedColumnNames(table) {
		parent, _ := splitFK(table.Columns[colName].FK)
		if parent == "" || parent == tableName || !g.sampled[parent] || drawn[parent] {
			continue
		}
		drawn[parent] = true
		g.pks[parent] = nil
		if err := g.gen.queryExistingPKs(g.ctx, g.conn, []string{parent}, g.sc.Tables, g.pks, g.dbType, nil); err != nil {
			return fmt.Errorf("table %s: redraw %s keys: %w", tableName, parent, err)
		}
		for _, col := range g.refCols[parent] {
			g.pks[referencePoolKey(parent, col)] = []interface{}{}
		}
		if err := g.queryReferencedValues([]string{parent}); err != nil {
			return fmt.Errorf("table %s: redraw %s values: %w", tableName, parent, err)
		}
	}
	return nil
}

// firstChunkRows caps the first chunk of a table when chunks are bounded by
// memory: its rows are measured to size the chunks after it.
const firstChunkRows = 2048

// chunkSampleRows is how many rows of a chunk are measured.
const chunkSampleRows = 256

// minChunkRows keeps very wide rows from degrading to one statement per row.
const minChunkRows = 32

// chunkLimit returns how many rows a chunk holds: chunk, or fewer when rows of
// rowMemory bytes would exceed chunkBytes, but never under minChunkRows.
func chunkLimit(chunk, chunkBytes, rowMemory int) int {
	if chunkBytes <= 0 || rowMemory <= 0 {
		return chunk
	}
	return max(min(chunk, chunkBytes/rowMemory), min(chunk, minChunkRows))
}

// keyIsAllForeign reports whether every primary-key column is a foreign key,
// the tables generateCompositeFKPKRows enumerates.
func keyIsAllForeign(table schema.Table) bool {
	pkCols := sortedPKColumns(table)
	for _, c := range pkCols {
		if table.Columns[c].FK == "" {
			return false
		}
	}
	return len(pkCols) > 0
}

// ForkTable returns a stream that generates tableName on its own goroutine:
// it shares what nothing else writes during generation (schema, stored keys and
// UNIQUE tuples, which are kept per table) and copies the rest for this table
// and the tables it references, whose pools must be final by then. Generate the
// table only on the fork, then MergeTable it back. Tables generated on forks at
// the same time must not reference each other.
func (g *Stream) ForkTable(tableName string) *Stream {
	g.mu.Lock()
	defer g.mu.Unlock()
	child := &Stream{
		sc: g.sc, existing: g.existing, conn: g.conn, dbType: g.dbType, ctx: g.ctx, sampled: g.sampled,
		gen:       generator{rnd: g.gen.rnd, shapes: g.gen.shapes.fork(tableName)},
		pks:       make(map[string][]interface{}),
		cursor:    map[string]int{tableName: g.cursor[tableName]},
		sinceDraw: map[string]int{tableName: g.sinceDraw[tableName]},
		offset:    map[string]int{tableName: g.offset[tableName]},
		refCols:   g.refCols,
	}
	if pool, ok := g.pks[tableName]; ok {
		child.pks[tableName] = pool
	}
	for _, col := range g.refCols[tableName] {
		key := referencePoolKey(tableName, col)
		child.pks[key] = g.pks[key]
	}
	for _, colName := range sortedColumnNames(g.sc.Tables[tableName]) {
		fk := g.sc.Tables[tableName].Columns[colName].FK
		parent, _ := splitFK(fk)
		if parent == "" {
			continue
		}
		if pool, ok := g.pks[parent]; ok {
			child.pks[parent] = pool
		}
		if pool, ok := g.pks[fk]; ok {
			child.pks[fk] = pool
		}
	}
	return child
}

// UseOwnRandom gives the stream a private random source, so a fork generating
// on its own goroutine never waits on the global source's lock. Output is then
// not reproducible by seed, which is why only concurrent generation uses it.
func (g *Stream) UseOwnRandom() {
	shapes := g.gen.shapes
	g.gen = privateGenerator()
	g.gen.shapes = shapes
}

// MergeTable records what a fork generated for tableName (its key pool, junction
// cursor, sequence and rule offsets) so later tables see it.
func (g *Stream) MergeTable(child *Stream, tableName string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if pool, ok := child.pks[tableName]; ok {
		g.pks[tableName] = pool
	}
	for _, col := range g.refCols[tableName] {
		key := referencePoolKey(tableName, col)
		g.pks[key] = child.pks[key]
	}
	g.cursor[tableName] = child.cursor[tableName]
	g.sinceDraw[tableName] = child.sinceDraw[tableName]
	g.offset[tableName] = child.offset[tableName]
}

// ReleaseTable drops a table's key pool once nothing left in the run can read
// it: the table is generated and every table referencing it is too. Pools hold
// up to poolLimit keys per table, so a run over many tables otherwise keeps
// every table's keys until it ends.
func (g *Stream) ReleaseTable(tableName string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.pks, tableName)
	for _, col := range g.refCols[tableName] {
		delete(g.pks, referencePoolKey(tableName, col))
	}
}

// Schema returns the schema the stream generates for.
func (g *Stream) Schema() *schema.Schema { return g.sc }

// KeyPoolLen reports how many keys the stream holds for a table (0 once
// released), for tests and diagnostics.
func (g *Stream) KeyPoolLen(tableName string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.pks[tableName])
}

// queryReferencedValues reads the stored values of referenced non-key columns.
func (g *Stream) queryReferencedValues(tables []string) error {
	for _, table := range tables {
		for _, col := range g.refCols[table] {
			if _, err := g.gen.scanPool(g.ctx, g.conn, table, col, referencePoolKey(table, col), g.pks, g.dbType); err != nil {
				return err
			}
		}
	}
	return nil
}
