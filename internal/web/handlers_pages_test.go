package web

import (
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
		{"/", http.StatusOK, "Connect to a database"},
		{"/connect", http.StatusOK, "Connect to a database"},
		{"/static/style.css", http.StatusOK, "--accent"},
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
			b := make([]byte, 4096)
			n, _ := res.Body.Read(b)
			if !strings.Contains(string(b[:n]), c.mustHave) {
				t.Fatalf("body missing %q", c.mustHave)
			}
		})
	}
}

func TestServer_apiRequiresSession(t *testing.T) {
	s, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// /api/graph without a session should return 401.
	res, err := http.Get(srv.URL + "/api/graph")
	if err != nil {
		t.Fatalf("GET /api/graph: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
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
	for _, p := range []string{"/seed", "/gaps", "/generate", "/enrich", "/export", "/introspect", "/graph"} {
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
