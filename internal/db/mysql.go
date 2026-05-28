package db

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func introspectMySQL(db *sql.DB) ([]Table, error) {
	// Get current database name
	var dbName string
	if err := db.QueryRow("SELECT DATABASE()").Scan(&dbName); err != nil {
		return nil, fmt.Errorf("failed to get current database: %w", err)
	}

	// List all tables
	tableRows, err := db.Query(`
		SELECT TABLE_NAME
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ?
		  AND TABLE_TYPE = 'BASE TABLE'
		ORDER BY TABLE_NAME`, dbName)
	if err != nil {
		return nil, fmt.Errorf("failed to list tables: %w", err)
	}
	defer tableRows.Close()

	var tableNames []string
	for tableRows.Next() {
		var name string
		if err := tableRows.Scan(&name); err != nil {
			return nil, err
		}
		tableNames = append(tableNames, name)
	}

	// Fetch FK relationships for the whole database
	fkMap, err := mysqlFKMap(db, dbName)
	if err != nil {
		return nil, err
	}

	// CHECK constraint introspection requires MySQL 8.0.16+, which is when
	// information_schema.CHECK_CONSTRAINTS was introduced. On 5.7 and earlier
	// we skip these maps; CHECK clauses are parsed as syntax but not enforced
	// or stored in metadata on those versions.
	var checkMap map[string]map[string][]string
	var rangeMap map[string]map[string]rangeConstraint
	supportsCheck, err := mysqlSupportsCheckConstraints(db)
	if err != nil {
		return nil, err
	}
	if supportsCheck {
		checkMap, err = mysqlCheckMap(db, dbName)
		if err != nil {
			return nil, err
		}
		rangeMap, err = mysqlRangeMap(db, dbName)
		if err != nil {
			return nil, err
		}
	}

	indexMap, err := mysqlIndexMap(db, dbName)
	if err != nil {
		return nil, err
	}

	tableComments, err := mysqlTableCommentMap(db, dbName)
	if err != nil {
		return nil, err
	}

	var tables []Table
	for _, tableName := range tableNames {
		cols, err := mysqlColumns(db, dbName, tableName, fkMap, checkMap, rangeMap)
		if err != nil {
			return nil, fmt.Errorf("failed to introspect table %s: %w", tableName, err)
		}
		tables = append(tables, Table{Name: tableName, Columns: cols, Indexes: indexMap[tableName], Comment: tableComments[tableName]})
	}

	return tables, nil
}

// mysqlSupportsCheckConstraints reports whether the server exposes
// information_schema.CHECK_CONSTRAINTS. Introduced in MySQL 8.0.16.
func mysqlSupportsCheckConstraints(db *sql.DB) (bool, error) {
	var one int
	err := db.QueryRow(`
		SELECT 1
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = 'information_schema'
		  AND TABLE_NAME   = 'CHECK_CONSTRAINTS'
		LIMIT 1`).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to probe CHECK_CONSTRAINTS support: %w", err)
	}
	return true, nil
}

func mysqlFKMap(db *sql.DB, dbName string) (map[string]map[string]*ForeignKey, error) {
	rows, err := db.Query(`
		SELECT
			kcu.TABLE_NAME,
			kcu.COLUMN_NAME,
			kcu.REFERENCED_TABLE_NAME,
			kcu.REFERENCED_COLUMN_NAME
		FROM information_schema.KEY_COLUMN_USAGE kcu
		JOIN information_schema.REFERENTIAL_CONSTRAINTS rc
		  ON rc.CONSTRAINT_NAME = kcu.CONSTRAINT_NAME
		 AND rc.CONSTRAINT_SCHEMA = kcu.CONSTRAINT_SCHEMA
		WHERE kcu.CONSTRAINT_SCHEMA = ?
		  AND kcu.REFERENCED_TABLE_NAME IS NOT NULL`, dbName)
	if err != nil {
		return nil, fmt.Errorf("failed to query FK constraints: %w", err)
	}
	defer rows.Close()

	fkMap := make(map[string]map[string]*ForeignKey)
	for rows.Next() {
		var table, column, refTable, refColumn string
		if err := rows.Scan(&table, &column, &refTable, &refColumn); err != nil {
			return nil, err
		}
		if fkMap[table] == nil {
			fkMap[table] = make(map[string]*ForeignKey)
		}
		fkMap[table][column] = &ForeignKey{TableName: refTable, ColumnName: refColumn}
	}
	return fkMap, nil
}

