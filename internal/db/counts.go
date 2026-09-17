package db

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// GetTableRowCounts queries SELECT COUNT(*) for each table and returns a
// map of table name → row count. The first table that cannot be counted ends
// the scan with its error; use CountTables to keep counting past failures.
func GetTableRowCounts(ctx context.Context, conn *sql.DB, dbType string, tableNames []string) (map[string]int64, error) {
	counts := make(map[string]int64, len(tableNames))
	for _, tableName := range tableNames {
		n, err := countRows(ctx, conn, dbType, tableName)
		if err != nil {
			return nil, err
		}
		counts[tableName] = n
	}
	return counts, nil
}

// CountTables counts every table it can within DefaultCountLimits. Counted
// tables are in counts; a table whose count failed is left out of counts (its
// row count is unknown, never 0) and its error is in failed. onTable, if set,
// is called after each table.
func CountTables(ctx context.Context, conn *sql.DB, dbType string, tableNames []string, onTable func(done, total int, table string)) (counts map[string]int64, failed map[string]error) {
	return CountTablesWithin(ctx, conn, dbType, tableNames, DefaultCountLimits, onTable)
}

// CountTablesWithin is CountTables with explicit limits: each count runs in its
// own read-only transaction (ReadOnce), lim.Concurrency at a time. After ctx
// ends the remaining tables fail with ctx's error.
func CountTablesWithin(ctx context.Context, conn *sql.DB, dbType string, tableNames []string, lim ReadLimits, onTable func(done, total int, table string)) (counts map[string]int64, failed map[string]error) {
	counts = make(map[string]int64, len(tableNames))
	failed = map[string]error{}
	var mu sync.Mutex
	done := 0
	record := func(table string, n int64, err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			failed[table] = err
		} else {
			counts[table] = n
		}
		done++
		if onTable != nil {
			onTable(done, len(tableNames), table)
		}
	}
	work := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < max(1, lim.Concurrency); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for table := range work {
				if err := ctx.Err(); err != nil {
					record(table, 0, err)
					continue
				}
				var n int64
				err := ReadOnce(ctx, conn, dbType, lim, func(ctx context.Context, q Querier) error {
					var err error
					n, err = countRows(ctx, q, dbType, table)
					return err
				})
				record(table, n, err)
			}
		}()
	}
	for _, table := range tableNames {
		work <- table
	}
	close(work)
	wg.Wait()
	return counts, failed
}

func countRows(ctx context.Context, conn Querier, dbType, tableName string) (int64, error) {
	var n int64
	//nolint:gosec
	row := conn.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", QuoteIdent(tableName, dbType)))
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count rows in %s: %w", tableName, err)
	}
	return n, nil
}
