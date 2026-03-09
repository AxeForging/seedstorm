package db

import (
	"database/sql"
	"fmt"
)

// Introspect connects to the database and returns all discovered tables.
func Introspect(dbType, dsn string) ([]Table, error) {
	db, err := sql.Open(dbType, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open connection: %w", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	switch dbType {
	case "pgx":
		return introspectPostgres(db)
	case "mysql":
		return introspectMySQL(db)
	default:
		return nil, fmt.Errorf("unsupported database type %q (use mysql or postgres)", dbType)
	}
}
