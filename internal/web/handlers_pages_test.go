package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServer_routes_smoke(t *testing.T) {
	s, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	cases := []struct {
		path       string
		wantStatus int
		mustHave   string
	}{
		{"/", http.StatusOK, "Connect to your seed target"},
		{"/connect", http.StatusOK, "Connect to your seed target"},
		{"/static/style.css", http.StatusOK, "--accent"},
		{"/static/style.css", http.StatusOK, ".table-modal"},
		{"/static/app.js", http.StatusOK, "/api/table?"},
		{"/static/app.js", http.StatusOK, "openTableModal"},
		{"/static/app.js", http.StatusOK, "GENERATED_DRAFT_KEY"},
		{"/static/app.js", http.StatusOK, "GRAPH_ROUTE_KEY"},
		{"/static/app.js", http.StatusOK, "route-step"},
		{"/static/app.js", http.StatusOK, "routeColorFor"},
		{"/static/app.js", http.StatusOK, "/api/clone-schema"},
		{"/static/app.js", http.StatusOK, "setupConnectionMenuPresets"},
		{"/static/app.js", http.StatusOK, "presetConnectionInfo"},
		{"/static/app.js", http.StatusOK, "connectionKey"},
		{"/static/app.js", http.StatusOK, "dataset.kind = \"preset\""},
		{"/static/style.css", http.StatusOK, ".result-shell"},
		{"/static/style.css", http.StatusOK, ".ws-route-toggle"},
		{"/static/style.css", http.StatusOK, ".conn-preset-list"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			res, err := http.Get(srv.URL + c.path)
			if err != nil {
				t.Fatalf("GET %s: %v", c.path, err)
			}
			defer res.Body.Close()
			if res.StatusCode != c.wantStatus {
				t.Fatalf("status = %d, want %d", res.StatusCode, c.wantStatus)
			}
			b, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if !strings.Contains(string(b), c.mustHave) {
				t.Fatalf("body missing %q", c.mustHave)
			}
		})
	}
}

func TestWorkspaceRendersConnectionLabels(t *testing.T) {
	s, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	active := &Session{ID: "active", Info: ConnectionInfo{Label: "pg", DBType: "postgres", DBName: "testdb", Host: "localhost", Port: 5432, User: "seedstorm"}}
	target := &Session{ID: "target", Info: ConnectionInfo{Label: "target-db", DBType: "postgres", DBName: "clonedb", Host: "localhost", Port: 5432, User: "seedstorm"}}
	targetDup := &Session{ID: "target-dupe", Info: ConnectionInfo{Label: "target-db", DBType: "postgres", DBName: "clonedb", Host: "localhost", Port: 5432, User: "seedstorm"}}
	s.sessions.sessions[active.ID] = active
	s.sessions.sessions[target.ID] = target
	s.sessions.sessions[targetDup.ID] = targetDup

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: targetDup.ID})
	rec := httptest.NewRecorder()
	s.handleIndex(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<span class="conn-label">target-db</span>`) {
		t.Fatalf("active connection should render preset label, body missing target label")
	}
	if strings.Count(body, `<strong>target-db</strong>`) != 1 {
		t.Fatalf("duplicate target connection rendered in switcher:\n%s", body)
	}
	if !strings.Contains(body, `<strong>pg</strong>`) {
		t.Fatalf("source connection should still render in switcher")
	}
	if strings.Contains(body, `<strong>clonedb</strong>`) {
		t.Fatalf("target DB name leaked as switcher row instead of preset label")
	}
}

func TestConnectionsJSONDedupesDuplicateLiveConnections(t *testing.T) {
	s, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	first := &Session{ID: "first", Info: ConnectionInfo{Label: "target-db", DBType: "postgres", DBName: "clonedb", Host: "localhost", Port: 5432, User: "seedstorm"}}
	active := &Session{ID: "active", Info: ConnectionInfo{Label: "target-db", DBType: "postgres", DBName: "clonedb", Host: "localhost", Port: 5432, User: "seedstorm"}}
	source := &Session{ID: "source", Info: ConnectionInfo{Label: "pg", DBType: "postgres", DBName: "testdb", Host: "localhost", Port: 5432, User: "seedstorm"}}
	s.sessions.sessions[first.ID] = first
	s.sessions.sessions[active.ID] = active
	s.sessions.sessions[source.ID] = source

	req := httptest.NewRequest(http.MethodGet, "/api/connections", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: active.ID})
	rec := httptest.NewRecorder()
	s.handleConnectionsJSON(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var rows []struct {
		ID     string         `json:"id"`
		Info   ConnectionInfo `json:"info"`
		Active bool           `json:"active"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("connections len = %d, want 2: %#v", len(rows), rows)
	}
	targets := 0
	for _, row := range rows {
		if row.Info.Label == "target-db" {
			targets++
			if row.ID != active.ID || !row.Active {
				t.Fatalf("dedupe should keep active target row, got %#v", row)
			}
		}
	}
	if targets != 1 {
		t.Fatalf("target rows = %d, want 1: %#v", targets, rows)
	}
}

func TestServer_apiRequiresSession(t *testing.T) {
	s, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	for _, p := range []string{"/api/graph", "/api/table?table=users"} {
		res, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", p, res.StatusCode)
		}
	}
}

func TestServer_protectedPagesRedirectToConnect(t *testing.T) {
	s, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	for _, p := range []string{"/generate", "/enrich", "/export"} {
		res, err := client.Get(srv.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s: status = %d, want 303", p, res.StatusCode)
		}
		if loc := res.Header.Get("Location"); loc != "/connect" {
			t.Fatalf("%s: Location = %q, want /connect", p, loc)
		}
	}
}
