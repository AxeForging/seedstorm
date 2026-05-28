package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// CloneOptions controls schema-only cloning from one connected database to
// another. Cloning is intentionally same-engine: seedstorm's introspection
// metadata is useful for reproducible test databases, not lossless cross-engine
// migration.
type CloneOptions struct {
	DropExisting bool
	DryRun       bool
}

// CloneResult describes the schema copy operation.
type CloneResult struct {
	Tables     int
	Statements []string
}

// CloneSchema introspects source and creates the same table structure in target.
func CloneSchema(ctx context.Context, sourceType, sourceDSN, targetType, targetDSN string, opts CloneOptions) (CloneResult, error) {
	if sourceType != targetType {
		return CloneResult{}, fmt.Errorf("schema clone requires matching database types: source %q target %q", sourceType, targetType)
	}
	tables, err := Introspect(sourceType, sourceDSN)
	if err != nil {
		return CloneResult{}, fmt.Errorf("introspect source: %w", err)
	}
	stmts, err := BuildSchemaDDL(tables, sourceType, opts.DropExisting)
	if err != nil {
		return CloneResult{}, err
	}
	result := CloneResult{Tables: len(tables), Statements: stmts}
	if opts.DryRun {
		return result, nil
	}

	conn, err := sql.Open(targetType, targetDSN)
	if err != nil {
		return CloneResult{}, fmt.Errorf("open target: %w", err)
	}
	defer conn.Close()
	if err := conn.PingContext(ctx); err != nil {
		return CloneResult{}, fmt.Errorf("ping target: %w", err)
	}
	if !opts.DropExisting {
		existing, err := introspectWithConn(conn, targetType)
		if err != nil {
			return CloneResult{}, fmt.Errorf("inspect target: %w", err)
		}
		if len(existing) > 0 {
			return CloneResult{}, fmt.Errorf("target database is not empty (%d tables); rerun with --drop-existing to replace it", len(existing))
		}
	}
	if err := ExecSchemaDDL(ctx, conn, targetType, stmts); err != nil {
		return CloneResult{}, err
	}
	return result, nil
}

// BuildSchemaDDL converts seedstorm's introspection metadata into executable
// schema DDL. It covers the constraints seedstorm understands and deliberately
// omits unsupported database objects such as indexes, views, triggers, and
// procedural code.
func BuildSchemaDDL(tables []Table, dbType string, dropExisting bool) ([]string, error) {
	if dbType != "pgx" && dbType != "mysql" {
		return nil, fmt.Errorf("unsupported database type %q", dbType)
	}
	ordered := orderTablesByName(tables)
	var stmts []string
	if dropExisting {
		for i := len(ordered) - 1; i >= 0; i-- {
			name := QuoteIdent(ordered[i].Name, dbType)
			if dbType == "pgx" {
				stmts = append(stmts, "DROP TABLE IF EXISTS "+name+" CASCADE")
			} else {
				stmts = append(stmts, "DROP TABLE IF EXISTS "+name)
			}
		}
	}
	if dbType == "mysql" && dropExisting {
		stmts = append([]string{"SET FOREIGN_KEY_CHECKS=0"}, stmts...)
		stmts = append(stmts, "SET FOREIGN_KEY_CHECKS=1")
	}
	for _, table := range ordered {
		stmt, err := buildCreateTable(table, dbType)
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, stmt)
	}
	for _, stmt := range buildForeignKeyDDL(ordered, dbType) {
		stmts = append(stmts, stmt)
	}
	return stmts, nil
}

// ExecSchemaDDL executes generated DDL in order.
func ExecSchemaDDL(ctx context.Context, conn *sql.DB, dbType string, stmts []string) error {
	if len(stmts) == 0 {
		return nil
	}
	if dbType == "pgx" {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin schema clone: %w", err)
		}
		for _, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("execute DDL %q: %w", stmt, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit schema clone: %w", err)
		}
		return nil
	}
	for _, stmt := range stmts {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("execute DDL %q: %w", stmt, err)
		}
	}
	return nil
}

func introspectWithConn(conn *sql.DB, dbType string) ([]Table, error) {
	switch dbType {
	case "pgx":
		return introspectPostgres(conn)
	case "mysql":
		return introspectMySQL(conn)
	default:
		return nil, fmt.Errorf("unsupported database type %q", dbType)
	}
}

