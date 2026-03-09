package db

import (
	"database/sql"
	"fmt"
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

	var tables []Table
	for _, tableName := range tableNames {
		cols, err := mysqlColumns(db, dbName, tableName, fkMap)
		if err != nil {
			return nil, fmt.Errorf("failed to introspect table %s: %w", tableName, err)
		}
		tables = append(tables, Table{Name: tableName, Columns: cols})
	}

	return tables, nil
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

func mysqlColumns(db *sql.DB, dbName, tableName string, fkMap map[string]map[string]*ForeignKey) ([]Column, error) {
	rows, err := db.Query(`
		SELECT
			COLUMN_NAME,
			DATA_TYPE,
			COLUMN_TYPE,
			IS_NULLABLE,
			COLUMN_KEY
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
		var name, dataType, columnType, isNullable, columnKey string
		if err := rows.Scan(&name, &dataType, &columnType, &isNullable, &columnKey); err != nil {
			return nil, err
		}

		col := Column{
			Name:       name,
			Type:       strings.ToLower(dataType),
			IsNullable: isNullable == "YES",
			IsPK:       columnKey == "PRI",
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

		columns = append(columns, col)
	}
	return columns, nil
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
