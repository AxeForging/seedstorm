package db

import "strings"

// QuoteIdent quotes a SQL identifier (table or column name) for safe
// interpolation into queries. PostgreSQL uses double-quotes with ""
// escaping; MySQL uses backticks with “ escaping.
//
// dbType must be "pgx" (PostgreSQL) or "mysql".
func QuoteIdent(name, dbType string) string {
	if dbType == "pgx" {
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
