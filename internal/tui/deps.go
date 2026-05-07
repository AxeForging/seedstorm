package tui

import (
	"github.com/AxeForging/seedstorm/internal/graph"
)

// ResolveDeps is a thin wrapper around graph.ResolveSelection retained for
// the TUI call sites and existing tests.
func ResolveDeps(g *graph.Graph, selected map[string]bool, sortedAll []string) (resolved []string, autoSelected map[string]bool) {
	return graph.ResolveSelection(g, selected, sortedAll)
}
