package web

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/AxeForging/seedstorm/internal/db"
)

// accessTTL is how long a connection's privilege report is reused. Grants
// rarely change during a session; ?refresh=1 re-reads them on demand.
const accessTTL = 2 * time.Minute

// Access levels summarise a report for the connection badge.
const (
	AccessFull     = "full"      // every table writable, tables can be created
	AccessLimited  = "limited"   // some writes, truncates or creates are missing
	AccessReadOnly = "read-only" // reads only
	AccessNone     = "none"      // not even reads on the tables in scope
)

// accessView is a privilege report plus what the UI needs to warn per run mode.
type accessView struct {
	db.Access
	Level string `json:"level"`
	// NoSelect, NoInsert and NoTruncate list tables missing that privilege.
	NoSelect   []string  `json:"noSelect"`
	NoInsert   []string  `json:"noInsert"`
	NoTruncate []string  `json:"noTruncate"`
	CheckedAt  time.Time `json:"checkedAt"`
}

// summarizeAccess classifies a report. A superuser is full whatever the table
// flags say.
func summarizeAccess(a db.Access, checkedAt time.Time) accessView {
	v := accessView{Access: a, CheckedAt: checkedAt, NoSelect: []string{}, NoInsert: []string{}, NoTruncate: []string{}}
	selects, inserts := 0, 0
	for name, t := range a.Tables {
		if t.Select {
			selects++
		} else {
			v.NoSelect = append(v.NoSelect, name)
		}
		if t.Insert {
			inserts++
		} else {
			v.NoInsert = append(v.NoInsert, name)
		}
		if !t.Truncate {
			v.NoTruncate = append(v.NoTruncate, name)
		}
	}
	sort.Strings(v.NoSelect)
	sort.Strings(v.NoInsert)
	sort.Strings(v.NoTruncate)
	total := len(a.Tables)
	switch {
	case a.Superuser:
		v.Level = AccessFull
		v.NoSelect, v.NoInsert, v.NoTruncate = []string{}, []string{}, []string{}
	case total > 0 && inserts == 0 && selects == 0:
		v.Level = AccessNone
	case total > 0 && inserts == 0 && !a.CreateTables:
		v.Level = AccessReadOnly
	case total == 0 && !a.CreateTables:
		v.Level = AccessReadOnly
	case len(v.NoInsert) == 0 && len(v.NoTruncate) == 0 && len(v.NoSelect) == 0 && a.CreateTables:
		v.Level = AccessFull
	default:
		v.Level = AccessLimited
	}
	return v
}

// Access returns the session user's privilege report, read at most every
// accessTTL unless force is set.
func (s *Session) Access(ctx context.Context, force bool) (accessView, error) {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	if !force && s.access != nil && time.Since(s.access.CheckedAt) < accessTTL {
		return *s.access, nil
	}
	a, err := db.InspectAccess(ctx, s.conn, s.DBType)
	if err != nil {
		return accessView{}, err
	}
	v := summarizeAccess(a, time.Now())
	s.access = &v
	return v, nil
}

// handleAccessJSON reports what a connection's user may do.
//
//	GET /api/access                 the active connection
//	GET /api/access?id=<session>    another live connection
//	GET /api/access?savedId=<id>    a saved connection (opened on demand)
//	&refresh=1                      bypass the cache
func (s *Server) handleAccessJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	q := r.URL.Query()
	var sess *Session
	var err error
	if q.Get("id") != "" || q.Get("savedId") != "" {
		if _, err := s.sessions.fromRequest(r); err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		sess, err = s.resolveConnection(ConnRef{ID: q.Get("id"), SavedID: q.Get("savedId")}, "connection")
	} else {
		sess, err = s.sessions.fromRequest(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
	}
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	view, err := sess.Access(r.Context(), q.Get("refresh") == "1")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read privileges: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}
