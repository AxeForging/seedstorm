package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// CloneObjects selects which non-table database objects a schema clone copies.
// The zero value copies none, which keeps the table-only clone unchanged.
type CloneObjects struct {
	Views    bool `json:"views"`    // views and (Postgres) materialized views
	Routines bool `json:"routines"` // functions and procedures
	Triggers bool `json:"triggers"`
}

// Any reports whether at least one object kind is selected.
func (o CloneObjects) Any() bool { return o.Views || o.Routines || o.Triggers }

// ParseCloneObjects parses a comma-separated object list such as
// "views,triggers" or "all". An empty string selects nothing.
func ParseCloneObjects(list string) (CloneObjects, error) {
	var out CloneObjects
	for _, raw := range strings.Split(list, ",") {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "":
		case "all":
			out = CloneObjects{Views: true, Routines: true, Triggers: true}
		case "views", "view":
			out.Views = true
		case "routines", "routine", "functions", "procedures":
			out.Routines = true
		case "triggers", "trigger":
			out.Triggers = true
		default:
			return CloneObjects{}, fmt.Errorf("unknown object kind %q (use views, routines, triggers or all)", strings.TrimSpace(raw))
		}
	}
	return out, nil
}

// ObjectKind names a cloneable database object type.
type ObjectKind string

const (
	ObjectView             ObjectKind = "view"
	ObjectMaterializedView ObjectKind = "materialized view"
	ObjectFunction         ObjectKind = "function"
	ObjectProcedure        ObjectKind = "procedure"
	ObjectTrigger          ObjectKind = "trigger"
)

// DBObject is one introspected object with the statement that recreates it.
type DBObject struct {
	Kind   ObjectKind
	Name   string
	Args   string // Postgres identity arguments, used to drop overloaded routines
	Table  string // table a trigger is attached to
	Create string // executable CREATE statement for the target
}

// SkippedObject is an object that exists on the source but cannot be cloned,
// typically because the connected user cannot see its definition.
type SkippedObject struct {
	Kind   ObjectKind `json:"kind"`
	Name   string     `json:"name"`
	Reason string     `json:"reason"`
}

// ObjectSet is the result of object introspection.
type ObjectSet struct {
	Objects []DBObject
	Skipped []SkippedObject
}

// LoadObjects opens a connection to dsn and introspects the selected objects.
func LoadObjects(ctx context.Context, dbType, dsn string, want CloneObjects) (ObjectSet, error) {
	if !want.Any() {
		return ObjectSet{}, nil
	}
	conn, err := sql.Open(dbType, dsn)
	if err != nil {
		return ObjectSet{}, fmt.Errorf("open source: %w", err)
	}
	defer conn.Close()
	return IntrospectObjects(ctx, conn, dbType, want)
}

// IntrospectObjects reads views, routines and triggers from the Postgres
// `public` schema or the connected MySQL database.
func IntrospectObjects(ctx context.Context, conn *sql.DB, dbType string, want CloneObjects) (ObjectSet, error) {
	if !want.Any() {
		return ObjectSet{}, nil
	}
	switch dbType {
	case "pgx":
		return introspectPostgresObjects(ctx, conn, want)
	case "mysql":
		return introspectMySQLObjects(ctx, conn, want)
	default:
		return ObjectSet{}, fmt.Errorf("unsupported database type %q", dbType)
	}
}

// BuildCloneDDL returns table DDL plus object DDL in execution order:
// object drops (triggers, views, routines) → table DDL → routines → views →
// triggers. With an empty ObjectSet it equals BuildSchemaDDL.
func BuildCloneDDL(tables []Table, objects ObjectSet, dbType string, dropExisting bool) ([]string, error) {
	tableStmts, err := BuildSchemaDDL(tables, dbType, dropExisting)
	if err != nil {
		return nil, err
	}
	if len(objects.Objects) == 0 {
		return tableStmts, nil
	}
	var stmts []string
	if dropExisting {
		stmts = append(stmts, BuildObjectDropDDL(objects.Objects, dbType)...)
	}
	stmts = append(stmts, tableStmts...)
	stmts = append(stmts, BuildObjectCreateDDL(objects.Objects)...)
	return stmts, nil
}

func objectCreateRank(kind ObjectKind) int {
	switch kind {
	case ObjectFunction, ObjectProcedure:
		return 0
	case ObjectView, ObjectMaterializedView:
		return 1
	default:
		return 2
	}
}

