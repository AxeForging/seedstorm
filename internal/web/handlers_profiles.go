package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/AxeForging/seedstorm/internal/compare"

	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/profiles"
	"github.com/AxeForging/seedstorm/internal/rules"
	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/AxeForging/seedstorm/internal/seeder"
)

// maxProfileBody bounds profile and saved-connection payloads, which are small
// documents.
const maxProfileBody = 1 << 20

func (s *Server) handleProfilesPage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.sessions.fromRequest(r); err != nil {
		http.Redirect(w, r, "/connect", http.StatusSeeOther)
		return
	}
	s.render(w, r, "profiles", pageData{Title: "Profiles", Active: "profiles"})
}

type generatorView struct {
	faker.Generator
	Sample string `json:"sample"`
}

// handleGeneratorsJSON lists the builder palette: every generator with a fresh
// sample value, plus the built-in template tokens.
func (s *Server) handleGeneratorsJSON(w http.ResponseWriter, r *http.Request) {
	catalog := faker.Catalog()
	out := make([]generatorView, 0, len(catalog))
	for _, g := range catalog {
		sample := ""
		if v, err := faker.Evaluate(g.Expr); err == nil {
			sample = fmt.Sprintf("%v", v)
		}
		out = append(out, generatorView{Generator: g, Sample: sample})
	}
	writeJSON(w, http.StatusOK, map[string]any{"generators": out, "tokens": rules.BuiltinTokens()})
}

