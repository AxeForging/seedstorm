package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/AxeForging/seedstorm/internal/faultinject"
)

// introspectConnectTimeout bounds how long Introspect waits for the database
// to answer before reporting it unreachable.
var introspectConnectTimeout = 10 * time.Second

// Introspect connects to the database and returns all discovered tables.
func Introspect(dbType, dsn string) ([]Table, error) {
	db, err := sql.Open(dbType, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open connection: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), introspectConnectTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}
	return IntrospectConn(context.Background(), db, dbType, nil)
}

// IntrospectConn reads every table of the connection's database. Its reads
// stop when ctx ends; onTable, if set, is called after each table (catalog
// queries for the whole database run before the first one).
func IntrospectConn(ctx context.Context, conn *sql.DB, dbType string, onTable func(done, total int, table string)) ([]Table, error) {
	if err := faultinject.Hit(ctx, "introspect", ""); err != nil {
		return nil, err
	}
	switch dbType {
	case "pgx":
		return introspectPostgres(ctx, conn, onTable)
	case "mysql":
		return introspectMySQL(ctx, conn, onTable)
	default:
		return nil, fmt.Errorf("unsupported database type %q (use mysql or postgres)", dbType)
	}
}
