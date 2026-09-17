package web

import (
	"database/sql"
	"sync"
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

// Pages load the schema at the same time (graph, counts, access). They share
// one introspection instead of each running one against the database.
func TestSession_ConcurrentSchemaCallsShareOneIntrospection(t *testing.T) {
	registerServeRunnerTestDriver()
	conn, err := sql.Open(serveRunnerTestDriverName, "counted")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	countedQueries.Store(0)
	one := &Session{DBType: "pgx", conn: conn}
	if _, err := one.Schema(false); err != nil {
		t.Fatal(err)
	}
	perIntrospection := countedQueries.Load()
	if perIntrospection == 0 {
		t.Fatal("introspection ran no queries on the session connection")
	}

	countedQueries.Store(0)
	shared := &Session{DBType: "pgx", conn: conn}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := shared.Schema(false); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := countedQueries.Load(); got != perIntrospection {
		t.Fatalf("12 concurrent calls ran %d queries, want one introspection's %d", got, perIntrospection)
	}
}
