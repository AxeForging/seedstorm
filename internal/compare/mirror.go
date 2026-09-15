package compare

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// MirrorMode selects how existing target rows are treated.
type MirrorMode string

const (
	// ModeTopUp keeps target rows and inserts only the shortfall.
	ModeTopUp MirrorMode = "topup"
	// ModeReset truncates the affected target tables, then inserts the full volume.
	ModeReset MirrorMode = "reset"
)

// DefaultParentRows is how many rows an empty required parent receives.
const DefaultParentRows int64 = 10

// ParseMirrorMode validates a user-supplied mode, defaulting to top-up.
func ParseMirrorMode(s string) (MirrorMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(ModeTopUp), "top-up":
		return ModeTopUp, nil
	case string(ModeReset):
		return ModeReset, nil
	}
	return "", fmt.Errorf("unknown mirror mode %q (use topup or reset)", s)
}

// MirrorOptions shapes a mirror plan.
type MirrorOptions struct {
	Mode MirrorMode `json:"mode"`
	// Scale multiplies source volumes (1 = match, 0.1 = a tenth, 2 = double).
	Scale float64 `json:"scale"`
	// MaxRows caps any single table's target volume; 0 means no cap.
	MaxRows int64 `json:"maxRows"`
	// ParentRows seeds a required FK parent that would otherwise stay empty.
	ParentRows int64 `json:"parentRows"`
	// Tables limits the plan to these tables (source or target names); empty
	// means every table present on both sides.
	Tables []string `json:"tables,omitempty"`
}

// Reasons attached to plan entries and skips.
const (
	ReasonMatch      = "match source"
	ReasonCapped     = "capped by max rows"
	ReasonParent     = "required parent is empty"
	ReasonDependent  = "truncated with its parent"
	ReasonNotTarget  = "not in target"
	ReasonUnknown    = "source row count unknown"
	ReasonSatisfied  = "target already has enough rows"
	ReasonNotInGraph = "not introspected on target"
)

// PlanEntry is one table the mirror will insert into.
type PlanEntry struct {
	Table       string `json:"table"`
	SourceTable string `json:"sourceTable,omitempty"`
	SourceRows  int64  `json:"sourceRows"`
	TargetRows  int64  `json:"targetRows"`
	Want        int64  `json:"want"`
	Insert      int64  `json:"insert"`
	Reason      string `json:"reason"`
}

