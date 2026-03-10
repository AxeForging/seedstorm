package db

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func introspectPostgres(db *sql.DB) ([]Table, error) {
	tableRows, err := db.Query(`
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = 'public'
		  AND table_type = 'BASE TABLE'
		ORDER BY table_name`)
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

	fkMap, err := postgresFKMap(db)
	if err != nil {
		return nil, err
	}

	pkMap, err := postgresPKMap(db)
	if err != nil {
		return nil, err
	}

	uniqueMap, err := postgresUniqueMap(db)
	if err != nil {
		return nil, err
	}

	checkMap, err := postgresCheckMap(db)
	if err != nil {
		return nil, err
	}

	rangeMap, err := postgresRangeMap(db)
	if err != nil {
		return nil, err
	}

	var tables []Table
	for _, tableName := range tableNames {
		cols, err := postgresColumns(db, tableName, fkMap, pkMap, uniqueMap, checkMap, rangeMap)
		if err != nil {
			return nil, fmt.Errorf("failed to introspect table %s: %w", tableName, err)
		}
		tables = append(tables, Table{Name: tableName, Columns: cols})
	}

	return tables, nil
}

func postgresFKMap(db *sql.DB) (map[string]map[string]*ForeignKey, error) {
	rows, err := db.Query(`
		SELECT
			kcu.table_name,
			kcu.column_name,
			ccu.table_name  AS foreign_table,
			ccu.column_name AS foreign_column
		FROM information_schema.table_constraints AS tc
		JOIN information_schema.key_column_usage AS kcu
		  ON tc.constraint_name = kcu.constraint_name
		 AND tc.table_schema = kcu.table_schema
		JOIN information_schema.constraint_column_usage AS ccu
		  ON ccu.constraint_name = tc.constraint_name
		 AND ccu.table_schema = tc.table_schema
		WHERE tc.constraint_type = 'FOREIGN KEY'
		  AND tc.table_schema = 'public'`)
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

func postgresPKMap(db *sql.DB) (map[string]map[string]bool, error) {
	rows, err := db.Query(`
		SELECT kcu.table_name, kcu.column_name
		FROM information_schema.table_constraints AS tc
		JOIN information_schema.key_column_usage AS kcu
		  ON tc.constraint_name = kcu.constraint_name
		 AND tc.table_schema = kcu.table_schema
		WHERE tc.constraint_type = 'PRIMARY KEY'
		  AND tc.table_schema = 'public'`)
	if err != nil {
		return nil, fmt.Errorf("failed to query PK constraints: %w", err)
	}
	defer rows.Close()

	pkMap := make(map[string]map[string]bool)
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return nil, err
		}
		if pkMap[table] == nil {
			pkMap[table] = make(map[string]bool)
		}
		pkMap[table][column] = true
	}
	return pkMap, nil
}

type rangeConstraint struct{ Min, Max int64 }

