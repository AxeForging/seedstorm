package faker

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// existingState describes rows already present in the target tables, so a run
// against a populated database appends instead of colliding with them. Key sets
// are bounded (see keySet), so a table of any size fits in memory.
type existingState struct {
	// keys holds the primary-key tuple of every existing row, per table, in the
	// same normalized form compositePKKey produces for generated rows. Tables
	// whose new keys cannot collide with stored ones have no set (see
	// keysCannotCollide).
	keys map[string]*keySet
	// seqStart is the index a UNIQUE sequence column continues from, per table
	// and column, derived from the column's current maximum.
	seqStart map[string]map[string]int
	// unique holds stored tuples per table and UNIQUE group.
	unique map[string]map[string]*keySet
}

func (e existingState) uniqueFor(table string) map[string]*keySet {
	if e.unique == nil {
		return nil
	}
	return e.unique[table]
}

func (e existingState) keysFor(table string) *keySet {
	if e.keys == nil {
		return nil
	}
	return e.keys[table]
}

func (e existingState) seqStartFor(table string) map[string]int {
	if e.seqStart == nil {
		return nil
	}
	return e.seqStart[table]
}

// record marks the UNIQUE tuples of generated rows as taken, so a later chunk
// of the same table avoids them just like stored rows. Primary keys are
// recorded as rows are generated (see Stream.GenerateChunks). Only sets that
// were loaded are extended.
func (e existingState) record(table schema.Table, tableName string, rows []map[string]interface{}) {
	for _, group := range validGroups(table) {
		set := e.uniqueFor(tableName)[groupID(group)]
		if set == nil {
			continue
		}
		for _, row := range rows {
			if !tupleHasNull(group, row) {
				set.Add(uniqueTupleKey(table, group, row))
			}
		}
	}
}

// advanceSequences moves UNIQUE sequence columns past the n values just
// assigned, whether or not every row survives.
func (e existingState) advanceSequences(table schema.Table, tableName string, n int) {
	starts := e.seqStartFor(tableName)
	if starts == nil {
		return
	}
	for colName, col := range table.Columns {
		if col.Faker == uniqueSequenceFaker {
			starts[colName] += n
		}
	}
}

// loadExistingState reads existing primary keys, UNIQUE tuples and sequence
// maxima for the tables about to be generated. preloaded names tables whose PK
// pools were read (their maximum id is known); overrides are the value rules.
func loadExistingState(ctx context.Context, conn *sql.DB, targetTables []string, tables map[string]schema.Table, dbType string, preloaded map[string]bool, overrides Overrides) (existingState, error) {
	state := existingState{
		keys:     make(map[string]*keySet),
		seqStart: make(map[string]map[string]int),
		unique:   make(map[string]map[string]*keySet),
	}
	for _, tableName := range targetTables {
		table, ok := tables[tableName]
		if !ok {
			continue
		}
		if !preloaded[tableName] || !keysCannotCollide(table) {
			keys, err := loadExistingKeys(ctx, conn, tableName, table, dbType)
			if err != nil {
				return state, err
			}
			if keys != nil {
				state.keys[tableName] = keys
			}
		}
		tuples, err := loadUniqueTuples(ctx, conn, tableName, table, dbType, overrides[tableName])
		if err != nil {
			return state, err
		}
		state.unique[tableName] = tuples
		starts, err := loadSequenceStarts(ctx, conn, tableName, table, dbType)
		if err != nil {
			return state, err
		}
		state.seqStart[tableName] = starts
	}
	return state, nil
}

// keysCannotCollide reports whether new primary keys are guaranteed distinct
// from stored ones without looking them up: a single integer key continues
// past the stored maximum, and a uuid key is random. Keys that are also foreign
// keys, strings of limited length or temporal values can repeat.
func keysCannotCollide(table schema.Table) bool {
	pkCols := sortedPKColumns(table)
	if len(pkCols) != 1 {
		return false
	}
	col := table.Columns[pkCols[0]]
	if col.FK != "" {
		return false
	}
	t := strings.ToLower(strings.TrimSpace(col.Type))
	if t == "uuid" {
		return true
	}
	return !strings.Contains(t, "char") && !strings.Contains(t, "text") && temporalPKFaker(t) == "" && isIntegerType(t)
}

