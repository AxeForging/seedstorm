package tui

import (
	"strings"

	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// ResolveDeps takes a set of explicitly selected tables and returns the full
// set including all transitive non-nullable FK parents required by the selection.
// The returned slice preserves topological order from sortedAll.
// The autoSelected map contains tables that were added as dependencies (not
// explicitly chosen by the user).
func ResolveDeps(s *schema.Schema, g *graph.Graph, selected map[string]bool, sortedAll []string) (resolved []string, autoSelected map[string]bool) {
	autoSelected = make(map[string]bool)
	needed := make(map[string]bool)

	// Seed needed with explicit selection
	for t := range selected {
		needed[t] = true
	}

	// BFS: for each needed table, add its required parents
	queue := make([]string, 0, len(needed))
	for t := range needed {
		queue = append(queue, t)
	}

	for len(queue) > 0 {
		table := queue[0]
		queue = queue[1:]

		for _, parent := range requiredParents(s, table) {
			if !needed[parent] {
				needed[parent] = true
				autoSelected[parent] = true
				queue = append(queue, parent)
			}
		}
	}

	// Filter sortedAll to only include needed tables, preserving topo order
	for _, t := range sortedAll {
		if needed[t] {
			resolved = append(resolved, t)
		}
	}

	return resolved, autoSelected
}

// requiredParents returns tables that `table` has non-nullable FK references to,
// excluding self-references.
func requiredParents(s *schema.Schema, table string) []string {
	t, ok := s.Tables[table]
	if !ok {
		return nil
	}

	seen := make(map[string]bool)
	var parents []string
	for _, col := range t.Columns {
		if col.FK == "" || col.Nullable {
			continue
		}
		parts := strings.SplitN(col.FK, ".", 2)
		if len(parts) != 2 {
			continue
		}
		ref := parts[0]
		if ref == table || seen[ref] {
			continue
		}
		seen[ref] = true
		parents = append(parents, ref)
	}
	return parents
}
