package db

import (
	"context"
	"database/sql"
	"fmt"
)

// GetTableRowCounts queries SELECT COUNT(*) for each table and returns a
// map of table name → row count. Tables that cannot be queried return 0 and
// the error is surfaced immediately.
func GetTableRowCounts(ctx context.Context, conn *sql.DB, tableNames []string) (map[string]int64, error) {
	counts := make(map[string]int64, len(tableNames))
	for _, tableName := range tableNames {
		var n int64
		//nolint:gosec
		row := conn.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", tableName))
		if err := row.Scan(&n); err != nil {
			return nil, fmt.Errorf("count rows in %s: %w", tableName, err)
		}
		counts[tableName] = n
	}
	return counts, nil
}
