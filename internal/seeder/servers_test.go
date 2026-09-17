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
	"github.com/AxeForging/seedstorm/internal/relations"
	"github.com/AxeForging/seedstorm/internal/schema"
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

// Shapes measured on a source do not all apply: junction keys and key columns
// are not shaped in v1. They come back named with a reason instead of being
// quietly seeded evenly. The wiring into a run is covered by
// TestShapedSeed_MirrorNamesEveryKeyItCannotShape.
func TestShapesForSchema_ReportsWhatItCannotApply(t *testing.T) {
	sc := &schema.Schema{Tables: map[string]schema.Table{
		"users":  {Columns: map[string]schema.Column{"id": {Type: "int", PK: true}}},
		"orders": {Columns: map[string]schema.Column{"id": {Type: "int", PK: true}, "user_id": {Type: "int", FK: "users.id"}}},
		"user_roles": {Columns: map[string]schema.Column{
			"user_id": {Type: "int", PK: true, FK: "users.id"},
			"role_id": {Type: "int", PK: true, FK: "roles.id"},
		}},
		"roles": {Columns: map[string]schema.Column{"id": {Type: "int", PK: true}}},
	}}
	shapes := []relations.Shape{
		{Child: "orders", Column: "user_id", Parent: "users", Min: 1, Avg: 2, Max: 5, Outcome: db.OutcomeOK},
		{Child: "user_roles", Column: "user_id", Parent: "users", Min: 1, Avg: 2, Max: 5, Outcome: db.OutcomeOK},
	}
	applied, skipped := shapesForSchema(sc, shapes)
	if len(applied) != 1 || applied["orders.user_id"].Max != 5 {
		t.Fatalf("applied = %v", applied)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "user_roles.user_id") || !strings.Contains(skipped[0], "junction") {
		t.Fatalf("skipped = %v, want user_roles.user_id with its reason", skipped)
	}
}

// Every measured shape must come back either applied or reported: a key that
// falls out of both lists is seeded evenly with nobody told (Keycloak: 38 of
// 67 keys were silently dropped this way).
func TestShapesForSchema_EveryMeasuredShapeIsAppliedOrReported(t *testing.T) {
	sc := &schema.Schema{Tables: map[string]schema.Table{
		"users":  {Columns: map[string]schema.Column{"id": {Type: "int", PK: true}}},
		"roles":  {Columns: map[string]schema.Column{"id": {Type: "int", PK: true}}},
		"orders": {Columns: map[string]schema.Column{"id": {Type: "int", PK: true}, "user_id": {Type: "int", FK: "users.id"}}},
		"folders": {Columns: map[string]schema.Column{
			"id":        {Type: "int", PK: true},
			"parent_id": {Type: "int", FK: "folders.id", Nullable: true},
		}},
		"user_roles": {Columns: map[string]schema.Column{
			"user_id": {Type: "int", PK: true, FK: "users.id"},
			"role_id": {Type: "int", PK: true, FK: "roles.id"},
		}},
		"daily": {Columns: map[string]schema.Column{
			"user_id": {Type: "int", PK: true, FK: "users.id"},
			"day":     {Type: "date", PK: true},
		}},
	}}
	measured := []relations.Shape{
		{Child: "orders", Column: "user_id", Parent: "users", Min: 1, Avg: 2, Max: 5, Outcome: db.OutcomeOK},
		{Child: "folders", Column: "parent_id", Parent: "folders", Min: 1, Avg: 2, Max: 3, SelfRef: true, Outcome: db.OutcomeOK},
		{Child: "user_roles", Column: "user_id", Parent: "users", Min: 1, Avg: 2, Max: 4, Outcome: db.OutcomeOK},
		{Child: "daily", Column: "user_id", Parent: "users", Min: 1, Avg: 3, Max: 9, Outcome: db.OutcomeOK},
		{Child: "gone", Column: "user_id", Parent: "users", Min: 1, Avg: 2, Max: 4, Outcome: db.OutcomeOK},
		{Child: "orders", Column: "coupon_id", Parent: "coupons", Avg: 2, Max: -1, Outcome: relations.OutcomeEstimated},
		{Child: "logs", Column: "user_id", Parent: "users", Outcome: db.OutcomeTimedOut},
	}
	applied, skipped := shapesForSchema(sc, measured)

	seen := map[string]int{}
	for key := range applied {
		seen[strings.ToLower(key)]++
	}
	for _, line := range skipped {
		key, reason, found := strings.Cut(line, ": ")
		if !found || reason == "" {
			t.Errorf("skipped line %q does not give a reason", line)
		}
		seen[strings.ToLower(key)]++
	}
	for _, m := range measured {
		key := strings.ToLower(m.Child + "." + m.Column)
		if seen[key] != 1 {
			t.Errorf("%s appears %d times in applied+skipped, want exactly 1", key, seen[key])
		}
	}
	if len(applied) != 1 || applied["orders.user_id"].Max != 5 {
		t.Errorf("applied = %v, want only the shapeable key", applied)
	}
	reasons := strings.Join(skipped, "\n")
	for _, want := range []string{"folders.parent_id: self-references", "user_roles.user_id: junction keys", "daily.user_id: key columns", "gone.user_id: gone.user_id is not in this database", "orders.coupon_id: not measured", "logs.user_id: not measured"} {
		if !strings.Contains(reasons, want) {
			t.Errorf("reports lack %q:\n%s", want, reasons)
		}
	}
}
