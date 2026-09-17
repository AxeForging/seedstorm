// Package compare snapshots table volumes on two databases, diffs them, and
// plans how to seed a target so its volumes follow a source. It never writes:
// execution lives in the seeder package.
package compare

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/relations"
)

// CountMode selects how row counts are read.
type CountMode string

const (
	// CountExact runs COUNT(*) per table.
	CountExact CountMode = "exact"
	// CountEstimate reads database statistics: instant and approximate. Tables
	// without a positive estimate are counted exactly (see Take).
	CountEstimate CountMode = "estimate"
)

// ParseCountMode validates a user-supplied mode, defaulting to exact.
func ParseCountMode(s string) (CountMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(CountExact):
		return CountExact, nil
	case string(CountEstimate):
		return CountEstimate, nil
	}
	return "", fmt.Errorf("unknown count mode %q (use exact or estimate)", s)
}

// TableStat is one table's volume on one database. Rows and Bytes are
// db.UnknownCount when the database could not report them.
type TableStat struct {
	Rows int64 `json:"rows"`
	// Estimated marks a row count taken from planner statistics, not COUNT(*).
	Estimated bool     `json:"estimated,omitempty"`
	Bytes     int64    `json:"bytes"`
	Columns   []string `json:"columns,omitempty"`
}

// Snapshot is every table's volume on one database at one moment.
type Snapshot struct {
	Label     string               `json:"label"`
	DBType    string               `json:"dbType"`
	CountMode CountMode            `json:"countMode"`
	TakenAt   time.Time            `json:"takenAt"`
	Tables    map[string]TableStat `json:"tables"`
	// Relationships are foreign-key shapes, when they were scanned.
	Relationships []relations.Shape `json:"relationships,omitempty"`
}

// Take reads table names, columns, sizes and row counts. progress, if set, is
// called after each table is counted in exact mode.
func Take(ctx context.Context, conn *sql.DB, dbType, label string, mode CountMode, progress func(done, total int, table string)) (Snapshot, error) {
	snap := Snapshot{Label: label, DBType: dbType, CountMode: mode, TakenAt: time.Now().UTC(), Tables: map[string]TableStat{}}
	columns, err := db.ListTableColumns(ctx, conn, dbType)
	if err != nil {
		return snap, err
	}
	names := make([]string, 0, len(columns))
	for name := range columns {
		names = append(names, name)
	}
	sort.Strings(names)

	sizes, err := db.GetTableSizes(ctx, conn, dbType)
	if err != nil {
		// Size is a nice-to-have (it needs catalog access some roles lack).
		sizes = nil
	}
	estimates := map[string]int64{}
	if mode == CountEstimate {
		if estimates, err = db.GetEstimatedRowCounts(ctx, conn, dbType); err != nil {
			return snap, err
		}
	}
	counts := make(map[string]int64, len(names))
	estimated := make(map[string]bool, len(names))
	for i, name := range names {
		// A missing estimate is unknown, and a zero one may just be stale (a
		// table filled since statistics were last gathered). Counting those
		// exactly is cheap when the table really is empty and correct when not.
		n, hasEstimate := estimates[name]
		size, hasSize := sizes[name]
		if !hasSize {
			size = db.UnknownCount
		}
		switch {
		case hasEstimate && n > 0:
			counts[name], estimated[name] = n, true
		case mode == CountEstimate && !exactFallback(n, hasEstimate, size):
			// Too large to scan in estimate mode: the count stays unknown.
		default:
			if err := ctx.Err(); err != nil {
				return snap, err
			}
			// A table that cannot be counted stays unknown instead of failing
			// the whole snapshot.
			one, _ := db.CountTables(ctx, conn, dbType, []string{name}, nil)
			if n, ok := one[name]; ok {
				counts[name] = n
			}
		}
		if progress != nil {
			progress(i+1, len(names), name)
		}
	}
	for _, name := range names {
		stat := TableStat{Rows: db.UnknownCount, Bytes: db.UnknownCount, Columns: columns[name]}
		if n, ok := counts[name]; ok {
			stat.Rows = n
			stat.Estimated = estimated[name]
		}
		if n, ok := sizes[name]; ok {
			stat.Bytes = n
		}
		snap.Tables[name] = stat
	}
	return snap, nil
}

// Status classifies one table in a comparison.
type Status string

const (
	StatusSame       Status = "same"
	StatusDiffers    Status = "differs"
	StatusSourceOnly Status = "source_only"
	StatusTargetOnly Status = "target_only"
	// StatusUnknown is a matched table whose row count one side could not
	// report: it is neither the same nor different.
	StatusUnknown Status = "unknown"
)

// Row compares one table across both databases.
type Row struct {
	// Table is the source name (the target name for target-only rows).
	Table string `json:"table"`
	// TargetTable is the matched target name; it differs from Table only by case
	// when engines fold identifiers differently.
	TargetTable    string     `json:"targetTable,omitempty"`
	Status         Status     `json:"status"`
	Source         *TableStat `json:"source,omitempty"`
	Target         *TableStat `json:"target,omitempty"`
	Delta          int64      `json:"delta"`
	MissingColumns []string   `json:"missingColumns,omitempty"`
	ExtraColumns   []string   `json:"extraColumns,omitempty"`
}

