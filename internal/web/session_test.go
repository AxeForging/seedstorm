package web

import (
	"testing"
	"time"
)

func TestSessionRegistryOpenDSNReusesExactExistingSession(t *testing.T) {
	r := NewSessionRegistry()
	existing := &Session{
		ID:     "existing",
		DBType: "pgx",
		DSN:    "postgres://seedstorm:secret@localhost:5432/clonedb?sslmode=disable",
		Info:   ConnectionInfo{Label: "target-db", DBType: "postgres", Host: "localhost", Port: 5432, DBName: "clonedb", User: "seedstorm"},
	}
	r.sessions[existing.ID] = existing

	got, err := r.OpenDSN(existing.DBType, existing.DSN, existing.Info)
	if err != nil {
		t.Fatalf("OpenDSN: %v", err)
	}
	if got != existing {
		t.Fatalf("OpenDSN returned a new session, want existing")
	}
	if len(r.sessions) != 1 {
		t.Fatalf("session count = %d, want 1", len(r.sessions))
	}
}

// A connection left open in the browser must not hold database connections
// while nobody uses it; the pool reopens one on the next request.
func TestSessionRegistryOpenReleasesIdleDatabaseConnections(t *testing.T) {
	defer func(old time.Duration) { sessionConnMaxIdle = old }(sessionConnMaxIdle)
	sessionConnMaxIdle = 10 * time.Millisecond
	stubSQL(t, nil)
	r := NewSessionRegistry()
	sess, err := r.OpenDSN("pgx", "postgres://idle", ConnectionInfo{DBType: "postgres"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close(sess.ID)

	deadline := time.Now().Add(5 * time.Second)
	for sess.Conn().Stats().OpenConnections > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("idle session still holds %d connection(s)", sess.Conn().Stats().OpenConnections)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := sess.Conn().Ping(); err != nil {
		t.Fatalf("the session must reconnect on use: %v", err)
	}
}
