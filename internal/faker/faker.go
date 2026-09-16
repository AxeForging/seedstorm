package faker

import (
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/schema"
)

const DefaultSelfRefDepth = 2

type GenerateOptions struct {
	SelfRefDepth int
	OnWarning    func(GenerationWarning)
	// Overrides rewrites individual columns after a table's rows are generated
	// (value rules). Columns it names are excluded from enum coverage.
	Overrides Overrides
	// RowOffset shifts the row index overrides see, per table, so a table
	// generated in several chunks keeps {{seq}} increasing across chunks.
	RowOffset map[string]int
	// ChunkBytes caps the estimated memory of one chunk (0: rows only). Wide
	// rows then come in smaller chunks: the first chunk probes the row size
	// and later ones are sized from it, never above the row cap.
	ChunkBytes int
}

type GenerationWarning struct {
	Table     string
	Requested int
	Generated int
	Reason    string
}

func DefaultGenerateOptions() GenerateOptions {
	return GenerateOptions{SelfRefDepth: DefaultSelfRefDepth}
}

func normalizeOptions(opts GenerateOptions) GenerateOptions {
	if opts.SelfRefDepth < 0 {
		opts.SelfRefDepth = 0
	}
	return opts
}

// Generate produces fake data rows for each table, respecting FK ordering.
// If conn is non-nil, existing PKs are read so FKs can reference them.
// dbType is the driver name ("pgx" or "mysql") used to quote SQL identifiers.
func Generate(s *schema.Schema, sortedTables []string, rows, enumRows int, conn *sql.DB, dbType string) (map[string][]map[string]interface{}, error) {
	return GenerateFiltered(s, sortedTables, sortedTables, rows, enumRows, conn, dbType)
}

func GenerateWithOptions(s *schema.Schema, sortedTables []string, rows, enumRows int, conn *sql.DB, dbType string, opts GenerateOptions) (map[string][]map[string]interface{}, error) {
	return GenerateFilteredWithOptions(s, sortedTables, sortedTables, rows, enumRows, nil, conn, dbType, opts)
}

// GenerateFiltered is like Generate but separates the two roles of sortedTables:
//   - allTables: the full set of tables used to pre-load existing PKs from the
//     database (so FK columns in targetTables can reference already-populated
//     parent tables).
//   - targetTables: the subset of tables for which fake rows are actually
//     generated (must be in topological order).
//
// Use this when you only want to seed a subset of tables (e.g. empty ones)
// while still being able to resolve FK references to already-populated parents.
func GenerateFiltered(s *schema.Schema, allTables, targetTables []string, rows, enumRows int, conn *sql.DB, dbType string) (map[string][]map[string]interface{}, error) {
	return GenerateFilteredWithCounts(s, allTables, targetTables, rows, enumRows, nil, conn, dbType)
}

// GenerateFilteredWithCounts is like GenerateFiltered, but tableRows can
// override the default row count for individual target tables.
func GenerateFilteredWithCounts(s *schema.Schema, allTables, targetTables []string, rows, enumRows int, tableRows map[string]int, conn *sql.DB, dbType string) (map[string][]map[string]interface{}, error) {
	return GenerateFilteredWithOptions(s, allTables, targetTables, rows, enumRows, tableRows, conn, dbType, DefaultGenerateOptions())
}

// GenerateFilteredWithOptions is like GenerateFilteredWithCounts, with
// generation guardrails for recursive/self-referential relationships.
func GenerateFilteredWithOptions(s *schema.Schema, allTables, targetTables []string, rows, enumRows int, tableRows map[string]int, conn *sql.DB, dbType string, opts GenerateOptions) (map[string][]map[string]interface{}, error) {
	g, err := NewStream(s, allTables, targetTables, conn, dbType, opts.Overrides)
	if err != nil {
		return nil, err
	}
	return g.Generate(targetTables, rows, enumRows, tableRows, opts)
}

// seqBaseTime anchors generated unique temporal sequences. Kept fixed (not
// time.Now) so output is reproducible and the date range stays valid.
var seqBaseTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// assignUniqueSequences fills every column flagged with the uniqueSequenceFaker
// with a distinct, monotonically increasing value sized to the column type. This
// guarantees UNIQUE numeric/temporal columns never collide regardless of row
// count — random fakers can't promise that, and non-PK UNIQUE columns have no
// retry loop. Values for non-PK columns are independent (nothing references
// them), so reassigning them after generation is safe.
// start offsets a column's sequence past values already stored in the database.
func assignUniqueSequences(rows []map[string]interface{}, table schema.Table, start map[string]int) {
	for colName, col := range table.Columns {
		if col.Faker != uniqueSequenceFaker {
			continue
		}
		for i := range rows {
			rows[i][colName] = uniqueSequenceValue(col.Type, start[colName]+i)
		}
	}
}

