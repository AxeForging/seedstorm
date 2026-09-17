package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/db"
)

// The Recommend dialog sends what SQL cannot see (size, disk) and gets values
// with a reason each, the detected server, the disk growth check, and the
// caveat that numbers depend on the database's load.
func TestTuning_RecommendsFromDetectedAndEnteredCapacity(t *testing.T) {
	defer func(old func(context.Context, *sql.DB, string) (db.ServerInfo, error)) { detectServer = old }(detectServer)
	detectServer = func(context.Context, *sql.DB, string) (db.ServerInfo, error) {
		return db.ServerInfo{
			Engine: "mysql", Version: "8.4.10", MaxConnections: 280, UsedConnections: 3,
			BufferPoolBytes: 53477376, LogBufferBytes: 67108864, MaxAllowedPacket: 33554432, UsedBytes: 200 << 20, ServerID: "u1",
		}, nil
	}
	s, err := New(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: "t", DBType: "mysql", schema: runnerRowCountSchema()}
	s.sessions.add(sess)

	r := httptest.NewRequest(http.MethodGet, "/api/tuning?vcpu=1&memoryMB=629&storage=network-ssd&storageGB=10&iops=300&rows=2000&tables=2", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Server         db.ServerInfo `json:"server"`
		Recommendation struct {
			Writers    int      `json:"writers"`
			Generators int      `json:"generators"`
			Reasons    []string `json:"reasons"`
			Growth     struct {
				Status string `json:"status"`
			} `json:"growth"`
		} `json:"recommendation"`
		Caveat string `json:"caveat"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	rec := body.Recommendation
	if rec.Writers != 2 || rec.Generators != 1 || len(rec.Reasons) < 2 {
		t.Fatalf("recommendation = %+v", rec)
	}
	if body.Server.MaxConnections != 280 || rec.Growth.Status != "ok" {
		t.Fatalf("server = %+v, growth = %+v", body.Server, rec.Growth)
	}
	if !strings.Contains(body.Caveat, "depend") {
		t.Fatalf("caveat = %q", body.Caveat)
	}
}
