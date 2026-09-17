//go:build integration

package integration_test

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// startFaultServe runs `seedstorm serve` from the fault-injection build and
// returns its base URL; the process is stopped when the test ends.
// lockedBuffer is written by the child process's output copier while the test
// reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func startFaultServe(t *testing.T, fault string) (string, *lockedBuffer) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	cmd := exec.Command(seedstormFaultBin(t), "--no-color", "serve", "--addr", "127.0.0.1:"+strconv.Itoa(port))
	cmd.Env = append(os.Environ(), "SEEDSTORM_FAULT="+fault, "XDG_CONFIG_HOME="+t.TempDir(), "HOME="+t.TempDir())
	logs := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if res, err := http.Get(base + "/connect"); err == nil {
			res.Body.Close()
			return base, logs
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve did not start:\n%s", logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A panic inside one web job used to kill `serve`: every session and every
// running job went with it. The job fails naming where; another job running
// at the same time finishes, and the server keeps answering.
func TestServe_PanicInOneJobLeavesTheServerAndOtherJobsRunning(t *testing.T) {
	e := postgresEngine()
	_, broken := e.scratchDB(t, "ss_reliability_web_a")
	_, healthy := e.scratchDB(t, "ss_reliability_web_b")
	execSQL(t, broken, `CREATE TABLE ledger_entries (id INT PRIMARY KEY, note TEXT)`)
	execSQL(t, healthy, `CREATE TABLE accounts (id INT PRIMARY KEY, name TEXT)`)

	base, logs := startFaultServe(t, "write:ledger_entries:panic")
	a, b := clientFor(t, base), clientFor(t, base)
	a.connectPostgres("ss_reliability_web_a")
	b.connectPostgres("ss_reliability_web_b")

	slow := b.start("/api/seed", map[string]any{"rows": 20000, "batchSize": 500, "workers": 2})
	failing := a.start("/api/seed", map[string]any{"rows": 200, "workers": 2})

	_, status, reason := a.stream(failing).finish(t, 60*time.Second)
	if status != "failed" || !strings.Contains(reason, "write · ledger_entries") || !strings.Contains(reason, "internal error") {
		t.Fatalf("failing job ended %q: %q", status, reason)
	}
	if _, status, reason := b.stream(slow).finish(t, 120*time.Second); status != "done" {
		t.Fatalf("the other job ended %q (%q) after a panic elsewhere\n%s", status, reason, logs.String())
	}
	if n := countRows(t, healthy, "accounts"); n != 20000 {
		t.Fatalf("accounts = %d rows, want 20000", n)
	}
	res, err := http.Get(base + "/connect")
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("server no longer answers after the panic: %v %v\n%s", res, err, logs.String())
	}
	res.Body.Close()
	if strings.Contains(logs.String(), "goroutine ") {
		t.Fatalf("serve printed a Go stack at the default log level:\n%s", logs.String())
	}
}
