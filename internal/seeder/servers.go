package seeder

import (
	"context"
	"errors"
	"fmt"

	"github.com/AxeForging/seedstorm/internal/db"
)

// ErrTargetReplica: the target refuses every write (a Postgres standby, or
// MySQL super_read_only).
var ErrTargetReplica = errors.New("the target is a read-only replica: point the mirror at the primary")

// detectServer and databaseIdentity are the db reads, replaceable in tests.
var (
	detectServer     = db.DetectServer
	databaseIdentity = db.Identity
)

// ServerRelation is what the two servers of a run say about each other. Known
// is false when a server could not be read (the checks are then skipped).
type ServerRelation struct {
	Known         bool
	SharedServer  bool
	SourceReplica bool
	TargetReplica bool
	// TargetWritesBlocked means no user can write there, so a mirror is refused.
	// A MySQL target with read_only but not super_read_only only warns: a user
	// with SUPER writes to it.
	TargetWritesBlocked bool
}

// RelateServers reads both live endpoints' servers. Snapshot endpoints and
// unreadable servers leave the relation unknown instead of failing.
func RelateServers(ctx context.Context, source, target Endpoint) ServerRelation {
	if source.Conn == nil || target.Conn == nil {
		return ServerRelation{}
	}
	src, err := detectServer(ctx, source.Conn, source.DBType)
	if err != nil {
		return ServerRelation{}
	}
	tgt, err := detectServer(ctx, target.Conn, target.DBType)
	if err != nil {
		return ServerRelation{}
	}
	return ServerRelation{
		Known:               true,
		SharedServer:        src.Engine == tgt.Engine && src.ServerID != "" && src.ServerID == tgt.ServerID,
		SourceReplica:       src.Replica,
		TargetReplica:       tgt.Replica,
		TargetWritesBlocked: tgt.WritesBlocked,
	}
}

// Notices explains the relation for logs and the plan view.
func (r ServerRelation) Notices() []string {
	var out []string
	if r.SharedServer {
		out = append(out, "source and target are databases on the same server: reading the source and writing the target share its CPU, memory and disk")
	}
	if r.TargetReplica && !r.TargetWritesBlocked {
		out = append(out, "the target has read_only set: only a user with SUPER can write to it, and it may be a replica")
	}
	if r.SourceReplica {
		out = append(out, "the source is a read replica: reads there do not load the primary")
	}
	return out
}

// ScanNotice describes the server a read-only scan runs on.
func ScanNotice(ctx context.Context, ep Endpoint) string {
	if ep.Conn == nil {
		return ""
	}
	info, err := detectServer(ctx, ep.Conn, ep.DBType)
	switch {
	case err != nil:
		return fmt.Sprintf("could not tell whether %s is a replica or a primary: %v", ep.Label, err)
	case info.Replica:
		return fmt.Sprintf("%s is a read replica: the scan does not load the primary", ep.Label)
	default:
		return fmt.Sprintf("%s is a primary: prefer a read replica for large scans", ep.Label)
	}
}
