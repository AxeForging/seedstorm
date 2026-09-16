package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/AxeForging/seedstorm/internal/compare"
)

// maxSnapshotBody bounds an imported counts document (a snapshot of tens of
// thousands of tables is still well under this).
const maxSnapshotBody = 8 << 20

// handleSnapshotEncode renders one side of a comparison as a counts file.
//
//	POST {report, side: source|target, format: json|yaml} -> {content, filename, tables}
func (s *Server) handleSnapshotEncode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if _, err := s.sessions.fromRequest(r); err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	var req struct {
		Report compare.Report `json:"report"`
		Side   string         `json:"side"`
		Format string         `json:"format"`
	}
	if err := decodeLimited(w, r, &req, maxSnapshotBody); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snap, err := compare.SnapshotFromReport(req.Report, req.Side)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	data, err := compare.EncodeSnapshot(snap, format)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"content":  string(data),
		"filename": snapshotFilename(snap.Label, req.Side, format),
		"tables":   len(snap.Tables),
	})
}

// handleSnapshotParse validates a pasted or uploaded counts file.
//
//	POST {data} -> {snapshot, tables, rows}
func (s *Server) handleSnapshotParse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if _, err := s.sessions.fromRequest(r); err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
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
	var rows int64
	for _, t := range snap.Tables {
		if t.Rows > 0 {
			rows += t.Rows
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshot": snap, "tables": len(snap.Tables), "rows": rows})
}

// snapshotFilename suggests "prod-source-counts.yaml" from a label like "prod@db:5432".
func snapshotFilename(label, side, format string) string {
	base := label
	if at := strings.Index(base, "@"); at > 0 {
		base = base[:at]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(base) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "database"
	}
	if format == "yml" {
		format = compare.FormatYAML
	}
	return name + "-" + side + "-counts." + format
}

// decodeLimited decodes a JSON body of at most limit bytes.
func decodeLimited(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return fmt.Errorf("request larger than %dMB", limit>>20)
		}
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}
