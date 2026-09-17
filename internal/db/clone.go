package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// CloneOptions controls schema-only cloning from one connected database to
// another. Cloning is intentionally same-engine: seedstorm's introspection
// metadata is useful for reproducible test databases, not lossless cross-engine
// migration.
type CloneOptions struct {
	DropExisting bool
	DryRun       bool
	// Objects selects views, routines and triggers to clone after tables.
	Objects CloneObjects
}

// CloneResult describes the schema copy operation.
type CloneResult struct {
	Tables     int
	Statements []string
	// Objects counts cloned views, routines and triggers.
	Objects int
	// Skipped lists source objects that could not be cloned, with reasons.
	Skipped []SkippedObject
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
	objects, err := LoadObjects(ctx, sourceType, sourceDSN, opts.Objects)
	if err != nil {
		return CloneResult{}, fmt.Errorf("introspect source objects: %w", err)
	}
	stmts, err := BuildCloneDDL(tables, objects, sourceType, opts.DropExisting)
	if err != nil {
		return CloneResult{}, err
	}
	result := CloneResult{Tables: len(tables), Statements: stmts, Objects: len(objects.Objects), Skipped: objects.Skipped}
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
// schema DDL for tables, foreign keys, indexes and comments. Views, routines
// and triggers are appended by BuildCloneDDL.
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
	done := 0
	markDone := func(stmt string) {
		done++
		emit(done, stmt)
	}
	if dbType == "pgx" {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin schema clone: %w", err)
		}
		if err := execDDLSequence(ctx, tx, stmts, true, markDone); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit schema clone: %w", err)
		}
		return nil
	}
	if err := execDDLSequence(ctx, conn, stmts, false, markDone); err != nil {
		var myErr *mysql.MySQLError
		if errors.As(err, &myErr) && myErr.Number == 1419 {
			return fmt.Errorf("%w (creating routines or triggers with binary logging on needs SUPER or log_bin_trust_function_creators=1 on the target server)", err)
		}
		return err
	}
	return nil
}

type ddlExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// execDDLSequence runs statements in order. Consecutive CREATE VIEW statements
// are retried in passes so views built on other views succeed regardless of
// name order; inside a Postgres transaction each attempt is wrapped in a
// savepoint so a failed attempt does not abort the transaction.
func execDDLSequence(ctx context.Context, exec ddlExecer, stmts []string, savepoints bool, done func(string)) error {
	if savepoints {
		for _, stmt := range stmts {
			if isRoutineCreate(stmt) {
				// Routine bodies may reference views created later in the clone.
				if _, err := exec.ExecContext(ctx, "SET LOCAL check_function_bodies = off"); err != nil {
					return fmt.Errorf("disable function body checks: %w", err)
				}
				break
			}
		}
	}
	for i := 0; i < len(stmts); {
		if !isViewCreate(stmts[i]) {
			if _, err := exec.ExecContext(ctx, stmts[i]); err != nil {
				return fmt.Errorf("execute DDL %q: %w", stmts[i], err)
			}
			done(stmts[i])
			i++
			continue
		}
		j := i
		for j < len(stmts) && isViewCreate(stmts[j]) {
			j++
		}
		attempt := func(stmt string) error {
			if !savepoints {
				_, err := exec.ExecContext(ctx, stmt)
				return err
			}
			if _, err := exec.ExecContext(ctx, "SAVEPOINT seedstorm_view"); err != nil {
				return err
			}
			if _, err := exec.ExecContext(ctx, stmt); err != nil {
				if _, rbErr := exec.ExecContext(ctx, "ROLLBACK TO SAVEPOINT seedstorm_view"); rbErr != nil {
					return errors.Join(err, rbErr)
				}
				return err
			}
			_, err := exec.ExecContext(ctx, "RELEASE SAVEPOINT seedstorm_view")
			return err
		}
		if err := execInPasses(stmts[i:j], attempt, done); err != nil {
			return err
		}
		i = j
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
		name := parts[len(parts)-1]
		if len(parts) >= 5 && strings.EqualFold(parts[2], "IF") && strings.EqualFold(parts[3], "EXISTS") {
			name = parts[4]
		}
		return "drop " + strings.Trim(name, "`\"")
	}
	if len(parts) >= 3 && strings.EqualFold(parts[0], "CREATE") && strings.EqualFold(parts[1], "INDEX") {
		return "index " + strings.Trim(parts[2], "`\"")
	}
	if len(parts) >= 4 && strings.EqualFold(parts[0], "CREATE") && strings.EqualFold(parts[1], "UNIQUE") && strings.EqualFold(parts[2], "INDEX") {
		return "index " + strings.Trim(parts[3], "`\"")
	}
	if verb, kind, name, ok := objectStatement(stmt); ok {
		if verb == "drop" {
			return "drop " + string(kind) + " " + name
		}
		return string(kind) + " " + name
	}
	if strings.EqualFold(parts[0], "COMMENT") {
		return "comment"
	}
	return strings.ToLower(parts[0])
}

func introspectWithConn(conn *sql.DB, dbType string) ([]Table, error) {
	return IntrospectConn(context.Background(), conn, dbType, nil)
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
	} else if dbType == "mysql" {
		// Without it MySQL 5.7 makes a TIMESTAMP NOT NULL with a zero default.
		def += " NULL"
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
			for i, col := range idx.Columns {
				part := QuoteIdent(col, dbType)
				if dbType == "mysql" && i < len(idx.Prefixes) && idx.Prefixes[i] > 0 {
					part += fmt.Sprintf("(%d)", idx.Prefixes[i])
				}
				cols = append(cols, part)
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
