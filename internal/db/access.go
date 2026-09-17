package db

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// TableAccess is what the connected user may do with one table.
type TableAccess struct {
	Select   bool `json:"select"`
	Insert   bool `json:"insert"`
	Update   bool `json:"update"`
	Delete   bool `json:"delete"`
	Truncate bool `json:"truncate"`
}

// Access describes the privileges of the connected database user within the
// scope seedstorm works in: schema public on PostgreSQL, the connection's
// current database on MySQL.
type Access struct {
	// User is the current user as the server reports it (MySQL: user@host).
	User string `json:"user"`
	// Database is the database the connection is using.
	Database string `json:"database"`
	// Superuser is true for PostgreSQL rolsuper, and for MySQL ALL PRIVILEGES
	// or SUPER on *.*.
	Superuser bool `json:"superuser"`
	// CreateTables is true when the user may CREATE tables in the scope
	// (PostgreSQL: CREATE on schema public; MySQL: CREATE on the database).
	CreateTables bool `json:"createTables"`
	// Tables holds the access for every base table in scope, keyed by the same
	// names Introspect returns.
	Tables map[string]TableAccess `json:"tables"`
	// Notes lists anything the report could not determine with certainty.
	Notes []string `json:"notes"`
}

// InspectAccess reports what the user behind conn is allowed to do. dbType is
// "pgx" (PostgreSQL) or "mysql".
func InspectAccess(ctx context.Context, conn *sql.DB, dbType string) (acc Access, err error) {
	var inspect func(context.Context, Querier) (Access, error)
	switch dbType {
	case "pgx", "postgres", "postgresql":
		inspect = inspectPostgresAccess
	case "mysql":
		inspect = inspectMySQLAccess
	default:
		return Access{}, fmt.Errorf("inspect access: unsupported database type %q", dbType)
	}
	err = ReadOnce(ctx, conn, dbType, ReadLimits{}, func(ctx context.Context, q Querier) error {
		acc, err = inspect(ctx, q)
		return err
	})
	return acc, err
}

func inspectPostgresAccess(ctx context.Context, conn Querier) (Access, error) {
	acc := Access{Tables: map[string]TableAccess{}, Notes: []string{}}
	var hasSchema, usage bool
	err := conn.QueryRowContext(ctx, `
		SELECT current_user::text,
		       current_database()::text,
		       COALESCE((SELECT rolsuper FROM pg_roles WHERE rolname = current_user), false),
		       n.oid IS NOT NULL,
		       CASE WHEN n.oid IS NULL THEN false ELSE has_schema_privilege(n.oid, 'CREATE') END,
		       CASE WHEN n.oid IS NULL THEN false ELSE has_schema_privilege(n.oid, 'USAGE') END
		FROM (SELECT 1) AS one
		LEFT JOIN pg_namespace n ON n.nspname = 'public'`).
		Scan(&acc.User, &acc.Database, &acc.Superuser, &hasSchema, &acc.CreateTables, &usage)
	if err != nil {
		return Access{}, fmt.Errorf("inspect access: read current user: %w", err)
	}
	if !hasSchema {
		acc.Notes = append(acc.Notes, "schema public does not exist")
		return acc, nil
	}

	rows, err := conn.QueryContext(ctx, `
		SELECT c.relname::text,
		       has_table_privilege(c.oid, 'SELECT'),
		       has_table_privilege(c.oid, 'INSERT'),
		       has_table_privilege(c.oid, 'UPDATE'),
		       has_table_privilege(c.oid, 'DELETE'),
		       has_table_privilege(c.oid, 'TRUNCATE'),
		       c.relrowsecurity
		         AND NOT COALESCE((SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user), false)
		         AND (c.relforcerowsecurity OR NOT pg_has_role(c.relowner, 'USAGE'))
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		  AND c.relkind IN ('r', 'p')
		ORDER BY c.relname`)
	if err != nil {
		return Access{}, fmt.Errorf("inspect access: read table privileges: %w", err)
	}
	defer rows.Close()
	var rlsTables []string
	for rows.Next() {
		var name string
		var ta TableAccess
		var rls bool
		if err := rows.Scan(&name, &ta.Select, &ta.Insert, &ta.Update, &ta.Delete, &ta.Truncate, &rls); err != nil {
			return Access{}, fmt.Errorf("inspect access: scan table privileges: %w", err)
		}
		if !usage {
			// Table grants are useless without USAGE on the schema.
			ta = TableAccess{}
		}
		if rls {
			rlsTables = append(rlsTables, name)
		}
		acc.Tables[name] = ta
	}
	if err := rows.Err(); err != nil {
		return Access{}, fmt.Errorf("inspect access: read table privileges: %w", err)
	}
	if !usage {
		acc.Notes = append(acc.Notes, "no USAGE privilege on schema public, so table privileges cannot be used")
	}
	if len(rlsTables) > 0 {
		acc.Notes = append(acc.Notes, "row-level security may restrict which rows can be read or written in: "+strings.Join(rlsTables, ", "))
	}
	return acc, nil
}

