package web

import (
	"fmt"
	"net/http"
	"strings"
)

// productionRefusal is a write to a production connection that the user has
// not confirmed by typing its label.
type productionRefusal struct {
	Label  string
	Action string
}

func (p *productionRefusal) Error() string {
	return fmt.Sprintf("%s is marked production: type its label (%s) to %s", p.Label, p.Label, p.Action)
}

// writeProductionRefusal answers 409 with what the page needs to ask for the
// label and retry.
func writeProductionRefusal(w http.ResponseWriter, p *productionRefusal) {
	writeJSON(w, http.StatusConflict, map[string]string{"error": p.Error(), "code": "production_confirm", "label": p.Label})
}

// connectionTarget is what a run writes to, as far as it is known before the
// run opens anything.
type connectionTarget struct {
	SavedID string
	Info    ConnectionInfo
}

// productionConnection returns the saved production connection a target
// points at: the saved entry itself, or any saved production entry reaching
// the same database (a session opened ad hoc, or reused by DSN, still counts).
func (s *Server) productionConnection(t connectionTarget) (SavedConnection, bool) {
	if s.store == nil {
		return SavedConnection{}, false
	}
	saved, err := s.store.List()
	if err != nil {
		return SavedConnection{}, false
	}
	key := infoKey(t.Info)
	for _, c := range saved {
		if !c.Production {
			continue
		}
		if t.SavedID != "" && c.ID == t.SavedID {
			return c, true
		}
		if key != "" && savedInfoKey(c) == key {
			return c, true
		}
	}
	return SavedConnection{}, false
}

// guardProduction refuses a write to a production target unless confirm is
// its label.
func (s *Server) guardProduction(t connectionTarget, confirm, action string) *productionRefusal {
	c, ok := s.productionConnection(t)
	if !ok || strings.TrimSpace(confirm) == c.Label {
		return nil
	}
	return &productionRefusal{Label: c.Label, Action: action}
}

// unflagRefusal refuses saving a production connection without the flag
// unless its label is typed: an older page or an API call that omits the
// field must not silently remove the protection.
func (s *Server) unflagRefusal(c SavedConnection, confirm string) *productionRefusal {
	if s.store == nil || c.ID == "" || c.Production {
		return nil
	}
	existing, ok, err := s.store.Get(c.ID)
	if err != nil || !ok || !existing.Production || strings.TrimSpace(confirm) == existing.Label {
		return nil
	}
	return &productionRefusal{Label: existing.Label, Action: "remove the production mark"}
}

// sessionTarget is the target of a run on a live session.
func sessionTarget(sess *Session) connectionTarget {
	if sess == nil {
		return connectionTarget{}
	}
	return connectionTarget{SavedID: sess.SavedID, Info: sess.Info}
}

// refTarget is the target of a run given by a ConnRef.
func (s *Server) refTarget(ref ConnRef) connectionTarget {
	if ref.SavedID != "" {
		return connectionTarget{SavedID: ref.SavedID}
	}
	if sess, ok := s.sessions.Get(ref.ID); ok {
		return sessionTarget(sess)
	}
	return connectionTarget{}
}

// infoKey identifies the database a connection reaches, like savedConnectionKey.
func infoKey(info ConnectionInfo) string {
	if info.DBName == "" && info.Host == "" {
		return ""
	}
	return savedConnectionKey(SavedConnection{DBType: info.DBType, Host: info.Host, Port: info.Port, DBName: info.DBName, User: info.User})
}

// savedInfoKey is infoKey for a saved connection, reading a raw DSN's parts.
func savedInfoKey(c SavedConnection) string {
	if c.DSN != "" && c.Host == "" && c.DBName == "" {
		if _, _, info, err := buildRawDSN(c.DBType, c.DSN, c.Params); err == nil {
			return infoKey(info)
		}
		return ""
	}
	return savedConnectionKey(c)
}
