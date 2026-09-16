package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/compare"
)

func snapshotCall(t *testing.T, s *Server, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "sess-snap"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestSnapshotsAPI_ExportThenImportRoundTrips(t *testing.T) {
	s := profileServer(t)
	s.sessions.sessions["sess-snap"] = &Session{ID: "sess-snap", DBType: "pgx"}
	report := compare.Diff(
		compare.Snapshot{Label: "prod@db:5432", DBType: "pgx", CountMode: compare.CountExact, TakenAt: time.Now().UTC(), Tables: map[string]compare.TableStat{"users": {Rows: 1200, Bytes: 10}, "orders": {Rows: 5000, Bytes: -1}}},
		compare.Snapshot{Label: "stage", DBType: "mysql", Tables: map[string]compare.TableStat{"USERS": {Rows: 3}}},
	)

	for _, format := range []string{"yaml", "json"} {
		rec, out := snapshotCall(t, s, "/api/snapshots/encode", map[string]any{"report": report, "side": "source", "format": format})
		if rec.Code != http.StatusOK {
			t.Fatalf("encode %s = %d %s", format, rec.Code, rec.Body)
		}
		if out["filename"] != "prod-source-counts."+format || out["tables"].(float64) != 2 {
			t.Fatalf("encode %s = %v", format, out)
		}
		rec, parsed := snapshotCall(t, s, "/api/snapshots/parse", map[string]any{"data": out["content"]})
		if rec.Code != http.StatusOK || parsed["tables"].(float64) != 2 || parsed["rows"].(float64) != 6200 {
			t.Fatalf("parse exported %s = %d %v", format, rec.Code, parsed)
		}
	}

	rec, out := snapshotCall(t, s, "/api/snapshots/encode", map[string]any{"report": report, "side": "target", "format": "yaml"})
	if rec.Code != http.StatusOK || !strings.Contains(out["content"].(string), "USERS") {
		t.Fatalf("target export keeps target names: %d %v", rec.Code, out)
	}
}

func TestSnapshotsAPI_RejectsWithReadableErrors(t *testing.T) {
	s := profileServer(t)
	s.sessions.sessions["sess-snap"] = &Session{ID: "sess-snap", DBType: "pgx"}
	cases := []struct {
		name, path string
		body       any
		want       string
	}{
		{"profile pasted as counts", "/api/snapshots/parse", map[string]any{"data": "name: loadtest\nrules: []\n"}, "seed profile"},
		{"empty paste", "/api/snapshots/parse", map[string]any{"data": "  "}, "empty"},
		{"unknown format", "/api/snapshots/encode", map[string]any{"report": compare.Report{Rows: []compare.Row{{Table: "a", Source: &compare.TableStat{Rows: 1}}}}, "side": "source", "format": "csv"}, "format"},
		{"unknown side", "/api/snapshots/encode", map[string]any{"report": compare.Report{}, "side": "left", "format": "json"}, "unknown side"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, out := snapshotCall(t, s, c.path, c.body)
			msg, _ := out["error"].(string)
			if rec.Code != http.StatusBadRequest || !strings.Contains(msg, c.want) {
				t.Fatalf("got %d %q, want 400 containing %q", rec.Code, msg, c.want)
			}
		})
	}

	req := httptest.NewRequest(http.MethodPost, "/api/snapshots/parse", strings.NewReader(`{"data":"tables: {a: 1}"}`))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("without a session = %d, want 401", rec.Code)
	}
}

func TestSnapshotFilename(t *testing.T) {
	cases := map[[3]string]string{
		{"prod@db.internal:5432", "source", "yaml"}: "prod-source-counts.yaml",
		{"Staging DB (EU)", "target", "json"}:       "staging-db-eu-target-counts.json",
		{"", "source", "yml"}:                       "database-source-counts.yaml",
		{"@@@", "target", "json"}:                   "database-target-counts.json",
	}
	for in, want := range cases {
		if got := snapshotFilename(in[0], in[1], in[2]); got != want {
			t.Errorf("snapshotFilename(%q) = %q, want %q", in, got, want)
		}
	}
}
