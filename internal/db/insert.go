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

const (
	// maxPlaceholders is the bind-parameter limit of one statement on both
	// Postgres and MySQL.
	maxPlaceholders = 65_535
	// maxBatchBytes keeps one INSERT well under MySQL 5.7's default 4MB packet.
	maxBatchBytes = 1 << 20
)

// SplitBatches groups rows, in order, into INSERT-sized batches of at most
// maxRows rows, never exceeding the placeholder limit or about 1MB of values.
// A row larger than the byte budget goes alone.
func SplitBatches(rows []map[string]interface{}, maxRows int) [][]map[string]interface{} {
	if maxRows < 1 {
		maxRows = 1
	}
	var out [][]map[string]interface{}
	start, bytes := 0, 0
	for i, row := range rows {
		size := rowBytes(row)
		n := i - start
		if n > 0 && (n >= maxRows || (n+1)*len(row) > maxPlaceholders || bytes+size > maxBatchBytes) {
			out = append(out, rows[start:i])
			start, bytes = i, 0
		}
		bytes += size
	}
	if start < len(rows) {
		out = append(out, rows[start:])
	}
	return out
}

// rowBytes estimates the wire size of a row's values: the data plus a small
// per-value overhead for the protocol's type and length framing.
func rowBytes(row map[string]interface{}) int {
	const framing = 8
	n := 0
	for _, v := range row {
		switch x := v.(type) {
		case string:
			n += len(x) + framing
		case []byte:
			n += len(x) + framing
		default:
			n += 16
		}
	}
	return n
}
