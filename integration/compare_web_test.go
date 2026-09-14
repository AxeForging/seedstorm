//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/web"
)

// webClient drives `seedstorm serve` the way the browser does: one cookie jar,
// JSON calls, and jobs polled until they finish.
type webClient struct {
	t    *testing.T
	base string
	http *http.Client
}

func newWebClient(t *testing.T) *webClient {
	t.Helper()
	dir := t.TempDir()
	s, err := web.New(web.Options{
		Addr:            "127.0.0.1:0",
		ConnectionsPath: filepath.Join(dir, "connections.yaml"),
		ProfilesPath:    filepath.Join(dir, "profiles.yaml"),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &webClient{t: t, base: srv.URL, http: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *webClient) connectPostgres(dbName string) {
	c.t.Helper()
	form := postgresForm()
	form.Set("dbName", dbName)
	form.Set("label", dbName)
	res, err := c.http.PostForm(c.base+"/connect", form)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		body, _ := io.ReadAll(res.Body)
		c.t.Fatalf("connect %s = %d: %s", dbName, res.StatusCode, body)
	}
}

func (c *webClient) json(method, path string, body any, out any) int {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, reader)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: decode %v: %s", method, path, err, raw)
		}
	}
	return res.StatusCode
}

type jobSnapshot struct {
	ID     string         `json:"id"`
	Status string         `json:"status"`
	Error  string         `json:"error"`
	Result map[string]any `json:"result"`
}

// run starts a job endpoint and waits for it to finish.
func (c *webClient) run(path string, body any) jobSnapshot {
	c.t.Helper()
	var started jobSnapshot
	if code := c.json(http.MethodPost, path, body, &started); code != http.StatusAccepted {
		c.t.Fatalf("POST %s = %d", path, code)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		var job jobSnapshot
		c.json(http.MethodGet, "/api/jobs/"+started.ID, nil, &job)
		if job.Status != "running" && job.Status != "pending" {
			return job
		}
		time.Sleep(50 * time.Millisecond)
	}
	c.t.Fatalf("job %s did not finish", path)
	return jobSnapshot{}
}

func (c *webClient) sessionIDs() map[string]string {
	c.t.Helper()
	var conns []struct {
		ID   string `json:"id"`
		Info struct {
			DBName string `json:"dbName"`
		} `json:"info"`
	}
	c.json(http.MethodGet, "/api/connections", nil, &conns)
	out := map[string]string{}
	for _, conn := range conns {
		out[conn.Info.DBName] = conn.ID
	}
	return out
}

func remarshal[T any](t *testing.T, v any) T {
	t.Helper()
	b, _ := json.Marshal(v)
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	return out
}