// handleProfiles serves the saved-profile collection:
//
//	GET    /api/profiles          -> list
//	POST   /api/profiles          -> create or update {id?, rules}
//	DELETE /api/profiles?id=...   -> delete
func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.profiles.List()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"profiles": list, "path": s.profiles.Path()})
	case http.MethodPost:
		var req struct {
			ID    string        `json:"id"`
			Rules rules.RuleSet `json:"rules"`
		}
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		saved, err := s.profiles.Save(req.ID, req.Rules)
		switch {
		case errors.Is(err, profiles.ErrNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case err != nil:
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeJSON(w, http.StatusOK, saved)
		}
	case http.MethodDelete:
		removed, err := s.profiles.Delete(r.URL.Query().Get("id"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !removed {
			writeError(w, http.StatusNotFound, profiles.ErrNotFound.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "GET, POST or DELETE required")
	}
}

// handleProfileYAML converts between the builder's JSON and YAML:
// POST {rules} -> {yaml}; POST {yaml} -> {rules, issues}.
func (s *Server) handleProfileYAML(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req struct {
		Rules *rules.RuleSet `json:"rules"`
		YAML  string         `json:"yaml"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Rules != nil {
		b, err := req.Rules.Marshal()
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"yaml": string(b)})
		return
	}
	rs, err := rules.Parse([]byte(req.YAML))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rs, "issues": nonNilIssues(rs.Validate(nil))})
}

// ExplainRequest previews a rule set against the active connection's schema.
type ExplainRequest struct {
	Rules rules.RuleSet `json:"rules"`
	Table string        `json:"table"`
	Rows  int           `json:"rows"`
}

// handleProfileExplain validates a rule set against the active schema and, for
// one table, returns each column's default vs effective generator and freshly
// generated sample rows. Nothing is written.
func (s *Server) handleProfileExplain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	sess, err := s.sessions.fromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	var req ExplainRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sc, err := sess.Schema(false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tables := make([]string, 0, len(sc.Tables))
	for name := range sc.Tables {
		tables = append(tables, name)
	}
	sort.Strings(tables)

	resp := map[string]any{
		"tables":   tables,
		"issues":   nonNilIssues(req.Rules.Validate(sc)),
		"counts":   ruleCoverage(&req.Rules, sc),
		"examples": req.Rules.Examples(sc, 3, req.Table),
		"ignored":  nonNilIgnored(req.Rules.IgnoredTables(sc)),
	}
	if req.Table == "" {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if _, ok := sc.Tables[req.Table]; !ok {
		writeError(w, http.StatusNotFound, "table not found")
		return
	}
	resp["table"] = req.Table
	resp["columns"] = req.Rules.Explain(sc, req.Table)
	rows := req.Rows
	if rows <= 0 || rows > 20 {
		rows = 5
	}
	overrides, err := req.Rules.Compile(sc, "preview")
	if err != nil {
		resp["sampleError"] = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	gen := faker.DefaultGenerateOptions()
	gen.Overrides = overrides
	data, err := seeder.Preview(sess.Conn(), sess.DBType, sc, []string{req.Table}, map[string]int{req.Table: rows}, rows, gen)
	if err != nil {
		resp["sampleError"] = err.Error()
	} else {
		resp["samples"] = stringifyRows(data[req.Table])
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleProfileExample previews one action on one column of the active schema:
// POST {action, table, column} -> {values} or {error}. It powers the live
// example in the column rule dialog.
func (s *Server) handleProfileExample(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	sess, err := s.sessions.fromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	var req struct {
		Action rules.Action `json:"action"`
		Table  string       `json:"table"`
		Column string       `json:"column"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sc, err := sess.Schema(false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	col, ok := sc.Tables[req.Table].Columns[req.Column]
	if !ok {
		writeError(w, http.StatusNotFound, "column not found")
		return
	}
	values, err := rules.ExampleValues(req.Action, col, req.Table, req.Column, 3)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"values": values})
}

// ruleCoverage reports, per pattern rule, how many columns it currently
// rewrites, so the builder can flag rules that match nothing.
func ruleCoverage(rs *rules.RuleSet, sc *schema.Schema) map[string]any {
	perRule := make([]int, len(rs.Rules))
	columns := 0
	for tableName := range sc.Tables {
		for _, plan := range rs.Explain(sc, tableName) {
			switch plan.Source.Kind {
			case "pattern":
				perRule[plan.Source.Index]++
				columns++
			case "column":
				columns++
			}
		}
	}
	return map[string]any{"rules": perRule, "columns": columns}
}

func stringifyRows(rows []map[string]interface{}) []map[string]string {
	out := make([]map[string]string, 0, len(rows))
	for _, row := range rows {
		m := make(map[string]string, len(row))
		for k, v := range row {
			m[k] = previewValue(v)
		}
		out = append(out, m)
	}
	return out
}

func nonNilIssues(issues []rules.Issue) []rules.Issue {
	if issues == nil {
		return []rules.Issue{}
	}
	return issues
}

// profileByID loads a saved profile for a run; empty id means no profile.
// handleProfileIgnored lists the tables a saved profile ignores in the active
// connection's schema, with the glob that matched each.
//
//	GET /api/profiles/ignored?id=<profile>
func (s *Server) handleProfileIgnored(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	sess, err := s.sessions.fromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	rs, err := s.profileByID(r.URL.Query().Get("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	sc, err := sess.Schema(false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := map[string]any{"ignored": []rules.IgnoredTable{}, "tableRows": map[string]int{}}
	if rs != nil {
		resp["ignored"] = nonNilIgnored(rs.IgnoredTables(sc))
		if rows := rules.MergeTableRowsFor(rs, sc, nil); rows != nil {
			resp["tableRows"] = rows
		}
		resp["name"] = rs.Name
	}
	writeJSON(w, http.StatusOK, resp)
}

func nonNilIgnored(list []rules.IgnoredTable) []rules.IgnoredTable {
	if list == nil {
		return []rules.IgnoredTable{}
	}
	return list
}

func (s *Server) profileByID(id string) (*rules.RuleSet, error) {
	if strings.TrimSpace(id) == "" {
		return nil, nil
	}
	p, err := s.profiles.Get(id)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", id, err)
	}
	rs := p.Rules
	return &rs, nil
}

func decodeBody(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxProfileBody))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

// handleProfileRelationships turns a counts file with relationships (snapshot
// version 2) into profile relationships:
// POST {data} -> {relationships, skipped}.
func (s *Server) handleProfileRelationships(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req struct {
		Data string `json:"data"`
	}
	if err := decodeLimited(w, r, &req, maxSnapshotBody); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snap, err := compare.ParseSnapshot([]byte(req.Data))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(snap.Relationships) == 0 {
		writeError(w, http.StatusBadRequest, "the file has no relationships: take it with Analyze relationships or snapshot --relationships")
		return
	}
	rels, skipped := rules.RelationshipsFromShapes(snap.Relationships)
	if skipped == nil {
		skipped = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"relationships": rels, "skipped": skipped})
}
