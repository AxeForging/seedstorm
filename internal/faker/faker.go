package faker

import (
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/brianvoe/gofakeit/v6"
)

const DefaultSelfRefDepth = 2

type GenerateOptions struct {
	SelfRefDepth int
	OnWarning    func(GenerationWarning)
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
	opts = normalizeOptions(opts)
	data := make(map[string][]map[string]interface{})
	generatedPKs := make(map[string][]interface{})

	if conn != nil {
		if err := queryExistingPKs(conn, allTables, s.Tables, generatedPKs, dbType); err != nil {
			return nil, err
		}
	}

	sortedTables := targetTables

	for _, tableName := range sortedTables {
		table := s.Tables[tableName]
		data[tableName] = nil
		tableRowCount := rows
		_, hasRowOverride := tableRows[tableName]
		if override := tableRows[tableName]; override > 0 {
			tableRowCount = override
		}

		enumCol, enumVals := findEnumColumn(table)

		if enumCol != "" && enumRows > 0 && !hasRowOverride {
			if err := generateEnumRows(data, generatedPKs, table, tableName, enumCol, enumVals, enumRows); err != nil {
				return nil, fmt.Errorf("table %s: %w", tableName, err)
			}
		} else if generated, ok, err := generateCompositeFKPKRows(data, generatedPKs, table, tableName, tableRowCount); err != nil {
			return nil, fmt.Errorf("table %s: %w", tableName, err)
		} else if ok {
			if generated < tableRowCount && opts.OnWarning != nil {
				opts.OnWarning(GenerationWarning{
					Table:     tableName,
					Requested: tableRowCount,
					Generated: generated,
					Reason:    "finite FK primary-key combinations",
				})
			}
		} else {
			if err := generateStandardRows(data, generatedPKs, table, tableName, tableRowCount); err != nil {
				return nil, fmt.Errorf("table %s: %w", tableName, err)
			}
			// Guarantee every enum value appears at least the requested table
			// row count, independently per column.
			enumCols := findAllEnumColumns(table)
			if len(enumCols) > 0 && !hasRowOverride {
				if err := topUpEnumCoverage(data, generatedPKs, table, tableName, enumCols, tableRowCount); err != nil {
					return nil, fmt.Errorf("table %s enum top-up: %w", tableName, err)
				}
			}
		}
		if err := backfillSelfReferences(data[tableName], table, tableName, opts.SelfRefDepth); err != nil {
			return nil, fmt.Errorf("table %s self-reference backfill: %w", tableName, err)
		}
		assignUniqueSequences(data[tableName], table)
	}

	return data, nil
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
func assignUniqueSequences(rows []map[string]interface{}, table schema.Table) {
	for colName, col := range table.Columns {
		if col.Faker != uniqueSequenceFaker {
			continue
		}
		for i := range rows {
			rows[i][colName] = uniqueSequenceValue(col.Type, i)
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

func queryExistingPKs(conn *sql.DB, sortedTables []string, tables map[string]schema.Table, generatedPKs map[string][]interface{}, dbType string) error {
	for _, tableName := range sortedTables {
		table := tables[tableName]
		for colName, col := range table.Columns {
			if !col.PK {
				continue
			}
			if err := scanPKs(conn, tableName, colName, generatedPKs, dbType); err != nil {
				return err
			}
		}
	}
	return nil
}

func scanPKs(conn *sql.DB, tableName, colName string, generatedPKs map[string][]interface{}, dbType string) error {
	rows, err := conn.Query(fmt.Sprintf("SELECT %s FROM %s", db.QuoteIdent(colName, dbType), db.QuoteIdent(tableName, dbType))) //nolint:gosec
	if err != nil {
		return fmt.Errorf("failed to query PKs for %s.%s: %w", tableName, colName, err)
	}
	defer rows.Close()

	for rows.Next() {
		var pk interface{}
		if err := rows.Scan(&pk); err != nil {
			return err
		}
		generatedPKs[tableName] = append(generatedPKs[tableName], pk)
	}
	return rows.Err()
}

func findEnumColumn(table schema.Table) (string, []string) {
	for colName, col := range table.Columns {
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
func topUpEnumCoverage(data map[string][]map[string]interface{}, generatedPKs map[string][]interface{}, table schema.Table, tableName string, enumCols map[string][]string, minRows int) error {
	// Seed seenKeys from already-generated rows so top-up rows don't collide on
	// composite PKs (e.g., junction tables that also carry an enum column).
	enforceUniquePK := pkColumnCount(table) > 0
	seenKeys := make(map[string]bool)
	if enforceUniquePK {
		seenKeys = make(map[string]bool, len(data[tableName]))
		for _, row := range data[tableName] {
			seenKeys[compositePKKey(row, table)] = true
		}
	}

	for colName, vals := range enumCols {
		if len(vals) > maxEnumTopUpValues {
			continue
		}
		counts := make(map[string]int, len(vals))
		for _, row := range data[tableName] {
			if v, ok := row[colName].(string); ok {
				counts[v]++
			}
		}
		for _, val := range vals {
			need := minRows - counts[val]
			for i := 0; i < need; i++ {
				v := val
				var row map[string]interface{}
				generated := false
				for attempt := 0; attempt < 200; attempt++ {
					var err error
					row, err = generateRow(table, tableName, generatedPKs, &v, colName)
					if err != nil {
						return err
					}
					key := compositePKKey(row, table)
					if !enforceUniquePK || !seenKeys[key] {
						if enforceUniquePK {
							seenKeys[key] = true
						}
						generated = true
						break
					}
					rollbackLastRowPKs(generatedPKs, tableName, table)
				}
				if !generated {
					return fmt.Errorf("could not generate unique PK for enum top-up (table %s, %s=%s)", tableName, colName, val)
				}
				data[tableName] = append(data[tableName], row)
				counts[val]++
			}
		}
	}
	return nil
}

func generateEnumRows(data map[string][]map[string]interface{}, generatedPKs map[string][]interface{}, table schema.Table, tableName, enumCol string, enumVals []string, enumRows int) error {
	seenKeys := make(map[string]bool)
	enforceUniquePK := pkColumnCount(table) > 0
	for _, enumVal := range enumVals {
		v := enumVal
		for i := 0; i < enumRows; i++ {
			var row map[string]interface{}
			generated := false
			for attempt := 0; attempt < 200; attempt++ {
				var err error
				row, err = generateRow(table, tableName, generatedPKs, &v, enumCol)
				if err != nil {
					return err
				}
				key := compositePKKey(row, table)
				if !enforceUniquePK || !seenKeys[key] {
					if enforceUniquePK {
						seenKeys[key] = true
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

func generateStandardRows(data map[string][]map[string]interface{}, generatedPKs map[string][]interface{}, table schema.Table, tableName string, rows int) error {
	seenKeys := make(map[string]bool) // guards composite PK uniqueness
	enforceUniquePK := pkColumnCount(table) > 0
	for i := 0; i < rows; i++ {
		var row map[string]interface{}
		generated := false
		for attempt := 0; attempt < 200; attempt++ {
			var err error
			row, err = generateRow(table, tableName, generatedPKs, nil, "")
			if err != nil {
				return err
			}
			key := compositePKKey(row, table)
			if !enforceUniquePK || !seenKeys[key] {
				if enforceUniquePK {
					seenKeys[key] = true
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
func generateCompositeFKPKRows(data map[string][]map[string]interface{}, generatedPKs map[string][]interface{}, table schema.Table, tableName string, rows int) (int, bool, error) {
	pkCols := make([]string, 0)
	for colName, col := range table.Columns {
		if col.PK {
			pkCols = append(pkCols, colName)
		}
	}
	if len(pkCols) == 0 {
		return 0, false, nil
	}
	sort.Strings(pkCols)

	pools := make([][]interface{}, 0, len(pkCols))
	for _, colName := range pkCols {
		col := table.Columns[colName]
		fkTable, _ := splitFK(col.FK)
		if fkTable == "" {
			return 0, false, nil
		}
		pool := generatedPKs[fkTable]
		if len(pool) == 0 {
			if col.Nullable {
				return 0, false, nil
			}
			return 0, true, fmt.Errorf("column %s: no PKs available for FK table %s", colName, fkTable)
		}
		pools = append(pools, pool)
	}

	capacity := 1
	for _, pool := range pools {
		if rows > 0 && capacity > rows/len(pool) {
			capacity = rows
			break
		}
		capacity *= len(pool)
	}
	limit := rows
	if capacity < limit {
		limit = capacity
	}

	for i := 0; i < limit; i++ {
		overrides := make(map[string]interface{}, len(pkCols))
		n := i
		for colIdx, colName := range pkCols {
			pool := pools[colIdx]
			overrides[colName] = pool[n%len(pool)]
			n /= len(pool)
		}
		row, err := generateRowWithOverrides(table, tableName, generatedPKs, nil, "", overrides)
		if err != nil {
			return len(data[tableName]), true, err
		}
		data[tableName] = append(data[tableName], row)
	}
	return limit, true, nil
}

// compositePKKey returns a deterministic string key for the composite PK values
// of a row. Parts are sorted so map iteration order doesn't affect the result.
func compositePKKey(row map[string]interface{}, table schema.Table) string {
	var parts []string
	for colName, col := range table.Columns {
		if col.PK {
			parts = append(parts, fmt.Sprintf("%s=%v", colName, row[colName]))
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

func generateRow(table schema.Table, tableName string, generatedPKs map[string][]interface{}, enumVal *string, enumCol string) (map[string]interface{}, error) {
	return generateRowWithOverrides(table, tableName, generatedPKs, enumVal, enumCol, nil)
}

func generateRowWithOverrides(table schema.Table, tableName string, generatedPKs map[string][]interface{}, enumVal *string, enumCol string, overrides map[string]interface{}) (map[string]interface{}, error) {
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
			val, err = generateValue(col, colName, tableName, generatedPKs, enumVal, enumCol)
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

func generateValue(col schema.Column, colName, tableName string, generatedPKs map[string][]interface{}, enumVal *string, enumCol string) (interface{}, error) {
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
			return pks[gofakeit.Number(0, len(pks)-1)], nil
		}
	}
	if col.PK {
		return generatePK(col.Type, len(generatedPKs[tableName]))
	}
	val, err := generate(col.Faker)
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
				continue
			}

			parentIdx := chooseSelfRefParent(levels, i, selfRefDepth)
			if parentIdx < 0 {
				if col.Nullable {
					rows[i][colName] = nil
					levels[i] = 0
					continue
				}
				rows[i][colName] = rows[i][refCol]
				levels[i] = 0
				continue
			}
			rows[i][colName] = rows[parentIdx][refCol]
			levels[i] = levels[parentIdx] + 1
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
func generatePK(colType string, existingCount int) (interface{}, error) {
	t := strings.ToLower(colType)
	switch {
	case t == "uuid":
		return gofakeit.UUID(), nil
	case strings.Contains(t, "char") || strings.Contains(t, "text"):
		return gofakeit.UUID(), nil
	case temporalPKFaker(t) != "":
		// Temporal PK columns (e.g. a DATE in a composite key) can't take a
		// sequential integer — that inserts "1" into a date/timestamp column.
		// Uniqueness is enforced by the caller's composite-PK retry loop.
		return generate(temporalPKFaker(t))
	default:
		// integer / serial / bigserial — sequential
		return existingCount + 1, nil
	}
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
	t := strings.ToLower(colType)
	return strings.Contains(t, "char") || strings.Contains(t, "text") ||
		t == "clob" || t == "tinytext" || t == "mediumtext" || t == "longtext"
}

func constrainStringValue(value string, col schema.Column) string {
	maxLen := stringLengthLimit(col)
	if maxLen <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxLen {
		return value
	}
	return string(runes[:maxLen])
}

func stringLengthLimit(col schema.Column) int {
	for _, typ := range []string{col.DDLType, col.Type} {
		if n := parseStringLength(typ); n > 0 {
			return n
		}
	}
	return 0
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
	"datetime": true, "json": true, uniqueSequenceFaker: true,
}

// knownParamFakers is the set of valid faker functions that take arguments.
var knownParamFakers = map[string]bool{
	"number": true, "price": true, "randomstring": true,
	"paragraph": true, "float64": true,
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

func generate(fakerStr string) (interface{}, error) {
	s := strings.TrimSpace(fakerStr)

	// Special cases that return non-string values
	if m := reArgs.FindStringSubmatch(s); m != nil {
		funcName := m[1]
		argsStr := strings.TrimSpace(m[2])
		args := splitArgs(argsStr)

		switch funcName {
		case "number":
			min, err := strconv.Atoi(strings.TrimSpace(args[0]))
			if err != nil {
				return nil, fmt.Errorf("number: bad min arg: %w", err)
			}
			max, err := strconv.Atoi(strings.TrimSpace(args[1]))
			if err != nil {
				return nil, fmt.Errorf("number: bad max arg: %w", err)
			}
			return gofakeit.Number(min, max), nil
		case "price":
			min, err := strconv.ParseFloat(strings.TrimSpace(args[0]), 64)
			if err != nil {
				return nil, fmt.Errorf("price: bad min arg: %w", err)
			}
			max, err := strconv.ParseFloat(strings.TrimSpace(args[1]), 64)
			if err != nil {
				return nil, fmt.Errorf("price: bad max arg: %w", err)
			}
			return gofakeit.Price(min, max), nil
		case "randomstring":
			return gofakeit.RandomString(args), nil
		case "paragraph":
			count := 1
			if len(args) > 0 {
				if n, err := strconv.Atoi(strings.TrimSpace(args[0])); err == nil {
					count = n
				}
			}
			return gofakeit.Paragraph(count, 3, 8, " "), nil
		case "float64":
			return gofakeit.Float64(), nil
		}
	}

	switch s {
	case "name":
		return gofakeit.Name(), nil
	case "firstname":
		return gofakeit.FirstName(), nil
	case "lastname":
		return gofakeit.LastName(), nil
	case "username":
		return gofakeit.Username(), nil
	case "email":
		return gofakeit.Email(), nil
	case "phone":
		return gofakeit.Phone(), nil
	case "street":
		return gofakeit.Street(), nil
	case "city":
		return gofakeit.City(), nil
	case "state":
		return gofakeit.State(), nil
	case "country":
		return gofakeit.Country(), nil
	case "zip":
		return gofakeit.Zip(), nil
	case "url":
		return gofakeit.URL(), nil
	case "uuid":
		return gofakeit.UUID(), nil
	case "ipv4":
		return gofakeit.IPv4Address(), nil
	case "macaddress":
		return gofakeit.MacAddress(), nil
	case "hexcolor":
		return gofakeit.HexColor(), nil
	case "productname":
		return gofakeit.ProductName(), nil
	case "company":
		return gofakeit.Company(), nil
	case "jobtitle":
		return gofakeit.JobTitle(), nil
	case "latitude":
		return gofakeit.Latitude(), nil
	case "longitude":
		return gofakeit.Longitude(), nil
	case "bool":
		return gofakeit.Bool(), nil
	case "float64":
		return gofakeit.Float64(), nil
	case "word":
		return gofakeit.Word(), nil
	case "sentence":
		return gofakeit.Sentence(5), nil
	case "date":
		return gofakeit.DateRange(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Now()).Format("2006-01-02"), nil
	case "time":
		return gofakeit.DateRange(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Now()).Format("15:04:05"), nil
	case "datetime":
		return gofakeit.DateRange(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Now()), nil
	case "json":
		return fmt.Sprintf(`{"key":"%s","value":"%s"}`, gofakeit.Word(), gofakeit.Word()), nil
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
		return gofakeit.Word(), nil
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
