package web

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// graphNode and graphEdge are the JSON shapes consumed by the Cytoscape view.
type graphNode struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Count   int64  `json:"count"`
	Counted bool   `json:"counted"`
}

type graphEdge struct {
	ID       string `json:"id"`
	Source   string `json:"source"` // parent table (must be seeded first)
	Target   string `json:"target"` // child table (depends on source)
	Column   string `json:"column"`
	Nullable bool   `json:"nullable"`
}

type graphPayload struct {
	Nodes []graphNode `json:"nodes"`
	Edges []graphEdge `json:"edges"`
	Order []string    `json:"order,omitempty"`
	Cycle bool        `json:"cycle"`
}

func (s *Server) handleGraphJSON(w http.ResponseWriter, r *http.Request) {
	sess, err := s.sessions.fromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	sc, err := sess.Schema(false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Row counts are best-effort; if the COUNT(*) sweep fails (e.g., a missing
	// table after a DDL edit) we still serve the structure.
	counts := map[string]int64{}
	tableNames := make([]string, 0, len(sc.Tables))
	for n := range sc.Tables {
		tableNames = append(tableNames, n)
	}
	if c, cerr := db.GetTableRowCounts(r.Context(), sess.Conn(), sess.DBType, tableNames); cerr == nil {
		counts = c
	}
	payload := buildGraphPayload(sc, counts)
	writeJSON(w, http.StatusOK, payload)
}

// handleCountsJSON exposes row counts on demand so the workspace can refresh
// the badges without redownloading the whole graph payload.
func (s *Server) handleCountsJSON(w http.ResponseWriter, r *http.Request) {
	sess, err := s.sessions.fromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	sc, err := sess.Schema(false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tables := make([]string, 0, len(sc.Tables))
	for n := range sc.Tables {
		tables = append(tables, n)
	}
	sort.Strings(tables)
	counts, cerr := db.GetTableRowCounts(r.Context(), sess.Conn(), sess.DBType, tables)
	if cerr != nil {
		writeError(w, http.StatusInternalServerError, cerr.Error())
		return
	}
	writeJSON(w, http.StatusOK, counts)
}

func buildGraphPayload(sc *schema.Schema, counts map[string]int64) graphPayload {
	out := graphPayload{}
	tableNames := make([]string, 0, len(sc.Tables))
	for name := range sc.Tables {
		tableNames = append(tableNames, name)
	}
	sort.Strings(tableNames)
	for _, name := range tableNames {
		c, ok := counts[name]
		out.Nodes = append(out.Nodes, graphNode{ID: name, Label: name, Count: c, Counted: ok})
	}
	edgeID := 0
	for _, tableName := range tableNames {
		t := sc.Tables[tableName]
		colNames := make([]string, 0, len(t.Columns))
		for c := range t.Columns {
			colNames = append(colNames, c)
		}
		sort.Strings(colNames)
		for _, colName := range colNames {
			col := t.Columns[colName]
			if col.FK == "" {
				continue
			}
			parts := strings.SplitN(col.FK, ".", 2)
			if len(parts) != 2 {
				continue
			}
			ref := parts[0]
			if ref == tableName {
				continue
			}
			edgeID++
			out.Edges = append(out.Edges, graphEdge{
				ID:       fmt.Sprintf("e%d", edgeID),
				Source:   ref,
				Target:   tableName,
				Column:   colName,
				Nullable: col.Nullable,
			})
		}
	}
	g := graph.Build(sc)
	if order, err := g.TopologicalSort(); err == nil {
		out.Order = order
	} else {
		out.Cycle = true
	}
	return out
}

func (s *Server) handleSchemaJSON(w http.ResponseWriter, r *http.Request) {
	sess, err := s.sessions.fromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	sc, err := sess.Schema(false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sc)
}
