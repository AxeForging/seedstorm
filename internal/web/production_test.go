package web

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// productionServer has a saved production connection and a live session
// connected to the same database (opened ad hoc, not from the saved entry).
func productionServer(t *testing.T) (*Server, *httptest.Server, *Session) {
	t.Helper()
	registerServeRunnerTestDriver()
	prev := sqlOpen
	sqlOpen = func(_, dsn string) (*sql.DB, error) { return sql.Open(serveRunnerTestDriverName, dsn) }
	t.Cleanup(func() { sqlOpen = prev })

	s, err := New(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Save(SavedConnection{Label: "billing-prod", DBType: "postgres", Host: "db.internal", Port: 5432, DBName: "billing", User: "svc", Production: true}, false); err != nil {
		t.Fatal(err)
	}
	sess, err := s.sessions.OpenDSN("pgx", "live-billing", ConnectionInfo{DBType: "postgres", Host: "DB.internal", Port: 5432, DBName: "billing", User: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv, sess
}

func postJSON(t *testing.T, srv *httptest.Server, sess *Session, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Seedstorm-Request", "1")
	if sess != nil {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// Writing to a connection marked production needs its label typed back. The
// refusal happens before a job exists, so nothing starts and nothing is written.
func TestProduction_WritesNeedTheTypedLabel(t *testing.T) {
	_, srv, sess := productionServer(t)
	s := srv.Config.Handler

	cases := []struct {
		name, path, body string
		want             int
	}{
		{"seed refused", "/api/seed", `{"rows":5}`, http.StatusConflict},
		{"seed with a wrong label", "/api/seed", `{"rows":5,"confirmProduction":"billing"}`, http.StatusConflict},
		{"seed dry run is not a write", "/api/seed", `{"rows":5,"dryRun":true}`, http.StatusAccepted},
		{"seed confirmed", "/api/seed", `{"rows":5,"confirmProduction":"billing-prod"}`, http.StatusAccepted},
		{"fill gaps refused", "/api/gaps", `{"rows":5,"fill":true}`, http.StatusConflict},
		{"scanning gaps is not a write", "/api/gaps", `{"rows":5}`, http.StatusAccepted},
		{"mirror into it refused", "/api/mirror", `{"source":{"id":"other"},"target":{"id":"` + sess.ID + `"}}`, http.StatusConflict},
		{"mirror dry run allowed", "/api/mirror", `{"source":{"id":"other"},"target":{"id":"` + sess.ID + `"},"dryRun":true}`, http.StatusAccepted},
		{"clone into it refused", "/api/clone-schema", `{"targetDsn":"postgres://svc@db.internal:5432/billing","target":{"dbType":"postgres","dbName":"billing","user":"svc","host":"db.internal","port":5432}}`, http.StatusConflict},
	}
	_ = s
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := postJSON(t, srv, sess, c.path, c.body)
			if status != c.want {
				t.Fatalf("status = %d (%v), want %d", status, body, c.want)
			}
			if c.want == http.StatusConflict {
				if body["code"] != "production_confirm" || body["label"] != "billing-prod" || !strings.Contains(body["error"].(string), "production") {
					t.Fatalf("refusal body = %v", body)
				}
			}
		})
	}
}

// Saving a connection without the flag (an older page, a PUT that omits it)
// must not silently clear it; clearing it needs the label too.
func TestProduction_FlagIsNotClearedWithoutTheLabel(t *testing.T) {
	s, srv, _ := productionServer(t)
	list, _ := s.store.List()
	id := list[0].ID
	put := func(body string) int {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/saved-connections?id="+id, strings.NewReader(body))
		req.Header.Set("X-Seedstorm-Request", "1")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := put(`{"label":"billing-prod","dbType":"postgres","host":"db.internal","port":5432,"dbName":"billing","user":"svc"}`); code != http.StatusConflict {
		t.Fatalf("unflag without label: status %d, want 409", code)
	}
	if got, _, _ := s.store.Get(id); !got.Production {
		t.Fatal("production flag was cleared")
	}
	if code := put(`{"label":"billing-prod","dbType":"postgres","host":"db.internal","port":5432,"dbName":"billing","user":"svc","confirmLabel":"billing-prod"}`); code != http.StatusOK {
		t.Fatalf("unflag with label: status %d", code)
	}
	if got, _, _ := s.store.Get(id); got.Production {
		t.Fatal("production flag not cleared with the label")
	}
}

// The connect form carries the flag, so saving from it keeps it.
func TestProduction_ConnectFormSavesTheFlag(t *testing.T) {
	s, srv := newConnectTestServer(t)
	form := url.Values{"action": {"save"}, "label": {"orders-prod"}, "dbType": {"postgres"}, "host": {"db"}, "port": {"5432"}, "dbName": {"orders"}, "user": {"svc"}, "production": {"on"}}
	res := postForm(t, srv, "/connect", form)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", res.StatusCode)
	}
	list, _ := s.store.List()
	if len(list) != 1 || !list[0].Production {
		t.Fatalf("saved = %+v", list)
	}
}