func mysqlColumns(db *sql.DB, dbName, tableName string, fkMap map[string]map[string]*ForeignKey, checkMap map[string]map[string][]string, rangeMap map[string]map[string]rangeConstraint) ([]Column, error) {
	rows, err := db.Query(`
		SELECT
			COLUMN_NAME,
			DATA_TYPE,
			COLUMN_TYPE,
			IS_NULLABLE,
			COLUMN_KEY,
			COLUMN_DEFAULT,
			EXTRA,
			GENERATION_EXPRESSION,
			COLUMN_COMMENT
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ?
		  AND TABLE_NAME = ?
		ORDER BY ORDINAL_POSITION`, dbName, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []Column
	for rows.Next() {
		var name, dataType, columnType, isNullable, columnKey, extra, generationExpr, comment string
		var defaultValue sql.NullString
		if err := rows.Scan(&name, &dataType, &columnType, &isNullable, &columnKey, &defaultValue, &extra, &generationExpr, &comment); err != nil {
			return nil, err
		}

		col := Column{
			Name:       name,
			Type:       strings.ToLower(dataType),
			DDLType:    columnType,
			IsNullable: isNullable == "YES",
			IsPK:       columnKey == "PRI",
			Unique:     columnKey == "UNI",
			Comment:    comment,
		}
		if defaultValue.Valid && !strings.Contains(strings.ToLower(extra), "generated") {
			col.Default = mysqlDefaultLiteral(defaultValue.String, dataType)
		}
		if strings.Contains(strings.ToLower(extra), "auto_increment") {
			col.AutoIncrement = true
			col.Default = ""
		}
		if strings.Contains(strings.ToLower(extra), "generated") {
			col.Generated = mysqlGeneratedExpression(generationExpr)
		}

		// Parse enum values from COLUMN_TYPE e.g. enum('a','b','c')
		if strings.EqualFold(dataType, "enum") || strings.EqualFold(dataType, "set") {
			col.EnumValues = parseEnumValues(columnType)
		}

		// Apply FK if known
		if fkMap[tableName] != nil {
			if fk, ok := fkMap[tableName][name]; ok {
				col.FK = fk
			}
		}

		// Apply CHECK constraint values if known
		if checkMap[tableName] != nil {
			if vals, ok := checkMap[tableName][name]; ok {
				col.CheckValues = vals
			}
		}

		if rangeMap[tableName] != nil {
			if r, ok := rangeMap[tableName][name]; ok {
				col.CheckMin = &r.Min
				col.CheckMax = &r.Max
			}
		}

		columns = append(columns, col)
	}
	return columns, nil
}

// mysqlCheckMap returns map[table][column]=[]values for CHECK IN constraints (MySQL 8.0.16+).
func mysqlCheckMap(db *sql.DB, dbName string) (map[string]map[string][]string, error) {
	rows, err := db.Query(`
		SELECT tc.TABLE_NAME, cc.CHECK_CLAUSE
		FROM information_schema.TABLE_CONSTRAINTS tc
		JOIN information_schema.CHECK_CONSTRAINTS cc
		  ON tc.CONSTRAINT_NAME  = cc.CONSTRAINT_NAME
		 AND tc.TABLE_SCHEMA     = cc.CONSTRAINT_SCHEMA
		WHERE tc.TABLE_SCHEMA    = ?
		  AND tc.CONSTRAINT_TYPE = 'CHECK'`, dbName)
	if err != nil {
		return nil, fmt.Errorf("failed to query CHECK constraints: %w", err)
	}
	defer rows.Close()

	m := make(map[string]map[string][]string)
	for rows.Next() {
		var table, clause string
		if err := rows.Scan(&table, &clause); err != nil {
			return nil, err
		}
		col, vals := parseMySQLCheckClause(clause)
		if col != "" && len(vals) > 0 {
			if m[table] == nil {
				m[table] = make(map[string][]string)
			}
			m[table][col] = vals
		}
	}
	return m, nil
}

var (
	// matches: `col` in (...) or col in (...)
	mysqlInRe  = regexp.MustCompile("(?i)`?(\\w+)`?\\s+in\\s*\\(([^)]+)\\)")
	mysqlValRe = regexp.MustCompile(`'([^']+)'`)
)

// parseMySQLCheckClause extracts the column name and allowed values from a MySQL
// CHECK clause such as "(role in (_utf8mb4'admin',_utf8mb4'user'))" or
// "(`role` in ('admin','user'))".
// MySQL 8.0 stores single quotes with a backslash escape inside the clause text
// (e.g. _utf8mb4\'admin\'), so we strip trailing backslashes from each value.
func parseMySQLCheckClause(clause string) (string, []string) {
	m := mysqlInRe.FindStringSubmatch(clause)
	if len(m) < 3 {
		return "", nil
	}
	col := m[1]
	var values []string
	for _, v := range mysqlValRe.FindAllStringSubmatch(m[2], -1) {
		val := strings.TrimRight(v[1], "\\")
		values = append(values, val)
	}
	return col, values
}

var (
	// matches: col >= N and col <= M  (case-insensitive, optional spaces, optional backtick-quoting)
	myRangeRe   = regexp.MustCompile("(?i)`?(\\w+)`?\\s*>=\\s*(-?\\d+).*?`?(\\w+)`?\\s*<=\\s*(-?\\d+)")
	myBetweenRe = regexp.MustCompile("(?i)`?(\\w+)`?\\s+between\\s+(-?\\d+)\\s+and\\s+(-?\\d+)")
)

// mysqlRangeMap returns map[table][column]=rangeConstraint for CHECK (col >= N AND col <= M).
func mysqlRangeMap(db *sql.DB, dbName string) (map[string]map[string]rangeConstraint, error) {
	rows, err := db.Query(`
		SELECT tc.TABLE_NAME, cc.CHECK_CLAUSE
		FROM information_schema.TABLE_CONSTRAINTS tc
		JOIN information_schema.CHECK_CONSTRAINTS cc
		  ON tc.CONSTRAINT_NAME  = cc.CONSTRAINT_NAME
		 AND tc.TABLE_SCHEMA     = cc.CONSTRAINT_SCHEMA
		WHERE tc.TABLE_SCHEMA    = ?
		  AND tc.CONSTRAINT_TYPE = 'CHECK'`, dbName)
	if err != nil {
		return nil, fmt.Errorf("failed to query range CHECK constraints: %w", err)
	}
	defer rows.Close()

	m := make(map[string]map[string]rangeConstraint)
	for rows.Next() {
		var table, clause string
		if err := rows.Scan(&table, &clause); err != nil {
			return nil, err
		}
		col, min, max, ok := parseMySQLCheckRange(clause)
		if ok {
			if m[table] == nil {
				m[table] = make(map[string]rangeConstraint)
			}
			m[table][col] = rangeConstraint{Min: min, Max: max}
		}
	}
	return m, nil
}

// parseMySQLCheckRange extracts (column, min, max) from MySQL CHECK clauses like
// "(spice_level >= 0 and spice_level <= 5)" or "(score between 1 and 10)".
func parseMySQLCheckRange(clause string) (string, int64, int64, bool) {
	if m := myBetweenRe.FindStringSubmatch(clause); len(m) == 4 {
		lo, err1 := strconv.ParseInt(m[2], 10, 64)
		hi, err2 := strconv.ParseInt(m[3], 10, 64)
		if err1 == nil && err2 == nil {
			return m[1], lo, hi, true
		}
	}
	if m := myRangeRe.FindStringSubmatch(clause); len(m) == 5 {
		lo, err1 := strconv.ParseInt(m[2], 10, 64)
		hi, err2 := strconv.ParseInt(m[4], 10, 64)
		if err1 == nil && err2 == nil {
			return m[1], lo, hi, true
		}
	}
	return "", 0, 0, false
}

// parseEnumValues extracts values from MySQL COLUMN_TYPE like enum('a','b','c').
func parseEnumValues(columnType string) []string {
	start := strings.Index(columnType, "(")
	end := strings.LastIndex(columnType, ")")
	if start == -1 || end == -1 || end <= start {
		return nil
	}
	inner := columnType[start+1 : end]
	parts := strings.Split(inner, ",")
	values := make([]string, 0, len(parts))
	for _, p := range parts {
		v := strings.TrimSpace(p)
		v = strings.Trim(v, "'\"")
		if v != "" {
			values = append(values, v)
		}
	}
	return values
}

func mysqlIndexMap(db *sql.DB, dbName string) (map[string][]Index, error) {
	rows, err := db.Query(`
		SELECT
			TABLE_NAME,
			INDEX_NAME,
			NON_UNIQUE,
			GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX SEPARATOR ',')
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ?
		  AND INDEX_NAME <> 'PRIMARY'
		GROUP BY TABLE_NAME, INDEX_NAME, NON_UNIQUE`, dbName)
	if err != nil {
		return nil, fmt.Errorf("failed to query indexes: %w", err)
	}
	defer rows.Close()

	m := make(map[string][]Index)
	for rows.Next() {
		var table, name, columns string
		var nonUnique int
		if err := rows.Scan(&table, &name, &nonUnique, &columns); err != nil {
			return nil, err
		}
		cols := strings.Split(columns, ",")
		if len(cols) == 1 && nonUnique == 0 {
			continue
		}
		m[table] = append(m[table], Index{Name: name, Columns: cols, Unique: nonUnique == 0})
	}
	return m, nil
}

func mysqlTableCommentMap(db *sql.DB, dbName string) (map[string]string, error) {
	rows, err := db.Query(`
		SELECT TABLE_NAME, TABLE_COMMENT
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ?
		  AND TABLE_TYPE = 'BASE TABLE'
		  AND TABLE_COMMENT <> ''`, dbName)
	if err != nil {
		return nil, fmt.Errorf("failed to query table comments: %w", err)
	}
	defer rows.Close()

	m := make(map[string]string)
	for rows.Next() {
		var table, comment string
		if err := rows.Scan(&table, &comment); err != nil {
			return nil, err
		}
		m[table] = comment
	}
	return m, nil
}

func mysqlDefaultLiteral(value, dataType string) string {
	switch strings.ToLower(dataType) {
	case "char", "varchar", "text", "mediumtext", "longtext", "enum", "set", "date", "time", "datetime", "timestamp":
		upper := strings.ToUpper(value)
		if upper == "CURRENT_TIMESTAMP" || strings.HasSuffix(upper, "()") {
			return value
		}
		return quoteStringLiteral(value)
	default:
		return value
	}
}

func mysqlGeneratedExpression(expr string) string {
	return strings.ReplaceAll(expr, `\'`, `'`)
}
