package graph

import (
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// makeSchema builds a minimal Schema from a map of table → column definitions.
func makeSchema(tables map[string]map[string]schema.Column) *schema.Schema {
	s := &schema.Schema{Tables: make(map[string]schema.Table, len(tables))}
	for tblName, cols := range tables {
		s.Tables[tblName] = schema.Table{Columns: cols}
	}
	return s
}

// ── Build ─────────────────────────────────────────────────────────────────────

func TestBuild_noEdges(t *testing.T) {
	s := makeSchema(map[string]map[string]schema.Column{
		"users": {"id": {Type: "integer", PK: true}},
		"tags":  {"id": {Type: "integer", PK: true}},
	})
	g := Build(s)
	if len(g.nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(g.nodes))
	}
	for _, node := range g.nodes {
		if g.inDegree[node] != 0 {
			t.Errorf("node %s: expected inDegree 0, got %d", node, g.inDegree[node])
		}
	}
	for _, edges := range g.edges {
		if len(edges) != 0 {
			t.Errorf("expected no edges, got %v", edges)
		}
	}
}

func TestBuild_singleFK(t *testing.T) {
	s := makeSchema(map[string]map[string]schema.Column{
		"users": {"id": {Type: "integer", PK: true}},
		"orders": {
			"id":      {Type: "integer", PK: true},
			"user_id": {Type: "integer", FK: "users.id"},
		},
	})
	g := Build(s)

	if g.inDegree["orders"] != 1 {
		t.Errorf("orders inDegree: got %d, want 1", g.inDegree["orders"])
	}
	if g.inDegree["users"] != 0 {
		t.Errorf("users inDegree: got %d, want 0", g.inDegree["users"])
	}
	if len(g.edges["users"]) != 1 || g.edges["users"][0] != "orders" {
		t.Errorf("edges[users]: got %v, want [orders]", g.edges["users"])
	}
}

func TestBuild_multipleParents(t *testing.T) {
	// products → categories AND brands
	s := makeSchema(map[string]map[string]schema.Column{
		"categories": {"id": {Type: "integer", PK: true}},
		"brands":     {"id": {Type: "integer", PK: true}},
		"products": {
			"id":          {Type: "integer", PK: true},
			"category_id": {FK: "categories.id"},
			"brand_id":    {FK: "brands.id"},
		},
	})
	g := Build(s)

	if g.inDegree["products"] != 2 {
		t.Errorf("products inDegree: got %d, want 2", g.inDegree["products"])
	}
	if g.inDegree["categories"] != 0 {
		t.Errorf("categories inDegree: got %d, want 0", g.inDegree["categories"])
	}
}

func TestBuild_nullableFK_skipped(t *testing.T) {
	// Classic near-cycle: employees → departments (hard), departments → employees (nullable).
	// The nullable edge must NOT be added to the graph, so departments stays at inDegree 0.
	s := makeSchema(map[string]map[string]schema.Column{
		"departments": {
			"id":      {Type: "integer", PK: true},
			"head_id": {Type: "integer", FK: "employees.id", Nullable: true},
		},
		"employees": {
			"id":      {Type: "integer", PK: true},
			"dept_id": {Type: "integer", FK: "departments.id"},
		},
	})
	g := Build(s)

	if g.inDegree["departments"] != 0 {
		t.Errorf("departments inDegree: got %d, want 0 (nullable FK must be skipped)", g.inDegree["departments"])
	}
	if g.inDegree["employees"] != 1 {
		t.Errorf("employees inDegree: got %d, want 1", g.inDegree["employees"])
	}
}

func TestBuild_selfRef_skipped(t *testing.T) {
	s := makeSchema(map[string]map[string]schema.Column{
		"categories": {
			"id":        {Type: "integer", PK: true},
			"parent_id": {Type: "integer", FK: "categories.id", Nullable: true},
		},
	})
	g := Build(s)

	if g.inDegree["categories"] != 0 {
		t.Errorf("categories inDegree: got %d, want 0 (self-ref must be skipped)", g.inDegree["categories"])
	}
	if len(g.edges["categories"]) != 0 {
		t.Errorf("expected no self-edges, got %v", g.edges["categories"])
	}
}

// ── TopologicalSort ───────────────────────────────────────────────────────────

func TestTopologicalSort_linear(t *testing.T) {
	// a → b → c
	s := makeSchema(map[string]map[string]schema.Column{
		"a": {"id": {PK: true}},
		"b": {"id": {PK: true}, "a_id": {FK: "a.id"}},
		"c": {"id": {PK: true}, "b_id": {FK: "b.id"}},
	})
	sorted, err := Build(s).TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sorted) != 3 {
		t.Fatalf("expected 3 tables, got %d: %v", len(sorted), sorted)
	}
	pos := posOf(sorted)
	if pos["a"] >= pos["b"] {
		t.Errorf("a must precede b; got order %v", sorted)
	}
	if pos["b"] >= pos["c"] {
		t.Errorf("b must precede c; got order %v", sorted)
	}
}

