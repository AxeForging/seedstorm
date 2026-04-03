package db

import (
	"fmt"
	"sort"
	"strings"
)

// BuildInsert builds a single-row INSERT statement with ordered columns.
func BuildInsert(tableName string, row map[string]interface{}, dbType string) (string, []interface{}) {
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

	quotedTable := QuoteIdent(tableName, dbType)
	quotedCols := make([]string, len(columns))
	for i, c := range columns {
		quotedCols[i] = QuoteIdent(c, dbType)
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		quotedTable,
		strings.Join(quotedCols, ", "),
		strings.Join(placeholders, ", "),
	)
	return query, values
}

// BuildBatchInsert builds a multi-row INSERT statement with ordered columns.
// All rows must have the same columns; column order is sorted alphabetically.
func BuildBatchInsert(tableName string, rows []map[string]interface{}, dbType string) (string, []interface{}) {
	if len(rows) == 0 {
		return "", nil
	}

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

	quotedTable := QuoteIdent(tableName, dbType)
	quotedCols := make([]string, len(columns))
	for i, c := range columns {
		quotedCols[i] = QuoteIdent(c, dbType)
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES %s",
		quotedTable,
		strings.Join(quotedCols, ", "),
		strings.Join(valueTuples, ", "),
	)
	return query, values
}