func isIntegerType(t string) bool {
	for _, name := range []string{"int", "serial"} {
		if strings.Contains(t, name) {
			return true
		}
	}
	return false
}

func loadExistingKeys(ctx context.Context, conn *sql.DB, tableName string, table schema.Table, dbType string) (*keySet, error) {
	pkCols := sortedPKColumns(table)
	if len(pkCols) == 0 {
		return nil, nil
	}
	keys := newKeySet(0)
	err := scanStoredRows(ctx, conn, tableName, pkCols, dbType, func(row map[string]interface{}) {
		keys.Add(compositePKKey(row, table))
	})
	return keys, err
}

// loadUniqueTuples reads stored tuples of every UNIQUE group. A lone sequence
// column no rule rewrites is skipped: new values continue past its maximum.
func loadUniqueTuples(ctx context.Context, conn *sql.DB, tableName string, table schema.Table, dbType string, overrides map[string]ColumnOverride) (map[string]*keySet, error) {
	out := make(map[string]*keySet)
	for _, group := range validGroups(table) {
		if len(group) == 1 && table.Columns[group[0]].Faker == uniqueSequenceFaker {
			if _, ruled := overrides[group[0]]; !ruled {
				continue
			}
		}
		tuples := newKeySet(0)
		err := scanStoredRows(ctx, conn, tableName, group, dbType, func(row map[string]interface{}) {
			if !tupleHasNull(group, row) {
				tuples.Add(uniqueTupleKey(table, group, row))
			}
		})
		if err != nil {
			return nil, err
		}
		out[groupID(group)] = tuples
	}
	return out, nil
}

// scanStoredRows reads the given columns of every stored row, normalising
// driver values, and hands each row to fn. A nil conn has no stored rows.
func scanStoredRows(ctx context.Context, conn *sql.DB, tableName string, cols []string, dbType string, fn func(map[string]interface{})) error {
	if conn == nil {
		return nil
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = db.QuoteIdent(c, dbType)
	}
	query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(quoted, ", "), db.QuoteIdent(tableName, dbType)) //nolint:gosec
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("read existing rows of %s: %w", tableName, err)
	}
	defer rows.Close()
	for rows.Next() {
		values := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		row := make(map[string]interface{}, len(cols))
		for i, c := range cols {
			row[c] = normalizeScanned(values[i])
		}
		fn(row)
	}
	return rows.Err()
}

func loadSequenceStarts(ctx context.Context, conn *sql.DB, tableName string, table schema.Table, dbType string) (map[string]int, error) {
	starts := make(map[string]int)
	for _, colName := range sortedColumnNames(table) {
		col := table.Columns[colName]
		if col.Faker != uniqueSequenceFaker {
			continue
		}
		if conn == nil {
			starts[colName] = 0
			continue
		}
		var maxVal interface{}
		query := fmt.Sprintf("SELECT MAX(%s) FROM %s", db.QuoteIdent(colName, dbType), db.QuoteIdent(tableName, dbType)) //nolint:gosec
		if err := conn.QueryRowContext(ctx, query).Scan(&maxVal); err != nil {
			return nil, fmt.Errorf("read max %s.%s: %w", tableName, colName, err)
		}
		starts[colName] = sequenceStartAfter(col.Type, normalizeScanned(maxVal))
	}
	return starts, nil
}

