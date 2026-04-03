package cli

import (
	"github.com/AxeForging/seedstorm/internal/db"
)

// normalizeDBType converts user-facing db names to driver names.
func normalizeDBType(dbType string) string {
	if dbType == "postgres" || dbType == "postgresql" {
		return "pgx"
	}
	return dbType
}

// quoteIdent quotes a SQL identifier (table or column name).
func quoteIdent(name, dbType string) string {
	return db.QuoteIdent(name, dbType)
}

// buildInsert delegates to db.BuildInsert.
func buildInsert(tableName string, row map[string]interface{}, dbType string) (string, []interface{}) {
	return db.BuildInsert(tableName, row, dbType)
}

// buildBatchInsert delegates to db.BuildBatchInsert.
func buildBatchInsert(tableName string, rows []map[string]interface{}, dbType string) (string, []interface{}) {
	return db.BuildBatchInsert(tableName, rows, dbType)
}
