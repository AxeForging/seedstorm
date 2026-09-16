package web

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/schema"
)

func TestSummarizeAccess_Levels(t *testing.T) {
	rw := db.TableAccess{Select: true, Insert: true, Update: true, Delete: true, Truncate: true}
	ro := db.TableAccess{Select: true}
	cases := []struct {
		name       string
		access     db.Access
		level      string
		noInsert   []string
		noTruncate []string
	}{
		{"owner of everything", db.Access{CreateTables: true, Tables: map[string]db.TableAccess{"a": rw, "b": rw}}, AccessFull, []string{}, []string{}},
		{"superuser wins over missing flags", db.Access{Superuser: true, Tables: map[string]db.TableAccess{"a": ro}}, AccessFull, []string{}, []string{}},
		{"cannot create tables", db.Access{Tables: map[string]db.TableAccess{"a": rw}}, AccessLimited, []string{}, []string{}},
		{"one table read-only", db.Access{CreateTables: true, Tables: map[string]db.TableAccess{"b": ro, "a": rw}}, AccessLimited, []string{"b"}, []string{"b"}},
		{"insert without truncate", db.Access{CreateTables: true, Tables: map[string]db.TableAccess{"a": {Select: true, Insert: true}}}, AccessLimited, []string{}, []string{"a"}},
		{"reads only", db.Access{Tables: map[string]db.TableAccess{"a": ro, "b": ro}}, AccessReadOnly, []string{"a", "b"}, []string{"a", "b"}},
		{"nothing at all", db.Access{Tables: map[string]db.TableAccess{"a": {}}}, AccessNone, []string{"a"}, []string{"a"}},
		{"empty database, no create", db.Access{Tables: map[string]db.TableAccess{}}, AccessReadOnly, []string{}, []string{}},
		{"empty database, can create", db.Access{CreateTables: true, Tables: map[string]db.TableAccess{}}, AccessFull, []string{}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := summarizeAccess(c.access, time.Now())
			if v.Level != c.level {
				t.Fatalf("level = %s, want %s", v.Level, c.level)
			}
			if !reflect.DeepEqual(v.NoInsert, c.noInsert) || !reflect.DeepEqual(v.NoTruncate, c.noTruncate) {
				t.Fatalf("noInsert=%v noTruncate=%v, want %v %v", v.NoInsert, v.NoTruncate, c.noInsert, c.noTruncate)
			}
		})
	}
}

func TestAccessAPI_RequiresAConnectionAndServesTheCachedReport(t *testing.T) {
	s := profileServer(t)
	if rec, _ := call(t, s, http.MethodGet, "/api/access", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session = %d, want 401", rec.Code)
	}
	sess := &Session{ID: "sess-access", DBType: "pgx"}
	// A fresh cached report is served without touching the database.
	cached := summarizeAccess(db.Access{User: "app", CreateTables: true, Tables: map[string]db.TableAccess{"a": {Select: true}}}, time.Now())
	sess.access = &cached
	s.sessions.sessions[sess.ID] = sess

	req := httptest.NewRequest(http.MethodGet, "/api/access", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"level":"limited"`) || !strings.Contains(rec.Body.String(), `"noInsert":["a"]`) {
		t.Fatalf("access = %d %s", rec.Code, rec.Body)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/access?id=nope", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown connection = %d %s, want 404", rec.Code, rec.Body)
	}
}

func TestProfileIgnoredAPI_ListsMatchesAgainstTheActiveSchema(t *testing.T) {
	s := profileServer(t)
	_, created := call(t, s, http.MethodPost, "/api/profiles", `{"rules":{"name":"staging","ignore":["flyway_*","*_AUDIT"],"tables":{"users":{"rows":7}}}}`)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create = %v", created)
	}
	sess := &Session{ID: "sess-ignored", DBType: "pgx"}
	sess.SetSchema(&schema.Schema{Tables: map[string]schema.Table{
		"users":                 {Columns: map[string]schema.Column{"id": {PK: true}}},
		"flyway_schema_history": {Columns: map[string]schema.Column{"id": {PK: true}}},
		"orders_audit":          {Columns: map[string]schema.Column{"id": {PK: true}}},
	}})
	s.sessions.sessions[sess.ID] = sess

	req := httptest.NewRequest(http.MethodGet, "/api/profiles/ignored?id="+id, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	want := `"ignored":[{"table":"flyway_schema_history","pattern":"flyway_*"},{"table":"orders_audit","pattern":"*_AUDIT"}]`
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) || !strings.Contains(rec.Body.String(), `"users":7`) {
		t.Fatalf("ignored = %d %s", rec.Code, rec.Body)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/profiles/ignored?id=p_missing", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown profile = %d, want 404", rec.Code)
	}
}
