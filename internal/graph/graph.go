package graph

import (
	"fmt"
	"sort"
	"strings"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// Graph represents a dependency graph for topological ordering.
type Graph struct {
	nodes    []string
	edges    map[string][]string
	inDegree map[string]int
}

// Build creates a dependency graph from a schema.
// An edge from A → B means "A must be seeded before B" (B has a FK to A).
func Build(s *schema.Schema) *Graph {
	g := &Graph{
		edges:    make(map[string][]string),
		inDegree: make(map[string]int),
	}

	for tableName := range s.Tables {
		g.nodes = append(g.nodes, tableName)
		if _, exists := g.inDegree[tableName]; !exists {
			g.inDegree[tableName] = 0
		}
	}

	for tableName, table := range s.Tables {
		for _, col := range table.Columns {
			if col.FK == "" {
				continue
			}
			// Nullable FK columns can be seeded as NULL — don't add a dependency
			// edge. This breaks near-cycles (e.g. departments.head_employee_id →
			// employees while employees.department_id → departments) without
			// losing FK integrity: the column will be NULL on first seed pass.
			if col.Nullable {
				continue
			}
			parts := strings.SplitN(col.FK, ".", 2)
			if len(parts) != 2 {
				continue
			}
			refTable := parts[0]
			if refTable == tableName {
				continue // self-reference: skip
			}
			g.edges[refTable] = append(g.edges[refTable], tableName)
			g.inDegree[tableName]++
		}
	}

	return g
}

// TopologicalSort returns tables in seed-safe order using Kahn's algorithm.
func (g *Graph) TopologicalSort() ([]string, error) {
	inDegree := make(map[string]int, len(g.inDegree))
	for k, v := range g.inDegree {
		inDegree[k] = v
	}

	var queue []string
	for _, node := range g.nodes {
		if inDegree[node] == 0 {
			queue = append(queue, node)
		}
	}

	var sorted []string
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		sorted = append(sorted, node)

		for _, neighbor := range g.edges[node] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				queue = append(queue, neighbor)
			}
		}
	}

	if len(sorted) != len(g.nodes) {
		return nil, fmt.Errorf("circular FK dependency detected — use --disable-fk to bypass")
	}

	return sorted, nil
}

// Parents returns the tables that `table` has hard (non-nullable) FK dependencies on.
func (g *Graph) Parents(table string) []string {
	var parents []string
	for parent, children := range g.edges {
		for _, child := range children {
			if child == table {
				parents = append(parents, parent)
			}
		}
	}
	sort.Strings(parents)
	return parents
}

// Children returns the tables that depend on `table` via hard (non-nullable) FKs.
func (g *Graph) Children(table string) []string {
	children := make([]string, len(g.edges[table]))
	copy(children, g.edges[table])
	sort.Strings(children)
	return children
}

// RenderPlan returns a formatted seed-plan string showing the FK-safe insertion
// order and, per table, which parent tables it depends on (hard dependencies
// listed first, nullable/optional ones marked with "?").
func RenderPlan(s *schema.Schema, sortedTables []string, rows int) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "\n=== Dry Run — Seed Plan (%d tables, %d rows each) ===\n\n", len(sortedTables), rows)

	// Calculate column widths.
	numWidth := len(fmt.Sprintf("%d", len(sortedTables)))
	tableWidth := 0
	for _, t := range sortedTables {
		if len(t) > tableWidth {
			tableWidth = len(t)
		}
	}

	fmt.Fprintf(&sb, "  %-*s  %-*s  %s\n", numWidth, "#", tableWidth, "Table", "Depends On")
	fmt.Fprintf(&sb, "  %s\n", strings.Repeat("─", numWidth+2+tableWidth+2+40))

	for i, tableName := range sortedTables {
		table := s.Tables[tableName]

		// Collect hard and nullable FK parent tables (deduplicated).
		seen := make(map[string]bool)
		var hard, nullable []string
		for _, col := range table.Columns {
			if col.FK == "" {
				continue
			}
			parts := strings.SplitN(col.FK, ".", 2)
			if len(parts) != 2 {
				continue
			}
			ref := parts[0]
			if ref == tableName || seen[ref] {
				continue
			}
			seen[ref] = true
			if col.Nullable {
				nullable = append(nullable, ref+"?")
			} else {
				hard = append(hard, ref)
			}
		}
		sort.Strings(hard)
		sort.Strings(nullable)

		deps := "—"
		if all := append(hard, nullable...); len(all) > 0 {
			deps = strings.Join(all, ", ")
		}

		fmt.Fprintf(&sb, "  %-*d  %-*s  %s\n", numWidth, i+1, tableWidth, tableName, deps)
	}

	fmt.Fprintln(&sb)
	return sb.String()
}