func postgresColumns(db *sql.DB, tableName string, fkMap map[string]map[string]*ForeignKey, pkMap map[string]map[string]bool, uniqueMap map[string]map[string]bool, checkMap map[string]map[string][]string, rangeMap map[string]map[string]rangeConstraint) ([]Column, error) {
	rows, err := db.Query(`
		SELECT
			column_name,
			data_type,
			udt_name,
			is_nullable
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = $1
		ORDER BY ordinal_position`, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []Column
	for rows.Next() {
		var name, dataType, udtName, isNullable string
		if err := rows.Scan(&name, &dataType, &udtName, &isNullable); err != nil {
			return nil, err
		}

		colType := strings.ToLower(dataType)
		// For user-defined types (enums), use udt_name
		if colType == "user-defined" {
			colType = strings.ToLower(udtName)
		}

		col := Column{
			Name:       name,
			Type:       colType,
			IsNullable: isNullable == "YES",
			IsPK:       pkMap[tableName] != nil && pkMap[tableName][name],
			Unique:     uniqueMap[tableName] != nil && uniqueMap[tableName][name],
		}

		// Resolve enum values for user-defined enum types
		if dataType == "USER-DEFINED" {
			col.EnumValues, _ = postgresEnumValues(db, udtName)
		}

		if fkMap[tableName] != nil {
			if fk, ok := fkMap[tableName][name]; ok {
				col.FK = fk
			}
		}

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

// postgresUniqueMap returns map[table][column]=true for single-column UNIQUE constraints.
func postgresUniqueMap(db *sql.DB) (map[string]map[string]bool, error) {
	rows, err := db.Query(`
		SELECT t.relname, a.attname
		FROM pg_constraint c
		JOIN pg_class t       ON c.conrelid = t.oid
		JOIN pg_namespace n   ON t.relnamespace = n.oid
		JOIN pg_attribute a   ON a.attrelid = t.oid AND a.attnum = ANY(c.conkey)
		WHERE c.contype = 'u'
		  AND n.nspname = 'public'
		  AND array_length(c.conkey, 1) = 1`)
	if err != nil {
		return nil, fmt.Errorf("failed to query UNIQUE constraints: %w", err)
	}
	defer rows.Close()

	m := make(map[string]map[string]bool)
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return nil, err
		}
		if m[table] == nil {
			m[table] = make(map[string]bool)
		}
		m[table][column] = true
	}
	return m, nil
}

// postgresCheckMap returns map[table][column]=[]values for single-column CHECK IN constraints.
func postgresCheckMap(db *sql.DB) (map[string]map[string][]string, error) {
	rows, err := db.Query(`
		SELECT t.relname, a.attname, pg_get_constraintdef(c.oid)
		FROM pg_constraint c
		JOIN pg_class t       ON c.conrelid = t.oid
		JOIN pg_namespace n   ON t.relnamespace = n.oid
		JOIN pg_attribute a   ON a.attrelid = t.oid AND a.attnum = ANY(c.conkey)
		WHERE c.contype = 'c'
		  AND n.nspname = 'public'
		  AND array_length(c.conkey, 1) = 1`)
	if err != nil {
		return nil, fmt.Errorf("failed to query CHECK constraints: %w", err)
	}
	defer rows.Close()

	m := make(map[string]map[string][]string)
	for rows.Next() {
		var table, column, clause string
		if err := rows.Scan(&table, &column, &clause); err != nil {
			return nil, err
		}
		if vals := parsePostgresCheckValues(clause); len(vals) > 0 {
			if m[table] == nil {
				m[table] = make(map[string][]string)
			}
			m[table][column] = vals
		}
	}
	return m, nil
}

// postgresRangeMap returns map[table][column]=rangeConstraint for CHECK (col >= N AND col <= M).
func postgresRangeMap(db *sql.DB) (map[string]map[string]rangeConstraint, error) {
	rows, err := db.Query(`
		SELECT t.relname, a.attname, pg_get_constraintdef(c.oid)
		FROM pg_constraint c
		JOIN pg_class t       ON c.conrelid = t.oid
		JOIN pg_namespace n   ON t.relnamespace = n.oid
		JOIN pg_attribute a   ON a.attrelid = t.oid AND a.attnum = ANY(c.conkey)
		WHERE c.contype = 'c'
		  AND n.nspname = 'public'
		  AND array_length(c.conkey, 1) = 1`)
	if err != nil {
		return nil, fmt.Errorf("failed to query range CHECK constraints: %w", err)
	}
	defer rows.Close()

	m := make(map[string]map[string]rangeConstraint)
	for rows.Next() {
		var table, column, clause string
		if err := rows.Scan(&table, &column, &clause); err != nil {
			return nil, err
		}
		if min, max, ok := parsePostgresCheckRange(clause); ok {
			if m[table] == nil {
				m[table] = make(map[string]rangeConstraint)
			}
			m[table][column] = rangeConstraint{Min: min, Max: max}
		}
	}
	return m, nil
}

var (
	pgArrayRe = regexp.MustCompile(`ARRAY\[([^\]]+)\]`)
	pgValRe   = regexp.MustCompile(`'([^']+)'`)
	// matches: (col >= N) AND (col <= M)  or  col >= N AND col <= M
	pgRangeRe = regexp.MustCompile(`(?i)(\w+)\s*>=\s*(-?\d+).*?(\w+)\s*<=\s*(-?\d+)`)
	// matches: col BETWEEN N AND M
	pgBetweenRe = regexp.MustCompile(`(?i)(\w+)\s+BETWEEN\s+(-?\d+)\s+AND\s+(-?\d+)`)
)

// parsePostgresCheckRange extracts (min, max) from CHECK constraints like
// "CHECK (((spice_level >= 0) AND (spice_level <= 5)))".
func parsePostgresCheckRange(clause string) (min, max int64, ok bool) {
	// Try BETWEEN first
	if m := pgBetweenRe.FindStringSubmatch(clause); len(m) == 4 {
		lo, err1 := strconv.ParseInt(m[2], 10, 64)
		hi, err2 := strconv.ParseInt(m[3], 10, 64)
		if err1 == nil && err2 == nil {
			return lo, hi, true
		}
	}
	// Try >= … AND <= …
	if m := pgRangeRe.FindStringSubmatch(clause); len(m) == 5 {
		lo, err1 := strconv.ParseInt(m[2], 10, 64)
		hi, err2 := strconv.ParseInt(m[4], 10, 64)
		if err1 == nil && err2 == nil {
			return lo, hi, true
		}
	}
	return 0, 0, false
}

// parsePostgresCheckValues extracts allowed values from a PostgreSQL CHECK constraint
// definition. PostgreSQL normalizes "col IN ('a','b')" to
// "CHECK ((col = ANY (ARRAY['a'::text, 'b'::text])))" so we look for ARRAY[...].
func parsePostgresCheckValues(clause string) []string {
	m := pgArrayRe.FindStringSubmatch(clause)
	if len(m) < 2 {
		return nil
	}
	var values []string
	for _, v := range pgValRe.FindAllStringSubmatch(m[1], -1) {
		values = append(values, v[1])
	}
	return values
}

func postgresEnumValues(db *sql.DB, typeName string) ([]string, error) {
	rows, err := db.Query(`
		SELECT e.enumlabel
		FROM pg_type t
		JOIN pg_enum e ON t.oid = e.enumtypid
		WHERE t.typname = $1
		ORDER BY e.enumsortorder`, typeName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var values []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, nil
}
