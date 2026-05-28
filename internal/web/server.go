package web

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed templates/*.html.tmpl
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

// Server is the HTTP front end for seedstorm features.
type Server struct {
	addr     string
	mux      *http.ServeMux
	pages    map[string]*template.Template
	sessions *SessionRegistry
	jobs     *Manager
}

// Options configures the Server.
type Options struct {
	Addr string
}

// New constructs a Server with all routes registered.
func New(opts Options) (*Server, error) {
	pages, err := loadTemplates()
	if err != nil {
		return nil, fmt.Errorf("load templates: %w", err)
	}
	s := &Server{
		addr:     opts.Addr,
		mux:      http.NewServeMux(),
		pages:    pages,
		sessions: NewSessionRegistry(),
		jobs:     NewManager(),
	}
	s.routes()
	return s, nil
}

// Addr returns the configured listen address.
func (s *Server) Addr() string { return s.addr }

// Handler returns the underlying http.Handler.
func (s *Server) Handler() http.Handler { return s.mux }

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{Addr: s.addr, Handler: s.mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	return srv.ListenAndServe()
}

func (s *Server) routes() {
	// Static assets.
	sub, _ := fs.Sub(staticFS, "static")
	s.mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))

	// Pages.
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/connect", s.handleConnect)
	s.mux.HandleFunc("/disconnect", s.handleDisconnect)
	s.mux.HandleFunc("/switch", s.handleSwitch)
	s.mux.HandleFunc("/api/connections", s.handleConnectionsJSON)
	s.mux.HandleFunc("/generate", s.handleGeneratePage)
	s.mux.HandleFunc("/enrich", s.handleEnrichPage)
	s.mux.HandleFunc("/export", s.handleExportPage)

	// JSON / API.
	s.mux.HandleFunc("/api/graph", s.handleGraphJSON)
	s.mux.HandleFunc("/api/counts", s.handleCountsJSON)
	s.mux.HandleFunc("/api/schema", s.handleSchemaJSON)
	s.mux.HandleFunc("/api/table", s.handleTablePreviewJSON)
	s.mux.HandleFunc("/api/jobs/", s.handleJobsAPI)
	s.mux.HandleFunc("/api/seed", s.handleSeedRun)
	s.mux.HandleFunc("/api/gaps", s.handleGapsRun)
	s.mux.HandleFunc("/api/generate", s.handleGenerateRun)
	s.mux.HandleFunc("/api/enrich", s.handleEnrichRun)
	s.mux.HandleFunc("/api/export", s.handleExportRun)
	s.mux.HandleFunc("/api/clone-schema", s.handleCloneSchemaRun)
}

// loadTemplates parses each page template as its own template set, with the
// shared layout composed in. Per-page sets prevent {{define "content"}} blocks
// from colliding across pages.
func loadTemplates() (map[string]*template.Template, error) {
	layoutB, err := fs.ReadFile(templatesFS, "templates/layout.html.tmpl")
	if err != nil {
		return nil, fmt.Errorf("read layout: %w", err)
	}
	pages := make(map[string]*template.Template)
	entries, err := fs.ReadDir(templatesFS, "templates")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html.tmpl") {
			continue
		}
		if e.Name() == "layout.html.tmpl" {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".html.tmpl")
		t := template.New(name).Funcs(templateFuncs())
		if _, err := t.Parse(string(layoutB)); err != nil {
			return nil, fmt.Errorf("layout: %w", err)
		}
		pageB, err := fs.ReadFile(templatesFS, "templates/"+e.Name())
		if err != nil {
			return nil, err
		}
		if _, err := t.Parse(string(pageB)); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		pages[name] = t
	}
	return pages, nil
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"def": func(a, b string) string {
			if a != "" {
				return a
			}
			return b
		},
		"connName": func(info ConnectionInfo) string {
			if info.Label != "" {
				return info.Label
			}
			if info.DBName != "" {
				return info.DBName
			}
			return info.Host
		},
	}
}
