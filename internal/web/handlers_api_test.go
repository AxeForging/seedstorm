package web

import (
	"reflect"
	"sort"
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

func TestBuildGraphPayload_orderingAndCounts(t *testing.T) {
	sc := &schema.Schema{
		Tables: map[string]schema.Table{
			"users": {Columns: map[string]schema.Column{
				"id": {PK: true, Type: "int"},
			}},
			"orders": {Columns: map[string]schema.Column{
				"id":      {PK: true, Type: "int"},
				"user_id": {Type: "int", FK: "users.id"},
			}},
			"items": {Columns: map[string]schema.Column{
				"id":       {PK: true, Type: "int"},
				"order_id": {Type: "int", FK: "orders.id", Nullable: true},
			}},
		},
	}
	counts := map[string]int64{"users": 5, "orders": 2}
	payload := buildGraphPayload(sc, counts)

	names := make([]string, 0, len(payload.Nodes))
	for _, n := range payload.Nodes {
		names = append(names, n.ID)
	}
	want := []string{"items", "orders", "users"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("nodes alphabetised? got %v want %v", names, want)
	}

	for _, n := range payload.Nodes {
		switch n.ID {
		case "users":
			if n.Count != 5 || !n.Counted {
				t.Fatalf("users count: got %+v", n)
			}
		case "items":
			if n.Counted {
				t.Fatalf("items should not be counted (no count provided): %+v", n)
			}
		}
	}

	// Two FK edges expected: items -> orders (nullable=true) and orders -> users (nullable=false).
	if len(payload.Edges) != 2 {
		t.Fatalf("edges = %d, want 2: %+v", len(payload.Edges), payload.Edges)
	}
	for _, e := range payload.Edges {
		if e.Source == "orders" && e.Target == "items" {
			if !e.Nullable {
				t.Fatalf("items.order_id is nullable but edge says hard")
			}
		}
		if e.Source == "users" && e.Target == "orders" && e.Nullable {
			t.Fatalf("orders.user_id is non-nullable but edge says nullable")
		}
	}

	// Topological order must put parents first.
	if !reflect.DeepEqual(payload.Order, []string{"users", "items", "orders"}) &&
		!reflect.DeepEqual(payload.Order, []string{"users", "orders", "items"}) &&
		!reflect.DeepEqual(payload.Order, []string{"items", "users", "orders"}) {
		// Kahn's algorithm with tiebreakers can yield several valid orders;
		// what matters is that the hard FK (orders ← users) is respected.
		idx := map[string]int{}
		for i, t := range payload.Order {
			idx[t] = i
		}
		if idx["users"] >= idx["orders"] {
			t.Fatalf("users must precede orders in topological order: %v", payload.Order)
		}
	}
	sort.Strings(names) // ensure no panic on names slice
}

func TestBuildGraphPayload_cycle(t *testing.T) {
	sc := &schema.Schema{
		Tables: map[string]schema.Table{
			"a": {Columns: map[string]schema.Column{
				"id":  {PK: true, Type: "int"},
				"bid": {Type: "int", FK: "b.id"},
			}},
			"b": {Columns: map[string]schema.Column{
				"id":  {PK: true, Type: "int"},
				"aid": {Type: "int", FK: "a.id"},
			}},
		},
	}
	payload := buildGraphPayload(sc, nil)
	if !payload.Cycle {
		t.Fatalf("expected cycle flag for mutually dependent tables")
	}
	if payload.Order != nil {
		t.Fatalf("order should be nil on cycle, got %v", payload.Order)
	}
}
