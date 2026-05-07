package web

import (
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	sess, _ := s.sessions.fromRequest(r)
	if sess == nil {
		s.render(w, r, "connect", pageData{Title: "Connect", Active: "connect"})
		return
	}
	s.render(w, r, "workspace", pageData{Title: "Workspace", Active: "workspace"})
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.render(w, r, "connect", pageData{Title: "Connect", Active: "connect"})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	port, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
	info := ConnectionInfo{
		DBType: strings.TrimSpace(r.FormValue("dbType")),
		Host:   strings.TrimSpace(r.FormValue("host")),
		Port:   port,
		DBName: strings.TrimSpace(r.FormValue("dbName")),
		User:   strings.TrimSpace(r.FormValue("user")),
		SSL:    strings.TrimSpace(r.FormValue("ssl")),
	}
	password := r.FormValue("password")
	if info.DBType == "" || info.DBName == "" || info.User == "" {
		s.render(w, r, "connect", pageData{
			Title:  "Connect",
			Active: "connect",
			Error:  "dbType, dbName, and user are required",
			Data:   info,
		})
		return
	}
	sess, err := s.sessions.Open(info, password)
	if err != nil {
		s.render(w, r, "connect", pageData{
			Title:  "Connect",
			Active: "connect",
			Error:  err.Error(),
			Data:   info,
		})
		return
	}
	setSessionCookie(w, sess.ID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	var current string
	if c, err := r.Cookie(sessionCookieName); err == nil {
		current = c.Value
		s.sessions.Close(c.Value)
	}
	if next := s.sessions.Pick(current); next != nil {
		setSessionCookie(w, next.ID)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.FormValue("id")
	if id == "" {
		// Allow query-string for plain anchors.
		id = r.URL.Query().Get("id")
	}
	if _, ok := s.sessions.Get(id); !ok {
		http.Error(w, "unknown connection", http.StatusNotFound)
		return
	}
	setSessionCookie(w, id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleConnectionsJSON(w http.ResponseWriter, r *http.Request) {
	current, _ := r.Cookie(sessionCookieName)
	type entry struct {
		ID     string         `json:"id"`
		Info   ConnectionInfo `json:"info"`
		Active bool           `json:"active"`
	}
	out := []entry{}
	for _, sess := range s.sessions.All() {
		out = append(out, entry{
			ID:     sess.ID,
			Info:   sess.Info,
			Active: current != nil && current.Value == sess.ID,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleIntrospect(w http.ResponseWriter, r *http.Request) {
	sess, err := s.sessions.fromRequest(r)
	if err != nil {
		http.Redirect(w, r, "/connect", http.StatusSeeOther)
		return
	}
	force := r.URL.Query().Get("refresh") == "1"
	tables, ierr := sess.RawTables()
	if ierr != nil {
		s.render(w, r, "introspect", pageData{
			Title: "Introspect", Active: "introspect", Error: ierr.Error(),
		})
		return
	}
	if force {
		// Refresh the cached schema.Schema mirror as well.
		_, _ = sess.Schema(true)
	}
	s.render(w, r, "introspect", pageData{
		Title:  "Introspect",
		Active: "introspect",
		Data:   tables,
	})
}

func (s *Server) handleGraphPage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.sessions.fromRequest(r); err != nil {
		http.Redirect(w, r, "/connect", http.StatusSeeOther)
		return
	}
	s.render(w, r, "graph", pageData{Title: "Dependency Graph", Active: "graph"})
}

func (s *Server) handleSeedPage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.sessions.fromRequest(r); err != nil {
		http.Redirect(w, r, "/connect", http.StatusSeeOther)
		return
	}
	s.render(w, r, "seed", pageData{Title: "Seed", Active: "seed"})
}

func (s *Server) handleGapsPage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.sessions.fromRequest(r); err != nil {
		http.Redirect(w, r, "/connect", http.StatusSeeOther)
		return
	}
	s.render(w, r, "gaps", pageData{Title: "Gaps", Active: "gaps"})
}

func (s *Server) handleGeneratePage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.sessions.fromRequest(r); err != nil {
		http.Redirect(w, r, "/connect", http.StatusSeeOther)
		return
	}
	s.render(w, r, "generate", pageData{Title: "Generate", Active: "generate"})
}

func (s *Server) handleEnrichPage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.sessions.fromRequest(r); err != nil {
		http.Redirect(w, r, "/connect", http.StatusSeeOther)
		return
	}
	s.render(w, r, "enrich", pageData{Title: "AI Enrich", Active: "enrich"})
}

func (s *Server) handleExportPage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.sessions.fromRequest(r); err != nil {
		http.Redirect(w, r, "/connect", http.StatusSeeOther)
		return
	}
	s.render(w, r, "export", pageData{Title: "Export", Active: "export"})
}