// PlanSkip is a table the mirror deliberately leaves alone.
type PlanSkip struct {
	Table  string `json:"table"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// MirrorPlan is the complete, reviewable mirror.
type MirrorPlan struct {
	Mode        MirrorMode  `json:"mode"`
	Scale       float64     `json:"scale"`
	Order       []string    `json:"order"`
	Entries     []PlanEntry `json:"entries"`
	Truncate    []string    `json:"truncate,omitempty"`
	Skipped     []PlanSkip  `json:"skipped,omitempty"`
	TotalInsert int64       `json:"totalInsert"`
}

// Counts returns table → rows to insert, the shape the seeder consumes.
func (p MirrorPlan) Counts() map[string]int {
	out := make(map[string]int, len(p.Entries))
	for _, e := range p.Entries {
		out[e.Table] = int(e.Insert)
	}
	return out
}

// PlanMirror decides, per target table, how many rows to insert so target
// volumes follow the source report. target is the target's schema, which
// supplies FK structure for ordering, truncation closure and required parents.
func PlanMirror(report Report, target *schema.Schema, opts MirrorOptions) (MirrorPlan, error) {
	if opts.Mode == "" {
		opts.Mode = ModeTopUp
	}
	if opts.Scale <= 0 {
		if opts.Scale < 0 {
			return MirrorPlan{}, fmt.Errorf("scale must be positive")
		}
		opts.Scale = 1
	}
	if opts.MaxRows < 0 || opts.ParentRows < 0 {
		return MirrorPlan{}, fmt.Errorf("max rows and parent rows must not be negative")
	}
	if opts.ParentRows == 0 {
		opts.ParentRows = DefaultParentRows
	}
	plan := MirrorPlan{Mode: opts.Mode, Scale: opts.Scale}

	g := graph.Build(target)
	sorted, err := g.TopologicalSort()
	if err != nil {
		return plan, fmt.Errorf("target schema: %w", err)
	}

	filter := make(map[string]bool, len(opts.Tables))
	for _, t := range opts.Tables {
		filter[strings.ToLower(strings.TrimSpace(t))] = true
	}
	selected := func(row Row) bool {
		if len(filter) == 0 {
			return true
		}
		return filter[strings.ToLower(row.Table)] || filter[strings.ToLower(row.TargetTable)]
	}

	targetRows := make(map[string]int64)
	entries := make(map[string]*PlanEntry)
	byTarget := make(map[string]Row)
	for _, row := range report.Rows {
		name := targetName(row)
		if row.Target != nil {
			targetRows[name] = knownRows(row.Target.Rows)
			byTarget[name] = row
		}
		if !selected(row) {
			continue
		}
		switch {
		case row.Status == StatusTargetOnly:
			continue
		case row.Status == StatusSourceOnly:
			plan.Skipped = append(plan.Skipped, PlanSkip{Table: row.Table, Reason: ReasonNotTarget})
			continue
		case row.Source.Rows < 0:
			plan.Skipped = append(plan.Skipped, PlanSkip{Table: name, Reason: ReasonUnknown})
			continue
		}
		if _, ok := target.Tables[name]; !ok {
			plan.Skipped = append(plan.Skipped, PlanSkip{Table: name, Reason: ReasonNotInGraph})
			continue
		}
		entries[name] = wantEntry(row, opts)
	}

	truncated := map[string]bool{}
	if opts.Mode == ModeReset {
		roots := make([]string, 0, len(entries))
		for name := range entries {
			roots = append(roots, name)
		}
		for name := range descendants(target, roots) {
			truncated[name] = true
		}
		for name := range entries {
			truncated[name] = true
		}
		for name := range truncated {
			if entries[name] != nil {
				continue
			}
			row, ok := byTarget[name]
			if !ok || row.Source == nil || row.Source.Rows < 0 {
				continue
			}
			e := wantEntry(row, opts)
			e.Reason = ReasonDependent
			entries[name] = e
		}
	}

	for name, e := range entries {
		switch {
		case opts.Mode == ModeReset:
			e.Insert = e.Want
		case e.TargetRows >= e.Want:
			e.Insert = 0
			if e.Want > 0 || e.TargetRows > 0 {
				plan.Skipped = append(plan.Skipped, PlanSkip{
					Table:  name,
					Reason: ReasonSatisfied,
					Detail: fmt.Sprintf("has %d, wants %d", e.TargetRows, e.Want),
				})
			}
		default:
			e.Insert = e.Want - e.TargetRows
		}
	}

	available := func(name string) int64 {
		have := targetRows[name]
		if truncated[name] {
			have = 0
		}
		if e := entries[name]; e != nil {
			have += e.Insert
		}
		return have
	}
	inserting := map[string]bool{}
	for name, e := range entries {
		if e.Insert > 0 {
			inserting[name] = true
		}
	}
	resolved, auto := graph.ResolveSelection(g, inserting, sorted)
	for _, name := range resolved {
		if !auto[name] || available(name) > 0 {
			continue
		}
		e := entries[name]
		if e == nil {
			e = &PlanEntry{Table: name, TargetRows: targetRows[name]}
			if row, ok := byTarget[name]; ok && row.Source != nil {
				e.SourceTable = sourceNameIfDifferent(row, name)
				e.SourceRows = knownRows(row.Source.Rows)
			}
			entries[name] = e
		}
		e.Want = opts.ParentRows
		e.Insert = opts.ParentRows
		e.Reason = ReasonParent
	}

	for _, name := range sorted {
		if truncated[name] {
			plan.Truncate = append(plan.Truncate, name)
		}
		e := entries[name]
		if e == nil || e.Insert <= 0 {
			continue
		}
		plan.Order = append(plan.Order, name)
		plan.Entries = append(plan.Entries, *e)
		plan.TotalInsert += e.Insert
	}
	sort.SliceStable(plan.Skipped, func(i, j int) bool { return plan.Skipped[i].Table < plan.Skipped[j].Table })
	return plan, nil
}

func wantEntry(row Row, opts MirrorOptions) *PlanEntry {
	name := targetName(row)
	e := &PlanEntry{
		Table:       name,
		SourceTable: sourceNameIfDifferent(row, name),
		SourceRows:  knownRows(row.Source.Rows),
		TargetRows:  knownRows(row.Target.Rows),
		Reason:      ReasonMatch,
	}
	e.Want = int64(math.Ceil(float64(e.SourceRows) * opts.Scale))
	if opts.MaxRows > 0 && e.Want > opts.MaxRows {
		e.Want = opts.MaxRows
		e.Reason = ReasonCapped
	}
	return e
}

func targetName(row Row) string {
	if row.TargetTable != "" {
		return row.TargetTable
	}
	return row.Table
}

func sourceNameIfDifferent(row Row, target string) string {
	if row.Table != target {
		return row.Table
	}
	return ""
}

// descendants returns every table that references one of roots through any FK
// (nullable included), transitively. Truncating a parent empties these too:
// Postgres cascades, and on MySQL they would be left orphaned.
func descendants(sc *schema.Schema, roots []string) map[string]bool {
	children := make(map[string][]string)
	for tableName, table := range sc.Tables {
		for _, col := range table.Columns {
			parent, _, ok := strings.Cut(col.FK, ".")
			if !ok || parent == tableName {
				continue
			}
			children[parent] = append(children[parent], tableName)
		}
	}
	out := map[string]bool{}
	queue := append([]string(nil), roots...)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, child := range children[cur] {
			if !out[child] {
				out[child] = true
				queue = append(queue, child)
			}
		}
	}
	return out
}
