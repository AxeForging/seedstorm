package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

type previewStringer string

func (s previewStringer) String() string { return string(s) }

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

func TestHandleTablePreviewJSON_requiresSession(t *testing.T) {
	s, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/table?table=users", nil)
	rec := httptest.NewRecorder()

	s.handleTablePreviewJSON(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestHandleTablePreviewJSON_validatesTableBeforeQuery(t *testing.T) {
	s, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := &Session{
		ID:     "test-session",
		DBType: "pgx",
		schema: &schema.Schema{Tables: map[string]schema.Table{
			"users": {Columns: map[string]schema.Column{
				"id": {Type: "int", PK: true},
			}},
		}},
	}
	s.sessions.sessions[sess.ID] = sess

	req := httptest.NewRequest(http.MethodGet, "/api/table?table=orders", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()

	s.handleTablePreviewJSON(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "table not found" {
		t.Fatalf("error = %q", body["error"])
	}
}

func TestHandleTablePreviewJSON_requiresTableName(t *testing.T) {
	s, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := &Session{ID: "test-session", schema: &schema.Schema{Tables: map[string]schema.Table{}}}
	s.sessions.sessions[sess.ID] = sess

	req := httptest.NewRequest(http.MethodGet, "/api/table", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()

	s.handleTablePreviewJSON(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestClampQueryInt(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?missing=&bad=nope&low=-4&high=500&ok=42", nil)

	cases := []struct {
		key  string
		want int
	}{
		{"missing", 25},
		{"bad", 25},
		{"low", 1},
		{"high", 100},
		{"ok", 42},
	}
	for _, tc := range cases {
		if got := clampQueryInt(req, tc.key, 25, 1, 100); got != tc.want {
			t.Fatalf("%s: got %d, want %d", tc.key, got, tc.want)
		}
	}
}

func TestPreviewValue(t *testing.T) {
	if got := previewValue(nil); got != "NULL" {
		t.Fatalf("nil = %q", got)
	}
	if got := previewValue([]byte("hello")); got != "hello" {
		t.Fatalf("bytes = %q", got)
	}
	if got := previewValue(previewStringer("custom")); got != "custom" {
		t.Fatalf("stringer = %q", got)
	}
	if got := previewValue(17); got != "17" {
		t.Fatalf("int = %q", got)
	}
}
