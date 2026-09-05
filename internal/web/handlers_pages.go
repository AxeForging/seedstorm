package web

import (
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	sess, _ := s.sessions.fromRequest(r)
	if sess == nil {
		s.renderConnect(w, r, s.connectPageFor(connectForm{}, ""), "")
		return
	}
	s.render(w, r, "workspace", pageData{Title: "Workspace", Active: "workspace"})
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		mode := strings.TrimSpace(r.URL.Query().Get("mode"))
		form := connectForm{}
		if id := strings.TrimSpace(r.URL.Query().Get("edit")); id != "" && s.store != nil {
			if saved, ok, _ := s.store.Get(id); ok {
				form = savedToForm(saved, "")
				form.Save = true
				form.SavePassword = saved.Password != ""
				form.NeedsPassword = saved.Password == ""
				form.Origin = "edit"
				mode = "form"
			}
		}
		// Duplicating drops the id so saving creates a second entry, and never
		// copies the secret into the page.
		if id := strings.TrimSpace(r.URL.Query().Get("duplicate")); id != "" && s.store != nil {
			if saved, ok, _ := s.store.Get(id); ok {
				form = savedToForm(saved, "")
				form.ID = ""
				form.Label = saved.Label + " copy"
				form.Save = true
				form.SavePassword = saved.Password != ""
				form.NeedsPassword = true
				form.Origin = "duplicate"
				mode = "form"
			}
		}
		s.renderConnect(w, r, s.connectPageFor(form, mode), "")
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := parseAnyForm(r); err != nil {
		http.Error(w, "bad form: "+err.Error(), http.StatusBadRequest)
		return
	}
	form := parseConnectForm(r)
	fail := func(msg string) {
		page := s.connectPageFor(form, "form")
		page.Hint = paramHintFromError(form.DBType, msg)
		s.renderConnect(w, r, page, msg)
	}
	if form.DSN == "" && (form.DBType == "" || form.DBName == "" || form.User == "") {
		fail("dbType, dbName, and user are required")
		return
	}
	// Saving is deliberately separate from connecting: editing a stored
	// connection should not force a session to be opened against it.
	if strings.TrimSpace(r.FormValue("action")) == "save" {
		if s.store == nil {
			fail("connection store unavailable")
			return
		}
		if err := validateParamNames(form.Params); err != nil {
			fail(err.Error())
			return
		}
		if _, err := s.store.Save(form.saved(), !form.SavePassword); err != nil {
			fail(err.Error())
			return
		}
		http.Redirect(w, r, "/connect?mode=chooser", http.StatusSeeOther)
		return
	}
	driver, dsn, info, err := form.dsnFor()
	if err != nil {
		fail(err.Error())
		return
	}
	sess, err := s.sessions.OpenDSN(driver, dsn, info)
	if err != nil {
		fail(err.Error())
		return
	}
	if form.Save && s.store != nil {
		saved := form.saved()
		saved.UsedAt = time.Now().UTC()
		if stored, err := s.store.Save(saved, !form.SavePassword); err == nil {
			form.ID = stored.ID
		}
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
	activeID := ""
	if current != nil {
		activeID = current.Value
	}
	for _, sess := range dedupeConnections(s.sessions.All(), activeID) {
		out = append(out, entry{
			ID:     sess.ID,
			Info:   sess.Info,
			Active: activeID == sess.ID,
		})
	}
	writeJSON(w, http.StatusOK, out)
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
