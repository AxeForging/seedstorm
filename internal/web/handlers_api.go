package web

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

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
	// CountsTakenAt is when the node counts were taken; empty when the graph
	// carries no counts yet (the page asks /api/counts).
	CountsTakenAt string `json:"countsTakenAt,omitempty"`
}

type tablePreviewPayload struct {
	Table   string              `json:"table"`
	Limit   int                 `json:"limit"`
	Offset  int                 `json:"offset"`
	Total   int64               `json:"total"`
	Columns []string            `json:"columns"`
	Rows    []map[string]string `json:"rows"`
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
	// The graph is structure only, with counts the session already holds:
	// counting every table here made each return to the workspace wait on a
	// COUNT(*) per table before anything was drawn.
	counts, at := sess.CachedCounts()
	payload := buildGraphPayload(sc, counts)
	if counts != nil {
		payload.CountsTakenAt = at.Format(time.RFC3339)
	}
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
	// Counts are cached per session; ?refresh=1 recounts. Tables whose count
	// fails are left out: the page keeps them uncounted instead of empty.
	counts, at := sess.Counts(r.Context(), tables, r.URL.Query().Get("refresh") == "1")
	if len(counts) == 0 && len(tables) > 0 {
		writeError(w, http.StatusInternalServerError, "no table could be counted (see the server log)")
		return
	}
	w.Header().Set("X-Counts-Taken-At", at.Format(time.RFC3339))
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

func (s *Server) handleTablePreviewJSON(w http.ResponseWriter, r *http.Request) {
	sess, err := s.sessions.fromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	tableName := strings.TrimSpace(r.URL.Query().Get("table"))
	if tableName == "" {
		writeError(w, http.StatusBadRequest, "missing table")
		return
	}
	limit := clampQueryInt(r, "limit", 25, 1, 100)
	offset := clampQueryInt(r, "offset", 0, 0, 1_000_000_000)

	sc, err := sess.Schema(false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	table, ok := sc.Tables[tableName]
	if !ok {
		writeError(w, http.StatusNotFound, "table not found")
		return
	}

	columns := make([]string, 0, len(table.Columns))
	for name := range table.Columns {
		columns = append(columns, name)
	}
	sort.Strings(columns)
	payload, err := loadTablePreview(r.Context(), sess.Conn(), sess.DBType, tableName, columns, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func clampQueryInt(r *http.Request, key string, def, min, max int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// loadTablePreview reads one page of a table in a read-only transaction that
// never waits behind a lock.
func loadTablePreview(ctx context.Context, conn *sql.DB, dbType, tableName string, columns []string, limit, offset int) (payload tablePreviewPayload, err error) {
	err = db.ReadOnce(ctx, conn, dbType, db.DefaultCountLimits, func(ctx context.Context, q db.Querier) error {
		payload, err = readTablePreview(ctx, q, dbType, tableName, columns, limit, offset)
		return err
	})
	return payload, err
}

func readTablePreview(ctx context.Context, conn db.Querier, dbType, tableName string, columns []string, limit, offset int) (tablePreviewPayload, error) {
	payload := tablePreviewPayload{
		Table:   tableName,
		Limit:   limit,
		Offset:  offset,
		Columns: columns,
		Rows:    []map[string]string{},
	}
	quotedTable := db.QuoteIdent(tableName, dbType)
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quotedTable).Scan(&payload.Total); err != nil {
		return payload, fmt.Errorf("count %s: %w", tableName, err)
	}
	if len(columns) == 0 || limit <= 0 || offset >= int(payload.Total) {
		return payload, nil
	}

	quotedCols := make([]string, len(columns))
	for i, name := range columns {
		quotedCols[i] = db.QuoteIdent(name, dbType)
	}
	query := fmt.Sprintf("SELECT %s FROM %s LIMIT %d OFFSET %d", strings.Join(quotedCols, ", "), quotedTable, limit, offset)
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return payload, fmt.Errorf("preview %s: %w", tableName, err)
	}
	defer rows.Close()

	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return payload, fmt.Errorf("scan %s: %w", tableName, err)
		}
		row := make(map[string]string, len(columns))
		for i, col := range columns {
			row[col] = previewValue(values[i])
		}
		payload.Rows = append(payload.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return payload, err
	}
	return payload, nil
}

func previewValue(v any) string {
	if v == nil {
		return "NULL"
	}
	switch x := v.(type) {
	case []byte:
		return string(x)
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}
