package faker

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// ColumnOverride computes a column's final value once a table's rows exist.
// row is the 0-based index of the row within this run and auto is the value the
// generator produced for the cell, so an override can wrap it (a prefix) or
// replace it outright.
type ColumnOverride func(row int, auto interface{}) (interface{}, error)

// Overrides maps table → column → override.
type Overrides map[string]map[string]ColumnOverride

// applyOverrides rewrites overridden columns in place. It runs after PKs, FKs,
// self-references and sequences are final: override targets are never PK or FK
// columns, so nothing references the values being replaced.
func applyOverrides(rows []map[string]interface{}, table schema.Table, tableName string, overrides map[string]ColumnOverride, rowOffset int) error {
	if len(overrides) == 0 {
		return nil
	}
	cols := make([]string, 0, len(overrides))
	for colName := range overrides {
		if _, ok := table.Columns[colName]; ok {
			cols = append(cols, colName)
		}
	}
	sort.Strings(cols)
	for _, colName := range cols {
		col := table.Columns[colName]
		fn := overrides[colName]
		for i := range rows {
			v, err := fn(rowOffset+i, rows[i][colName])
			if err != nil {
				return fmt.Errorf("rule for %s.%s: %w", tableName, colName, err)
			}
			v, err = CoerceValue(col, v)
			if err != nil {
				return fmt.Errorf("rule for %s.%s: %w", tableName, colName, err)
			}
			rows[i][colName] = v
		}
	}
	return nil
}

// CoerceValue converts a rule's output into a value the column accepts:
// numeric and boolean columns parse strings, string columns stringify and
// respect their declared length. Unparseable input is an error naming the
// value rather than a database error later.
func CoerceValue(col schema.Column, v interface{}) (interface{}, error) {
	if v == nil {
		return nil, nil
	}
	t := strings.ToLower(strings.TrimSpace(col.Type))
	s, isString := v.(string)
	switch {
	case isString && (t == "tinyint" || t == "bit"):
		// MySQL spells BOOLEAN as TINYINT(1) and flags as BIT(1): accept the
		// words a portable profile uses, and numbers for real tinyints.
		trimmed := strings.TrimSpace(s)
		if b, err := strconv.ParseBool(trimmed); err == nil && (t == "bit" || !isDigits(trimmed)) {
			return b, nil
		}
		if n, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return n, nil
		}
		return nil, fmt.Errorf("value %q is not a number or boolean but the column is %s", s, col.Type)
	case isNumericType(t):
		if !isString {
			return v, nil
		}
		trimmed := strings.TrimSpace(s)
		if n, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return n, nil
		}
		if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return f, nil
		}
		return nil, fmt.Errorf("value %q is not a number but the column is %s", s, col.Type)
	case t == "bool" || t == "boolean":
		if !isString {
			return v, nil
		}
		b, err := strconv.ParseBool(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("value %q is not a boolean but the column is %s", s, col.Type)
		}
		return b, nil
	case isStringColType(t):
		if !isString {
			s = fmt.Sprintf("%v", v)
		}
		return constrainStringValue(s, col), nil
	}
	return v, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Value kinds reported by ValueKind.
const (
	KindText   = "text"
	KindNumber = "number"
	KindBool   = "bool"
	KindOther  = "other"
)

// ValueKind classifies a column type by the values CoerceValue accepts for it:
// text, number, bool, or other (temporal, uuid, json, binary...).
func ValueKind(colType string) string {
	t := strings.ToLower(strings.TrimSpace(colType))
	switch {
	case isNumericType(t):
		return KindNumber
	case t == "bool" || t == "boolean":
		return KindBool
	case isStringColType(t):
		return KindText
	}
	return KindOther
}

// withoutColumns returns a shallow copy of table minus the named columns, used
// to hide overridden columns from enum detection.
func withoutColumns(table schema.Table, drop map[string]ColumnOverride) schema.Table {
	if len(drop) == 0 {
		return table
	}
	out := schema.Table{Columns: make(map[string]schema.Column, len(table.Columns))}
	for name, col := range table.Columns {
		if _, skip := drop[name]; !skip {
			out.Columns[name] = col
		}
	}
	return out
}
