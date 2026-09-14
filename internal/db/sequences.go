package db

import (
	"context"
	"database/sql"
	"fmt"
)

// SequenceAdjustment records a sequence moved past ids that were inserted
// explicitly, so the application's next default id does not collide.
type SequenceAdjustment struct {
	Table    string `json:"table"`
	Column   string `json:"column"`
	Sequence string `json:"sequence"`
	From     int64  `json:"from"`
	To       int64  `json:"to"`
}

// SyncSequences advances every sequence owned by a column of the given tables
// (SERIAL and IDENTITY) to at least MAX(column). It never moves a sequence
// backwards. MySQL needs nothing: AUTO_INCREMENT follows explicit ids itself.
func SyncSequences(ctx context.Context, conn *sql.DB, dbType string, tables []string) ([]SequenceAdjustment, error) {
	if dbType != "pgx" {
		return nil, nil
	}
	var out []SequenceAdjustment
	for _, table := range tables {
		owned, err := ownedSequences(ctx, conn, table)
		if err != nil {
			return out, err
		}
		for column, sequence := range owned {
			adj, changed, err := advanceSequence(ctx, conn, table, column, sequence)
			if err != nil {
				return out, err
			}
			if changed {
				out = append(out, adj)
			}
		}
	}
	return out, nil
}

func ownedSequences(ctx context.Context, conn *sql.DB, table string) (map[string]string, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT a.attname, pg_get_serial_sequence(quote_ident(c.relname), a.attname)
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relname = $1
		  AND a.attnum > 0 AND NOT a.attisdropped
		  AND pg_get_serial_sequence(quote_ident(c.relname), a.attname) IS NOT NULL`, table)
	if err != nil {
		return nil, fmt.Errorf("find sequences of %s: %w", table, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var column, sequence string
		if err := rows.Scan(&column, &sequence); err != nil {
			return nil, err
		}
		out[column] = sequence
	}
	return out, rows.Err()
}

func advanceSequence(ctx context.Context, conn *sql.DB, table, column, sequence string) (SequenceAdjustment, bool, error) {
	adj := SequenceAdjustment{Table: table, Column: column, Sequence: sequence}
	var maxID sql.NullInt64
	//nolint:gosec // identifiers are quoted; the sequence name comes from pg_get_serial_sequence
	if err := conn.QueryRowContext(ctx, fmt.Sprintf("SELECT MAX(%s)::bigint FROM %s", QuoteIdent(column, "pgx"), QuoteIdent(table, "pgx"))).Scan(&maxID); err != nil {
		return adj, false, fmt.Errorf("read max %s.%s: %w", table, column, err)
	}
	if !maxID.Valid {
		return adj, false, nil
	}
	var last int64
	var called bool
	//nolint:gosec // see above
	if err := conn.QueryRowContext(ctx, "SELECT last_value, is_called FROM "+sequence).Scan(&last, &called); err != nil {
		return adj, false, fmt.Errorf("read sequence %s: %w", sequence, err)
	}
	// The next default id is last_value+1 once the sequence has been used,
	// last_value itself before that.
	next := last
	if called {
		next = last + 1
	}
	if maxID.Int64 < next {
		return adj, false, nil
	}
	if _, err := conn.ExecContext(ctx, "SELECT setval($1, $2, true)", sequence, maxID.Int64); err != nil {
		return adj, false, fmt.Errorf("advance sequence %s: %w", sequence, err)
	}
	adj.From, adj.To = next-1, maxID.Int64
	return adj, true, nil
}
