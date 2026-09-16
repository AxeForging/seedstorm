package rules

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// IgnoredTable is a schema table excluded from writes, with the first ignore
// glob (in document order) that matched it.
type IgnoredTable struct {
	Table   string `json:"table"`
	Pattern string `json:"pattern"`
}

// IgnorePattern returns the first ignore glob matching table, compared
// case-insensitively like every other rule glob. Blank and malformed globs never
// match: a blank one would otherwise mean "*" and silently ignore everything.
func (rs *RuleSet) IgnorePattern(table string) (string, bool) {
	if rs == nil {
		return "", false
	}
	for _, raw := range rs.Ignore {
		pattern := strings.TrimSpace(raw)
		if pattern != "" && globMatch(pattern, table) {
			return pattern, true
		}
	}
	return "", false
}

// IgnoredTables lists the schema tables the rule set ignores, sorted by table.
// It is nil for a nil rule set, a nil schema or when nothing matches.
func (rs *RuleSet) IgnoredTables(sc *schema.Schema) []IgnoredTable {
	if rs == nil || sc == nil || len(rs.Ignore) == 0 {
		return nil
	}
	var out []IgnoredTable
	for _, table := range sortedKeys(sc.Tables) {
		if pattern, ok := rs.IgnorePattern(table); ok {
			out = append(out, IgnoredTable{Table: table, Pattern: pattern})
		}
	}
	return out
}

// IgnoredSet is IgnoredTables as a lookup keyed by schema table name, the shape
// graph.ApplyIgnore and compare.MirrorOptions consume. It is nil when nothing
// is ignored.
func (rs *RuleSet) IgnoredSet(sc *schema.Schema) map[string]bool {
	list := rs.IgnoredTables(sc)
	if len(list) == 0 {
		return nil
	}
	set := make(map[string]bool, len(list))
	for _, it := range list {
		set[it.Table] = true
	}
	return set
}

// validateIgnoreStructure checks ignore globs without a schema.
func (rs *RuleSet) validateIgnoreStructure() []Issue {
	var issues []Issue
	for i, raw := range rs.Ignore {
		p := fmt.Sprintf("ignore[%d]", i)
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			issues = append(issues, Issue{Severity: SeverityError, Path: p, Message: "table pattern is required"})
			continue
		}
		if _, err := path.Match(pattern, "x"); err != nil {
			issues = append(issues, Issue{Severity: SeverityError, Path: p, Message: fmt.Sprintf("invalid table pattern %q", pattern)})
		}
	}
	return issues
}

// validateIgnoreSchema warns about globs that match nothing and about explicit
// table rules that an ignore glob makes dead.
func (rs *RuleSet) validateIgnoreSchema(sc *schema.Schema) []Issue {
	var issues []Issue
	tables := sortedKeys(sc.Tables)
	for i, raw := range rs.Ignore {
		pattern := strings.TrimSpace(raw)
		matched := false
		for _, table := range tables {
			if globMatch(pattern, table) {
				matched = true
				break
			}
		}
		if !matched {
			issues = append(issues, Issue{
				Severity: SeverityWarning,
				Path:     fmt.Sprintf("ignore[%d]", i),
				Message:  fmt.Sprintf("pattern %q matches no table in this database", pattern),
			})
		}
	}
	for _, docTable := range sortedKeys(rs.Tables) {
		table, ok := rs.schemaTableFor(sc, docTable)
		if !ok {
			continue
		}
		if pattern, ignored := rs.IgnorePattern(table); ignored {
			issues = append(issues, Issue{
				Severity: SeverityWarning,
				Path:     "tables." + docTable,
				Message:  fmt.Sprintf("rules for %s never apply: %s is ignored by %q", table, table, pattern),
			})
		}
	}
	sort.SliceStable(issues, func(i, j int) bool { return issues[i].Path < issues[j].Path })
	return issues
}