// BuildObjectCreateDDL orders CREATE statements routines → views → triggers,
// keeping introspection order within a kind. Views that depend on each other
// are resolved at execution time by retrying in passes.
func BuildObjectCreateDDL(objects []DBObject) []string {
	ordered := append([]DBObject(nil), objects...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return objectCreateRank(ordered[i].Kind) < objectCreateRank(ordered[j].Kind)
	})
	stmts := make([]string, 0, len(ordered))
	for _, obj := range ordered {
		stmts = append(stmts, obj.Create)
	}
	return stmts
}

// BuildObjectDropDDL drops objects in reverse dependency order: triggers →
// views → routines.
func BuildObjectDropDDL(objects []DBObject, dbType string) []string {
	ordered := append([]DBObject(nil), objects...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return objectCreateRank(ordered[i].Kind) > objectCreateRank(ordered[j].Kind)
	})
	var stmts []string
	for _, obj := range ordered {
		name := QuoteIdent(obj.Name, dbType)
		if dbType == "pgx" {
			switch obj.Kind {
			case ObjectTrigger:
				stmts = append(stmts, fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s", name, QuoteIdent(obj.Table, dbType)))
			case ObjectView:
				stmts = append(stmts, "DROP VIEW IF EXISTS "+name+" CASCADE")
			case ObjectMaterializedView:
				stmts = append(stmts, "DROP MATERIALIZED VIEW IF EXISTS "+name+" CASCADE")
			case ObjectFunction:
				stmts = append(stmts, fmt.Sprintf("DROP FUNCTION IF EXISTS %s(%s) CASCADE", name, obj.Args))
			case ObjectProcedure:
				stmts = append(stmts, fmt.Sprintf("DROP PROCEDURE IF EXISTS %s(%s) CASCADE", name, obj.Args))
			}
			continue
		}
		switch obj.Kind {
		case ObjectTrigger:
			stmts = append(stmts, "DROP TRIGGER IF EXISTS "+name)
		case ObjectView, ObjectMaterializedView:
			stmts = append(stmts, "DROP VIEW IF EXISTS "+name)
		case ObjectFunction:
			stmts = append(stmts, "DROP FUNCTION IF EXISTS "+name)
		case ObjectProcedure:
			stmts = append(stmts, "DROP PROCEDURE IF EXISTS "+name)
		}
	}
	return stmts
}

// ── Postgres ──────────────────────────────────────────────────────────────────

func introspectPostgresObjects(ctx context.Context, conn *sql.DB, want CloneObjects) (ObjectSet, error) {
	var set ObjectSet
	if want.Views {
		rows, err := conn.QueryContext(ctx, `
			SELECT c.relname, c.relkind::text, pg_get_viewdef(c.oid, true)
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public'
			  AND c.relkind IN ('v', 'm')
			  AND NOT EXISTS (
			    SELECT 1 FROM pg_depend d
			    WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
			ORDER BY c.relname`)
		if err != nil {
			return ObjectSet{}, fmt.Errorf("list views: %w", err)
		}
		for rows.Next() {
			var name, relkind string
			var def sql.NullString
			if err := rows.Scan(&name, &relkind, &def); err != nil {
				rows.Close()
				return ObjectSet{}, err
			}
			kind := ObjectView
			if relkind == "m" {
				kind = ObjectMaterializedView
			}
			body := strings.TrimRight(strings.TrimSpace(def.String), ";")
			if !def.Valid || strings.TrimSpace(body) == "" {
				set.Skipped = append(set.Skipped, SkippedObject{Kind: kind, Name: name, Reason: "definition is not visible to the connected user"})
				continue
			}
			create := "CREATE VIEW " + QuoteIdent(name, "pgx") + " AS\n" + body
			if kind == ObjectMaterializedView {
				create = "CREATE MATERIALIZED VIEW " + QuoteIdent(name, "pgx") + " AS\n" + body + "\nWITH NO DATA"
			}
			set.Objects = append(set.Objects, DBObject{Kind: kind, Name: name, Create: create})
		}
		if err := closeRows(rows); err != nil {
			return ObjectSet{}, fmt.Errorf("list views: %w", err)
		}
	}
	if want.Routines {
		rows, err := conn.QueryContext(ctx, `
			SELECT p.proname, p.prokind::text, pg_get_function_identity_arguments(p.oid), pg_get_functiondef(p.oid)
			FROM pg_proc p
			JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = 'public'
			  AND p.prokind IN ('f', 'p')
			  AND NOT EXISTS (
			    SELECT 1 FROM pg_depend d
			    WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
			ORDER BY p.proname, 3`)
		if err != nil {
			return ObjectSet{}, fmt.Errorf("list routines: %w", err)
		}
		for rows.Next() {
			var name, prokind, args string
			var def sql.NullString
			if err := rows.Scan(&name, &prokind, &args, &def); err != nil {
				rows.Close()
				return ObjectSet{}, err
			}
			kind := ObjectFunction
			if prokind == "p" {
				kind = ObjectProcedure
			}
			if !def.Valid || strings.TrimSpace(def.String) == "" {
				set.Skipped = append(set.Skipped, SkippedObject{Kind: kind, Name: name + "(" + args + ")", Reason: "definition is not visible to the connected user"})
				continue
			}
			set.Objects = append(set.Objects, DBObject{Kind: kind, Name: name, Args: args, Create: strings.TrimSpace(def.String)})
		}
		if err := closeRows(rows); err != nil {
			return ObjectSet{}, fmt.Errorf("list routines: %w", err)
		}
	}
	if want.Triggers {
		rows, err := conn.QueryContext(ctx, `
			SELECT t.tgname, c.relname, pg_get_triggerdef(t.oid, false)
			FROM pg_trigger t
			JOIN pg_class c ON c.oid = t.tgrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public'
			  AND NOT t.tgisinternal
			ORDER BY c.relname, t.tgname`)
		if err != nil {
			return ObjectSet{}, fmt.Errorf("list triggers: %w", err)
		}
		for rows.Next() {
			var name, table string
			var def sql.NullString
			if err := rows.Scan(&name, &table, &def); err != nil {
				rows.Close()
				return ObjectSet{}, err
			}
			if !def.Valid || strings.TrimSpace(def.String) == "" {
				set.Skipped = append(set.Skipped, SkippedObject{Kind: ObjectTrigger, Name: name, Reason: "definition is not visible to the connected user"})
				continue
			}
			set.Objects = append(set.Objects, DBObject{Kind: ObjectTrigger, Name: name, Table: table, Create: strings.TrimSpace(def.String)})
		}
		if err := closeRows(rows); err != nil {
			return ObjectSet{}, fmt.Errorf("list triggers: %w", err)
		}
	}
	return set, nil
}

