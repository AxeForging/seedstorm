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
	total := len(seedOrder)
	emit := func(done int, table string) {
		if progress != nil {
			progress(done, total, table)
		}
	}

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
		emit(total, "")
		return nil
	}

	// MySQL: disable FK checks, truncate in reverse order, re-enable
	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=0"); err != nil {
		return fmt.Errorf("disable FK checks: %w", err)
	}
	done := 0
	for i := len(seedOrder) - 1; i >= 0; i-- {
		query := fmt.Sprintf("TRUNCATE TABLE %s", QuoteIdent(seedOrder[i], dbType)) //nolint:gosec
		if _, err := conn.ExecContext(ctx, query); err != nil {
			_, _ = conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=1") //nolint:errcheck
			return fmt.Errorf("truncate %s: %w", seedOrder[i], err)
		}
		done++
		emit(done, seedOrder[i])
	}
	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=1"); err != nil {
		return fmt.Errorf("re-enable FK checks: %w", err)
	}
	return nil
}
