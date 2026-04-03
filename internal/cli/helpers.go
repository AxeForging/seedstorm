package cli

import "github.com/AxeForging/seedstorm/internal/db"

// normalizeDBType converts user-facing db names to driver names.
func normalizeDBType(dbType string) string {
	if dbType == "postgres" || dbType == "postgresql" {
		return "pgx"
	}
	return dbType
}

// quoteIdent quotes a SQL identifier (table or column name).
// PostgreSQL uses double-quotes, MySQL uses backticks.
func quoteIdent(name, dbType string) string {
	return db.QuoteIdent(name, dbType)
}