// Totals summarises a report.
type Totals struct {
	SourceRows  int64 `json:"sourceRows"`
	TargetRows  int64 `json:"targetRows"`
	SourceBytes int64 `json:"sourceBytes"`
	TargetBytes int64 `json:"targetBytes"`
	Same        int   `json:"same"`
	Differs     int   `json:"differs"`
	SourceOnly  int   `json:"sourceOnly"`
	TargetOnly  int   `json:"targetOnly"`
	// Unknown counts matched tables whose row count failed on either side.
	Unknown int `json:"unknown"`
	// ColumnDrift counts matched tables whose column names differ.
	ColumnDrift int `json:"columnDrift"`
}

// SnapshotInfo is a snapshot without its tables, for report headers.
type SnapshotInfo struct {
	Label     string    `json:"label"`
	DBType    string    `json:"dbType"`
	CountMode CountMode `json:"countMode"`
	TakenAt   time.Time `json:"takenAt"`
}

// Report is the full comparison.
type Report struct {
	Source SnapshotInfo `json:"source"`
	Target SnapshotInfo `json:"target"`
	Rows   []Row        `json:"rows"`
	Totals Totals       `json:"totals"`
	// Relationships is the per-foreign-key shape drift, when it was compared.
	Relationships []ShapeDrift `json:"relationships,omitempty"`
}

func info(s Snapshot) SnapshotInfo {
	return SnapshotInfo{Label: s.Label, DBType: s.DBType, CountMode: s.CountMode, TakenAt: s.TakenAt}
}

// Diff matches tables by exact name, then case-insensitively, and classifies
// each. Rows are sorted by table name.
func Diff(source, target Snapshot) Report {
	r := Report{Source: info(source), Target: info(target)}
	matched := make(map[string]bool, len(target.Tables))
	byLower := make(map[string][]string, len(target.Tables))
	for name := range target.Tables {
		byLower[strings.ToLower(name)] = append(byLower[strings.ToLower(name)], name)
	}

	for _, name := range sortedNames(source.Tables) {
		src := source.Tables[name]
		tgtName, ok := name, false
		if _, exact := target.Tables[name]; exact {
			ok = true
		} else if candidates := byLower[strings.ToLower(name)]; len(candidates) == 1 && !matched[candidates[0]] {
			tgtName, ok = candidates[0], true
		}
		srcCopy := src
		if !ok {
			r.Rows = append(r.Rows, Row{Table: name, Status: StatusSourceOnly, Source: &srcCopy, Delta: -knownRows(src.Rows)})
			continue
		}
		matched[tgtName] = true
		tgt := target.Tables[tgtName]
		row := Row{Table: name, Status: StatusSame, Source: &srcCopy, Target: &tgt}
		if tgtName != name {
			row.TargetTable = tgtName
		}
		switch {
		case src.Rows < 0 || tgt.Rows < 0:
			row.Status = StatusUnknown
		case src.Rows != tgt.Rows:
			row.Status = StatusDiffers
			row.Delta = tgt.Rows - src.Rows
		}
		row.MissingColumns, row.ExtraColumns = columnDiff(src.Columns, tgt.Columns)
		r.Rows = append(r.Rows, row)
	}
	for _, name := range sortedNames(target.Tables) {
		if matched[name] {
			continue
		}
		tgt := target.Tables[name]
		r.Rows = append(r.Rows, Row{Table: name, Status: StatusTargetOnly, Target: &tgt, Delta: knownRows(tgt.Rows)})
	}
	sort.SliceStable(r.Rows, func(i, j int) bool {
		return strings.ToLower(r.Rows[i].Table) < strings.ToLower(r.Rows[j].Table)
	})

	for _, row := range r.Rows {
		switch row.Status {
		case StatusSame:
			r.Totals.Same++
		case StatusDiffers:
			r.Totals.Differs++
		case StatusSourceOnly:
			r.Totals.SourceOnly++
		case StatusTargetOnly:
			r.Totals.TargetOnly++
		case StatusUnknown:
			r.Totals.Unknown++
		}
		if len(row.MissingColumns) > 0 || len(row.ExtraColumns) > 0 {
			r.Totals.ColumnDrift++
		}
		if row.Source != nil {
			r.Totals.SourceRows += knownRows(row.Source.Rows)
			r.Totals.SourceBytes += knownRows(row.Source.Bytes)
		}
		if row.Target != nil {
			r.Totals.TargetRows += knownRows(row.Target.Rows)
			r.Totals.TargetBytes += knownRows(row.Target.Bytes)
		}
	}
	return r
}

// columnDiff compares column names case-insensitively.
func columnDiff(source, target []string) (missing, extra []string) {
	tgt := make(map[string]bool, len(target))
	for _, c := range target {
		tgt[strings.ToLower(c)] = true
	}
	src := make(map[string]bool, len(source))
	for _, c := range source {
		src[strings.ToLower(c)] = true
		if !tgt[strings.ToLower(c)] {
			missing = append(missing, c)
		}
	}
	for _, c := range target {
		if !src[strings.ToLower(c)] {
			extra = append(extra, c)
		}
	}
	return missing, extra
}

func knownRows(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

func sortedNames(m map[string]TableStat) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// exactFallbackMaxBytes is the largest table estimate mode still counts
// exactly when its statistics are missing or zero.
const exactFallbackMaxBytes = 256 << 20

// exactFallback reports whether estimate mode counts a table exactly: its
// estimate is missing or zero, and the table is small enough (or of unknown
// size) that COUNT(*) is cheap. Large tables stay unknown instead of scanned.
func exactFallback(estimate int64, hasEstimate bool, bytes int64) bool {
	if hasEstimate && estimate > 0 {
		return false
	}
	return bytes <= exactFallbackMaxBytes
}
