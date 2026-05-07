package web

import (
	"bytes"
	"encoding/json"
	"net/http"
)

type pageData struct {
	Title       string
	Active      string
	Session     *Session
	Connections []*Session
	Flash       string
	Error       string
	Data        any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data pageData) {
	sess, _ := s.sessions.fromRequest(r)
	data.Session = sess
	data.Connections = s.sessions.All()
	if data.Title == "" {
		data.Title = "seedstorm"
	}
	t, ok := s.pages[name]
	if !ok {
		http.Error(w, "no such page: "+name, http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		http.Error(w, "template: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