// uniqueSequenceValue returns the i-th value (0-based) of a unique sequence for
// the given column type: a monotonic integer for numerics, or a stepped
// date/time/datetime for temporal columns.
func uniqueSequenceValue(colType string, i int) interface{} {
	switch temporalPKFaker(strings.ToLower(strings.TrimSpace(colType))) {
	case "date":
		return seqBaseTime.AddDate(0, 0, i).Format("2006-01-02")
	case "time":
		// 86400 distinct seconds in a day; wraps for very large row counts.
		return seqBaseTime.Add(time.Duration(i) * time.Second).Format("15:04:05")
	case "datetime":
		return seqBaseTime.Add(time.Duration(i) * time.Second)
	default:
		return i + 1
	}
}

// queryExistingPKs reads the PK pools of sortedTables and records in sampled
// which tables were too large to keep whole.
func (gen generator) queryExistingPKs(conn *sql.DB, sortedTables []string, tables map[string]schema.Table, generatedPKs map[string][]interface{}, dbType string, sampled map[string]bool) error {
	for _, tableName := range sortedTables {
		table := tables[tableName]
		for _, colName := range sortedPKColumns(table) {
			wasSampled, err := gen.scanPKs(conn, tableName, colName, generatedPKs, dbType)
			if err != nil {
				return err
			}
			if wasSampled && sampled != nil {
				sampled[tableName] = true
			}
		}
	}
	return nil
}

