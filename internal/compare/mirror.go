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
	// Ignore holds target table names a profile ignores (matched ignoring case;
	// see rules.RuleSet.IgnoredSet). They are never truncated or inserted into.
	Ignore map[string]bool `json:"ignore,omitempty"`
}

// Reasons attached to plan entries and skips.
const (
	ReasonMatch     = "match source"
	ReasonCapped    = "capped by max rows"
	ReasonParent    = "required parent is empty"
	ReasonDependent = "truncated with its parent"
	ReasonNotTarget = "not in target"
	ReasonUnknown   = "source row count unknown"
	// ReasonTargetUnknown: the target's count failed, so how many rows it
	// already has is unknown; filling it could double its volume.
	ReasonTargetUnknown = "target row count unknown"
	ReasonSatisfied     = "target already has enough rows"
	ReasonNotInGraph    = "not introspected on target"
	// ReasonIgnored marks a table the profile ignores.
	ReasonIgnored = "ignored by profile"
	// ReasonIgnoredParent marks a table whose required FK parent is ignored and empty.
	ReasonIgnoredParent = "required parent is ignored and empty"
	// ReasonTruncatesIgnored marks a reset table whose truncation would empty an ignored table.
	ReasonTruncatesIgnored = "reset would empty an ignored table"
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
//
// Tables in opts.Ignore are never truncated or inserted into. A table whose
// reset would empty an ignored table, or whose required FK parent is ignored
// and empty, is skipped with a reason instead of planning a run that fails.
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
	ignoredFold := make(map[string]bool, len(opts.Ignore))
	for t, on := range opts.Ignore {
		if on {
			ignoredFold[strings.ToLower(strings.TrimSpace(t))] = true
		}
	}
	isIgnored := func(name string) bool { return ignoredFold[strings.ToLower(name)] }

	var staticSkips []PlanSkip
	targetRows := make(map[string]int64)
	candidates := make(map[string]Row)
	byTarget := make(map[string]Row)
	// unknownTarget holds tables whose target count failed: never filled, and
	// never assumed empty when a child needs a parent with rows.
	unknownTarget := make(map[string]bool)
	for _, row := range report.Rows {
		name := targetName(row)
		if row.Target != nil {
			targetRows[name] = knownRows(row.Target.Rows)
			unknownTarget[name] = row.Target.Rows < 0
			byTarget[name] = row
		}
		if !selected(row) {
			continue
		}
		switch {
		case isIgnored(name) || isIgnored(row.Table):
			staticSkips = append(staticSkips, PlanSkip{Table: name, Reason: ReasonIgnored})
			continue
		case row.Status == StatusTargetOnly:
			continue
		case row.Status == StatusSourceOnly:
			staticSkips = append(staticSkips, PlanSkip{Table: row.Table, Reason: ReasonNotTarget})
			continue
		case row.Source.Rows < 0:
			staticSkips = append(staticSkips, PlanSkip{Table: name, Reason: ReasonUnknown})
			continue
		case unknownTarget[name]:
			staticSkips = append(staticSkips, PlanSkip{Table: name, Reason: ReasonTargetUnknown})
			continue
		}
		if _, ok := target.Tables[name]; !ok {
			staticSkips = append(staticSkips, PlanSkip{Table: name, Reason: ReasonNotInGraph})
			continue
		}
		candidates[name] = row
	}

	if opts.Mode == ModeReset && len(ignoredFold) > 0 {
		for name := range candidates {
			var hit []string
			for d := range descendants(target, []string{name}) {
				if isIgnored(d) {
					hit = append(hit, d)
				}
			}
			if len(hit) == 0 {
				continue
			}
			sort.Strings(hit)
			staticSkips = append(staticSkips, PlanSkip{
				Table:  name,
				Reason: ReasonTruncatesIgnored,
				Detail: "truncating it would also empty " + strings.Join(hit, ", "),
			})
			delete(candidates, name)
		}
	}

	// Skipping a table can strand its children (they needed its rows) and, in
	// reset mode, changes the truncation closure, so plan again until stable.
	excluded := map[string]PlanSkip{}
	var (
		entries   map[string]*PlanEntry
		truncated map[string]bool
		skips     []PlanSkip
		available func(string) int64
	)
	for {
		entries, truncated, skips = planPass(candidates, excluded, byTarget, target, opts, isIgnored)
		available = func(name string) int64 {
			have := targetRows[name]
			if unknownTarget[name] && !truncated[name] {
				// Unknown is not empty: do not refill it as a missing parent.
				have = max(have, 1)
			}
			if truncated[name] {
				have = 0
			}
			if e := entries[name]; e != nil {
				have += e.Insert
			}
			return have
		}
		// blocker names the ignored, empty table a run of name would need, or "".
		memo := map[string]string{}
		var blocker func(name string) string
		blocker = func(name string) string {
			if b, ok := memo[name]; ok {
				return b
			}
			memo[name] = ""
			for _, parent := range g.Parents(name) {
				if available(parent) > 0 {
					continue
				}
				b := parent
				if !isIgnored(parent) {
					b = blocker(parent)
				}
				if b != "" {
					memo[name] = b
					return b
				}
			}
			return ""
		}
		changed := false
		for _, name := range sorted {
			e := entries[name]
			if e == nil || e.Insert <= 0 {
				continue
			}
			b := blocker(name)
			if b == "" {
				continue
			}
			skip := PlanSkip{Table: name, Reason: ReasonIgnoredParent, Detail: "needs rows in ignored table " + b + ", which is empty"}
			e.Insert = 0
			if e.Reason == ReasonDependent {
				skips = append(skips, skip)
				continue
			}
			excluded[name] = skip
			changed = true
		}
		if !changed {
			break
		}
	}

	inserting := map[string]bool{}
	for name, e := range entries {
		if e.Insert > 0 {
			inserting[name] = true
		}
	}
	resolved, auto := graph.ResolveSelection(g, inserting, sorted)
	for _, name := range resolved {
		if !auto[name] || available(name) > 0 || isIgnored(name) {
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
	plan.Skipped = append(plan.Skipped, staticSkips...)
	for _, s := range excluded {
		plan.Skipped = append(plan.Skipped, s)
	}
	plan.Skipped = append(plan.Skipped, skips...)
	sort.SliceStable(plan.Skipped, func(i, j int) bool { return plan.Skipped[i].Table < plan.Skipped[j].Table })
	return plan, nil
}

// planPass builds entries for every candidate not excluded, expands reset
// truncation to descendants, and sets each entry's insert count.
func planPass(candidates map[string]Row, excluded map[string]PlanSkip, byTarget map[string]Row,
	target *schema.Schema, opts MirrorOptions, isIgnored func(string) bool,
) (map[string]*PlanEntry, map[string]bool, []PlanSkip) {
	entries := make(map[string]*PlanEntry, len(candidates))
	for name, row := range candidates {
		if _, skip := excluded[name]; !skip {
			entries[name] = wantEntry(row, opts)
		}
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
			if entries[name] != nil || isIgnored(name) {
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

	var skips []PlanSkip
	for name, e := range entries {
		switch {
		case opts.Mode == ModeReset:
			e.Insert = e.Want
		case e.TargetRows >= e.Want:
			e.Insert = 0
			if e.Want > 0 || e.TargetRows > 0 {
				skips = append(skips, PlanSkip{
					Table:  name,
					Reason: ReasonSatisfied,
					Detail: fmt.Sprintf("has %d, wants %d", e.TargetRows, e.Want),
				})
			}
		default:
			e.Insert = e.Want - e.TargetRows
		}
	}
	return entries, truncated, skips
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
