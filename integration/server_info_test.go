//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/db"
)

// Tuning reads what a database says about its capacity: connection limit and
// use, reserved buffers, the database's size, version, replica or not.
func TestDetectServer_ReadsCapacityOnBothEngines(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			_, conn := e.scratchDB(t, "ss_server_info")
			execSQL(t, conn, `CREATE TABLE sized (id INT PRIMARY KEY, body VARCHAR(200))`)
			for i := 0; i < 50; i++ {
				execSQL(t, conn, fmt.Sprintf(`INSERT INTO sized (id, body) VALUES (%d, 'row')`, i))
			}
			info, err := db.DetectServer(context.Background(), conn, e.driver)
			if err != nil {
				t.Fatal(err)
			}
			if info.MaxConnections <= 0 || info.UsedConnections <= 0 || info.UsedConnections > info.MaxConnections {
				t.Fatalf("connections %d/%d", info.UsedConnections, info.MaxConnections)
			}
			if info.UsedBytes <= 0 || info.Version == "" || info.ServerID == "" {
				t.Fatalf("info = %+v", info)
			}
			if info.Replica || info.WritesBlocked {
				t.Fatalf("a primary reported as read-only: %+v", info)
			}
			switch e.driver {
			case postgresDriver:
				if info.SharedBuffersBytes <= 0 {
					t.Fatalf("shared_buffers = %d", info.SharedBuffersBytes)
				}
			default:
				if info.BufferPoolBytes <= 0 || info.MaxAllowedPacket <= 0 {
					t.Fatalf("mysql buffers = %+v", info)
				}
			}
			// Two databases on the same server share the server id.
			_, other := e.scratchDB(t, "ss_server_info_other")
			otherInfo, err := db.DetectServer(context.Background(), other, e.driver)
			if err != nil {
				t.Fatal(err)
			}
			if otherInfo.ServerID != info.ServerID || strings.Contains(info.ServerID, "ss_server_info") {
				t.Fatalf("server ids %q vs %q: must be equal and not name the database", info.ServerID, otherInfo.ServerID)
			}
		})
	}
}