// inspectMySQLAccess runs on one session (ReadOnce pins it), so CURRENT_ROLE()
// and SHOW GRANTS describe the same connection.
func inspectMySQLAccess(ctx context.Context, c Querier) (Access, error) {
	var user string
	var database sql.NullString
	if err := c.QueryRowContext(ctx, `SELECT CURRENT_USER(), DATABASE()`).Scan(&user, &database); err != nil {
		return Access{}, fmt.Errorf("inspect access: read current user: %w", err)
	}

	var tables []string
	if database.Valid {
		rows, err := c.QueryContext(ctx, `
			SELECT TABLE_NAME
			FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = ?
			  AND TABLE_TYPE = 'BASE TABLE'
			ORDER BY TABLE_NAME`, database.String)
		if err != nil {
			return Access{}, fmt.Errorf("inspect access: list tables: %w", err)
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				_ = rows.Close()
				return Access{}, fmt.Errorf("inspect access: list tables: %w", err)
			}
			tables = append(tables, n)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return Access{}, fmt.Errorf("inspect access: list tables: %w", err)
		}
	}

	lines, err := mysqlShowGrants(ctx, c, `SHOW GRANTS`)
	if err != nil {
		return Access{}, fmt.Errorf("inspect access: %w", err)
	}

	var notes []string
	if roles := mysqlGrantedRoles(lines); len(roles) > 0 {
		var active sql.NullString
		if err := c.QueryRowContext(ctx, `SELECT CURRENT_ROLE()`).Scan(&active); err != nil {
			notes = append(notes, "roles are granted ("+strings.Join(roles, ", ")+") but their privileges could not be expanded: "+err.Error())
		} else if !active.Valid || strings.EqualFold(active.String, "NONE") || active.String == "" {
			notes = append(notes, "roles are granted ("+strings.Join(roles, ", ")+") but none is active in this session, so their privileges do not apply")
		} else {
			expanded, err := mysqlShowGrants(ctx, c, `SHOW GRANTS FOR CURRENT_USER() USING `+active.String)
			if err != nil {
				notes = append(notes, "roles are active ("+active.String+") but their privileges could not be expanded: "+err.Error())
			} else {
				lines = expanded
			}
		}
	}

	wildcards := true
	var partial sql.NullInt64
	if err := c.QueryRowContext(ctx, `SELECT @@partial_revokes`).Scan(&partial); err == nil && partial.Valid && partial.Int64 == 1 {
		// With partial_revokes on, % and _ in database grants are literal.
		wildcards = false
	}

	acc := parseMySQLGrants(lines, database.String, tables, wildcards)
	acc.User = user
	if !database.Valid {
		acc.Notes = append(acc.Notes, "no database selected on this connection")
	}
	acc.Notes = append(append([]string{}, notes...), acc.Notes...)
	return acc, nil
}

func mysqlShowGrants(ctx context.Context, c Querier, query string) ([]string, error) {
	rows, err := c.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", query, err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, fmt.Errorf("%s: %w", query, err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", query, err)
	}
	return lines, nil
}

// mysqlPriv is a bit set of the MySQL privileges seedstorm cares about.
type mysqlPriv uint16

const (
	mysqlSelect mysqlPriv = 1 << iota
	mysqlInsert
	mysqlUpdate
	mysqlDelete
	mysqlDrop
	mysqlCreate
	mysqlSuper
	mysqlAll // ALL [PRIVILEGES]; implies every other bit
)

const mysqlEveryPriv = mysqlSelect | mysqlInsert | mysqlUpdate | mysqlDelete | mysqlDrop | mysqlCreate | mysqlSuper | mysqlAll

func mysqlPrivFromName(name string) mysqlPriv {
	switch name {
	case "SELECT":
		return mysqlSelect
	case "INSERT":
		return mysqlInsert
	case "UPDATE":
		return mysqlUpdate
	case "DELETE":
		return mysqlDelete
	case "DROP":
		return mysqlDrop
	case "CREATE":
		return mysqlCreate
	case "SUPER":
		return mysqlSuper
	case "ALL", "ALL PRIVILEGES":
		return mysqlEveryPriv
	}
	return 0
}

