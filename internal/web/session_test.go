package web

import "testing"

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
