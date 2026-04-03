package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/AxeForging/seedstorm/internal/db"
)

// normalizeDBType converts user-facing db names to driver names.
func normalizeDBType(dbType string) string {
	if dbType == "postgres" || dbType == "postgresql" {
		return "pgx"
	}
	return dbType
}

// quoteIdent quotes a SQL identifier (table or column name).
// PostgreSQL uses double-quotes, MySQL uses backticks.
func quoteIdent(name, dbType string) string {
	return db.QuoteIdent(name, dbType)
}

// buildInsert builds a single-row INSERT statement with ordered columns.
func buildInsert(tableName string, row map[string]interface{}, dbType string) (string, []interface{}) {
	columns := make([]string, 0, len(row))
	for colName := range row {
		columns = append(columns, colName)
	}
	sort.Strings(columns)

	placeholders := make([]string, 0, len(columns))
	values := make([]interface{}, 0, len(columns))

	for i, colName := range columns {
		if dbType == "pgx" {
			placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
		} else {
			placeholders = append(placeholders, "?")
		}
		values = append(values, row[colName])
	}

	quotedTable := quoteIdent(tableName, dbType)
	quotedCols := make([]string, len(columns))
	for i, c := range columns {
		quotedCols[i] = quoteIdent(c, dbType)
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		quotedTable,
		strings.Join(quotedCols, ", "),
		strings.Join(placeholders, ", "),
	)
	return query, values
}

// buildBatchInsert builds a multi-row INSERT statement with ordered columns.
// All rows must have the same columns; column order is taken from the first row
// and sorted alphabetically for determinism.
func buildBatchInsert(tableName string, rows []map[string]interface{}, dbType string) (string, []interface{}) {
	if len(rows) == 0 {
		return "", nil
	}

	// Derive sorted column list from the first row.
	columns := make([]string, 0, len(rows[0]))
	for colName := range rows[0] {
		columns = append(columns, colName)
	}
	sort.Strings(columns)

	values := make([]interface{}, 0, len(columns)*len(rows))
	valueTuples := make([]string, 0, len(rows))
	paramIdx := 1

	for _, row := range rows {
		placeholders := make([]string, 0, len(columns))
		for _, colName := range columns {
			if dbType == "pgx" {
				placeholders = append(placeholders, fmt.Sprintf("$%d", paramIdx))
			} else {
				placeholders = append(placeholders, "?")
			}
			values = append(values, row[colName])
			paramIdx++
		}
		valueTuples = append(valueTuples, "("+strings.Join(placeholders, ", ")+")")
	}

	quotedTable := quoteIdent(tableName, dbType)
	quotedCols := make([]string, len(columns))
	for i, c := range columns {
		quotedCols[i] = quoteIdent(c, dbType)
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES %s",
		quotedTable,
		strings.Join(quotedCols, ", "),
		strings.Join(valueTuples, ", "),
	)
	return query, values
}
