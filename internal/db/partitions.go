package db

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// postgresPartitionNames selects the names of partitions in schema public: they
// hold a partitioned table's rows and are never listed as tables themselves.
const postgresPartitionNames = `
	SELECT pc.relname FROM pg_class pc
	JOIN pg_namespace pn ON pn.oid = pc.relnamespace
	WHERE pn.nspname = 'public' AND pc.relispartition AND pc.relkind IN ('r', 'p')`

// postgresPartitioning reads every partitioned table's key and the bounds of
// its direct partitions.
func postgresPartitioning(ctx context.Context, db *sql.DB) (map[string]*Partitioning, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.relname, pt.partstrat,
		       ARRAY(SELECT COALESCE(a.attname, '')
		             FROM unnest(pt.partattrs::int2[]) WITH ORDINALITY AS k(attnum, ord)
		             LEFT JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
		             ORDER BY k.ord)::text[]
		FROM pg_partitioned_table pt
		JOIN pg_class c     ON c.oid = pt.partrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'`)
	if err != nil {
		return nil, fmt.Errorf("failed to query partitioned tables: %w", err)
	}
	out := map[string]*Partitioning{}
	for rows.Next() {
		var name, strategy, cols string
		if err := rows.Scan(&name, &strategy, &cols); err != nil {
			rows.Close()
			return nil, err
		}
		p := &Partitioning{Strategy: map[string]string{"r": "range", "l": "list", "h": "hash"}[strategy]}
		p.Columns = parseTextArray(cols)
		out[name] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	bounds, err := db.QueryContext(ctx, `
		SELECT parent.relname, pg_get_expr(child.relpartbound, child.oid)
		FROM pg_inherits i
		JOIN pg_class child  ON child.oid = i.inhrelid
		JOIN pg_class parent ON parent.oid = i.inhparent
		JOIN pg_namespace n  ON n.oid = parent.relnamespace
		WHERE n.nspname = 'public' AND child.relispartition AND child.relkind IN ('r', 'p')
		ORDER BY parent.relname, child.relname`)
	if err != nil {
		return nil, fmt.Errorf("failed to query partition bounds: %w", err)
	}
	defer bounds.Close()
	for bounds.Next() {
		var parent, bound string
		if err := bounds.Scan(&parent, &bound); err != nil {
			return nil, err
		}
		if p := out[parent]; p != nil {
			applyPartitionBound(p, bound)
		}
	}
	return out, bounds.Err()
}

var (
	reRangeBound = regexp.MustCompile(`(?i)^FOR VALUES FROM \((.*)\) TO \((.*)\)$`)
	reListBound  = regexp.MustCompile(`(?i)^FOR VALUES IN \((.*)\)$`)
)

// applyPartitionBound records one partition's bound, as pg_get_expr prints it.
func applyPartitionBound(p *Partitioning, bound string) {
	bound = strings.TrimSpace(bound)
	switch {
	case strings.EqualFold(bound, "DEFAULT"):
		p.Default = true
	case reRangeBound.MatchString(bound):
		m := reRangeBound.FindStringSubmatch(bound)
		p.Ranges = append(p.Ranges, PartitionRange{From: strings.TrimSpace(m[1]), To: strings.TrimSpace(m[2])})
	case reListBound.MatchString(bound):
		for _, v := range splitSQLList(reListBound.FindStringSubmatch(bound)[1]) {
			if !strings.EqualFold(v, "NULL") {
				p.Values = append(p.Values, unquoteSQL(v))
			}
		}
	}
}

// splitSQLList splits a comma-separated list of SQL literals, keeping commas
// inside quotes.
func splitSQLList(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\'':
			inQuote = !inQuote
			cur.WriteByte(ch)
		case ch == ',' && !inQuote:
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(ch)
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, strings.TrimSpace(cur.String()))
	}
	return out
}

// unquoteSQL turns 'it”s' into it's and strips a ::type cast; unquoted
// literals are returned trimmed.
func unquoteSQL(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.LastIndex(v, "::"); i > 0 && strings.HasSuffix(v[:i], "'") {
		v = v[:i]
	}
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
	}
	return v
}

// parseTextArray parses a Postgres text[] literal like {a,"",b}.
func parseTextArray(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimSuffix(s, "}"), "{")
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		out = append(out, strings.Trim(part, `"`))
	}
	return out
}
