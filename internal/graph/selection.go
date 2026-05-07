package graph

// ResolveSelection takes an explicitly selected set of tables and expands it to
// include every transitive non-nullable FK parent required to seed those tables.
// The returned slice preserves the topological order from sortedAll. The
// autoSelected map identifies tables that were pulled in only as dependencies.
//
// Nullable FKs are not followed because the dependent column can be seeded as
// NULL on the first pass; this matches Build's edge construction.
func ResolveSelection(g *Graph, selected map[string]bool, sortedAll []string) (resolved []string, autoSelected map[string]bool) {
	autoSelected = make(map[string]bool)
	needed := make(map[string]bool, len(selected))
	for t := range selected {
		needed[t] = true
	}

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

	for _, t := range sortedAll {
		if needed[t] {
			resolved = append(resolved, t)
		}
	}
	return resolved, autoSelected
}
