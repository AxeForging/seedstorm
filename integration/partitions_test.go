//go:build integration

package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/compare"
)

// Postgres partitioned tables were listed together with each of their
// partitions, so counts doubled, and seeding generated partition-key values
// outside every partition ("no partition of relation found for row").
func TestPartitionedTables_CountedOnceAndSeededInsideTheirBounds(t *testing.T) {
	e := postgresEngine()
	dsn, conn := e.scratchDB(t, "ss_partitions")
	execSQL(t, conn, `
		CREATE TABLE customers (id SERIAL PRIMARY KEY, name TEXT NOT NULL);
		CREATE TABLE events (id BIGINT NOT NULL, customer_id INT NOT NULL REFERENCES customers (id),
			created_at DATE NOT NULL, kind TEXT NOT NULL, PRIMARY KEY (id, created_at)) PARTITION BY RANGE (created_at);
		CREATE TABLE events_2025 PARTITION OF events FOR VALUES FROM ('2025-01-01') TO ('2026-01-01');
		CREATE TABLE events_2026 PARTITION OF events FOR VALUES FROM ('2026-01-01') TO ('2027-01-01');
		CREATE TABLE tickets (id INT NOT NULL, region TEXT NOT NULL, PRIMARY KEY (id, region)) PARTITION BY LIST (region);
		CREATE TABLE tickets_eu PARTITION OF tickets FOR VALUES IN ('eu-west', 'eu-north');
		CREATE TABLE tickets_us PARTITION OF tickets FOR VALUES IN ('us-east');
		CREATE TABLE scores (id INT NOT NULL, bucket INT NOT NULL, PRIMARY KEY (id, bucket)) PARTITION BY RANGE (bucket);
		CREATE TABLE scores_low PARTITION OF scores FOR VALUES FROM (0) TO (100);
		CREATE TABLE scores_rest PARTITION OF scores DEFAULT;
		CREATE TABLE shards (id INT PRIMARY KEY, body TEXT) PARTITION BY HASH (id);
		CREATE TABLE shards_0 PARTITION OF shards FOR VALUES WITH (modulus 2, remainder 0);
		CREATE TABLE shards_1 PARTITION OF shards FOR VALUES WITH (modulus 2, remainder 1)`)

	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.yaml")
	runBin(t, "introspect", "--db", "postgres", "--dsn", dsn, "--out", schemaPath)
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, partition := range []string{"events_2025", "events_2026", "tickets_eu", "tickets_us", "scores_low", "scores_rest", "shards_0", "shards_1"} {
		if strings.Contains(string(raw), "\n  "+partition+":") {
			t.Errorf("partition %s is introspected as its own table", partition)
		}
	}

	runBin(t, "seed", "--db", "postgres", "--dsn", dsn, "--schema", schemaPath, "--rows", "60", "--workers", "1")
	for _, table := range []string{"events", "tickets", "scores", "shards"} {
		if n := countRows(t, conn, table); n < 60 {
			t.Errorf("%s has %d rows after seeding 60", table, n)
		}
	}
	if n := scalar(t, conn, `SELECT COUNT(*) FROM events_2025`) + scalar(t, conn, `SELECT COUNT(*) FROM events_2026`); n != countRows(t, conn, "events") {
		t.Errorf("events rows outside its partitions: partitions hold %d of %d", n, countRows(t, conn, "events"))
	}

	snap := decodeJSON[compare.Snapshot](t, runBin(t, "snapshot", "--db", "postgres", "--dsn", dsn, "--format", "json"))
	for _, partition := range []string{"events_2025", "tickets_eu", "shards_0"} {
		if _, ok := snap.Tables[partition]; ok {
			t.Errorf("snapshot lists partition %s: parent rows would be counted twice", partition)
		}
	}
	if got, want := snap.Tables["events"].Rows, int64(countRows(t, conn, "events")); got != want {
		t.Errorf("snapshot events rows = %d, want %d", got, want)
	}
	if snap.Tables["events"].Bytes <= 0 {
		t.Errorf("snapshot events bytes = %d, want the size of its partitions", snap.Tables["events"].Bytes)
	}
}

// A partition key seedstorm cannot generate inside the bounds (an expression)
// is refused before anything is written, with a way out.
func TestPartitionedTables_ExpressionKeyIsRefusedBeforeWriting(t *testing.T) {
	e := postgresEngine()
	dsn, conn := e.scratchDB(t, "ss_partitions_expr")
	execSQL(t, conn, `
		CREATE TABLE plain (id INT PRIMARY KEY);
		INSERT INTO plain VALUES (1), (2), (3);
		CREATE TABLE logs (id INT NOT NULL, created_at TIMESTAMP NOT NULL) PARTITION BY RANGE (date_trunc('month', created_at));
		CREATE TABLE logs_jan PARTITION OF logs FOR VALUES FROM ('2026-01-01') TO ('2026-02-01')`)
	schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
	runBin(t, "introspect", "--db", "postgres", "--dsn", dsn, "--out", schemaPath)
	_, stderr, err := runBinResult(t, "seed", "--db", "postgres", "--dsn", dsn, "--schema", schemaPath, "--rows", "5", "--workers", "1", "--truncate", "--yes")
	if err == nil {
		t.Fatal("seed succeeded on a table partitioned by an expression")
	}
	if !strings.Contains(stderr, "logs") || !strings.Contains(stderr, "partitioned by an expression") || !strings.Contains(stderr, "value rule") {
		t.Fatalf("stderr does not explain the refusal:\n%s", stderr)
	}
	if n := countRows(t, conn, "plain"); n != 3 {
		t.Fatalf("plain has %d rows, want its 3: the run truncated or wrote before refusing", n)
	}
}