func TestTopologicalSort_diamond(t *testing.T) {
	// a → b, a → c, b → d, c → d
	s := makeSchema(map[string]map[string]schema.Column{
		"a": {"id": {PK: true}},
		"b": {"id": {PK: true}, "a_id": {FK: "a.id"}},
		"c": {"id": {PK: true}, "a_id": {FK: "a.id"}},
		"d": {"id": {PK: true}, "b_id": {FK: "b.id"}, "c_id": {FK: "c.id"}},
	})
	sorted, err := Build(s).TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sorted) != 4 {
		t.Fatalf("expected 4 tables, got %d: %v", len(sorted), sorted)
	}
	pos := posOf(sorted)
	if pos["a"] >= pos["b"] || pos["a"] >= pos["c"] {
		t.Errorf("a must precede b and c; got %v", sorted)
	}
	if pos["b"] >= pos["d"] || pos["c"] >= pos["d"] {
		t.Errorf("b and c must precede d; got %v", sorted)
	}
}

func TestTopologicalSort_rootsOnly(t *testing.T) {
	s := makeSchema(map[string]map[string]schema.Column{
		"a": {"id": {PK: true}},
		"b": {"id": {PK: true}},
		"c": {"id": {PK: true}},
	})
	sorted, err := Build(s).TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sorted) != 3 {
		t.Errorf("expected 3 tables, got %d", len(sorted))
	}
}

func TestTopologicalSort_nearCycle_resolvedByNullable(t *testing.T) {
	// departments ↔ employees: only employees→departments is hard; the reverse is nullable.
	// Must resolve without error.
	s := makeSchema(map[string]map[string]schema.Column{
		"departments": {
			"id":      {PK: true},
			"head_id": {FK: "employees.id", Nullable: true},
		},
		"employees": {
			"id":      {PK: true},
			"dept_id": {FK: "departments.id"},
		},
	})
	sorted, err := Build(s).TopologicalSort()
	if err != nil {
		t.Fatalf("near-cycle should resolve via nullable FK, got error: %v", err)
	}
	pos := posOf(sorted)
	if pos["departments"] >= pos["employees"] {
		t.Errorf("departments must precede employees; got %v", sorted)
	}
}

func TestTopologicalSort_cycle_returnsError(t *testing.T) {
	// a → b → a (both non-nullable) — must be detected as a cycle.
	s := makeSchema(map[string]map[string]schema.Column{
		"a": {"id": {PK: true}, "b_id": {FK: "b.id"}},
		"b": {"id": {PK: true}, "a_id": {FK: "a.id"}},
	})
	_, err := Build(s).TopologicalSort()
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "circular") {
		t.Errorf("error should mention circular dependency, got: %v", err)
	}
}

// ── RenderPlan ────────────────────────────────────────────────────────────────

func TestRenderPlan_containsAllTables(t *testing.T) {
	s := makeSchema(map[string]map[string]schema.Column{
		"users":  {"id": {PK: true}},
		"orders": {"id": {PK: true}, "user_id": {FK: "users.id"}},
	})
	g := Build(s)
	sorted, _ := g.TopologicalSort()
	out := RenderPlan(s, sorted, 100)

	for _, tbl := range sorted {
		if !strings.Contains(out, tbl) {
			t.Errorf("RenderPlan output missing table %q", tbl)
		}
	}
}

func TestRenderPlan_showsHardDep(t *testing.T) {
	s := makeSchema(map[string]map[string]schema.Column{
		"brands":   {"id": {PK: true}},
		"products": {"id": {PK: true}, "brand_id": {FK: "brands.id"}},
	})
	g := Build(s)
	sorted, _ := g.TopologicalSort()
	out := RenderPlan(s, sorted, 50)

	if !strings.Contains(out, "brands") {
		t.Error("expected 'brands' in Depends On column")
	}
	// products row must list brands (no "?")
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.Contains(line, "products") {
			if !strings.Contains(line, "brands") {
				t.Errorf("products row should list 'brands' as dep; got: %q", line)
			}
			if strings.Contains(line, "brands?") {
				t.Errorf("brands is not nullable — should not have '?'; got: %q", line)
			}
		}
	}
}

func TestRenderPlan_showsNullableDepWithQuestionMark(t *testing.T) {
	s := makeSchema(map[string]map[string]schema.Column{
		"coupons": {"id": {PK: true}},
		"orders": {
			"id":        {PK: true},
			"coupon_id": {FK: "coupons.id", Nullable: true},
		},
	})
	g := Build(s)
	sorted, _ := g.TopologicalSort()
	out := RenderPlan(s, sorted, 10)

	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.Contains(line, "orders") {
			if !strings.Contains(line, "coupons?") {
				t.Errorf("nullable FK should appear as 'coupons?' in orders row; got: %q", line)
			}
		}
	}
}

func TestRenderPlan_rootHasDash(t *testing.T) {
	s := makeSchema(map[string]map[string]schema.Column{
		"brands": {"id": {PK: true}},
	})
	out := RenderPlan(s, []string{"brands"}, 5)
	if !strings.Contains(out, "—") {
		t.Errorf("root table should show '—' for Depends On; got:\n%s", out)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func posOf(sorted []string) map[string]int {
	m := make(map[string]int, len(sorted))
	for i, n := range sorted {
		m[n] = i
	}
	return m
}
