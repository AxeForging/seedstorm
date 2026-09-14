package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func profileServer(t *testing.T) *Server {
	t.Helper()
	opts := testOptions(t)
	opts.ProfilesPath = filepath.Join(t.TempDir(), "profiles.yaml")
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func call(t *testing.T, s *Server, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestProfilesAPI_CreateUpdateListDelete(t *testing.T) {
	s := profileServer(t)
	rec, created := call(t, s, http.MethodPost, "/api/profiles", `{"rules":{"name":"loadtest","rules":[{"column":"*email*","template":"lt+{{seq}}@x.io"}]}}`)
	if rec.Code != http.StatusOK || created["id"] == "" {
		t.Fatalf("create = %d %v", rec.Code, created)
	}
	id := created["id"].(string)

	rec, _ = call(t, s, http.MethodPost, "/api/profiles", `{"id":"`+id+`","rules":{"name":"loadtest","description":"v2"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rec.Code, rec.Body)
	}
	_, list := call(t, s, http.MethodGet, "/api/profiles", "")
	profiles := list["profiles"].([]any)
	if len(profiles) != 1 || profiles[0].(map[string]any)["rules"].(map[string]any)["description"] != "v2" {
		t.Fatalf("list = %v", list)
	}

	rec, _ = call(t, s, http.MethodDelete, "/api/profiles?id="+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d", rec.Code)
	}
	if rec, _ = call(t, s, http.MethodDelete, "/api/profiles?id="+id, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", rec.Code)
	}
}

func TestProfilesAPI_RejectsInvalidInput(t *testing.T) {
	s := profileServer(t)
	cases := []struct {
		name, body, want string
		code             int
	}{
		{"malformed json", `{"rules":`, "invalid JSON", http.StatusBadRequest},
		{"missing name", `{"rules":{"rules":[{"column":"a","value":"x"}]}}`, "name is required", http.StatusBadRequest},
		{"broken rule", `{"rules":{"name":"x","rules":[{"column":"a"}]}}`, "invalid profile", http.StatusBadRequest},
		{"unknown id", `{"id":"p_nope","rules":{"name":"x"}}`, "not found", http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, out := call(t, s, http.MethodPost, "/api/profiles", c.body)
			if rec.Code != c.code || !strings.Contains(out["error"].(string), c.want) {
				t.Fatalf("got %d %v, want %d containing %q", rec.Code, out, c.code, c.want)
			}
		})
	}
	if rec, _ := call(t, s, http.MethodPut, "/api/profiles", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT = %d", rec.Code)
	}
}

func TestProfileYAML_RoundTripsBuilderJSON(t *testing.T) {
	s := profileServer(t)
	_, out := call(t, s, http.MethodPost, "/api/profiles/yaml", `{"rules":{"name":"rt","rules":[{"column":"note","setNull":true}]}}`)
	yamlText, _ := out["yaml"].(string)
	if !strings.Contains(yamlText, "setNull: true") || !strings.Contains(yamlText, "name: rt") {
		t.Fatalf("yaml = %q", yamlText)
	}
	body, _ := json.Marshal(map[string]string{"yaml": yamlText})
	_, parsed := call(t, s, http.MethodPost, "/api/profiles/yaml", string(body))
	rules := parsed["rules"].(map[string]any)["rules"].([]any)
	if len(rules) != 1 || rules[0].(map[string]any)["setNull"] != true {
		t.Fatalf("parsed = %v", parsed)
	}
	rec, bad := call(t, s, http.MethodPost, "/api/profiles/yaml", `{"yaml":"rules: [\n"}`)
	if rec.Code != http.StatusBadRequest || bad["error"] == nil {
		t.Fatalf("bad yaml = %d %v", rec.Code, bad)
	}
}

func TestGeneratorsAPI_ListsCatalogWithSamplesAndTokens(t *testing.T) {
	s := profileServer(t)
	_, out := call(t, s, http.MethodGet, "/api/generators", "")
	gens := out["generators"].([]any)
	if len(gens) < 30 {
		t.Fatalf("generators = %d", len(gens))
	}
	for _, g := range gens {
		m := g.(map[string]any)
		if m["expr"] == "" || m["sample"] == "" || m["category"] == "" {
			t.Fatalf("generator without expr/sample/category: %v", m)
		}
	}
	if tokens := out["tokens"].([]any); len(tokens) != 5 {
		t.Fatalf("tokens = %v", tokens)
	}
}

func TestProfileExplainAndRuns_RequireConnection(t *testing.T) {
	s := profileServer(t)
	for _, path := range []string{"/api/profiles/explain", "/api/profiles/example", "/api/compare", "/api/mirror"} {
		if rec, _ := call(t, s, http.MethodPost, path, `{}`); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a session = %d, want 401", path, rec.Code)
		}
	}
	for _, page := range []string{"/compare", "/profiles"} {
		rec, _ := call(t, s, http.MethodGet, page, "")
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/connect" {
			t.Errorf("%s without a session = %d %s", page, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestRunMirror_ValidatesRequestBeforeTouchingDatabases(t *testing.T) {
	s := profileServer(t)
	cases := []struct {
		name string
		req  MirrorRequest
		want string
	}{
		{"bad mode", MirrorRequest{Mode: "merge"}, "unknown mirror mode"},
		{"bad counts", MirrorRequest{Counts: "fast"}, "unknown count mode"},
		{"missing profile", MirrorRequest{ProfileID: "p_missing"}, "profile"},
		{"no source", MirrorRequest{}, "choose a source connection"},
		{"gone target", MirrorRequest{Source: ConnRef{ID: "x"}}, "source connection not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.runMirror(t.Context(), nil, c.req, testJobControl{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}
