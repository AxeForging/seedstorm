package graph

import (
	"fmt"
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
