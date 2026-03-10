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
	if dbType == "pgx" {
		names := make([]string, len(seedOrder))
		for i, t := range seedOrder {
			names[i] = fmt.Sprintf("%q", t)
		}
		// reverse: truncate leaf tables first (cosmetic; CASCADE handles FK order)
		for i, j := 0, len(names)-1; i < j; i, j = i+1, j-1 {
			names[i], names[j] = names[j], names[i]
		}
		query := fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE", strings.Join(names, ", "))
		if _, err := conn.ExecContext(ctx, query); err != nil {
			return err
		}
		return nil
	}

	// MySQL: disable FK checks, truncate in reverse order, re-enable
	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=0"); err != nil {
		return fmt.Errorf("disable FK checks: %w", err)
	}
	for i := len(seedOrder) - 1; i >= 0; i-- {
		query := fmt.Sprintf("TRUNCATE TABLE `%s`", seedOrder[i]) //nolint:gosec
		if _, err := conn.ExecContext(ctx, query); err != nil {
			_, _ = conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=1") //nolint:errcheck
			return fmt.Errorf("truncate %s: %w", seedOrder[i], err)
		}
	}
	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS=1"); err != nil {
		return fmt.Errorf("re-enable FK checks: %w", err)
	}
	return nil
}
