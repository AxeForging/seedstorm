package db

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RenderInsert renders rows as one INSERT with literal values, for SQL that is
// read or run as a script (generate, export, dry runs) rather than executed
// with bound parameters. Columns are the sorted union of every row's keys; a
// row without a column gets NULL.
func RenderInsert(tableName string, rows []map[string]interface{}, dbType string) string {
	if len(rows) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var columns []string
	for _, row := range rows {
		for c := range row {
			if !seen[c] {
				seen[c] = true
				columns = append(columns, c)
			}
		}
	}
	sort.Strings(columns)

	var sb strings.Builder
	sb.WriteString("INSERT INTO ")
	sb.WriteString(QuoteIdent(tableName, dbType))
	sb.WriteString(" (")
	for i, c := range columns {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(QuoteIdent(c, dbType))
	}
	sb.WriteString(") VALUES ")
	for i, row := range rows {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteByte('(')
		for j, c := range columns {
			if j > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(sqlLiteral(row[c], dbType))
		}
		sb.WriteByte(')')
	}
	sb.WriteByte(';')
	return sb.String()
}

// sqlLiteral renders one value as a literal the given engine parses back to the
// same value. Postgres keeps standard_conforming_strings (backslashes are
// literal); MySQL's default mode treats backslash as an escape.
func sqlLiteral(v interface{}, dbType string) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case bool:
		if x {
			return "TRUE"
		}
		return "FALSE"
	case int:
		return strconv.Itoa(x)
	case int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", x)
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case time.Time:
		s := x.UTC().Format("2006-01-02 15:04:05.999999")
		if dbType == "pgx" {
			return "'" + s + "+00'"
		}
		return "'" + s + "'"
	case []byte:
		if dbType == "pgx" {
			return `'\x` + hex.EncodeToString(x) + "'"
		}
		return "X'" + hex.EncodeToString(x) + "'"
	case string:
		return quoteString(x, dbType)
	default:
		return quoteString(fmt.Sprint(x), dbType)
	}
}

func quoteString(s, dbType string) string {
	if dbType != "pgx" {
		s = strings.ReplaceAll(s, `\`, `\\`)
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
