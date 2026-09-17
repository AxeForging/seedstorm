package faker

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// partitionKeyFaker returns the faker that keeps a partitioned table's key
// column inside its partitions' bounds, or why the key cannot be generated.
// An empty faker with no refusal means any value fits (hash partitions, or a
// DEFAULT partition).
func partitionKeyFaker(colType string, p *db.Partitioning) (faker, refuse string) {
	if p == nil || p.Strategy == "hash" || p.Default {
		return "", ""
	}
	if len(p.Columns) != 1 {
		return "", "partitioned by several columns"
	}
	if p.Columns[0] == "" {
		return "", "partitioned by an expression"
	}
	t := strings.ToLower(colType)
	switch p.Strategy {
	case "list":
		if len(p.Values) == 0 {
			return "", "list-partitioned with no listed values"
		}
		return "randomstring(" + strings.Join(p.Values, ",") + ")", ""
	case "range":
		switch {
		case isIntegerType(t):
			return intRangeFaker(p.Ranges)
		case strings.Contains(t, "timestamp") || strings.Contains(t, "datetime"):
			return timeRangeFaker(p.Ranges, "datetimerange", timeLayout)
		case strings.Contains(t, "date"):
			return timeRangeFaker(p.Ranges, "daterange", dateLayout)
		}
		return "", "range-partitioned on a " + rangeTypeLabel(t) + " column"
	}
	return "", "partitioned by " + p.Strategy
}

const (
	dateLayout = "2006-01-02"
	timeLayout = "2006-01-02 15:04:05"
)

func rangeTypeLabel(t string) string {
	if strings.Contains(t, "char") || strings.Contains(t, "text") {
		return "text"
	}
	return t
}

// span is one range partition as numbers (unix seconds for times).
type span struct{ from, to int64 }

// widest merges touching ranges and returns the widest merged one: generating
// across a gap between partitions would produce keys no partition accepts.
func widest(spans []span) span {
	sort.Slice(spans, func(i, j int) bool { return spans[i].from < spans[j].from })
	best, cur := spans[0], spans[0]
	for _, s := range spans[1:] {
		if s.from <= cur.to {
			cur.to = max(cur.to, s.to)
		} else {
			cur = s
		}
		if cur.to-cur.from > best.to-best.from {
			best = cur
		}
	}
	return best
}

const openRangeWidth = 1000

func intRangeFaker(ranges []db.PartitionRange) (string, string) {
	var spans []span
	for _, r := range ranges {
		from, fok := parseIntBound(r.From)
		to, tok := parseIntBound(r.To)
		switch {
		case !fok && !tok:
			continue
		case !fok:
			from = to - openRangeWidth
		case !tok:
			to = from + openRangeWidth
		}
		if from < 0 && r.From == "MINVALUE" && to > 0 {
			from = 0
		}
		if to > from {
			spans = append(spans, span{from, to})
		}
	}
	if len(spans) == 0 {
		return "", "range-partitioned with bounds seedstorm cannot read"
	}
	w := widest(spans)
	return fmt.Sprintf("number(%d,%d)", w.from, w.to-1), ""
}

func parseIntBound(s string) (int64, bool) {
	n, err := strconv.ParseInt(unquoteBound(s), 10, 64)
	return n, err == nil
}

func timeRangeFaker(ranges []db.PartitionRange, name, layout string) (string, string) {
	const openYears = 1
	var spans []span
	for _, r := range ranges {
		from, fok := parseTimeBound(r.From)
		to, tok := parseTimeBound(r.To)
		switch {
		case !fok && !tok:
			continue
		case !fok:
			from = to.AddDate(-openYears, 0, 0)
		case !tok:
			to = from.AddDate(openYears, 0, 0)
		}
		if to.After(from) {
			spans = append(spans, span{from.Unix(), to.Unix()})
		}
	}
	if len(spans) == 0 {
		return "", "range-partitioned with bounds seedstorm cannot read"
	}
	w := widest(spans)
	return fmt.Sprintf("%s(%s,%s)", name, time.Unix(w.from, 0).UTC().Format(layout), time.Unix(w.to, 0).UTC().Format(layout)), ""
}

func parseTimeBound(s string) (time.Time, bool) {
	v := unquoteBound(s)
	for _, layout := range []string{timeLayout, dateLayout, "2006-01-02 15:04:05-07", "2006-01-02 15:04:05.999999"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// unquoteBound strips quotes and a ::type cast from a printed bound literal.
func unquoteBound(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "::"); i > 0 {
		s = s[:i]
	}
	return strings.Trim(s, "'")
}

// rangeBounds parses a range faker's two bounds (from inclusive, to exclusive).
func rangeBounds(args []string, layout string) (time.Time, time.Time, error) {
	if len(args) != 2 {
		return time.Time{}, time.Time{}, fmt.Errorf("want 2 arguments (from, to), got %d", len(args))
	}
	from, err := time.Parse(layout, strings.TrimSpace(args[0]))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("bad from: %w", err)
	}
	to, err := time.Parse(layout, strings.TrimSpace(args[1]))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("bad to: %w", err)
	}
	if !to.After(from) {
		return time.Time{}, time.Time{}, fmt.Errorf("empty range %s to %s", args[0], args[1])
	}
	return from, to, nil
}

// applyPartitioning describes a partitioned table in the schema and points its
// key column at a faker that stays inside the partitions, or marks the table
// unseedable when no faker can.
func applyPartitioning(st *schema.Table, t db.Table, p *db.Partitioning) {
	cols := make([]string, len(p.Columns))
	for i, c := range p.Columns {
		cols[i] = c
		if c == "" {
			cols[i] = "expression"
		}
	}
	st.PartitionedBy = p.Strategy + " (" + strings.Join(cols, ", ") + ")"
	colType := ""
	if len(p.Columns) == 1 {
		for _, c := range t.Columns {
			if c.Name == p.Columns[0] {
				colType = c.Type
			}
		}
	}
	faker, refuse := partitionKeyFaker(colType, p)
	if refuse != "" {
		st.Unseedable = refuse
		return
	}
	if faker != "" {
		col := st.Columns[p.Columns[0]]
		col.Faker = faker
		col.PartitionKey = true
		st.Columns[p.Columns[0]] = col
	}
}

// CheckSeedable refuses tables whose rows cannot be generated (see
// schema.Table.Unseedable) unless value rules set some of their columns. Runs
// call it before writing anything, truncation included.
func CheckSeedable(sc *schema.Schema, tables []string, overrides Overrides) error {
	for _, name := range tables {
		t, ok := sc.Tables[name]
		if !ok || t.Unseedable == "" || len(overrides[name]) > 0 {
			continue
		}
		return fmt.Errorf("table %s is %s, which seedstorm cannot generate inside its partitions: add a value rule for its partition key columns in a profile, or ignore the table", name, t.Unseedable)
	}
	return nil
}
