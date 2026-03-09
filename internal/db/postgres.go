package db

import (
	"database/sql"
	"fmt"
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

	var tables []Table
	for _, tableName := range tableNames {
		cols, err := postgresColumns(db, tableName, fkMap, pkMap)
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

func postgresColumns(db *sql.DB, tableName string, fkMap map[string]map[string]*ForeignKey, pkMap map[string]map[string]bool) ([]Column, error) {
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

		columns = append(columns, col)
	}
	return columns, nil
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