// sequenceStartAfter returns the sequence index that yields a value strictly
// greater than maxVal for a column of the given type (see uniqueSequenceValue).
// A nil or unparseable max starts from 0.
func sequenceStartAfter(colType string, maxVal interface{}) int {
	if maxVal == nil {
		return 0
	}
	switch temporalPKFaker(strings.ToLower(strings.TrimSpace(colType))) {
	case "date":
		if t, ok := asTime(maxVal, "2006-01-02"); ok {
			return clampStart(int(t.Sub(seqBaseTime).Hours()/24) + 1)
		}
	case "time":
		if t, ok := asTime(maxVal, "15:04:05"); ok {
			return clampStart(t.Hour()*3600 + t.Minute()*60 + t.Second() + 1)
		}
	case "datetime":
		if t, ok := asTime(maxVal, "2006-01-02 15:04:05"); ok {
			return clampStart(int(t.Sub(seqBaseTime).Seconds()) + 1)
		}
	default:
		if n, ok := asInt(maxVal); ok {
			// uniqueSequenceValue emits i+1, so index n yields n+1.
			return clampStart(int(n))
		}
	}
	return 0
}

func clampStart(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// nextSequentialPK returns the count argument for generatePK so the next
// integer id lands after both the pool size and the largest id already in the
// pool. Preloaded pools are sorted ascending, and generated ids only grow, so
// the last element is the maximum.
func nextSequentialPK(pool []interface{}) int {
	next := len(pool)
	if len(pool) == 0 {
		return next
	}
	if last, ok := asInt(pool[len(pool)-1]); ok && int(last) > next {
		next = int(last)
	}
	return next
}

// sortIntPool orders a pool ascending when every value is an integer, which
// lets nextSequentialPK read the maximum in O(1).
func sortIntPool(pool []interface{}) {
	ints := make([]int64, len(pool))
	for i, v := range pool {
		n, ok := asInt(v)
		if !ok {
			return
		}
		ints[i] = n
	}
	sort.SliceStable(pool, func(i, j int) bool {
		a, _ := asInt(pool[i])
		b, _ := asInt(pool[j])
		return a < b
	})
}

// normalizeScanned converts driver-specific scan results into the Go types the
// generator produces, so keys and FK values compare equal across sources.
// MySQL's text protocol returns []byte for every column.
func normalizeScanned(v interface{}) interface{} {
	b, ok := v.([]byte)
	if !ok {
		return v
	}
	s := string(b)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	return s
}

func asInt(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint32:
		return int64(n), true
	case uint64:
		return int64(n), true
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	}
	return 0, false
}

func asTime(v interface{}, layout string) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t.UTC(), true
	case string:
		for _, l := range []string{layout, time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02", "15:04:05"} {
			if parsed, err := time.Parse(l, t); err == nil {
				return parsed, true
			}
		}
	}
	return time.Time{}, false
}

// keyValue renders one primary-key value for compositePKKey. Temporal values
// arrive as strings from the generator but as time.Time from drivers, so they
// are formatted by column type; everything else uses its natural formatting.
func keyValue(colType string, v interface{}) string {
	switch temporalPKFaker(strings.ToLower(strings.TrimSpace(colType))) {
	case "date":
		if t, ok := asTime(v, "2006-01-02"); ok {
			return t.Format("2006-01-02")
		}
	case "time":
		if t, ok := asTime(v, "15:04:05"); ok {
			return t.Format("15:04:05")
		}
	case "datetime":
		if t, ok := asTime(v, "2006-01-02 15:04:05"); ok {
			return t.Format("2006-01-02 15:04:05")
		}
	}
	if n, ok := asInt(v); ok {
		return strconv.FormatInt(n, 10)
	}
	return fmt.Sprintf("%v", normalizeScanned(v))
}

func sortedPKColumns(table schema.Table) []string {
	cols := make([]string, 0)
	for name, col := range table.Columns {
		if col.PK {
			cols = append(cols, name)
		}
	}
	sort.Strings(cols)
	return cols
}

func sortedColumnNames(table schema.Table) []string {
	cols := make([]string, 0, len(table.Columns))
	for name := range table.Columns {
		cols = append(cols, name)
	}
	sort.Strings(cols)
	return cols
}
