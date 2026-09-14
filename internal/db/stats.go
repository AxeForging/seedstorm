package db

import (
	"context"
	"database/sql"
	"fmt"
)

// UnknownCount marks a row count or size the database could not report (for
// example a Postgres table that has never been analyzed).
const UnknownCount int64 = -1

// ListTableColumns returns every base table in the connection's schema with its
// column names in ordinal order. It is a light alternative to Introspect for
// callers that only need names.
func ListTableColumns(ctx context.Context, conn *sql.DB, dbType string) (map[string][]string, error) {
	query := `
		SELECT c.table_name, c.column_name
		FROM information_schema.columns c
		JOIN information_schema.tables t
		  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = 'public' AND t.table_type = 'BASE TABLE'
		ORDER BY c.table_name, c.ordinal_position`
	if dbType == "mysql" {
		query = `
			SELECT c.TABLE_NAME, c.COLUMN_NAME
			FROM information_schema.COLUMNS c
			JOIN information_schema.TABLES t
			  ON t.TABLE_SCHEMA = c.TABLE_SCHEMA AND t.TABLE_NAME = c.TABLE_NAME
			WHERE c.TABLE_SCHEMA = DATABASE() AND t.TABLE_TYPE = 'BASE TABLE'
			ORDER BY c.TABLE_NAME, c.ORDINAL_POSITION`
	}
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list columns: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]string)
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return nil, err
		}
		out[table] = append(out[table], column)
	}
	return out, rows.Err()
}

// GetTableSizes returns on-disk bytes (data plus indexes) per base table.
// MySQL reports cached statistics, so treat its numbers as approximate.
func GetTableSizes(ctx context.Context, conn *sql.DB, dbType string) (map[string]int64, error) {
	query := `
		SELECT c.relname, pg_total_relation_size(c.oid)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')`
	if dbType == "mysql" {
		query = `
			SELECT TABLE_NAME, COALESCE(DATA_LENGTH, 0) + COALESCE(INDEX_LENGTH, 0)
			FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'`
	}
	return scanNameInt64(ctx, conn, query, "table sizes")
}

// GetEstimatedRowCounts returns row estimates per base table without scanning
// data. A table with no usable estimate (never analyzed, statistics reset, or
// NULL) reports UnknownCount; callers decide whether to count it exactly.
//
// Postgres prefers the live tuple counter (n_live_tup), which tracks inserts
// without ANALYZE, over planner statistics (reltuples, 0 or -1 before the first
// ANALYZE depending on version). MySQL 8 caches information_schema statistics
// for up to a day, so the session asks for fresh ones.
func GetEstimatedRowCounts(ctx context.Context, conn *sql.DB, dbType string) (map[string]int64, error) {
	if dbType == "mysql" {
		c, err := conn.Conn(ctx)
		if err != nil {
			return nil, fmt.Errorf("read estimated row counts: %w", err)
		}
		defer func() { _ = c.Close() }()
		// MySQL 5.7 has no such variable and never caches: ignore the error.
		_, _ = c.ExecContext(ctx, "SET SESSION information_schema_stats_expiry = 0")
		rows, err := c.QueryContext(ctx, `
			SELECT TABLE_NAME, COALESCE(TABLE_ROWS, -1)
			FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'`)
		if err != nil {
			return nil, fmt.Errorf("read estimated row counts: %w", err)
		}
		return collectNameInt64(rows, "estimated row counts")
	}
	return scanNameInt64(ctx, conn, `
		SELECT c.relname,
		       CASE WHEN COALESCE(s.n_live_tup, 0) > 0 THEN s.n_live_tup
		            WHEN c.reltuples > 0 THEN c.reltuples::bigint
		            ELSE -1 END
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')`, "estimated row counts")
}

func scanNameInt64(ctx context.Context, conn *sql.DB, query, what string) (map[string]int64, error) {
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", what, err)
	}
	return collectNameInt64(rows, what)
}

func collectNameInt64(rows *sql.Rows, what string) (map[string]int64, error) {
	defer rows.Close()
	out := make(map[string]int64)
	for rows.Next() {
		var name string
		var n int64
		if err := rows.Scan(&name, &n); err != nil {
			return nil, fmt.Errorf("scan %s: %w", what, err)
		}
		out[name] = n
	}
	return out, rows.Err()
}

// Identity returns a string that is equal for two connections only when they
// reach the same database on the same server, whatever DSN spelling was used.
func Identity(ctx context.Context, conn *sql.DB, dbType string) (string, error) {
	// The server address is left out on purpose: localhost and 127.0.0.1 reach
	// the same database over different interfaces.
	query := `SELECT current_database() || '#' || pg_postmaster_start_time()::text`
	if dbType == "mysql" {
		query = `SELECT CONCAT(COALESCE(DATABASE(), ''), '@', @@server_uuid)`
	}
	var id string
	if err := conn.QueryRowContext(ctx, query).Scan(&id); err != nil {
		return "", fmt.Errorf("read database identity: %w", err)
	}
	return dbType + ":" + id, nil
}
