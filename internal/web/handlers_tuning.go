package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/AxeForging/seedstorm/internal/tuning"
)

// detectServer reads the active database's capacity; a variable so tests can
// stand in for a server.
var detectServer = db.DetectServer

// tuningCaveat is shown with every recommendation.
const tuningCaveat = "Estimates from benchmarks on one machine: real numbers depend on the database's load and network. Watch the rate during the first minute and adjust."

// handleTuning recommends writers and generators for the active connection:
//
//	GET /api/tuning?vcpu&memoryMB&storage&storageGB&iops&shared&ha&rows&tables
//
// What SQL cannot see (vCPU, memory, disk) comes from the query; connections,
// buffers and used space are detected.
func (s *Server) handleTuning(w http.ResponseWriter, r *http.Request) {
	sess, err := s.sessions.fromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	q := r.URL.Query()
	num := func(key string) float64 {
		v, _ := strconv.ParseFloat(strings.TrimSpace(q.Get(key)), 64)
		return max(v, 0)
	}
	info, detectErr := detectServer(r.Context(), sess.Conn(), sess.DBType)
	_, production := s.productionConnection(sessionTarget(sess))
	target := tuning.Database{
		Engine: map[string]string{"pgx": "postgres", "mysql": "mysql"}[sess.DBType],
		VCPU:   num("vcpu"), MemoryMB: int(num("memoryMB")), Storage: tuning.StorageType(q.Get("storage")),
		StorageGB: int(num("storageGB")), IOPS: int(num("iops")),
		Shared: isChecked(q.Get("shared")), HA: isChecked(q.Get("ha")), Production: production,
		MaxConnections: info.MaxConnections, UsedConnections: info.UsedConnections,
		BufferPoolBytes: info.BufferPoolBytes, LogBufferBytes: info.LogBufferBytes, SharedBuffersBytes: info.SharedBuffersBytes,
		MaxAllowedPacket: info.MaxAllowedPacket, UsedBytes: info.UsedBytes,
	}
	sc, _ := sess.Schema(false)
	rows := int64(num("rows"))
	tables := int64(max(num("tables"), 1))
	run := tuning.Run{Rows: rows * tables, AvgRowBytes: averageRowBytes(sc), IndexFactor: 0.5}
	host := tuning.DetectHost()
	out := map[string]any{
		"host":           host,
		"server":         info,
		"production":     production,
		"recommendation": tuning.Recommend(host, target, run),
		"caveat":         tuningCaveat,
	}
	if detectErr != nil {
		out["detectError"] = detectErr.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

// averageRowBytes estimates a row's stored size from column types, for the
// disk growth check. Unknown shapes count as a modest text value.
func averageRowBytes(sc *schema.Schema) int {
	if sc == nil || len(sc.Tables) == 0 {
		return 256
	}
	total, tables := 0, 0
	for _, t := range sc.Tables {
		row := 24 // tuple header
		for _, c := range t.Columns {
			row += columnBytes(strings.ToLower(c.Type))
		}
		total += row
		tables++
	}
	return total / tables
}

func columnBytes(t string) int {
	switch {
	case strings.Contains(t, "bigint"), strings.Contains(t, "double"), strings.Contains(t, "timestamp"), strings.Contains(t, "datetime"):
		return 8
	case strings.Contains(t, "int"), strings.Contains(t, "date"), strings.Contains(t, "float"), strings.Contains(t, "real"):
		return 4
	case strings.Contains(t, "bool"):
		return 1
	case strings.Contains(t, "uuid"):
		return 16
	case strings.Contains(t, "json"), strings.Contains(t, "text"), strings.Contains(t, "blob"):
		return 256
	default:
		return 32
	}
}