type mysqlGrantScope int

const (
	mysqlScopeGlobal mysqlGrantScope = iota
	mysqlScopeDatabase
	mysqlScopeTable
)

// mysqlGrant is one parsed GRANT or REVOKE line with an object.
type mysqlGrant struct {
	revoke bool
	scope  mysqlGrantScope
	db     string // raw database pattern as written (unquoted, escapes kept)
	table  string
	privs  mysqlPriv
}

// parseMySQLGrants turns SHOW GRANTS output into an Access for database and its
// tables. wildcards reports whether % and _ in database-level grants act as
// patterns (false when partial_revokes is enabled). The result's User is left
// empty.
func parseMySQLGrants(lines []string, database string, tables []string, wildcards bool) Access {
	acc := Access{Database: database, Tables: make(map[string]TableAccess, len(tables)), Notes: []string{}}

	var global, revoked, exactDB, wildDB mysqlPriv
	wildMatches := 0
	tablePrivs := map[string]mysqlPriv{}
	for _, line := range lines {
		g, ok := parseMySQLGrantLine(line)
		if !ok {
			continue
		}
		switch g.scope {
		case mysqlScopeGlobal:
			if !g.revoke {
				global |= g.privs
			}
		case mysqlScopeDatabase:
			if g.revoke {
				// Partial revokes name a literal database and only subtract
				// from global privileges.
				if unescapeMySQLPattern(g.db) == database {
					revoked |= g.privs
				}
				continue
			}
			if !wildcards || !hasMySQLWildcard(g.db) {
				if unescapeMySQLPattern(g.db) == database {
					exactDB |= g.privs
				}
				continue
			}
			if mysqlPatternMatch(g.db, database) {
				wildDB |= g.privs
				wildMatches++
			}
		case mysqlScopeTable:
			if !g.revoke && g.db == database {
				tablePrivs[g.table] |= g.privs
			}
		}
	}

	dbPrivs := exactDB
	if exactDB == 0 {
		dbPrivs = wildDB
		if wildMatches > 1 {
			acc.Notes = append(acc.Notes, "several wildcard database grants match "+database+"; MySQL applies only the most specific one, so this report may overstate access")
		}
	}
	effective := (global &^ revoked) | dbPrivs

	acc.Superuser = global&mysqlAll != 0 || global&mysqlSuper != 0
	acc.CreateTables = database != "" && effective&mysqlCreate != 0
	for _, t := range tables {
		p := effective | tablePrivs[t]
		acc.Tables[t] = TableAccess{
			Select:   p&mysqlSelect != 0,
			Insert:   p&mysqlInsert != 0,
			Update:   p&mysqlUpdate != 0,
			Delete:   p&mysqlDelete != 0,
			Truncate: p&mysqlDrop != 0, // TRUNCATE TABLE requires DROP
		}
	}
	return acc
}

// mysqlGrantedRoles returns the roles named in role-grant lines
// (GRANT `r`@`%` TO ...), as written.
func mysqlGrantedRoles(lines []string) []string {
	var roles []string
	for _, line := range lines {
		text, mask := mysqlLineMask(line)
		if !strings.HasPrefix(mask, "GRANT ") || strings.Contains(mask, " ON ") {
			continue
		}
		to := strings.Index(mask, " TO ")
		if to < 0 {
			continue
		}
		for _, r := range splitMasked(text[len("GRANT "):to], mask[len("GRANT "):to], ',') {
			if r = strings.TrimSpace(r); r != "" {
				roles = append(roles, r)
			}
		}
	}
	sort.Strings(roles)
	return roles
}

