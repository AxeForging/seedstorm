package tui

import (
	"github.com/AxeForging/seedstorm/internal/graph"
)

// ResolveDeps takes a set of explicitly selected tables and returns the full
// set including all transitive non-nullable FK parents required by the selection.
// The returned slice preserves topological order from sortedAll.
// The autoSelected map contains tables that were added as dependencies (not
// explicitly chosen by the user).
func ResolveDeps(g *graph.Graph, selected map[string]bool, sortedAll []string) (resolved []string, autoSelected map[string]bool) {
	autoSelected = make(map[string]bool)
	needed := make(map[string]bool)

	for t := range selected {
		needed[t] = true
	}

	// BFS: for each needed table, add its required (hard FK) parents
	queue := make([]string, 0, len(needed))
	for t := range needed {
		queue = append(queue, t)
	}

	for len(queue) > 0 {
		table := queue[0]
		queue = queue[1:]

		for _, parent := range g.Parents(table) {
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
