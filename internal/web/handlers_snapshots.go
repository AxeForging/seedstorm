package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/runerr"
)

// maxSnapshotBody bounds an imported counts document (a snapshot of tens of
// thousands of tables is still well under this).
const maxSnapshotBody = 8 << 20

// handleSnapshotEncode renders a counts file: one side of a comparison, or a
// snapshot taken from one connection.
//
//	POST {report, side: source|target, format} | {snapshot, format} -> {content, filename, tables}
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
		Report   compare.Report    `json:"report"`
		Side     string            `json:"side"`
		Snapshot *compare.Snapshot `json:"snapshot"`
		Format   string            `json:"format"`
		// Relationships keeps the shapes the report or snapshot holds;
		// without it the file has counts only.
		Relationships bool `json:"relationships"`
	}
	if err := decodeLimited(w, r, &req, maxSnapshotBody); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var snap compare.Snapshot
	if req.Snapshot != nil {
		snap = *req.Snapshot
		req.Side = ""
	} else {
		var err error
		if snap, err = compare.SnapshotFromReport(req.Report, req.Side); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if !req.Relationships {
		snap.Relationships = nil
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	data, err := compare.EncodeSnapshot(snap, format)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"content":       string(data),
		"filename":      snapshotFilename(snap.Label, req.Side, format),
		"tables":        len(snap.Tables),
		"relationships": len(snap.Relationships),
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
	if side == "" {
		return name + "-counts." + format
	}
	return name + "-" + side + "-counts." + format
}

// SnapshotRequest takes the active connection's counts.
type SnapshotRequest struct {
	Counts string `json:"counts"`
}

func (s *Server) handleSnapshotRun(w http.ResponseWriter, r *http.Request) {
	startRun(s, w, r, "snapshot", s.runSnapshot)
}

// runSnapshot reads every table's row count, size and columns on the active
// connection (read-only), with per-table progress.
func (s *Server) runSnapshot(ctx context.Context, sess *Session, req SnapshotRequest, jc JobControl) (map[string]any, error) {
	log := jobLogger(jc)
	mode, err := compare.ParseCountMode(req.Counts)
	if err != nil {
		return nil, err
	}
	jc.Phase("count")
	label := sessionLabel(sess)
	log.Info().Str("database", label).Str("counts", string(mode)).Msg("Reading table counts")
	snap, err := compare.Take(ctx, sess.Conn(), sess.DBType, label, mode, func(done, total int, table string) {
		jc.Progress(done, total, table)
	})
	if err != nil {
		return nil, runerr.At(runerr.PhaseCount, "", err)
	}
	unknown := 0
	for _, st := range snap.Tables {
		if st.Rows < 0 {
			unknown++
		}
	}
	jc.Phase("done")
	log.Info().Int("tables", len(snap.Tables)).Int("unknown", unknown).Msg("Snapshot taken")
	return map[string]any{"snapshot": snap, "tables": len(snap.Tables), "unknown": unknown}, nil
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