// parseMySQLGrantLine parses a GRANT/REVOKE line that names privileges on an
// object. Role grants, routine grants and PROXY grants report ok=false.
func parseMySQLGrantLine(line string) (mysqlGrant, bool) {
	text, mask := mysqlLineMask(line)
	var g mysqlGrant
	var verb, target string
	switch {
	case strings.HasPrefix(mask, "GRANT "):
		verb, target = "GRANT ", " TO "
	case strings.HasPrefix(mask, "REVOKE "):
		verb, target, g.revoke = "REVOKE ", " FROM ", true
	default:
		return g, false
	}
	on := strings.Index(mask, " ON ")
	if on < 0 {
		return g, false // role grant
	}
	end := strings.Index(mask[on:], target)
	if end < 0 {
		end = len(mask)
	} else {
		end += on
	}

	privText, privMask := text[len(verb):on], mask[len(verb):on]
	pos := 0
	for _, part := range splitMasked(privText, privMask, ',') {
		partMask := privMask[pos : pos+len(part)]
		pos += len(part) + 1
		if strings.Contains(partMask, "(") {
			continue // column-level grant: no table-level privilege
		}
		g.privs |= mysqlPrivFromName(strings.Join(strings.Fields(strings.ToUpper(part)), " "))
	}
	if g.privs == 0 {
		return g, false
	}

	objText := strings.TrimSpace(text[on+len(" ON ") : end])
	objMask := strings.TrimSpace(mask[on+len(" ON ") : end])
	switch {
	case strings.HasPrefix(objMask, "TABLE "):
		objText, objMask = strings.TrimSpace(objText[len("TABLE "):]), strings.TrimSpace(objMask[len("TABLE "):])
	case strings.HasPrefix(objMask, "PROCEDURE "), strings.HasPrefix(objMask, "FUNCTION "):
		return g, false
	}
	dot := strings.Index(objMask, ".")
	if dot < 0 {
		return g, false
	}
	left, right := strings.TrimSpace(objText[:dot]), strings.TrimSpace(objText[dot+1:])
	switch {
	case left == "*" && right == "*":
		g.scope = mysqlScopeGlobal
	case right == "*":
		g.scope, g.db = mysqlScopeDatabase, unquoteMySQLIdent(left)
	default:
		g.scope, g.db, g.table = mysqlScopeTable, unquoteMySQLIdent(left), unquoteMySQLIdent(right)
	}
	return g, true
}

// mysqlLineMask returns the trimmed line and an upper-cased mask of equal byte
// length in which quoted identifiers, string literals and parenthesised
// content are replaced by '#', so keywords and separators can be found only at
// the top level.
func mysqlLineMask(line string) (string, string) {
	text := strings.TrimRight(strings.TrimSpace(line), "; \t")
	mask := []byte(strings.ToUpper(text))
	var quote byte
	depth := 0
	for i := 0; i < len(text); i++ {
		ch := text[i]
		switch {
		case quote != 0:
			mask[i] = '#'
			if ch == quote {
				if i+1 < len(text) && text[i+1] == quote {
					mask[i+1] = '#'
					i++
				} else {
					quote = 0
				}
			} else if ch == '\\' && quote == '\'' && i+1 < len(text) {
				mask[i+1] = '#'
				i++
			}
		case ch == '`' || ch == '\'' || ch == '"':
			quote = ch
			mask[i] = '#'
		case ch == '(':
			depth++
		case ch == ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth > 0 {
				mask[i] = '#'
			}
		}
	}
	return text, string(mask)
}

// splitMasked splits text at sep wherever mask has sep at the same offset.
func splitMasked(text, mask string, sep byte) []string {
	var parts []string
	start := 0
	for i := 0; i < len(mask); i++ {
		if mask[i] == sep {
			parts = append(parts, text[start:i])
			start = i + 1
		}
	}
	return append(parts, text[start:])
}

func unquoteMySQLIdent(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '`' && s[len(s)-1] == '`' {
		return strings.ReplaceAll(s[1:len(s)-1], "``", "`")
	}
	return s
}

// hasMySQLWildcard reports whether a database pattern contains an unescaped
// % or _.
func hasMySQLWildcard(pattern string) bool {
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			i++
		case '%', '_':
			return true
		}
	}
	return false
}

// unescapeMySQLPattern removes backslash escapes from a database pattern.
func unescapeMySQLPattern(pattern string) string {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '\\' && i+1 < len(pattern) {
			i++
		}
		b.WriteByte(pattern[i])
	}
	return b.String()
}

// mysqlPatternMatch matches name against a grant database pattern where %
// matches any sequence, _ any single character and \x the literal x.
func mysqlPatternMatch(pattern, name string) bool {
	var b strings.Builder
	b.WriteString("^")
	escaped := false
	for _, ch := range pattern {
		switch {
		case escaped:
			escaped = false
			b.WriteString(regexp.QuoteMeta(string(ch)))
		case ch == '\\':
			escaped = true
		case ch == '%':
			b.WriteString("(?s:.*)")
		case ch == '_':
			b.WriteString("(?s:.)")
		default:
			b.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	if escaped {
		b.WriteString(regexp.QuoteMeta("\\"))
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(name)
}
