//go:build integration && loadsim

package integration_test

import (
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var (
	staticBinOnce sync.Once
	staticBinPath string
	staticBinErr  error
)

// seedstormStaticBin builds a CGO-free binary that runs inside a plain Alpine
// container.
func seedstormStaticBin(t *testing.T) string {
	t.Helper()
	staticBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "seedstorm-static-")
		if err != nil {
			staticBinErr = err
			return
		}
		staticBinPath = filepath.Join(dir, "seedstorm")
		cmd := exec.Command("go", "build", "-o", staticBinPath, "../cmd/seedstorm")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			staticBinErr = errors.New(string(out))
		}
	})
	if staticBinErr != nil {
		t.Fatalf("build static binary: %v", staticBinErr)
	}
	return staticBinPath
}

// runInContainer runs the binary in an Alpine container with limits, on the
// host network so it reaches databases on 127.0.0.1.
func runInContainer(t *testing.T, limits []string, args ...string) (string, int) {
	t.Helper()
	bin := seedstormStaticBin(t)
	dockerArgs := append([]string{"run", "--rm", "--network", "host", "-v", bin + ":/seedstorm:ro"}, limits...)
	dockerArgs = append(dockerArgs, "alpine", "/seedstorm", "--no-color")
	dockerArgs = append(dockerArgs, args...)
	out, err := exec.Command("docker", dockerArgs...).CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(out), exit.ExitCode()
	}
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	return string(out), 0
}

// Inside a container limited to 2 CPUs and 512MB, seedstorm sees
// those limits, not the host's, and never runs more generators than 2.
func TestLoadsim_DetectsContainerLimits(t *testing.T) {
	requireHeadroom(t, 2048)
	e := postgresEngine()
	dsn, conn := e.scratchDB(t, "ss_loadsim_detect")
	execSQL(t, conn, `CREATE TABLE a (id INT PRIMARY KEY, v TEXT); CREATE TABLE b (id INT PRIMARY KEY, v TEXT); CREATE TABLE c (id INT PRIMARY KEY, v TEXT)`)
	limits := []string{"--cpus", "2", "--memory", "512m", "--memory-swap", "512m"}

	out, code := runInContainer(t, limits, "tune", "--db", "postgres", "--dsn", dsn)
	if code != 0 || !strings.Contains(out, "Host         2 CPUs, 512MB memory") {
		t.Fatalf("tune in a 2 CPU / 512MB container (exit %d):\n%s", code, out)
	}

	schema := filepath.Join(t.TempDir(), "schema.yaml")
	runBin(t, "introspect", "--db", "postgres", "--dsn", dsn, "--out", schema)
	raw, _ := os.ReadFile(schema)
	out, code = runInContainer(t, append(limits, "-v", filepath.Dir(schema)+":/work:ro"),
		"seed", "--db", "postgres", "--dsn", dsn, "--schema", "/work/"+filepath.Base(schema), "--rows", "200", "--workers", "4", "--gen-workers", "16")
	if code != 0 || !strings.Contains(out, "Generators limited to the CPUs available") || !strings.Contains(out, "using=2") {
		t.Fatalf("seed --gen-workers 16 in a 2 CPU container (exit %d, schema %d bytes):\n%s", code, len(raw), out)
	}
}

