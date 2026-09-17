package seeder

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/db"
)

func stubServers(t *testing.T, byConn map[*sql.DB]db.ServerInfo) {
	t.Helper()
	prev := detectServer
	detectServer = func(_ context.Context, conn *sql.DB, _ string) (db.ServerInfo, error) {
		info, ok := byConn[conn]
		if !ok {
			return db.ServerInfo{}, errors.New("permission denied")
		}
		return info, nil
	}
	t.Cleanup(func() { detectServer = prev })
}

func TestRelateServers_SharedServerReplicasAndUnknown(t *testing.T) {
	a, b, c, dead := &sql.DB{}, &sql.DB{}, &sql.DB{}, &sql.DB{}
	stubServers(t, map[*sql.DB]db.ServerInfo{
		a: {Engine: "postgres", ServerID: "boot1/5432"},
		b: {Engine: "postgres", ServerID: "boot1/5432"},
		c: {Engine: "postgres", ServerID: "boot2/5432", Replica: true, WritesBlocked: true},
	})
	ctx := context.Background()

	shared := RelateServers(ctx, Endpoint{Conn: a}, Endpoint{Conn: b})
	if !shared.Known || !shared.SharedServer || len(shared.Notices()) != 1 || !strings.Contains(shared.Notices()[0], "same server") {
		t.Fatalf("shared = %+v %v", shared, shared.Notices())
	}
	fromReplica := RelateServers(ctx, Endpoint{Conn: c}, Endpoint{Conn: a})
	if fromReplica.SharedServer || !fromReplica.SourceReplica || fromReplica.TargetReplica {
		t.Fatalf("from replica = %+v", fromReplica)
	}
	if r := RelateServers(ctx, Endpoint{Conn: a}, Endpoint{Conn: dead}); r.Known || r.SharedServer {
		t.Fatalf("an unreadable server must leave the relation unknown: %+v", r)
	}
	snap := compare.Snapshot{Tables: map[string]compare.TableStat{"t": {Rows: 1}}}
	if r := RelateServers(ctx, Endpoint{Snapshot: &snap}, Endpoint{Conn: a}); r.Known {
		t.Fatalf("a snapshot side has no server: %+v", r)
	}

	if n := ScanNotice(ctx, Endpoint{Conn: c, Label: "replica-1"}); !strings.Contains(n, "replica-1 is a read replica") {
		t.Fatalf("replica notice = %q", n)
	}
	if n := ScanNotice(ctx, Endpoint{Conn: dead, Label: "x"}); !strings.Contains(n, "could not tell") {
		t.Fatalf("unknown notice = %q", n)
	}
}

// Writing to a read-only standby is refused before anything is counted or planned.
func TestPrepareMirror_RefusesAReplicaTarget(t *testing.T) {
	src, tgt := &sql.DB{}, &sql.DB{}
	stubServers(t, map[*sql.DB]db.ServerInfo{
		src: {Engine: "postgres", ServerID: "p"},
		tgt: {Engine: "postgres", ServerID: "r", Replica: true, WritesBlocked: true},
	})
	prevIdentity := databaseIdentity
	databaseIdentity = func(_ context.Context, conn *sql.DB, _ string) (string, error) {
		if conn == src {
			return "src", nil
		}
		return "tgt", nil
	}
	t.Cleanup(func() { databaseIdentity = prevIdentity })
	_, err := PrepareMirror(context.Background(), Endpoint{Conn: src, DBType: "pgx"}, Endpoint{Conn: tgt, DBType: "pgx"}, MirrorConfig{})
	if !errors.Is(err, ErrTargetReplica) {
		t.Fatalf("err = %v, want ErrTargetReplica", err)
	}
}

// MySQL's read_only does not stop a user with SUPER, so it is a warning, not a
// refusal; only super_read_only (or a Postgres standby) blocks every write.
func TestRelateServers_MySQLReadOnlyWarnsButSuperReadOnlyRefuses(t *testing.T) {
	src, readOnly, superReadOnly := &sql.DB{}, &sql.DB{}, &sql.DB{}
	stubServers(t, map[*sql.DB]db.ServerInfo{
		src:           {Engine: "mysql", ServerID: "a"},
		readOnly:      {Engine: "mysql", ServerID: "b", Replica: true},
		superReadOnly: {Engine: "mysql", ServerID: "c", Replica: true, WritesBlocked: true},
	})
	prevIdentity := databaseIdentity
	databaseIdentity = func(_ context.Context, conn *sql.DB, _ string) (string, error) { return fmt.Sprintf("%p", conn), nil }
	t.Cleanup(func() { databaseIdentity = prevIdentity })

	r := RelateServers(context.Background(), Endpoint{Conn: src}, Endpoint{Conn: readOnly})
	if r.TargetWritesBlocked {
		t.Fatalf("read_only alone must not block: %+v", r)
	}
	notices := strings.Join(r.Notices(), "; ")
	if !strings.Contains(notices, "read_only") {
		t.Fatalf("no warning about a read_only target: %q", notices)
	}
	if _, err := PrepareMirror(context.Background(), Endpoint{Conn: src, DBType: "mysql"}, Endpoint{Conn: readOnly, DBType: "mysql"}, MirrorConfig{}); errors.Is(err, ErrTargetReplica) {
		t.Fatal("a read_only MySQL target was refused; a user with SUPER can still write")
	}
	_, err := PrepareMirror(context.Background(), Endpoint{Conn: src, DBType: "mysql"}, Endpoint{Conn: superReadOnly, DBType: "mysql"}, MirrorConfig{})
	if !errors.Is(err, ErrTargetReplica) {
		t.Fatalf("super_read_only target = %v, want ErrTargetReplica", err)
	}
}
