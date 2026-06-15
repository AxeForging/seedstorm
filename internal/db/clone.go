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
	stmts = append(stmts, buildForeignKeyDDL(ordered, dbType)...)
	stmts = append(stmts, buildIndexDDL(ordered, dbType)...)
	stmts = append(stmts, buildCommentDDL(ordered, dbType)...)
	return stmts, nil
}

// ExecSchemaDDL executes generated DDL in order.
func ExecSchemaDDL(ctx context.Context, conn *sql.DB, dbType string, stmts []string) error {
	return ExecSchemaDDLWithProgress(ctx, conn, dbType, stmts, nil)
}

func ExecSchemaDDLWithProgress(ctx context.Context, conn *sql.DB, dbType string, stmts []string, progress func(done, total int, label string)) error {
	if len(stmts) == 0 {
		return nil
	}
	emit := func(done int, stmt string) {
		if progress == nil {
			return
		}
		progress(done, len(stmts), ddlProgressLabel(stmt))
	}
	if dbType == "pgx" {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin schema clone: %w", err)
		}
		for i, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("execute DDL %q: %w", stmt, err)
			}
			emit(i+1, stmt)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit schema clone: %w", err)
		}
		return nil
	}
	for i, stmt := range stmts {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("execute DDL %q: %w", stmt, err)
		}
		emit(i+1, stmt)
	}
	return nil
}

func ddlProgressLabel(stmt string) string {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" {
		return "DDL"
	}
	parts := strings.Fields(stmt)
	if len(parts) == 0 {
		return "DDL"
	}
	if len(parts) >= 3 && strings.EqualFold(parts[0], "CREATE") && strings.EqualFold(parts[1], "TABLE") {
		return "create " + strings.Trim(parts[2], "`\"")
	}
	if len(parts) >= 3 && strings.EqualFold(parts[0], "ALTER") && strings.EqualFold(parts[1], "TABLE") {
		return "fk " + strings.Trim(parts[2], "`\"")
	}
	if len(parts) >= 3 && strings.EqualFold(parts[0], "DROP") && strings.EqualFold(parts[1], "TABLE") {
		return "drop " + strings.Trim(parts[len(parts)-1], "`\"")
	}
	if len(parts) >= 3 && strings.EqualFold(parts[0], "CREATE") && strings.EqualFold(parts[1], "INDEX") {
		return "index " + strings.Trim(parts[2], "`\"")
	}
	if len(parts) >= 4 && strings.EqualFold(parts[0], "CREATE") && strings.EqualFold(parts[1], "UNIQUE") && strings.EqualFold(parts[2], "INDEX") {
		return "index " + strings.Trim(parts[3], "`\"")
	}
	if strings.EqualFold(parts[0], "COMMENT") {
		return "comment"
	}
	return strings.ToLower(parts[0])
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
		def := buildColumnDDL(col, dbType)
		defs = append(defs, def)
		if col.IsPK {
			pkCols = append(pkCols, QuoteIdent(col.Name, dbType))
		}
	}
	if len(pkCols) > 0 {
		defs = append(defs, "PRIMARY KEY ("+strings.Join(pkCols, ", ")+")")
	}
	stmt := fmt.Sprintf("CREATE TABLE %s (\n  %s\n)", QuoteIdent(table.Name, dbType), strings.Join(defs, ",\n  "))
	if dbType == "mysql" && table.Comment != "" {
		stmt += " COMMENT=" + quoteStringLiteral(table.Comment)
	}
	return stmt, nil
}

func buildColumnDDL(col Column, dbType string) string {
	def := fmt.Sprintf("%s %s", QuoteIdent(col.Name, dbType), cloneColumnType(col, dbType))
	if col.Generated != "" {
		def += " GENERATED ALWAYS AS (" + col.Generated + ") STORED"
		if dbType == "mysql" && col.Comment != "" {
			def += " COMMENT " + quoteStringLiteral(col.Comment)
		}
		return def
	}
	if col.AutoIncrement {
		if dbType == "pgx" {
			def += " GENERATED BY DEFAULT AS IDENTITY"
		} else {
			def += " AUTO_INCREMENT"
		}
	}
	if !col.IsNullable || col.IsPK {
		def += " NOT NULL"
	}
	if col.Default != "" && !col.AutoIncrement {
		def += " DEFAULT " + cloneColumnDefault(col, dbType)
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
	if dbType == "mysql" && col.Comment != "" {
		def += " COMMENT " + quoteStringLiteral(col.Comment)
	}
	return def
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

func buildIndexDDL(tables []Table, dbType string) []string {
	var stmts []string
	for _, table := range tables {
		indexes := append([]Index(nil), table.Indexes...)
		sort.Slice(indexes, func(i, j int) bool { return indexes[i].Name < indexes[j].Name })
		for _, idx := range indexes {
			if idx.Name == "" || len(idx.Columns) == 0 {
				continue
			}
			cols := make([]string, 0, len(idx.Columns))
			for _, col := range idx.Columns {
				cols = append(cols, QuoteIdent(col, dbType))
			}
			unique := ""
			if idx.Unique {
				unique = "UNIQUE "
			}
			stmts = append(stmts, fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)",
				unique,
				QuoteIdent(idx.Name, dbType),
				QuoteIdent(table.Name, dbType),
				strings.Join(cols, ", ")))
		}
	}
	return stmts
}

func buildCommentDDL(tables []Table, dbType string) []string {
	if dbType != "pgx" {
		return nil
	}
	var stmts []string
	for _, table := range tables {
		if table.Comment != "" {
			stmts = append(stmts, fmt.Sprintf("COMMENT ON TABLE %s IS %s", QuoteIdent(table.Name, dbType), quoteStringLiteral(table.Comment)))
		}
		cols := append([]Column(nil), table.Columns...)
		sort.Slice(cols, func(i, j int) bool { return cols[i].Name < cols[j].Name })
		for _, col := range cols {
			if col.Comment == "" {
				continue
			}
			stmts = append(stmts, fmt.Sprintf("COMMENT ON COLUMN %s.%s IS %s",
				QuoteIdent(table.Name, dbType),
				QuoteIdent(col.Name, dbType),
				quoteStringLiteral(col.Comment)))
		}
	}
	return stmts
}

func cloneColumnType(col Column, dbType string) string {
	if dbType == "mysql" && len(col.EnumValues) > 0 {
		return "ENUM(" + quotedLiterals(col.EnumValues) + ")"
	}
	if dbType == "pgx" && len(col.EnumValues) > 0 {
		return "TEXT"
	}
	if typ := strings.TrimSpace(col.DDLType); typ != "" {
		return typ
	}
	t := strings.ToLower(strings.TrimSpace(col.Type))
	if t == "" {
		t = "text"
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

func cloneColumnDefault(col Column, dbType string) string {
	if dbType == "pgx" && len(col.EnumValues) > 0 {
		if idx := strings.Index(col.Default, "::"); idx > 0 {
			return col.Default[:idx]
		}
	}
	return col.Default
}

func quotedLiterals(values []string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	return strings.Join(parts, ", ")
}

func quoteStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func orderTablesByName(tables []Table) []Table {
	out := append([]Table(nil), tables...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