func closeRows(rows *sql.Rows) error {
	err := rows.Err()
	rows.Close()
	return err
}

// ── MySQL ─────────────────────────────────────────────────────────────────────

type mysqlObjectRef struct {
	kind  ObjectKind
	name  string
	table string
}

func introspectMySQLObjects(ctx context.Context, conn *sql.DB, want CloneObjects) (ObjectSet, error) {
	var schemaName sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&schemaName); err != nil {
		return ObjectSet{}, fmt.Errorf("read current database: %w", err)
	}
	var refs []mysqlObjectRef
	list := func(query string, scan func(rows *sql.Rows) (mysqlObjectRef, error)) error {
		rows, err := conn.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		for rows.Next() {
			ref, err := scan(rows)
			if err != nil {
				rows.Close()
				return err
			}
			refs = append(refs, ref)
		}
		return closeRows(rows)
	}
	if want.Routines {
		err := list(`SELECT ROUTINE_NAME, ROUTINE_TYPE FROM information_schema.ROUTINES
			WHERE ROUTINE_SCHEMA = DATABASE() AND ROUTINE_TYPE IN ('FUNCTION', 'PROCEDURE')
			ORDER BY ROUTINE_NAME, ROUTINE_TYPE`, func(rows *sql.Rows) (mysqlObjectRef, error) {
			var name, typ string
			if err := rows.Scan(&name, &typ); err != nil {
				return mysqlObjectRef{}, err
			}
			kind := ObjectFunction
			if strings.EqualFold(typ, "PROCEDURE") {
				kind = ObjectProcedure
			}
			return mysqlObjectRef{kind: kind, name: name}, nil
		})
		if err != nil {
			return ObjectSet{}, fmt.Errorf("list routines: %w", err)
		}
	}
	if want.Views {
		err := list(`SELECT TABLE_NAME FROM information_schema.VIEWS
			WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME`, func(rows *sql.Rows) (mysqlObjectRef, error) {
			var name string
			err := rows.Scan(&name)
			return mysqlObjectRef{kind: ObjectView, name: name}, err
		})
		if err != nil {
			return ObjectSet{}, fmt.Errorf("list views: %w", err)
		}
	}
	if want.Triggers {
		err := list(`SELECT TRIGGER_NAME, EVENT_OBJECT_TABLE FROM information_schema.TRIGGERS
			WHERE TRIGGER_SCHEMA = DATABASE()
			ORDER BY EVENT_OBJECT_TABLE, ACTION_TIMING, EVENT_MANIPULATION, ACTION_ORDER`, func(rows *sql.Rows) (mysqlObjectRef, error) {
			var name, table string
			err := rows.Scan(&name, &table)
			return mysqlObjectRef{kind: ObjectTrigger, name: name, table: table}, err
		})
		if err != nil {
			return ObjectSet{}, fmt.Errorf("list triggers: %w", err)
		}
	}

	var set ObjectSet
	for _, ref := range refs {
		var query, column string
		quoted := QuoteIdent(ref.name, "mysql")
		switch ref.kind {
		case ObjectView:
			query, column = "SHOW CREATE VIEW "+quoted, "Create View"
		case ObjectFunction:
			query, column = "SHOW CREATE FUNCTION "+quoted, "Create Function"
		case ObjectProcedure:
			query, column = "SHOW CREATE PROCEDURE "+quoted, "Create Procedure"
		default:
			query, column = "SHOW CREATE TRIGGER "+quoted, "SQL Original Statement"
		}
		def, err := showCreateColumn(ctx, conn, query, column)
		if err != nil {
			set.Skipped = append(set.Skipped, SkippedObject{Kind: ref.kind, Name: ref.name, Reason: err.Error()})
			continue
		}
		if !def.Valid || strings.TrimSpace(def.String) == "" {
			set.Skipped = append(set.Skipped, SkippedObject{Kind: ref.kind, Name: ref.name, Reason: "definition is not visible to the connected user (requires being the definer or the SHOW_ROUTINE privilege)"})
			continue
		}
		create := StripMySQLDefiner(strings.TrimSpace(def.String))
		if ref.kind == ObjectView && schemaName.Valid {
			create = StripMySQLSchemaQualifier(create, schemaName.String)
		}
		set.Objects = append(set.Objects, DBObject{Kind: ref.kind, Name: ref.name, Table: ref.table, Create: create})
	}
	return set, nil
}