// A server with few free connections gets fewer writers, says so,
// and the run completes instead of failing with too many connections.
func TestLoadsim_FewFreeConnectionsClampWriters(t *testing.T) {
	requireHeadroom(t, 2048)
	p := loadsimProfiles["cloudsql-micro"]
	p.postgres = append(append([]string(nil), p.postgres...), "max_connections=20")
	d := startLoadsimDB(t, postgresDriver, p, false)
	execSQL(t, d.conn, `CREATE TABLE parents (id INT PRIMARY KEY, name TEXT); CREATE TABLE kids (id INT PRIMARY KEY, parent_id INT NOT NULL REFERENCES parents (id), note TEXT)`)

	// Other clients hold 12 connections.
	var held []*sql.Conn
	for i := 0; i < 12; i++ {
		c, err := d.conn.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	defer func() {
		for _, c := range held {
			c.Close()
		}
	}()

	schema := filepath.Join(t.TempDir(), "schema.yaml")
	runBin(t, "introspect", "--db", "postgres", "--dsn", d.dsn, "--out", schema)
	_, stderr, err := runBinResult(t, "seed", "--db", "postgres", "--dsn", d.dsn, "--schema", schema, "--rows", "3000", "--workers", "50")
	if err != nil {
		t.Fatalf("seed with 50 writers on a busy 20-connection server failed: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "instead of 50") {
		t.Fatalf("the clamp was not reported:\n%s", stderr)
	}
	if n := countRows(t, d.conn, "kids"); n != 3000 {
		t.Fatalf("kids = %d rows", n)
	}
}

// A database whose disk fills up ends the run with a clear error
// naming the table, and nothing hangs.
func TestLoadsim_FullDiskFailsClearly(t *testing.T) {
	requireHeadroom(t, 3072)
	p := loadsimProfiles["cloudsql-micro"]
	p.memory = "1024m" // the 256MB tmpfs is charged to the container's memory
	d := startLoadsimDBWithTmpfs(t, postgresDriver, p, "256m")
	execSQL(t, d.conn, `CREATE TABLE blobs (id INT PRIMARY KEY, body TEXT)`)
	schema := filepath.Join(t.TempDir(), "schema.yaml")
	runBin(t, "introspect", "--db", "postgres", "--dsn", d.dsn, "--out", schema)
	profile := filepath.Join(t.TempDir(), "wide.yaml")
	if err := os.WriteFile(profile, []byte("version: 1\nname: wide\nrules:\n  - column: body\n    template: \"{{auto}}{{auto}}{{auto}}{{auto}}{{auto}}{{auto}}{{auto}}{{auto}}\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := runBinResult(t, "seed", "--db", "postgres", "--dsn", d.dsn, "--schema", schema, "--rows", "3000000", "--workers", "2", "--profile", profile)
	if err == nil {
		t.Fatal("seeding 3M rows into a 256MB disk succeeded")
	}
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	t.Logf("seed ended with: %s", lines[len(lines)-1])
	// The disk, not memory, must be what stopped the run: an OOM kill also
	// closes connections and would pass the message check below.
	if d.oomKilled(t) {
		t.Fatal("the database was OOM-killed: this run did not test a full disk")
	}
	serverLog, _ := exec.Command("docker", "logs", d.name).CombinedOutput()
	if !strings.Contains(strings.ToLower(string(serverLog)), "no space left on device") {
		t.Fatalf("the database never reported a full disk; its log ends:\n%s", tail(string(serverLog), 20))
	}
	// Postgres may report the full disk, or crash when its WAL cannot be
	// written: either way the message names the table and says what happened.
	last := strings.ToLower(lines[len(lines)-1])
	explained := strings.Contains(last, "disk is full") || strings.Contains(last, "closed the connection")
	if !strings.Contains(last, "write · blobs") || !explained {
		t.Fatalf("the failure does not name the table and explain it:\n%s", stderr)
	}
}

// tail returns the last n lines of s.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// Seedstorm itself stays within a 256MB container while seeding a
// million rows.
func TestLoadsim_SeedStaysUnderAMemoryLimit(t *testing.T) {
	requireHeadroom(t, 2048)
	e := postgresEngine()
	dsn, conn := e.scratchDB(t, "ss_loadsim_memory")
	execSQL(t, conn, `CREATE TABLE events (id BIGINT PRIMARY KEY, kind TEXT, payload TEXT, created_at TIMESTAMP)`)
	schema := filepath.Join(t.TempDir(), "schema.yaml")
	runBin(t, "introspect", "--db", "postgres", "--dsn", dsn, "--out", schema)
	out, code := runInContainer(t, []string{"--memory", "256m", "--memory-swap", "256m", "-v", filepath.Dir(schema) + ":/work:ro"},
		"seed", "--db", "postgres", "--dsn", dsn, "--schema", "/work/"+filepath.Base(schema), "--rows", "1000000", "--workers", "4")
	if code == 137 {
		t.Fatalf("seedstorm was OOM-killed under 256MB:\n%s", out)
	}
	if code != 0 {
		t.Fatalf("seed exited %d:\n%s", code, out)
	}
	if n := countRows(t, conn, "events"); n != 1000000 {
		t.Fatalf("events = %d rows", n)
	}
}
