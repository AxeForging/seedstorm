//go:build integration

package integration_test

import (
	"context"
	"testing"

	"github.com/AxeForging/seedstorm/internal/seeder"
)

// Two databases on one server are marked as sharing it; neither compose
// server is a replica.
func TestServers_TwoDatabasesOnOneServerAreShared(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			_, a := e.scratchDB(t, "ss_servers_a")
			_, b := e.scratchDB(t, "ss_servers_b")
			r := seeder.RelateServers(context.Background(), seeder.Endpoint{Conn: a, DBType: e.driver}, seeder.Endpoint{Conn: b, DBType: e.driver})
			if !r.Known || !r.SharedServer || r.SourceReplica || r.TargetReplica {
				t.Fatalf("relation = %+v", r)
			}
		})
	}
}