// showCreateColumn runs a SHOW CREATE statement and returns one named column.
func showCreateColumn(ctx context.Context, conn *sql.DB, query, column string) (sql.NullString, error) {
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return sql.NullString{}, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return sql.NullString{}, err
	}
	idx := -1
	for i, c := range cols {
		if strings.EqualFold(c, column) {
			idx = i
		}
	}
	if idx < 0 {
		return sql.NullString{}, fmt.Errorf("%s: column %q not returned", query, column)
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return sql.NullString{}, err
		}
		return sql.NullString{}, errors.New("object not found")
	}
	values := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return sql.NullString{}, err
	}
	return values[idx], nil
}

// mysqlDefinerHead matches the DEFINER clause in the header of a MySQL
// SHOW CREATE statement: `CREATE [ALGORITHM=x] DEFINER=user@host ...`.
var mysqlDefinerHead = regexp.MustCompile("(?is)^(\\s*CREATE\\s+(?:OR\\s+REPLACE\\s+)?(?:ALGORITHM\\s*=\\s*\\w+\\s+)?)" +
	"DEFINER\\s*=\\s*(?:CURRENT_USER(?:\\s*\\(\\s*\\))?|" + mysqlUserPart + "(?:\\s*@\\s*" + mysqlUserPart + ")?)\\s+")

const mysqlUserPart = "(?:`(?:[^`]|``)*`|'(?:[^'\\\\]|''|\\\\.)*'|\"(?:[^\"\\\\]|\"\"|\\\\.)*\"|[^\\s@`'\"]+)"

// StripMySQLDefiner removes the DEFINER clause from a CREATE statement so the
// object is owned by the user running the clone on the target.
func StripMySQLDefiner(stmt string) string {
	return mysqlDefinerHead.ReplaceAllString(stmt, "${1}")
}

// StripMySQLSchemaQualifier removes `schema`. qualifiers that reference the
// source database, leaving string literals and other identifiers untouched.
func StripMySQLSchemaQualifier(stmt, schema string) string {
	var sb strings.Builder
	sb.Grow(len(stmt))
	for i := 0; i < len(stmt); {
		c := stmt[i]
		switch c {
		case '\'', '"':
			end := scanQuoted(stmt, i, c, true)
			sb.WriteString(stmt[i:end])
			i = end
		case '`':
			end := scanQuoted(stmt, i, '`', false)
			ident := stmt[i:end]
			if end < len(stmt) && stmt[end] == '.' && unquoteBacktick(ident) == schema {
				i = end + 1
				continue
			}
			sb.WriteString(ident)
			i = end
		default:
			sb.WriteByte(c)
			i++
		}
	}
	return sb.String()
}

