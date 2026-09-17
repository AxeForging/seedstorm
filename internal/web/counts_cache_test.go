package web

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Leaving the workspace and coming back re-ran COUNT(*) on every table before
// the graph appeared. The graph is structure only; counts are counted once per
// session, cached with when they were taken, and recounted on request or after
// a run that writes.
func TestWorkspaceCounts_GraphDoesNotCountAndCountsAreCached(t *testing.T) {
	registerServeRunnerTestDriver()
	conn, err := sql.Open(serveRunnerTestDriverName, "counted")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s, err := New(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: "ws", DBType: "pgx", conn: conn, schema: runnerRowCountSchema()}
	s.sessions.add(sess)
	get := func(path string) (*httptest.ResponseRecorder, map[string]any) {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		body := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w, body
	}

	countedQueries.Store(0)
	_, graph := get("/api/graph")
	if n := countedQueries.Load(); n != 0 {
		t.Fatalf("the graph ran %d queries; it must serve structure without counting", n)
	}
	if graph["countsTakenAt"] != nil && graph["countsTakenAt"] != "" {
		t.Fatalf("graph claims counts before any were taken: %v", graph["countsTakenAt"])
	}

	w, counts := get("/api/counts")
	if counts["users"] != float64(3) || w.Header().Get("X-Counts-Taken-At") == "" {
		t.Fatalf("counts = %v, header %q", counts, w.Header().Get("X-Counts-Taken-At"))
	}
	first := countedQueries.Load()

	get("/api/counts")
	if n := countedQueries.Load(); n != first {
		t.Fatalf("a second counts request recounted (%d queries)", n-first)
	}
	_, graph = get("/api/graph")
	if graph["countsTakenAt"] == "" || graph["countsTakenAt"] == nil {
		t.Fatal("the graph does not carry the cached counts' time")
	}

	get("/api/counts?refresh=1")
	if n := countedQueries.Load(); n == first {
		t.Fatal("refresh=1 did not recount")
	}

	sess.InvalidateCounts()
	before := countedQueries.Load()
	get("/api/counts")
	if countedQueries.Load() == before {
		t.Fatal("counts were not recounted after being invalidated")
	}
}
