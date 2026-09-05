//go:build integration

package integration_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/web"
)

// These tests drive the real web server against the real databases from
// docker-compose, so the DSNs they build are proved against the actual drivers
// rather than against a stub.

type testOutcome struct {
	OK        bool   `json:"ok"`
	Driver    string `json:"driver"`
	Target    string `json:"target"`
	ElapsedMs int64  `json:"elapsedMs"`
	Error     string `json:"error"`
	Hint      *struct {
		Add *struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"add"`
		Remove string `json:"remove"`
		Note   string `json:"note"`
	} `json:"hint"`
	Issues []struct {
		Name    string `json:"name"`
		Level   string `json:"level"`
		Message string `json:"message"`
	} `json:"issues"`
}

func newWebServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	storePath := filepath.Join(t.TempDir(), "connections.yaml")
	s, err := web.New(web.Options{Addr: "127.0.0.1:0", ConnectionsPath: storePath})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, storePath
}

func noRedirect() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func postConnectForm(t *testing.T, srv *httptest.Server, path string, form url.Values) *http.Response {
	t.Helper()
	res, err := noRedirect().PostForm(srv.URL+path, form)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func decodeOutcome(t *testing.T, res *http.Response) testOutcome {
	t.Helper()
	var out testOutcome
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func postgresForm() url.Values {
	return url.Values{
		"dbType":   {"postgres"},
		"host":     {envOrDefault("SEEDSTORM_PG_HOST", "localhost")},
		"port":     {envOrDefault("SEEDSTORM_PG_PORT", "5432")},
		"dbName":   {"testdb"},
		"user":     {"seedstorm"},
		"password": {"seedstorm"},
		"ssl":      {"disable"},
	}
}

func mysqlForm() url.Values {
	return url.Values{
		"dbType":   {"mysql"},
		"host":     {envOrDefault("SEEDSTORM_MYSQL_HOST", "localhost")},
		"port":     {envOrDefault("SEEDSTORM_MYSQL_PORT", "3306")},
		"dbName":   {"testdb"},
		"user":     {"seedstorm"},
		"password": {"seedstorm"},
	}
}

func TestWebConnectTest_realDatabases(t *testing.T) {
	srv, _ := newWebServer(t)
	cases := []struct {
		name string
		form url.Values
	}{
		{"postgres", postgresForm()},
		{"mysql", mysqlForm()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decodeOutcome(t, postConnectForm(t, srv, "/connect/test", c.form))
			if !got.OK {
				t.Fatalf("test failed against a live database: %s", got.Error)
			}
			if !strings.Contains(got.Target, "testdb") {
				t.Fatalf("target = %q", got.Target)
			}
			if got.ElapsedMs < 0 {
				t.Fatalf("elapsed = %d", got.ElapsedMs)
			}
		})
	}
}

func TestWebConnectTest_wrongPasswordReportsDriverError(t *testing.T) {
	srv, _ := newWebServer(t)
	for name, form := range map[string]url.Values{"postgres": postgresForm(), "mysql": mysqlForm()} {
		t.Run(name, func(t *testing.T) {
			form.Set("password", "definitely-not-the-password")
			got := decodeOutcome(t, postConnectForm(t, srv, "/connect/test", form))
			if got.OK {
				t.Fatal("expected authentication to fail")
			}
			if !strings.Contains(strings.ToLower(got.Error), "password") &&
				!strings.Contains(strings.ToLower(got.Error), "access denied") {
				t.Fatalf("error should explain the rejection, got %q", got.Error)
			}
		})
	}
}

// Extra parameters must reach the real drivers, both from the structured fields
// and from a raw connection string.
func TestWebConnectTest_extraParamsReachTheDriver(t *testing.T) {
	srv, _ := newWebServer(t)

	t.Run("postgres application_name via fields", func(t *testing.T) {
		form := postgresForm()
		form["paramName"] = []string{"application_name", "connect_timeout"}
		form["paramValue"] = []string{"seedstorm-itest", "5"}
		got := decodeOutcome(t, postConnectForm(t, srv, "/connect/test", form))
		if !got.OK {
			t.Fatalf("extra params broke the connection: %s", got.Error)
		}
	})

	t.Run("mysql charset via fields", func(t *testing.T) {
		form := mysqlForm()
		form["paramName"] = []string{"charset", "timeout"}
		form["paramValue"] = []string{"utf8mb4", "5s"}
		got := decodeOutcome(t, postConnectForm(t, srv, "/connect/test", form))
		if !got.OK {
			t.Fatalf("extra params broke the connection: %s", got.Error)
		}
	})

	t.Run("mysql session variable via fields", func(t *testing.T) {
		// Unknown MySQL parameters are sent as "SET name = value", which is
		// exactly how a session variable is applied.
		form := mysqlForm()
		form["paramName"] = []string{"foreign_key_checks"}
		form["paramValue"] = []string{"0"}
		got := decodeOutcome(t, postConnectForm(t, srv, "/connect/test", form))
		if !got.OK {
			t.Fatalf("session variable rejected: %s", got.Error)
		}
		if len(got.Issues) != 1 || got.Issues[0].Level != "warn" {
			t.Fatalf("a session variable should warn, not error: %+v", got.Issues)
		}
	})

	t.Run("postgres raw dsn plus params", func(t *testing.T) {
		form := url.Values{
			"dbType":     {"postgres"},
			"dsn":        {"postgres://seedstorm:seedstorm@" + envOrDefault("SEEDSTORM_PG_HOST", "localhost") + ":" + envOrDefault("SEEDSTORM_PG_PORT", "5432") + "/testdb?sslmode=disable"},
			"paramName":  {"application_name"},
			"paramValue": {"seedstorm-raw"},
		}
		got := decodeOutcome(t, postConnectForm(t, srv, "/connect/test", form))
		if !got.OK {
			t.Fatalf("raw dsn with params failed: %s", got.Error)
		}
	})

	t.Run("mysql raw dsn plus params", func(t *testing.T) {
		form := url.Values{
			"dbType":     {"mysql"},
			"dsn":        {"seedstorm:seedstorm@tcp(" + envOrDefault("SEEDSTORM_MYSQL_HOST", "localhost") + ":" + envOrDefault("SEEDSTORM_MYSQL_PORT", "3306") + ")/testdb"},
			"paramName":  {"charset"},
			"paramValue": {"utf8mb4"},
		}
		got := decodeOutcome(t, postConnectForm(t, srv, "/connect/test", form))
		if !got.OK {
			t.Fatalf("raw dsn with params failed: %s", got.Error)
		}
	})
}

// allowPublicKeyRetrieval is a Connector/J flag. go-sql-driver forwards unknown
// parameters to the server as "SET name = value", so MySQL rejects it — which
// is why the UI flags it before dialing and offers to remove it afterwards.
func TestWebConnectTest_jdbcOnlyParamIsRejectedByMySQLAndExplained(t *testing.T) {
	srv, _ := newWebServer(t)
	form := mysqlForm()
	form["paramName"] = []string{"allowPublicKeyRetrieval"}
	form["paramValue"] = []string{"true"}

	got := decodeOutcome(t, postConnectForm(t, srv, "/connect/test", form))
	if got.OK {
		t.Fatal("MySQL should reject an unknown system variable")
	}
	if !strings.Contains(strings.ToLower(got.Error), "unknown system variable") {
		t.Fatalf("error = %q, want MySQL's unknown system variable message", got.Error)
	}
	if got.Hint == nil || got.Hint.Remove != "allowPublicKeyRetrieval" {
		t.Fatalf("hint = %+v, want a suggestion to remove the parameter", got.Hint)
	}
	if len(got.Issues) != 1 || got.Issues[0].Level != "error" {
		t.Fatalf("the parameter should have been flagged before dialing: %+v", got.Issues)
	}
}

func TestWebConnect_savedConnectionSurvivesARestart(t *testing.T) {
	srv, storePath := newWebServer(t)

	form := postgresForm()
	form.Set("label", "itest pg")
	form.Set("save", "1")
	form.Set("savePassword", "1")
	form["paramName"] = []string{"application_name"}
	form["paramValue"] = []string{"seedstorm-itest"}

	res := postConnectForm(t, srv, "/connect", form)
	if res.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("connect status = %d: %s", res.StatusCode, body)
	}

	// A fresh server over the same store stands in for restarting `serve`.
	restarted, err := web.New(web.Options{Addr: "127.0.0.1:0", ConnectionsPath: storePath})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	srv2 := httptest.NewServer(restarted.Handler())
	defer srv2.Close()

	page, err := http.Get(srv2.URL + "/connect")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer page.Body.Close()
	body, _ := io.ReadAll(page.Body)
	if !strings.Contains(string(body), "itest pg") {
		t.Fatalf("saved connection missing after restart:\n%s", body)
	}
	if !strings.Contains(string(body), "Pick a connection") {
		t.Fatal("a restarted server should land on the chooser")
	}

	// And it connects again without retyping anything.
	var id string
	listRes, err := http.Get(srv2.URL + "/api/saved-connections")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer listRes.Body.Close()
	var list []struct {
		ID          string `json:"id"`
		Label       string `json:"label"`
		HasPassword bool   `json:"hasPassword"`
	}
	if err := json.NewDecoder(listRes.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 || !list[0].HasPassword {
		t.Fatalf("list = %+v", list)
	}
	id = list[0].ID

	connectRes := postConnectForm(t, srv2, "/connect/saved", url.Values{"id": {id}})
	if connectRes.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(connectRes.Body)
		t.Fatalf("reconnect status = %d: %s", connectRes.StatusCode, body)
	}
}

func TestWebConnect_bothDriversCanBeSavedAndListed(t *testing.T) {
	srv, _ := newWebServer(t)

	for _, form := range []url.Values{postgresForm(), mysqlForm()} {
		f := form
		f.Set("label", f.Get("dbType")+" itest")
		f.Set("save", "1")
		f.Set("savePassword", "1")
		if res := postConnectForm(t, srv, "/connect", f); res.StatusCode != http.StatusSeeOther {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("connect %s = %d: %s", f.Get("dbType"), res.StatusCode, body)
		}
	}

	res, err := http.Get(srv.URL + "/connect")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	for _, want := range []string{"postgres itest", "mysql itest", "conn-dot postgres", "conn-dot mysql"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("chooser missing %q", want)
		}
	}
	// Both are live, so both offer a switch rather than a second connect.
	if strings.Count(string(body), "Switch to") != 2 {
		t.Fatalf("expected two live connections offering a switch:\n%s", body)
	}
}
