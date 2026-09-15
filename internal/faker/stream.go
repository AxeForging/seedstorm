package faker

import (
	"database/sql"
	"fmt"
	"math"

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
	// sampled marks preloaded tables whose pool is a sample of the stored keys.
	sampled map[string]bool
	// sinceDraw counts rows generated per table since its parents' samples
	// were last drawn.
	sinceDraw map[string]int
	// offset is how many rows value rules have already numbered, per table.
	offset map[string]int
}

// NewStream reads existing PKs of allTables and the stored keys of
// targetTables. With a nil conn nothing is read and the stream starts empty. overrides are the value rules
// that will be applied, which decide what stored UNIQUE values must be read.
func NewStream(s *schema.Schema, allTables, targetTables []string, conn *sql.DB, dbType string, overrides Overrides) (*Stream, error) {
	g := &Stream{
		sc: s, pks: make(map[string][]interface{}), cursor: make(map[string]int),
		conn: conn, dbType: dbType, sampled: make(map[string]bool), sinceDraw: make(map[string]int), offset: make(map[string]int),
	}
	preloaded := make(map[string]bool, len(targetTables))
	if conn != nil {
		if err := queryExistingPKs(conn, allTables, s.Tables, g.pks, dbType, g.sampled); err != nil {
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
	if g.existing, err = loadExistingState(conn, targetTables, s.Tables, dbType, preloaded, overrides); err != nil {
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
	if chunk <= 0 {
		chunk = math.MaxInt
	}
	keys := g.existing.keysFor(tableName)
	trackKeys := keys != nil && pkColumnCount(table) > 0
	var buf []map[string]interface{}
	preloaded := len(g.pks[tableName])

	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		out, err := g.finalize(tableName, table, buf, preloaded, opts)
		buf = nil
		if err != nil {
			return err
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
			}
			k := min(n, chunk-len(buf))
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
			if len(buf) >= chunk {
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
	_, _, composite, err := enumerateCompositeFKPKRows(scratch, g.pks, table, tableName, 0, keys, g.cursor[tableName])
	switch {
	case err != nil:
		return fmt.Errorf("table %s: %w", tableName, err)
	case enumCol != "" && enumRows > 0 && !hasRowOverride:
		for _, v := range enumVals {
			vals := []string{v}
			err := add(enumRows, func(piece map[string][]map[string]interface{}, k int) error {
				return generateEnumRows(piece, g.pks, table, tableName, enumCol, vals, k, keys)
			}, nil)
			if err != nil {
				return err
			}
		}
	case composite:
		generated := 0
		err := add(rows, func(piece map[string][]map[string]interface{}, k int) error {
			n, next, _, err := enumerateCompositeFKPKRows(piece, g.pks, table, tableName, k, keys, g.cursor[tableName])
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
			return generateStandardRows(piece, g.pks, table, tableName, k, keys)
		}, count)
		if err != nil {
			return err
		}
		if topUp {
			counts = countEnumValues(counts, enumCols, nil)
			err := enumTopUp(enumCols, rows, counts, func(col, val string, n int) error {
				vals := []string{val}
				return add(n, func(piece map[string][]map[string]interface{}, k int) error {
					return generateEnumRows(piece, g.pks, table, tableName, col, vals, k, keys)
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
	if kept, dropped := enforceUniqueGroups(rows, table, opts.Overrides[tableName], existing.uniqueFor(tableName)); len(dropped) > 0 {
		requested := len(rows)
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
	g.sinceDraw[tableName] += len(rows)
	g.pks[tableName] = capPool(g.pks[tableName], poolLimit)
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
		if err := queryExistingPKs(g.conn, []string{parent}, g.sc.Tables, g.pks, g.dbType, nil); err != nil {
			return fmt.Errorf("table %s: redraw %s keys: %w", tableName, parent, err)
		}
	}
	return nil
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
