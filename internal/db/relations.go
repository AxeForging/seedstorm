package db

import (
	"context"
	"fmt"
	"strings"
)

// DegreeBucket counts parents whose number of children falls in [Min, Max]:
// exact for small degrees, powers of two above.
type DegreeBucket struct {
	Min      int64 `json:"min" yaml:"min"`
	Max      int64 `json:"max" yaml:"max"`
	Parents  int64 `json:"parents" yaml:"parents"`
	Children int64 `json:"children" yaml:"children"`
}

// exactDegrees is the largest degree with a bucket of its own.
const exactDegrees = 16

// degreeBucketExpr maps a degree c to its bucket's upper bound, the same SQL
// on both engines (no LOG2 in common).
func degreeBucketExpr() string {
	var b strings.Builder
	b.WriteString("CASE")
	for d := 1; d <= exactDegrees; d++ {
		fmt.Fprintf(&b, " WHEN c = %d THEN %d", d, d)
	}
	for upper := int64(exactDegrees * 2); upper <= 1<<40; upper *= 2 {
		fmt.Fprintf(&b, " WHEN c <= %d THEN %d", upper, upper)
	}
	b.WriteString(" ELSE c END")
	return b.String()
}

// DegreeHistogram aggregates children per parent of child.column in the
// database (no rows travel): one row per bucket, plus how many child rows have
// a NULL key. It is a full pass over the key: callers gate unindexed keys.
func DegreeHistogram(ctx context.Context, q Querier, dbType, child, column string) (buckets []DegreeBucket, nulls int64, err error) {
	t, c := QuoteIdent(child, dbType), QuoteIdent(column, dbType)
	//nolint:gosec // identifiers are quoted
	query := fmt.Sprintf(`
		SELECT bucket, COUNT(*), MIN(c), MAX(c), SUM(c)
		FROM (SELECT %s AS bucket, c FROM (SELECT %s AS k, COUNT(*) AS c FROM %s WHERE %s IS NOT NULL GROUP BY %s) per_parent) b
		GROUP BY bucket ORDER BY bucket`, degreeBucketExpr(), c, t, c, c)
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, 0, err
	}
	for rows.Next() {
		var upper, parents, lo, hi, sum int64
		if err := rows.Scan(&upper, &parents, &lo, &hi, &sum); err != nil {
			rows.Close()
			return nil, 0, err
		}
		buckets = append(buckets, DegreeBucket{Min: lo, Max: hi, Parents: parents, Children: sum})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	//nolint:gosec // identifiers are quoted
	err = q.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) - COUNT(%s) FROM %s`, c, t)).Scan(&nulls)
	return buckets, nulls, err
}

// LeadingIndexed reports whether column is the first column of any index on
// table (primary key, unique and partial indexes included): only then can the
// database group by it without a full table scan.
func LeadingIndexed(ctx context.Context, q Querier, dbType, table, column string) (bool, error) {
	var n int
	var err error
	if dbType == "mysql" {
		err = q.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ? AND SEQ_IN_INDEX = 1`, table, column).Scan(&n)
	} else {
		err = q.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM pg_index i
			JOIN pg_class t ON t.oid = i.indrelid
			JOIN pg_namespace ns ON ns.oid = t.relnamespace
			JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = i.indkey[0]
			WHERE ns.nspname = 'public' AND t.relname = $1 AND a.attname = $2`, table, column).Scan(&n)
	}
	return n > 0, err
}

// DegreeEstimate is what planner statistics say about a key's degrees, without
// reading the table. Unknown values are negative.
type DegreeEstimate struct {
	ChildRows    int64
	ParentRows   int64
	Distinct     int64
	NullFraction float64
	// MaxFraction is the most common key value's share of child rows.
	MaxFraction float64
}

// EstimateDegrees reads statistics: Postgres pg_stats (null_frac, n_distinct,
// most common frequencies); MySQL index cardinality (no null share or maximum).
func EstimateDegrees(ctx context.Context, q Querier, dbType, child, column, parent string) (DegreeEstimate, error) {
	est := DegreeEstimate{ChildRows: -1, ParentRows: -1, Distinct: -1, NullFraction: -1, MaxFraction: -1}
	if dbType == "mysql" {
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(TABLE_ROWS, -1) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, child).Scan(&est.ChildRows); err != nil {
			return est, err
		}
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(TABLE_ROWS, -1) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, parent).Scan(&est.ParentRows); err != nil {
			return est, err
		}
		err := q.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(CARDINALITY), -1) FROM information_schema.STATISTICS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ? AND SEQ_IN_INDEX = 1`, child, column).Scan(&est.Distinct)
		return est, err
	}
	rowsOf := `SELECT COALESCE((SELECT GREATEST(c.reltuples, 0)::bigint FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public' AND c.relname = $1), -1)`
	if err := q.QueryRowContext(ctx, rowsOf, child).Scan(&est.ChildRows); err != nil {
		return est, err
	}
	if err := q.QueryRowContext(ctx, rowsOf, parent).Scan(&est.ParentRows); err != nil {
		return est, err
	}
	var nDistinct float64
	rows, err := q.QueryContext(ctx, `
		SELECT null_frac, n_distinct, COALESCE((SELECT MAX(f) FROM unnest(most_common_freqs) f), 0)
		FROM pg_stats WHERE schemaname = 'public' AND tablename = $1 AND attname = $2`, child, column)
	if err != nil {
		return est, err
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&est.NullFraction, &nDistinct, &est.MaxFraction); err != nil {
			return est, err
		}
		if nDistinct < 0 && est.ChildRows >= 0 {
			nDistinct = -nDistinct * float64(est.ChildRows)
		}
		est.Distinct = int64(nDistinct)
	}
	return est, rows.Err()
}