func buildCreateTable(table Table, dbType string) (string, error) {
	if table.Name == "" {
		return "", errors.New("table name is empty")
	}
	cols := append([]Column(nil), table.Columns...)
	sort.Slice(cols, func(i, j int) bool { return cols[i].Name < cols[j].Name })
	var defs []string
	var pkCols []string
	for _, col := range cols {
		if col.Name == "" {
			return "", fmt.Errorf("table %s has empty column name", table.Name)
		}
		def := fmt.Sprintf("%s %s", QuoteIdent(col.Name, dbType), cloneColumnType(col, dbType))
		if !col.IsNullable || col.IsPK {
			def += " NOT NULL"
		}
		if col.Unique && !col.IsPK {
			def += " UNIQUE"
		}
		if len(col.CheckValues) > 0 {
			def += " CHECK (" + QuoteIdent(col.Name, dbType) + " IN (" + quotedLiterals(col.CheckValues) + "))"
		}
		if dbType == "pgx" && len(col.EnumValues) > 0 {
			def += " CHECK (" + QuoteIdent(col.Name, dbType) + " IN (" + quotedLiterals(col.EnumValues) + "))"
		}
		if col.CheckMin != nil && col.CheckMax != nil {
			def += fmt.Sprintf(" CHECK (%s BETWEEN %d AND %d)", QuoteIdent(col.Name, dbType), *col.CheckMin, *col.CheckMax)
		}
		defs = append(defs, def)
		if col.IsPK {
			pkCols = append(pkCols, QuoteIdent(col.Name, dbType))
		}
	}
	if len(pkCols) > 0 {
		defs = append(defs, "PRIMARY KEY ("+strings.Join(pkCols, ", ")+")")
	}
	return fmt.Sprintf("CREATE TABLE %s (\n  %s\n)", QuoteIdent(table.Name, dbType), strings.Join(defs, ",\n  ")), nil
}

func buildForeignKeyDDL(tables []Table, dbType string) []string {
	var stmts []string
	for _, table := range tables {
		cols := append([]Column(nil), table.Columns...)
		sort.Slice(cols, func(i, j int) bool { return cols[i].Name < cols[j].Name })
		for _, col := range cols {
			if col.FK == nil {
				continue
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD FOREIGN KEY (%s) REFERENCES %s (%s)",
				QuoteIdent(table.Name, dbType),
				QuoteIdent(col.Name, dbType),
				QuoteIdent(col.FK.TableName, dbType),
				QuoteIdent(col.FK.ColumnName, dbType)))
		}
	}
	return stmts
}

func cloneColumnType(col Column, dbType string) string {
	t := strings.ToLower(strings.TrimSpace(col.Type))
	if t == "" {
		t = "text"
	}
	if dbType == "mysql" && len(col.EnumValues) > 0 {
		return "ENUM(" + quotedLiterals(col.EnumValues) + ")"
	}
	if dbType == "pgx" && len(col.EnumValues) > 0 {
		return "TEXT"
	}
	switch t {
	case "character varying":
		if dbType == "mysql" {
			return "VARCHAR(255)"
		}
		return "VARCHAR"
	case "timestamp without time zone", "timestamp with time zone":
		return "TIMESTAMP"
	case "double precision":
		if dbType == "mysql" {
			return "DOUBLE"
		}
	case "bool":
		return "BOOLEAN"
	case "int", "int4":
		return "INTEGER"
	case "int8":
		return "BIGINT"
	case "float8":
		if dbType == "mysql" {
			return "DOUBLE"
		}
		return "DOUBLE PRECISION"
	}
	if dbType == "mysql" {
		switch t {
		case "varchar":
			return "VARCHAR(255)"
		case "text", "longtext", "mediumtext", "json", "date", "time", "datetime", "timestamp", "boolean", "bool", "integer", "bigint", "smallint", "decimal", "numeric", "float", "double":
			return strings.ToUpper(t)
		}
		return "TEXT"
	}
	switch t {
	case "varchar":
		return "VARCHAR"
	case "text", "json", "jsonb", "date", "time", "timestamp", "boolean", "integer", "bigint", "smallint", "numeric", "decimal", "real", "uuid":
		return strings.ToUpper(t)
	}
	return "TEXT"
}

func quotedLiterals(values []string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	return strings.Join(parts, ", ")
}

func orderTablesByName(tables []Table) []Table {
	out := append([]Table(nil), tables...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
