package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Truncate clears all tables in reverse seed order (children before parents).
//
// For PostgreSQL a single TRUNCATE … RESTART IDENTITY CASCADE statement is used,
// so FK order does not matter but reverse order is kept for clarity.
//
// For MySQL FK checks are disabled for the duration of the truncation.
//
// dbType must be "pgx" (PostgreSQL) or "mysql".
func Truncate(ctx context.Context, conn *sql.DB, dbType string, seedOrder []string) error {
	return TruncateWithProgress(ctx, conn, dbType, seedOrder, nil)
}

// TruncateWithProgress is like Truncate but reports progress via the optional
// callback as tables are cleared. For MySQL each table is truncated in its own
// statement, so progress fires once per table (in reverse seed order) with the
// table name. For PostgreSQL a single TRUNCATE … CASCADE clears every table
// atomically, so there are no intermediate per-table steps — progress fires once
// on completion with an empty table name.
func TruncateWithProgress(ctx context.Context, conn *sql.DB, dbType string, seedOrder []string, progress func(done, total int, table string)) error {
	return TruncateConcurrently(ctx, conn, dbType, seedOrder, 1, progress)
}

// TruncateConcurrently clears tables like TruncateWithProgress. workers is
// accepted for callers that size every phase from one setting, but MySQL
// truncates stay on one pinned connection: concurrent TRUNCATEs of tables
// linked by foreign keys (FK checks off) left later inserts failing FK checks
// against rows that did exist. progress calls never overlap.
func TruncateConcurrently(ctx context.Context, conn *sql.DB, dbType string, seedOrder []string, _ int, progress func(done, total int, table string)) error {
	total := len(seedOrder)
	if dbType == "pgx" {
		names := make([]string, len(seedOrder))
		for i, t := range seedOrder {
			names[i] = QuoteIdent(t, dbType)
		}
		// reverse: truncate leaf tables first (cosmetic; CASCADE handles FK order)
		for i, j := 0, len(names)-1; i < j; i, j = i+1, j-1 {
			names[i], names[j] = names[j], names[i]
		}
		query := fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE", strings.Join(names, ", "))
		if _, err := conn.ExecContext(ctx, query); err != nil {
			return err
		}
		if progress != nil {
			progress(total, total, "")
		}
		return nil
	}

	// Reverse seed order: children first.
	queue := make(chan string, total)
	for i := len(seedOrder) - 1; i >= 0; i-- {
		queue <- seedOrder[i]
	}
	close(queue)
	done := 0
	return truncateMySQLTables(ctx, conn, queue, func(table string) {
		done++
		if progress != nil {
			progress(done, total, table)
		}
	})
}

// truncateMySQLTables truncates tables from queue on one pinned connection with
// FK checks disabled, restoring them before the connection returns to the pool.
func truncateMySQLTables(ctx context.Context, conn *sql.DB, queue <-chan string, emit func(table string)) error {
	c, err := conn.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open connection: %w", err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=0"); err != nil {
		return fmt.Errorf("disable FK checks: %w", err)
	}
	defer func() {
		_, _ = c.ExecContext(context.WithoutCancel(ctx), "SET FOREIGN_KEY_CHECKS=1") //nolint:errcheck
	}()
	for table := range queue {
		if err := ctx.Err(); err != nil {
			return err
		}
		query := fmt.Sprintf("TRUNCATE TABLE %s", QuoteIdent(table, "mysql")) //nolint:gosec
		if _, err := c.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("truncate %s: %w", table, err)
		}
		emit(table)
	}
	return nil
}