func (gen generator) scanPKs(conn *sql.DB, tableName, colName string, generatedPKs map[string][]interface{}, dbType string) (bool, error) {
	rows, err := conn.Query(fmt.Sprintf("SELECT %s FROM %s", db.QuoteIdent(colName, dbType), db.QuoteIdent(tableName, dbType))) //nolint:gosec
	if err != nil {
		return false, fmt.Errorf("failed to query PKs for %s.%s: %w", tableName, colName, err)
	}
	defer rows.Close()

	pool := newPoolSampler(generatedPKs[tableName], poolLimit, gen.rnd)
	for rows.Next() {
		var pk interface{}
		if err := rows.Scan(&pk); err != nil {
			return false, err
		}
		pool.offer(normalizeScanned(pk))
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	generatedPKs[tableName] = pool.values()
	return pool.seen > pool.limit, nil
}

// findEnumColumn returns the first enum column by name. Sorted, not map order:
// the column chosen shapes the generated rows, and --seed must reproduce them.
func findEnumColumn(table schema.Table) (string, []string) {
	for _, colName := range sortedColumnNames(table) {
		col := table.Columns[colName]
		if strings.HasPrefix(col.Faker, "randomstring(") {
			if m := reParens.FindStringSubmatch(col.Faker); len(m) > 1 {
				return colName, strings.Split(m[1], ",")
			}
		}
	}
	return "", nil
}

// findAllEnumColumns returns every column whose faker is a randomstring(...),
// mapping column name → slice of enum values.
func findAllEnumColumns(table schema.Table) map[string][]string {
	result := make(map[string][]string)
	for colName, col := range table.Columns {
		if strings.HasPrefix(col.Faker, "randomstring(") {
			if m := reParens.FindStringSubmatch(col.Faker); len(m) > 1 {
				vals := strings.Split(m[1], ",")
				for i, v := range vals {
					vals[i] = strings.TrimSpace(v)
				}
				result[colName] = vals
			}
		}
	}
	return result
}

// maxEnumTopUpValues is the maximum pool size for which topUpEnumCoverage will
// add extra rows. Pools larger than this are treated as "example lists" (e.g.
// AI-generated name suggestions) rather than true DB enums, so we skip the
// top-up to avoid generating far more rows than the user requested.
const maxEnumTopUpValues = 12

// topUpEnumCoverage ensures each enum value appears at least minRows times.
// For each enum column it counts existing occurrences and appends rows until
// every value reaches minRows. Each column is handled independently — no
// cartesian product is produced.
// Columns with more than maxEnumTopUpValues values are skipped: large pools
// are AI example lists, not true enums, and top-up would inflate row counts.
func (gen generator) topUpEnumCoverage(data map[string][]map[string]interface{}, generatedPKs map[string][]interface{}, table schema.Table, tableName string, enumCols map[string][]string, minRows int, existingKeys takenKeys) error {
	// Rows already generated occupy keys too, so top-up rows never collide with
	// them on composite PKs (e.g., junction tables that also carry an enum).
	enforceUniquePK := pkColumnCount(table) > 0
	taken := newSeen(existingKeys)
	if enforceUniquePK {
		for _, row := range data[tableName] {
			taken.Add(compositePKKey(row, table))
		}
	}
	counts := countEnumValues(nil, enumCols, data[tableName])
	return enumTopUp(enumCols, minRows, counts, func(col, val string, n int) error {
		piece := map[string][]map[string]interface{}{tableName: nil}
		if err := gen.generateEnumRows(piece, generatedPKs, table, tableName, col, []string{val}, n, taken); err != nil {
			return fmt.Errorf("enum top-up (table %s, %s=%s): %w", tableName, col, val, err)
		}
		for _, row := range piece[tableName] {
			if enforceUniquePK {
				taken.Add(compositePKKey(row, table))
			}
		}
		data[tableName] = append(data[tableName], piece[tableName]...)
		countEnumValues(counts, enumCols, piece[tableName])
		return nil
	})
}

// enumTopUp asks add for rows until each value of each enum column appears
// minRows times. add generates n rows with col fixed to val and must record
// every row it produces in counts, since those rows also carry values of the
// other enum columns.
func enumTopUp(enumCols map[string][]string, minRows int, counts map[string]map[string]int, add func(col, val string, n int) error) error {
	cols := make([]string, 0, len(enumCols))
	for col := range enumCols {
		cols = append(cols, col)
	}
	sort.Strings(cols)
	for _, col := range cols {
		vals := enumCols[col]
		if len(vals) > maxEnumTopUpValues {
			continue
		}
		for _, val := range vals {
			if need := minRows - counts[col][val]; need > 0 {
				if err := add(col, val, need); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// countEnumValues adds the enum values found in rows to counts (created when
// nil) and returns it.
func countEnumValues(counts map[string]map[string]int, enumCols map[string][]string, rows []map[string]interface{}) map[string]map[string]int {
	if counts == nil {
		counts = make(map[string]map[string]int, len(enumCols))
	}
	for col := range enumCols {
		if counts[col] == nil {
			counts[col] = map[string]int{}
		}
		for _, row := range rows {
			if v, ok := row[col].(string); ok {
				counts[col][v]++
			}
		}
	}
	return counts
}

func (gen generator) generateEnumRows(data map[string][]map[string]interface{}, generatedPKs map[string][]interface{}, table schema.Table, tableName, enumCol string, enumVals []string, enumRows int, existingKeys takenKeys) error {
	seenKeys := newSeen(existingKeys)
	enforceUniquePK := pkColumnCount(table) > 0
	for _, enumVal := range enumVals {
		v := enumVal
		for i := 0; i < enumRows; i++ {
			var row map[string]interface{}
			generated := false
			for attempt := 0; attempt < 200; attempt++ {
				var err error
				row, err = gen.generateRow(table, tableName, generatedPKs, &v, enumCol)
				if err != nil {
					return err
				}
				key := compositePKKey(row, table)
				if !enforceUniquePK || !seenKeys.Has(key) {
					if enforceUniquePK {
						seenKeys.Add(key)
					}
					generated = true
					break
				}
				rollbackLastRowPKs(generatedPKs, tableName, table)
			}
			if !generated {
				return fmt.Errorf("could not generate unique composite PK after 200 attempts for table %s (enum=%s, FK pool too small?)", tableName, enumVal)
			}
			data[tableName] = append(data[tableName], row)
		}
	}
	return nil
}

func (gen generator) generateStandardRows(data map[string][]map[string]interface{}, generatedPKs map[string][]interface{}, table schema.Table, tableName string, rows int, existingKeys takenKeys) error {
	seenKeys := newSeen(existingKeys) // guards PK uniqueness against this run and stored rows
	enforceUniquePK := pkColumnCount(table) > 0
	for i := 0; i < rows; i++ {
		var row map[string]interface{}
		generated := false
		for attempt := 0; attempt < 200; attempt++ {
			var err error
			row, err = gen.generateRow(table, tableName, generatedPKs, nil, "")
			if err != nil {
				return err
			}
			key := compositePKKey(row, table)
			if !enforceUniquePK || !seenKeys.Has(key) {
				if enforceUniquePK {
					seenKeys.Add(key)
				}
				generated = true
				break
			}
			// Collision detected — discard the PK values just appended and retry.
			// Roll back the PKs that were added for this row.
			rollbackLastRowPKs(generatedPKs, tableName, table)
		}
		if !generated {
			return fmt.Errorf("could not generate a unique composite PK after 200 attempts for table %s (FK pool too small?)", tableName)
		}
		data[tableName] = append(data[tableName], row)
	}
	return nil
}

func pkColumnCount(table schema.Table) int {
	count := 0
	for _, col := range table.Columns {
		if col.PK {
			count++
		}
	}
	return count
}

// generateCompositeFKPKRows deterministically generates rows for tables whose
// whole primary key is composed of FK columns. This covers both many-to-many
// junction tables and one-to-one identifying tables where the PK is also an FK.
// Random retries can exhaust quickly when the parent pools are small;
// enumerating combinations avoids false failures and caps impossible requests
// to the available pool.
func (gen generator) generateCompositeFKPKRows(data map[string][]map[string]interface{}, generatedPKs map[string][]interface{}, table schema.Table, tableName string, rows int, existingKeys takenKeys) (int, bool, error) {
	generated, _, handled, err := gen.enumerateCompositeFKPKRows(data, generatedPKs, table, tableName, rows, existingKeys, 0)
	return generated, handled, err
}

// enumerateCompositeFKPKRows walks key combinations from position start and
// returns where it stopped, so a table generated in chunks resumes instead of
// re-walking every combination already used.
func (gen generator) enumerateCompositeFKPKRows(data map[string][]map[string]interface{}, generatedPKs map[string][]interface{}, table schema.Table, tableName string, rows int, existingKeys takenKeys, start int) (int, int, bool, error) {
	pkCols := make([]string, 0)
	for colName, col := range table.Columns {
		if col.PK {
			pkCols = append(pkCols, colName)
		}
	}
	if len(pkCols) == 0 {
		return 0, start, false, nil
	}
	sort.Strings(pkCols)

	pools := make([][]interface{}, 0, len(pkCols))
	for _, colName := range pkCols {
		col := table.Columns[colName]
		fkTable, _ := splitFK(col.FK)
		if fkTable == "" {
			return 0, start, false, nil
		}
		pool := generatedPKs[fkTable]
		if len(pool) == 0 {
			if col.Nullable {
				return 0, start, false, nil
			}
			return 0, start, true, fmt.Errorf("column %s: no PKs available for FK table %s", colName, fkTable)
		}
		pools = append(pools, pool)
	}

	// Stored rows occupy some combinations, so walk up to that many extra
	// combinations past the requested count before giving up.
	walk := start + rows + keyCount(existingKeys)
	capacity := 1
	for _, pool := range pools {
		if walk > 0 && capacity > walk/len(pool) {
			capacity = walk
			break
		}
		capacity *= len(pool)
	}
	if capacity > walk {
		capacity = walk
	}

	generated := 0
	i := start
	for ; i < capacity && generated < rows; i++ {
		overrides := make(map[string]interface{}, len(pkCols))
		n := i
		for colIdx, colName := range pkCols {
			pool := pools[colIdx]
			overrides[colName] = pool[n%len(pool)]
			n /= len(pool)
		}
		if hasKey(existingKeys, compositePKKey(overrides, table)) {
			continue
		}
		row, err := gen.generateRowWithOverrides(table, tableName, generatedPKs, nil, "", overrides)
		if err != nil {
			return generated, i, true, err
		}
		data[tableName] = append(data[tableName], row)
		generated++
	}
	return generated, i, true, nil
}

// compositePKKey returns a deterministic string key for the composite PK values
// of a row. Parts are sorted so map iteration order doesn't affect the result.
func compositePKKey(row map[string]interface{}, table schema.Table) string {
	var parts []string
	for colName, col := range table.Columns {
		if col.PK {
			parts = append(parts, colName+"="+keyValue(col.Type, row[colName]))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// rollbackLastRowPKs removes the PK entries added by the most-recent generateRow call.
func rollbackLastRowPKs(generatedPKs map[string][]interface{}, tableName string, table schema.Table) {
	pkCount := 0
	for _, col := range table.Columns {
		if col.PK {
			pkCount++
		}
	}
	if pkCount == 0 {
		return
	}
	pks := generatedPKs[tableName]
	if len(pks) >= pkCount {
		generatedPKs[tableName] = pks[:len(pks)-pkCount]
	}
}

func (gen generator) generateRow(table schema.Table, tableName string, generatedPKs map[string][]interface{}, enumVal *string, enumCol string) (map[string]interface{}, error) {
	return gen.generateRowWithOverrides(table, tableName, generatedPKs, enumVal, enumCol, nil)
}

func (gen generator) generateRowWithOverrides(table schema.Table, tableName string, generatedPKs map[string][]interface{}, enumVal *string, enumCol string, overrides map[string]interface{}) (map[string]interface{}, error) {
	row := make(map[string]interface{})
	var pksToAdd []interface{}

	// Sort column names for deterministic iteration order — required for
	// reproducible output when using --seed.
	colNames := make([]string, 0, len(table.Columns))
	for colName := range table.Columns {
		colNames = append(colNames, colName)
	}
	sort.Strings(colNames)

	for _, colName := range colNames {
		col := table.Columns[colName]
		if col.Generated {
			continue
		}
		val, ok := overrides[colName]
		if !ok {
			var err error
			val, err = gen.generateValue(col, colName, tableName, generatedPKs, enumVal, enumCol)
			if err != nil {
				return nil, fmt.Errorf("column %s: %w", colName, err)
			}
		}
		row[colName] = val
		if col.PK {
			pksToAdd = append(pksToAdd, val)
		}
	}
	// Add PKs only after all columns are generated so self-referential FK columns
	// don't see the current row's own PK during generation (would skip the first NULL root).
	generatedPKs[tableName] = append(generatedPKs[tableName], pksToAdd...)
	return row, nil
}

func (gen generator) generateValue(col schema.Column, colName, tableName string, generatedPKs map[string][]interface{}, enumVal *string, enumCol string) (interface{}, error) {
	if enumVal != nil && colName == enumCol {
		return *enumVal, nil
	}
	// FK check before PK: handles junction tables where each composite-PK column
	// is also a FK (e.g. user_favorites.product_id = PK+FK). Using sequential PK
	// assignment for both columns would double-increment the shared counter and
	// produce IDs that exceed the referenced table's row count.
	if col.FK != "" {
		parts := strings.SplitN(col.FK, ".", 2)
		if len(parts) == 2 {
			fkTable := parts[0]
			pks := generatedPKs[fkTable]
			if len(pks) == 0 {
				if fkTable == tableName {
					// Self-referential FKs are resolved after all rows for the
					// table have PKs, so the first row can be safely rooted and
					// non-nullable self-FKs can reference an existing generated PK.
					return nil, nil
				}
				if col.Nullable {
					// Nullable FK with no parent rows yet: insert NULL. This
					// handles near-cycles where the parent table is seeded later.
					return nil, nil
				}
				return nil, fmt.Errorf("no PKs available for FK table %s", fkTable)
			}
			return pks[gen.rnd.Number(0, len(pks)-1)], nil
		}
	}
	if col.PK {
		pk, err := gen.generatePK(col.Type, nextSequentialPK(generatedPKs[tableName]))
		if err != nil {
			return nil, err
		}
		return fitStringPK(pk, col), nil
	}
	val, err := gen.generate(col.Faker)
	if err != nil {
		return nil, err
	}
	// Safety: coerce numeric values to string for string-typed columns so
	// AI-suggested numeric fakers don't break varchar/text inserts.
	if val != nil && isStringColType(col.Type) {
		switch v := val.(type) {
		case int:
			return constrainStringValue(fmt.Sprintf("%d", v), col), nil
		case int64:
			return constrainStringValue(fmt.Sprintf("%d", v), col), nil
		case float64:
			return constrainStringValue(fmt.Sprintf("%g", v), col), nil
		}
	}
	if s, ok := val.(string); ok {
		return constrainStringValue(s, col), nil
	}
	return val, nil
}

func backfillSelfReferences(rows []map[string]interface{}, table schema.Table, tableName string, selfRefDepth int) error {
	if len(rows) == 0 {
		return nil
	}
	if selfRefDepth < 0 {
		selfRefDepth = 0
	}

	colNames := make([]string, 0, len(table.Columns))
	for colName, col := range table.Columns {
		if fkTable, _ := splitFK(col.FK); fkTable == tableName {
			colNames = append(colNames, colName)
		}
	}
	sort.Strings(colNames)

	for _, colName := range colNames {
		col := table.Columns[colName]
		_, refCol := splitFK(col.FK)
		if refCol == "" {
			continue
		}
		if _, ok := table.Columns[refCol]; !ok {
			return fmt.Errorf("%s references missing column %s", colName, refCol)
		}

		levels := make([]int, len(rows))
		// lastEligible is the latest row whose depth still allows a child: what
		// chooseSelfRefParent finds by scanning backwards, kept as rows are
		// assigned so each row costs O(1) instead of O(rows).
		lastEligible := -1
		track := func(i int) {
			if levels[i] < selfRefDepth {
				lastEligible = i
			}
		}
		for i := range rows {
			if _, ok := rows[i][refCol]; !ok {
				return fmt.Errorf("%s references unavailable generated value %s", colName, refCol)
			}
			if i == 0 {
				if col.Nullable {
					rows[i][colName] = nil
				} else {
					rows[i][colName] = rows[i][refCol]
				}
				levels[i] = 0
				track(i)
				continue
			}

			parentIdx := lastEligible
			if selfRefDepth <= 0 {
				parentIdx = -1
			}
			if parentIdx < 0 {
				if col.Nullable {
					rows[i][colName] = nil
				} else {
					rows[i][colName] = rows[i][refCol]
				}
				levels[i] = 0
				track(i)
				continue
			}
			rows[i][colName] = rows[parentIdx][refCol]
			levels[i] = levels[parentIdx] + 1
			track(i)
		}
	}
	return nil
}

func chooseSelfRefParent(levels []int, rowIdx, maxDepth int) int {
	if rowIdx <= 0 {
		return -1
	}
	if maxDepth <= 0 {
		return -1
	}
	for i := rowIdx - 1; i >= 0; i-- {
		if levels[i] < maxDepth {
			return i
		}
	}
	return -1
}

func splitFK(fk string) (string, string) {
	parts := strings.SplitN(fk, ".", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// generatePK returns an appropriate primary key value based on the column's DB type.
// Sequential integers for numeric types, UUIDs for uuid/string types.
func (gen generator) generatePK(colType string, existingCount int) (interface{}, error) {
	t := strings.ToLower(colType)
	switch {
	case t == "uuid":
		return gen.rnd.UUID(), nil
	case strings.Contains(t, "char") || strings.Contains(t, "text"):
		return gen.rnd.UUID(), nil
	case temporalPKFaker(t) != "":
		// Temporal PK columns (e.g. a DATE in a composite key) can't take a
		// sequential integer — that inserts "1" into a date/timestamp column.
		// Uniqueness is enforced by the caller's composite-PK retry loop.
		return gen.generate(temporalPKFaker(t))
	default:
		// integer / serial / bigserial — sequential
		return existingCount + 1, nil
	}
}

// fitStringPK shortens a generated string key to the column's declared length.
// Dashes are dropped first so short keys keep as much randomness as possible;
// uniqueness is enforced by the caller's key retry loop.
func fitStringPK(pk interface{}, col schema.Column) interface{} {
	s, ok := pk.(string)
	limit := stringLengthLimit(col)
	if !ok || limit <= 0 || len(s) <= limit {
		return pk
	}
	return strings.ReplaceAll(s, "-", "")[:limit]
}

// temporalPKFaker maps a temporal column type to the faker that produces a
// valid value for it, or "" when the type is not temporal. datetime/timestamp
// is checked before date/time because those substrings overlap.
func temporalPKFaker(t string) string {
	switch {
	case strings.Contains(t, "datetime"), strings.Contains(t, "timestamp"):
		return "datetime"
	case strings.Contains(t, "date"):
		return "date"
	case strings.Contains(t, "time"):
		return "time"
	}
	return ""
}

func isStringColType(colType string) bool {
	if v, ok := stringTypeCache.Load(colType); ok {
		return v.(bool)
	}
	t := strings.ToLower(colType)
	is := strings.Contains(t, "char") || strings.Contains(t, "text") ||
		t == "clob" || t == "tinytext" || t == "mediumtext" || t == "longtext"
	stringTypeCache.Store(colType, is)
	return is
}

// Column types and faker strings repeat on every row, so what is parsed from
// them is cached. The caches grow only with the distinct types and faker
// strings of a schema.
var (
	stringTypeCache  sync.Map // col.Type -> bool
	stringLimitCache sync.Map // [2]string{DDLType, Type} -> int
	fakerSpecCache   sync.Map // faker string -> *fakerSpec
)

func constrainStringValue(value string, col schema.Column) string {
	maxLen := stringLengthLimit(col)
	// A string never has more runes than bytes, so a short one needs no count.
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxLen {
		return value
	}
	return string(runes[:maxLen])
}

func stringLengthLimit(col schema.Column) int {
	key := [2]string{col.DDLType, col.Type}
	if v, ok := stringLimitCache.Load(key); ok {
		return v.(int)
	}
	limit := 0
	for _, typ := range []string{col.DDLType, col.Type} {
		if n := parseStringLength(typ); n > 0 {
			limit = n
			break
		}
	}
	stringLimitCache.Store(key, limit)
	return limit
}

func parseStringLength(colType string) int {
	m := reStringLength.FindStringSubmatch(strings.ToLower(strings.TrimSpace(colType)))
	if len(m) != 2 {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// knownFakers is the set of valid bare faker function names (no args).
// uniqueSequenceFaker marks a column whose values must be globally unique and
// can't be a uuid string (numeric/temporal types). Values are filled after row
// generation with a monotonic sequence (see assignUniqueSequences) — the numeric
// analog of uuid, guaranteeing distinct values without a per-row retry loop.
const uniqueSequenceFaker = "sequence"

var knownFakers = map[string]bool{
	"name": true, "firstname": true, "lastname": true, "username": true,
	"email": true, "phone": true, "street": true, "city": true,
	"state": true, "country": true, "zip": true, "url": true,
	"uuid": true, "ipv4": true, "macaddress": true, "hexcolor": true,
	"productname": true, "company": true, "jobtitle": true,
	"latitude": true, "longitude": true, "bool": true, "float64": true,
	"word": true, "sentence": true, "date": true, "time": true,
	"datetime": true, "json": true, "domain": true, uniqueSequenceFaker: true,
}

// knownParamFakers is the set of valid faker functions that take arguments.
var knownParamFakers = map[string]bool{
	"number": true, "price": true, "randomstring": true,
	"paragraph": true, "float64": true, "lexify": true, "numerify": true,
}

// ValidFaker reports whether a faker string is recognized by the generate engine.
// Valid forms: known bare names, known parameterised calls, or empty string (nil output).
func ValidFaker(faker string) bool {
	s := strings.TrimSpace(faker)
	if s == "" {
		return true
	}
	if knownFakers[s] {
		return true
	}
	if m := reArgs.FindStringSubmatch(s); m != nil {
		return knownParamFakers[m[1]]
	}
	return false
}

var (
	reParens       = regexp.MustCompile(`\((.+)\)`)
	reArgs         = regexp.MustCompile(`^(\w+)\((.*)\)$`)
	reStringLength = regexp.MustCompile(`^(?:varchar|char|character varying|character)\((\d+)\)$`)
)

// fakerSpec is a faker string parsed once: the call name, its raw and split
// arguments, and numeric bounds with any parse error, returned on every use as
// parsing on every call did.
type fakerSpec struct {
	bare     string // trimmed faker string
	call     bool   // name(args) form
	name     string
	raw      string // text inside the parentheses
	args     []string
	intMin   int
	intMax   int
	floatMin float64
	floatMax float64
	count    int
	err      error
}

func parseFakerSpec(fakerStr string) *fakerSpec {
	if v, ok := fakerSpecCache.Load(fakerStr); ok {
		return v.(*fakerSpec)
	}
	spec := &fakerSpec{bare: strings.TrimSpace(fakerStr)}
	if m := reArgs.FindStringSubmatch(spec.bare); m != nil {
		spec.call, spec.name, spec.raw = true, m[1], m[2]
		spec.args = splitArgs(strings.TrimSpace(m[2]))
		switch spec.name {
		case "number":
			var err error
			if spec.intMin, err = strconv.Atoi(argAt(spec.args, 0)); err != nil {
				spec.err = fmt.Errorf("number: bad min arg: %w", err)
			} else if spec.intMax, err = strconv.Atoi(argAt(spec.args, 1)); err != nil {
				spec.err = fmt.Errorf("number: bad max arg: %w", err)
			}
		case "price":
			var err error
			if spec.floatMin, err = strconv.ParseFloat(argAt(spec.args, 0), 64); err != nil {
				spec.err = fmt.Errorf("price: bad min arg: %w", err)
			} else if spec.floatMax, err = strconv.ParseFloat(argAt(spec.args, 1), 64); err != nil {
				spec.err = fmt.Errorf("price: bad max arg: %w", err)
			}
		case "paragraph":
			spec.count = 1
			if len(spec.args) > 0 {
				if n, err := strconv.Atoi(spec.args[0]); err == nil {
					spec.count = n
				}
			}
		}
	}
	fakerSpecCache.Store(fakerStr, spec)
	return spec
}

// argAt returns args[i] trimmed, or "" (which fails to parse, reported as a
// bad argument) when absent.
func argAt(args []string, i int) string {
	if i < len(args) {
		return strings.TrimSpace(args[i])
	}
	return ""
}

func (gen generator) generate(fakerStr string) (interface{}, error) {
	spec := parseFakerSpec(fakerStr)
	s := spec.bare

	// Special cases that return non-string values
	if spec.call {
		switch spec.name {
		case "number":
			if spec.err != nil {
				return nil, spec.err
			}
			return gen.rnd.Number(spec.intMin, spec.intMax), nil
		case "price":
			if spec.err != nil {
				return nil, spec.err
			}
			return gen.rnd.Price(spec.floatMin, spec.floatMax), nil
		case "randomstring":
			return gen.rnd.RandomString(spec.args), nil
		case "paragraph":
			return gen.rnd.Paragraph(spec.count, 3, 8, " "), nil
		case "float64":
			return gen.rnd.Float64(), nil
		case "lexify":
			// The pattern is raw text, not a comma-separated list.
			return gen.rnd.Lexify(spec.raw), nil
		case "numerify":
			return gen.rnd.Numerify(spec.raw), nil
		}
	}

	switch s {
	case "name":
		return gen.rnd.Name(), nil
	case "firstname":
		return gen.rnd.FirstName(), nil
	case "lastname":
		return gen.rnd.LastName(), nil
	case "username":
		return gen.rnd.Username(), nil
	case "email":
		return gen.rnd.Email(), nil
	case "phone":
		return gen.rnd.Phone(), nil
	case "street":
		return gen.rnd.Street(), nil
	case "city":
		return gen.rnd.City(), nil
	case "state":
		return gen.rnd.State(), nil
	case "country":
		return gen.rnd.Country(), nil
	case "zip":
		return gen.rnd.Zip(), nil
	case "url":
		return gen.rnd.URL(), nil
	case "domain":
		return gen.rnd.DomainName(), nil
	case "uuid":
		return gen.rnd.UUID(), nil
	case "ipv4":
		return gen.rnd.IPv4Address(), nil
	case "macaddress":
		return gen.rnd.MacAddress(), nil
	case "hexcolor":
		return gen.rnd.HexColor(), nil
	case "productname":
		return gen.rnd.ProductName(), nil
	case "company":
		return gen.rnd.Company(), nil
	case "jobtitle":
		return gen.rnd.JobTitle(), nil
	case "latitude":
		return gen.rnd.Latitude(), nil
	case "longitude":
		return gen.rnd.Longitude(), nil
	case "bool":
		return gen.rnd.Bool(), nil
	case "float64":
		return gen.rnd.Float64(), nil
	case "word":
		return gen.rnd.Word(), nil
	case "sentence":
		return gen.rnd.Sentence(5), nil
	case "date":
		return gen.rnd.DateRange(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), dateRangeEnd()).Format("2006-01-02"), nil
	case "time":
		return gen.rnd.DateRange(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), dateRangeEnd()).Format("15:04:05"), nil
	case "datetime":
		return gen.rnd.DateRange(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), dateRangeEnd()), nil
	case "json":
		return fmt.Sprintf(`{"key":"%s","value":"%s"}`, gen.rnd.Word(), gen.rnd.Word()), nil
	case uniqueSequenceFaker:
		// Placeholder — overwritten per row by assignUniqueSequences once the
		// full row count for the table is known.
		return nil, nil
	case "":
		return nil, nil
	default:
		// Unknown faker: return a word as safe fallback but log a warning so
		// users notice misconfigured or AI-generated faker strings.
		logging.Log.Warn().Str("faker", s).Msg("Unknown faker function — falling back to random word")
		return gen.rnd.Word(), nil
	}
}

func splitArgs(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}