// scanQuoted returns the index just past the quoted token starting at start.
// A doubled quote is an escaped quote; backslash escapes apply to strings.
func scanQuoted(s string, start int, quote byte, backslash bool) int {
	for i := start + 1; i < len(s); i++ {
		switch {
		case backslash && s[i] == '\\':
			i++
		case s[i] == quote:
			if i+1 < len(s) && s[i+1] == quote {
				i++
				continue
			}
			return i + 1
		}
	}
	return len(s)
}

func unquoteBacktick(ident string) string {
	if len(ident) < 2 || ident[0] != '`' || ident[len(ident)-1] != '`' {
		return ident
	}
	return strings.ReplaceAll(ident[1:len(ident)-1], "``", "`")
}

// ── statement classification ──────────────────────────────────────────────────

// objectStatement classifies a CREATE/DROP statement for a view, routine or
// trigger. verb is "create" or "drop".
func objectStatement(stmt string) (verb string, kind ObjectKind, name string, ok bool) {
	head := strings.TrimSpace(stmt)
	if len(head) > 1024 {
		head = head[:1024]
	}
	tokens := strings.Fields(head)
	if len(tokens) < 3 {
		return "", "", "", false
	}
	switch strings.ToUpper(tokens[0]) {
	case "CREATE":
		verb = "create"
	case "DROP":
		verb = "drop"
	default:
		return "", "", "", false
	}
	materialized := false
	for i := 1; i < len(tokens); i++ {
		upper := strings.ToUpper(tokens[i])
		switch {
		case upper == "=":
			i++ // skip the value of ALGORITHM = x / DEFINER = x
		case strings.HasPrefix(upper, "ALGORITHM=") || strings.HasPrefix(upper, "DEFINER="):
		case verb == "create" && isObjectHeadModifier(upper):
		case verb == "drop" && (upper == "IF" || upper == "EXISTS"):
		case upper == "MATERIALIZED":
			materialized = true
		case upper == "VIEW" || upper == "FUNCTION" || upper == "PROCEDURE" || upper == "TRIGGER":
			kind = ObjectKind(strings.ToLower(upper))
			if materialized && kind == ObjectView {
				kind = ObjectMaterializedView
			}
			for j := i + 1; j < len(tokens); j++ {
				u := strings.ToUpper(tokens[j])
				if verb == "drop" && (u == "IF" || u == "EXISTS") {
					continue
				}
				return verb, kind, lastIdentPart(tokens[j]), true
			}
			return "", "", "", false
		default:
			return "", "", "", false
		}
	}
	return "", "", "", false
}

func isObjectHeadModifier(upper string) bool {
	switch upper {
	case "OR", "REPLACE", "ALGORITHM", "DEFINER", "SQL", "SECURITY", "INVOKER",
		"CONSTRAINT", "TEMP", "TEMPORARY", "RECURSIVE", "UNDEFINED", "MERGE", "TEMPTABLE":
		return true
	}
	return false
}

// lastIdentPart returns the unquoted last part of a possibly schema-qualified
// identifier, stopping at an argument list: public.f(a → f, "s"."v" → v.
func lastIdentPart(token string) string {
	start := 0
	end := len(token)
	var quote byte
	for i := 0; i < len(token); i++ {
		c := token[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '`' {
			quote = c
			continue
		}
		if c == '.' {
			start = i + 1
		}
		if c == '(' {
			end = i
			break
		}
	}
	if start > end {
		start = end
	}
	part := token[start:end]
	if len(part) >= 2 && (part[0] == '"' || part[0] == '`') && part[len(part)-1] == part[0] {
		q := string(part[0])
		part = strings.ReplaceAll(part[1:len(part)-1], q+q, q)
	}
	return part
}

func isViewCreate(stmt string) bool {
	verb, kind, _, ok := objectStatement(stmt)
	return ok && verb == "create" && (kind == ObjectView || kind == ObjectMaterializedView)
}

func isRoutineCreate(stmt string) bool {
	verb, kind, _, ok := objectStatement(stmt)
	return ok && verb == "create" && (kind == ObjectFunction || kind == ObjectProcedure)
}

// execInPasses executes statements that may depend on each other in unknown
// order (views on views). Failed statements are retried while each pass makes
// progress; when a pass succeeds with nothing, the remaining errors are returned.
func execInPasses(stmts []string, exec func(stmt string) error, done func(stmt string)) error {
	pending := stmts
	for len(pending) > 0 {
		var failed []string
		var errs []error
		for _, stmt := range pending {
			if err := exec(stmt); err != nil {
				failed = append(failed, stmt)
				errs = append(errs, fmt.Errorf("execute DDL %q: %w", stmt, err))
				continue
			}
			done(stmt)
		}
		if len(failed) == len(pending) {
			return errors.Join(errs...)
		}
		pending = failed
	}
	return nil
}