func TestWebCompareMirrorAndProfiles_realDatabases(t *testing.T) {
	e := postgresEngine()
	srcDSN, src := e.scratchDB(t, "ss_web_src")
	tgtDSN, tgt := e.scratchDB(t, "ss_web_tgt")
	e.schema(t, src)
	schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
	runBin(t, "introspect", "--dsn", srcDSN, "--out", schemaPath)
	runBin(t, "seed", "--dsn", srcDSN, "--schema", schemaPath, "--rows", "4")
	runBin(t, "clone-schema", "--source-dsn", srcDSN, "--target-dsn", tgtDSN)
	source := tableCounts(t, e, src)

	c := newWebClient(t)
	c.connectPostgres("ss_web_src")
	c.connectPostgres("ss_web_tgt")
	ids := c.sessionIDs()
	srcRef, tgtRef := map[string]string{"id": ids["ss_web_src"]}, map[string]string{"id": ids["ss_web_tgt"]}
	if srcRef["id"] == "" || tgtRef["id"] == "" {
		t.Fatalf("sessions = %v", ids)
	}

	var profile struct {
		ID string `json:"id"`
	}
	profileRules := map[string]any{
		"name":  "web-eval",
		"rules": []map[string]any{{"column": "*email*", "template": "web+{{seq}}@seedstorm.test"}},
	}
	if code := c.json(http.MethodPost, "/api/profiles", map[string]any{"rules": profileRules}, &profile); code != http.StatusOK {
		t.Fatalf("save profile = %d", code)
	}

	t.Run("explain shows effective rules and tagged samples", func(t *testing.T) {
		var out struct {
			Issues  []map[string]any    `json:"issues"`
			Columns []map[string]any    `json:"columns"`
			Samples []map[string]string `json:"samples"`
			Error   string              `json:"sampleError"`
		}
		// The active (cookie) session is the target, whose users table is empty:
		// samples must still generate.
		c.json(http.MethodPost, "/api/profiles/explain", map[string]any{"rules": profileRules, "table": "users", "rows": 3}, &out)
		if out.Error != "" || len(out.Samples) != 3 {
			t.Fatalf("samples = %v, error = %s", out.Samples, out.Error)
		}
		for _, row := range out.Samples {
			if !strings.HasPrefix(row["email"], "web+") {
				t.Errorf("sample email = %q", row["email"])
			}
		}
		for _, col := range out.Columns {
			if col["column"] == "email" && col["source"].(map[string]any)["kind"] != "pattern" {
				t.Errorf("email column plan = %v", col)
			}
			if col["column"] == "id" && col["protected"] != "primary key" {
				t.Errorf("id column plan = %v", col)
			}
		}
	})

	t.Run("compare job reports both sides", func(t *testing.T) {
		job := c.run("/api/compare", map[string]any{"source": srcRef, "target": tgtRef})
		if job.Status != "done" {
			t.Fatalf("compare job = %+v", job)
		}
		report := remarshal[struct {
			Rows []struct {
				Table  string `json:"table"`
				Status string `json:"status"`
			} `json:"rows"`
		}](t, job.Result["report"])
		if len(report.Rows) != len(source) {
			t.Fatalf("report rows = %d, tables = %d", len(report.Rows), len(source))
		}
	})

	t.Run("dry run plans and previews without writing", func(t *testing.T) {
		job := c.run("/api/mirror", map[string]any{"source": srcRef, "target": tgtRef, "dryRun": true, "profileId": profile.ID, "previewRows": 2})
		if job.Status != "done" || job.Result["preview"] == nil {
			t.Fatalf("dry run = %+v", job)
		}
		plan := remarshal[struct {
			TotalInsert int64 `json:"totalInsert"`
		}](t, job.Result["plan"])
		if plan.TotalInsert == 0 {
			t.Fatal("dry run planned nothing")
		}
		for table, n := range tableCounts(t, e, tgt) {
			if n != 0 {
				t.Errorf("dry run wrote into %s", table)
			}
		}
	})

	t.Run("mirror run matches the source and applies the profile", func(t *testing.T) {
		job := c.run("/api/mirror", map[string]any{"source": srcRef, "target": tgtRef, "profileId": profile.ID})
		if job.Status != "done" {
			t.Fatalf("mirror job = %+v", job)
		}
		run := remarshal[struct {
			Missing int64 `json:"missing"`
		}](t, job.Result["run"])
		if run.Missing != 0 {
			t.Fatalf("missing rows: %v", job.Result["run"])
		}
		assertCounts(t, tableCounts(t, e, tgt), source, 1)
		if bad := scalar(t, tgt, "SELECT COUNT(*) FROM users WHERE email NOT LIKE 'web+%'"); bad != 0 {
			t.Errorf("%d users ignore the profile", bad)
		}
	})

	t.Run("application inserts still work after web mirror and seed", func(t *testing.T) {
		if _, err := tgt.Exec("INSERT INTO metric_sources (name) VALUES ('app after mirror')"); err != nil {
			t.Fatalf("insert with default id after mirror: %v", err)
		}
		job := c.run("/api/seed", map[string]any{"rows": 5, "tables": []string{"metric_sources"}})
		if job.Status != "done" {
			t.Fatalf("seed job = %+v", job)
		}
		if _, err := tgt.Exec("INSERT INTO metric_sources (name) VALUES ('app after seed')"); err != nil {
			t.Fatalf("insert with default id after workspace seed: %v", err)
		}
	})

	t.Run("mirror into the source database fails the job", func(t *testing.T) {
		job := c.run("/api/mirror", map[string]any{"source": srcRef, "target": srcRef})
		if job.Status != "failed" || !strings.Contains(job.Error, "same database") {
			t.Fatalf("job = %+v", job)
		}
		assertCounts(t, tableCounts(t, e, src), source, 1)
	})

	t.Run("workspace generate honours a profile", func(t *testing.T) {
		job := c.run("/api/generate", map[string]any{"rows": 2, "format": "yaml", "tables": []string{"users"}, "profileId": profile.ID})
		output, _ := job.Result["output"].(string)
		if job.Status != "done" || !strings.Contains(output, "web+1@seedstorm.test") {
			t.Fatalf("generate = %s %s\n%s", job.Status, job.Error, output)
		}
	})

	t.Run("saved connection can be a mirror target", func(t *testing.T) {
		form := url.Values{
			"dbType": {"postgres"}, "host": {envOrDefault("SEEDSTORM_PG_HOST", "localhost")}, "port": {envOrDefault("SEEDSTORM_PG_PORT", "5432")},
			"dbName": {"ss_web_tgt"}, "user": {"seedstorm"}, "password": {"seedstorm"}, "ssl": {"disable"}, "label": {"saved target"},
			"action": {"save"}, "savePassword": {"1"},
		}
		res, err := c.http.PostForm(c.base+"/connect", form)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		var saved []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		}
		c.json(http.MethodGet, "/api/saved-connections", nil, &saved)
		if len(saved) != 1 {
			t.Fatalf("saved = %v", saved)
		}
		job := c.run("/api/compare", map[string]any{"source": srcRef, "target": map[string]string{"savedId": saved[0].ID}})
		if job.Status != "done" {
			t.Fatalf("compare with saved target = %+v", job)
		}
	})
}
