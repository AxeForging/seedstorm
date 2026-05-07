package graph

import (
	"reflect"
	"sort"
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

func newGraph(t *testing.T, tables map[string]map[string]schema.Column) *Graph {
	t.Helper()
	s := &schema.Schema{Tables: make(map[string]schema.Table, len(tables))}
	for name, cols := range tables {
		s.Tables[name] = schema.Table{Columns: cols}
	}
	return Build(s)
}

func TestResolveSelection_pullsHardParents(t *testing.T) {
	g := newGraph(t, map[string]map[string]schema.Column{
		"users":     {"id": {PK: true, Type: "int"}},
		"orders":    {"id": {PK: true, Type: "int"}, "user_id": {Type: "int", FK: "users.id"}},
		"items":     {"id": {PK: true, Type: "int"}, "order_id": {Type: "int", FK: "orders.id"}},
		"unrelated": {"id": {PK: true, Type: "int"}},
	})
	sorted, err := g.TopologicalSort()
	if err != nil {
		t.Fatalf("topo: %v", err)
	}
	resolved, auto := ResolveSelection(g, map[string]bool{"items": true}, sorted)
	got := append([]string(nil), resolved...)
	want := []string{"users", "orders", "items"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved = %v, want %v", got, want)
	}
	autoNames := keys(auto)
	sort.Strings(autoNames)
	if !reflect.DeepEqual(autoNames, []string{"orders", "users"}) {
		t.Fatalf("auto = %v, want [orders users]", autoNames)
	}
}

func TestResolveSelection_skipsNullableParents(t *testing.T) {
	g := newGraph(t, map[string]map[string]schema.Column{
		"departments": {"id": {PK: true, Type: "int"}},
		"employees": {
			"id":  {PK: true, Type: "int"},
			"dep": {Type: "int", FK: "departments.id", Nullable: true},
		},
	})
	sorted, err := g.TopologicalSort()
	if err != nil {
		t.Fatalf("topo: %v", err)
	}
	resolved, auto := ResolveSelection(g, map[string]bool{"employees": true}, sorted)
	if !reflect.DeepEqual(resolved, []string{"employees"}) {
		t.Fatalf("nullable parent should not be auto-included, got %v", resolved)
	}
	if len(auto) != 0 {
		t.Fatalf("auto should be empty, got %v", auto)
	}
}

func TestResolveSelection_emptySelection(t *testing.T) {
	g := newGraph(t, map[string]map[string]schema.Column{
		"a": {"id": {PK: true, Type: "int"}},
	})
	sorted, _ := g.TopologicalSort()
	resolved, auto := ResolveSelection(g, map[string]bool{}, sorted)
	if len(resolved) != 0 {
		t.Fatalf("resolved should be empty for empty selection, got %v", resolved)
	}
	if len(auto) != 0 {
		t.Fatalf("auto should be empty for empty selection, got %v", auto)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
