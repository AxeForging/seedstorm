package tuning

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Containers limit CPU with a quota, which runtime.NumCPU ignores (it reported
// 16 inside --cpus=2). Go's GOMAXPROCS follows the quota but never goes below 2,
// so a 1-CPU or half-CPU container still looked like 2 cores.
func TestCPUQuota_ReadsCgroupLimits(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  float64
		found bool
	}{
		{"v2 two cpus", map[string]string{"cpu.max": "200000 100000\n"}, 2, true},
		{"v2 half a cpu", map[string]string{"cpu.max": "50000 100000\n"}, 0.5, true},
		{"v2 unlimited", map[string]string{"cpu.max": "max 100000\n"}, 0, false},
		{"v1 one and a half", map[string]string{"cpu/cpu.cfs_quota_us": "150000\n", "cpu/cpu.cfs_period_us": "100000\n"}, 1.5, true},
		{"v1 unlimited", map[string]string{"cpu/cpu.cfs_quota_us": "-1\n", "cpu/cpu.cfs_period_us": "100000\n"}, 0, false},
		{"no cgroup files", map[string]string{}, 0, false},
		{"garbage", map[string]string{"cpu.max": "abc def\n"}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, body := range c.files {
				writeFile(t, root, rel, body)
			}
			got, found := cpuQuota(root)
			if got != c.want || found != c.found {
				t.Fatalf("cpuQuota = %v, %v; want %v, %v", got, found, c.want, c.found)
			}
		})
	}
}

func TestMaxGenerators_FollowsTheQuotaNotTheHostCores(t *testing.T) {
	cases := []struct {
		hostCPUs int
		quota    float64
		limited  bool
		want     int
	}{
		{16, 2, true, 2},
		{16, 0.5, true, 1},
		{16, 1.5, true, 1},
		{16, 0, false, 16},
		{4, 8, true, 4}, // a quota above the host's cores changes nothing
		{1, 0, false, 1},
	}
	for _, c := range cases {
		if got := maxGenerators(c.hostCPUs, c.quota, c.limited); got != c.want {
			t.Errorf("maxGenerators(host=%d, quota=%v, limited=%v) = %d, want %d", c.hostCPUs, c.quota, c.limited, got, c.want)
		}
	}
}

// A container's memory limit (cgroup v2 memory.max, v1 limit_in_bytes) is the
// memory seedstorm may use, not the host's total.
func TestMemoryLimit_ReadsCgroupThenMeminfo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "sys/fs/cgroup/memory.max", "536870912\n")
	writeFile(t, root, "proc/meminfo", "MemTotal:       32000000 kB\nMemAvailable:   9000000 kB\n")
	if got := memoryLimitMB(root); got != 512 {
		t.Fatalf("cgroup v2 limit: %dMB, want 512", got)
	}

	root = t.TempDir()
	writeFile(t, root, "sys/fs/cgroup/memory.max", "max\n")
	writeFile(t, root, "proc/meminfo", "MemTotal:       32000000 kB\n")
	if got := memoryLimitMB(root); got != 31250 {
		t.Fatalf("no cgroup limit: %dMB, want MemTotal 31250", got)
	}

	root = t.TempDir()
	writeFile(t, root, "sys/fs/cgroup/memory/memory.limit_in_bytes", "1073741824\n")
	writeFile(t, root, "proc/meminfo", "MemTotal:       32000000 kB\n")
	if got := memoryLimitMB(root); got != 1024 {
		t.Fatalf("cgroup v1 limit: %dMB, want 1024", got)
	}

	root = t.TempDir()
	writeFile(t, root, "sys/fs/cgroup/memory/memory.limit_in_bytes", "9223372036854771712\n")
	writeFile(t, root, "proc/meminfo", "MemTotal:       2048000 kB\n")
	if got := memoryLimitMB(root); got != 2000 {
		t.Fatalf("cgroup v1 'unlimited' sentinel: %dMB, want MemTotal 2000", got)
	}
}
