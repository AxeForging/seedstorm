//go:build integration && loadsim

package integration_test

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// loadsimDB is a throwaway database container limited like a profile.
type loadsimDB struct {
	engine engine
	dsn    string
	conn   *sql.DB
	name   string // container name
}

// requireHeadroom skips resource-limited tests on a machine that cannot spare
// the memory, instead of starving everything else running on it.
func requireHeadroom(t *testing.T, needMB int) {
	t.Helper()
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			kb, _ := strconv.Atoi(fields[1])
			if kb/1024 < needMB {
				t.Skipf("only %dMB available, need %dMB for this profile", kb/1024, needMB)
			}
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// dataDevice is the block device holding Docker's data, for IOPS limits.
func dataDevice(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("findmnt", "-no", "SOURCE", "-T", "/var/lib/docker").Output()
	if err != nil {
		return ""
	}
	src := strings.TrimSpace(string(out))
	if i := strings.Index(src, "["); i > 0 {
		src = src[:i]
	}
	// /dev/nvme0n1p7 -> /dev/nvme0n1, /dev/sda1 -> /dev/sda
	for _, prefix := range []string{"/dev/nvme", "/dev/mmcblk"} {
		if strings.HasPrefix(src, prefix) {
			if i := strings.LastIndex(src, "p"); i > len(prefix) {
				return src[:i]
			}
		}
	}
	return strings.TrimRight(src, "0123456789")
}

// startLoadsimDB starts a limited database container for profile and waits
// until it answers. The container is removed when the test ends.
func startLoadsimDB(t *testing.T, driver string, p loadsimProfile, limitIO bool) *loadsimDB {
	t.Helper()
	return startLoadsimDBWith(t, driver, p, limitIO, "")
}

// startLoadsimDBWithTmpfs keeps the data directory on a tmpfs of size, so the
// database's disk fills up at that size.
func startLoadsimDBWithTmpfs(t *testing.T, driver string, p loadsimProfile, size string) *loadsimDB {
	t.Helper()
	return startLoadsimDBWith(t, driver, p, false, size)
}

func startLoadsimDBWith(t *testing.T, driver string, p loadsimProfile, limitIO bool, tmpfsSize string) *loadsimDB {
	t.Helper()
	port := freePort(t)
	name := fmt.Sprintf("ss-loadsim-%s-%s-%d", strings.ReplaceAll(p.name, "cloudsql-", ""), map[string]string{postgresDriver: "pg", mysqlDriver: "my"}[driver], port)
	args := []string{"run", "-d", "--rm", "--name", name, "--cpus", p.cpus, "--memory", p.memory, "--memory-swap", p.memory,
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", port, map[string]int{postgresDriver: 5432, mysqlDriver: 3306}[driver])}
	if dev := dataDevice(t); limitIO && p.writeIOPS > 0 && dev != "" {
		args = append(args, "--device-write-iops", fmt.Sprintf("%s:%d", dev, p.writeIOPS))
	}
	if tmpfsSize != "" {
		dataDir := map[string]string{postgresDriver: "/var/lib/postgresql/data", mysqlDriver: "/var/lib/mysql"}[driver]
		args = append(args, "--mount", "type=tmpfs,destination="+dataDir+",tmpfs-size="+tmpfsSize)
	}
	var e engine
	var dsn string
	switch driver {
	case postgresDriver:
		args = append(args, "-e", "POSTGRES_USER=seedstorm", "-e", "POSTGRES_PASSWORD=seedstorm", "-e", "POSTGRES_DB=loadsim", "postgres:17-alpine")
		for _, setting := range p.postgres {
			args = append(args, "-c", setting)
		}
		dsn = fmt.Sprintf("postgres://seedstorm:seedstorm@127.0.0.1:%d/loadsim?sslmode=disable", port)
		e = postgresEngine()
	default:
		args = append(args, "-e", "MYSQL_ROOT_PASSWORD=root", "-e", "MYSQL_DATABASE=loadsim", "-e", "MYSQL_USER=seedstorm", "-e", "MYSQL_PASSWORD=seedstorm", "mysql:8.4")
		args = append(args, p.mysql...)
		dsn = fmt.Sprintf("seedstorm:seedstorm@tcp(127.0.0.1:%d)/loadsim?parseTime=true&multiStatements=true", port)
		e = mysqlEngine()
	}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Fatalf("docker run %s: %v\n%s", name, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	deadline := time.Now().Add(3 * time.Minute)
	for {
		conn, err := sql.Open(driver, dsn)
		if err == nil {
			if err = conn.Ping(); err == nil {
				t.Cleanup(func() { conn.Close() })
				return &loadsimDB{engine: e, dsn: dsn, conn: conn, name: name}
			}
			conn.Close()
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "logs", "--tail", "30", name).CombinedOutput()
			t.Fatalf("%s did not accept connections: %v\n%s", name, err, logs)
		}
		time.Sleep(time.Second)
	}
}

// oomKilled reports whether the container was killed for memory, from the
// kernel's own counter (memory.peak sits at the limit on healthy runs: it
// counts page cache).
func (d *loadsimDB) oomKilled(t *testing.T) bool {
	t.Helper()
	out, err := exec.Command("docker", "exec", d.name, "cat", "/sys/fs/cgroup/memory.events").Output()
	if err != nil {
		state, _ := exec.Command("docker", "inspect", "-f", "{{.State.OOMKilled}}", d.name).Output()
		return strings.TrimSpace(string(state)) == "true"
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "oom_kill ") {
			n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "oom_kill ")))
			return n > 0
		}
	}
	return false
}
