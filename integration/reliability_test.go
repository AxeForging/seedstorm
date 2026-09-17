//go:build integration

package integration_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var (
	faultBinOnce sync.Once
	faultBinPath string
	faultBinErr  error
)

// seedstormFaultBin builds the CLI with the faultinject tag: SEEDSTORM_FAULT
// then makes a named step panic, fail or hang, so tests exercise real failure
// paths without mocking seedstorm's own code.
func seedstormFaultBin(t *testing.T) string {
	t.Helper()
	faultBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "seedstorm-faultbin-")
		if err != nil {
			faultBinErr = err
			return
		}
		faultBinPath = filepath.Join(dir, "seedstorm")
		out, err := exec.Command("go", "build", "-tags", "faultinject", "-o", faultBinPath, "../cmd/seedstorm").CombinedOutput()
		if err != nil {
			faultBinErr = errors.New(string(out))
		}
	})
	if faultBinErr != nil {
		t.Fatalf("build fault-injection binary: %v", faultBinErr)
	}
	return faultBinPath
}

func runFault(t *testing.T, fault string, args ...string) (stderr string, code int) {
	t.Helper()
	cmd := exec.Command(seedstormFaultBin(t), append([]string{"--no-color"}, args...)...)
	cmd.Env = append(os.Environ(), "SEEDSTORM_FAULT="+fault)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return errBuf.String(), exit.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return errBuf.String(), 0
}

// A panic is an internal error (exit 70) that says where it happened; a
// refused run is exit 1; neither prints a Go stack unless asked.
func TestCLI_ExitCodesSayWhatKindOfFailureItWas(t *testing.T) {
	e := postgresEngine()
	dsn, conn := e.scratchDB(t, "ss_reliability_cli")
	execSQL(t, conn, `CREATE TABLE users (id INT PRIMARY KEY, name TEXT); CREATE TABLE posts (id INT PRIMARY KEY, user_id INT NOT NULL REFERENCES users (id))`)
	schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
	runBin(t, "introspect", "--db", "postgres", "--dsn", dsn, "--out", schemaPath)
	seed := []string{"seed", "--db", "postgres", "--dsn", dsn, "--schema", schemaPath, "--rows", "50", "--workers", "2"}

	stderr, code := runFault(t, "write:users:panic", seed...)
	if code != 70 {
		t.Fatalf("panic while writing: exit %d, want 70\n%s", code, stderr)
	}
	for _, want := range []string{"internal error", "write", "users", "--log-level debug"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "goroutine ") {
		t.Fatalf("a stack was printed without --log-level debug:\n%s", stderr)
	}
	if n := countRows(t, conn, "posts"); n != 0 {
		t.Fatalf("posts has %d rows after its parent's writes panicked", n)
	}

	stderr, code = runFault(t, "write:users:error", seed...)
	if code != 1 || !strings.Contains(stderr, "write · users") {
		t.Fatalf("failed write: exit %d, want 1 naming write · users\n%s", code, stderr)
	}
}

// Ctrl+C ends a run with exit 130 and a line saying so, promptly.
func TestCLI_InterruptExitsWith130(t *testing.T) {
	e := postgresEngine()
	dsn, conn := e.scratchDB(t, "ss_reliability_sigint")
	execSQL(t, conn, `CREATE TABLE users (id INT PRIMARY KEY, name TEXT)`)
	schemaPath := filepath.Join(t.TempDir(), "schema.yaml")
	runBin(t, "introspect", "--db", "postgres", "--dsn", dsn, "--out", schemaPath)

	cmd := exec.Command(seedstormFaultBin(t), "--no-color", "seed", "--db", "postgres", "--dsn", dsn, "--schema", schemaPath, "--rows", "1000", "--workers", "1")
	cmd.Env = append(os.Environ(), "SEEDSTORM_FAULT=write:users:hang")
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	start := time.Now()
	_ = cmd.Process.Signal(syscall.SIGINT)
	err := cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 130 {
		t.Fatalf("exit = %v, want 130\n%s", err, errBuf.String())
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %s to stop after Ctrl+C", d)
	}
	if !strings.Contains(errBuf.String(), "interrupted") {
		t.Fatalf("stderr does not say it was interrupted:\n%s", errBuf.String())
	}
}

// The normal build carries no fault-injection code.
func TestFaultInjection_AbsentFromTheDefaultBinary(t *testing.T) {
	raw, err := os.ReadFile(seedstormBin(t))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("SEEDSTORM_FAULT")) {
		t.Fatal("the default binary contains fault-injection code")
	}
}
