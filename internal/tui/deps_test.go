package tui

import (
	"testing"

	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/schema"
)

func makeSchema(tables map[string]map[string]schema.Column) *schema.Schema {
	s := &schema.Schema{Tables: make(map[string]schema.Table)}
	for name, cols := range tables {
		s.Tables[name] = schema.Table{Columns: cols}
	}
	return s
}

func TestResolveDeps_linearChain(t *testing.T) {
	// A -> B -> C (C depends on B, B depends on A)
	s := makeSchema(map[string]map[string]schema.Column{
		"a": {"id": {Type: "integer", PK: true}},
		"b": {"id": {Type: "integer", PK: true}, "a_id": {Type: "integer", FK: "a.id"}},
		"c": {"id": {Type: "integer", PK: true}, "b_id": {Type: "integer", FK: "b.id"}},
	})
	g := graph.Build(s)
	sorted, _ := g.TopologicalSort()

	// Select only C — should auto-select A and B
	selected := map[string]bool{"c": true}
	resolved, auto := ResolveDeps(g, selected, sorted)

	if len(resolved) != 3 {
		t.Fatalf("expected 3 tables, got %d: %v", len(resolved), resolved)
	}
	if !auto["a"] || !auto["b"] {
		t.Errorf("expected a and b to be auto-selected, got auto=%v", auto)
	}
	if auto["c"] {
		t.Error("c should NOT be auto-selected (it was explicitly selected)")
	}
	// Topo order: a before b before c
	if resolved[0] != "a" || resolved[1] != "b" || resolved[2] != "c" {
		t.Errorf("expected [a b c], got %v", resolved)
	}
}

func TestResolveDeps_diamond(t *testing.T) {
	// A and B are roots; C depends on both; D depends on C
	s := makeSchema(map[string]map[string]schema.Column{
		"a": {"id": {Type: "integer", PK: true}},
		"b": {"id": {Type: "integer", PK: true}},
		"c": {"id": {Type: "integer", PK: true}, "a_id": {Type: "integer", FK: "a.id"}, "b_id": {Type: "integer", FK: "b.id"}},
		"d": {"id": {Type: "integer", PK: true}, "c_id": {Type: "integer", FK: "c.id"}},
	})
	g := graph.Build(s)
	sorted, _ := g.TopologicalSort()

	// Select only D — should pull in C, A, B
	selected := map[string]bool{"d": true}
	resolved, auto := ResolveDeps(g, selected, sorted)

	if len(resolved) != 4 {
		t.Fatalf("expected 4 tables, got %d: %v", len(resolved), resolved)
	}
	if !auto["a"] || !auto["b"] || !auto["c"] {
		t.Errorf("expected a,b,c auto-selected, got auto=%v", auto)
	}
}

func TestResolveDeps_nullableFK_notAutoSelected(t *testing.T) {
	// B has nullable FK to A — selecting B should NOT auto-select A
	s := makeSchema(map[string]map[string]schema.Column{
		"a": {"id": {Type: "integer", PK: true}},
		"b": {"id": {Type: "integer", PK: true}, "a_id": {Type: "integer", FK: "a.id", Nullable: true}},
	})
	g := graph.Build(s)
	sorted, _ := g.TopologicalSort()

	selected := map[string]bool{"b": true}
	resolved, auto := ResolveDeps(g, selected, sorted)

	if len(resolved) != 1 {
		t.Fatalf("expected 1 table, got %d: %v", len(resolved), resolved)
	}
	if auto["a"] {
		t.Error("a should NOT be auto-selected (FK is nullable)")
	}
}

func TestResolveDeps_selfRef_noInfiniteLoop(t *testing.T) {
	// Table references itself — should not cause infinite loop
	s := makeSchema(map[string]map[string]schema.Column{
		"cats": {"id": {Type: "integer", PK: true}, "parent_id": {Type: "integer", FK: "cats.id", Nullable: true}},
	})
	g := graph.Build(s)
	sorted, _ := g.TopologicalSort()

	selected := map[string]bool{"cats": true}
	resolved, auto := ResolveDeps(g, selected, sorted)

	if len(resolved) != 1 {
		t.Fatalf("expected 1 table, got %d", len(resolved))
	}
	if len(auto) != 0 {
		t.Errorf("expected no auto-selections, got %v", auto)
	}
}

func TestResolveDeps_selectParent_noExtraChildren(t *testing.T) {
	// A is parent of B and C. Selecting only A should NOT pull in B or C.
	s := makeSchema(map[string]map[string]schema.Column{
		"a": {"id": {Type: "integer", PK: true}},
		"b": {"id": {Type: "integer", PK: true}, "a_id": {Type: "integer", FK: "a.id"}},
		"c": {"id": {Type: "integer", PK: true}, "a_id": {Type: "integer", FK: "a.id"}},
	})
	g := graph.Build(s)
	sorted, _ := g.TopologicalSort()

	selected := map[string]bool{"a": true}
	resolved, _ := ResolveDeps(g, selected, sorted)

	if len(resolved) != 1 {
		t.Fatalf("expected 1 table (only a), got %d: %v", len(resolved), resolved)
	}
}

func TestResolveDeps_preservesTopoOrder(t *testing.T) {
	s := makeSchema(map[string]map[string]schema.Column{
		"x": {"id": {Type: "integer", PK: true}},
		"y": {"id": {Type: "integer", PK: true}, "x_id": {Type: "integer", FK: "x.id"}},
		"z": {"id": {Type: "integer", PK: true}, "y_id": {Type: "integer", FK: "y.id"}},
	})
	g := graph.Build(s)
	sorted, _ := g.TopologicalSort()

	// Select z and x explicitly — y should be auto-selected, order should be x,y,z
	selected := map[string]bool{"z": true, "x": true}
	resolved, auto := ResolveDeps(g, selected, sorted)

	if len(resolved) != 3 {
		t.Fatalf("expected 3 tables, got %d", len(resolved))
	}
	if !auto["y"] {
		t.Error("y should be auto-selected")
	}
	// Verify topo order
	idx := make(map[string]int)
	for i, t := range resolved {
		idx[t] = i
	}
	if idx["x"] > idx["y"] || idx["y"] > idx["z"] {
		t.Errorf("expected x < y < z order, got %v", resolved)
	}
}
