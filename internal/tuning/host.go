// Package tuning decides how hard a run may push: how many generators the
// machine running seedstorm can feed, and (later) how many writers a database
// can take.
package tuning

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// cgroupRoot is where the process's own cgroup is mounted in a container.
const cgroupRoot = "/sys/fs/cgroup"

// MaxGenerators is how many tables may generate at once on this machine: its
// cores, or fewer when a container CPU quota limits the process.
func MaxGenerators() int {
	quota, limited := cpuQuota(cgroupRoot)
	return maxGenerators(runtime.NumCPU(), quota, limited)
}

// ClampGenerators bounds a requested generator count to MaxGenerators.
func ClampGenerators(n int) int {
	return max(1, min(n, MaxGenerators()))
}

// maxGenerators rounds a fractional quota down: generation is CPU-bound, and a
// half core cannot run a second generator.
func maxGenerators(hostCPUs int, quota float64, limited bool) int {
	n := max(1, hostCPUs)
	if limited {
		n = min(n, max(1, int(math.Floor(quota))))
	}
	return n
}

// cpuQuota reads the CPU limit of the cgroup mounted at root, in cores: cgroup
// v2 cpu.max ("200000 100000" is 2 cores, "max ..." is none), else cgroup v1
// cfs quota and period (-1 is none).
func cpuQuota(root string) (float64, bool) {
	if raw, err := os.ReadFile(filepath.Join(root, "cpu.max")); err == nil {
		fields := strings.Fields(string(raw))
		if len(fields) == 2 && fields[0] != "max" {
			return ratio(fields[0], fields[1])
		}
		return 0, false
	}
	for _, dir := range []string{"cpu", "cpu,cpuacct"} {
		quota, qerr := os.ReadFile(filepath.Join(root, dir, "cpu.cfs_quota_us"))
		period, perr := os.ReadFile(filepath.Join(root, dir, "cpu.cfs_period_us"))
		if qerr == nil && perr == nil {
			return ratio(strings.TrimSpace(string(quota)), strings.TrimSpace(string(period)))
		}
	}
	return 0, false
}

func ratio(quota, period string) (float64, bool) {
	q, qerr := strconv.ParseFloat(quota, 64)
	p, perr := strconv.ParseFloat(period, 64)
	if qerr != nil || perr != nil || q <= 0 || p <= 0 {
		return 0, false
	}
	return q / p, true
}

// DetectHost describes the machine running seedstorm: usable CPUs (a container
// quota when there is one) and memory (a container limit, else the total).
func DetectHost() Host {
	cpus := float64(runtime.NumCPU())
	if quota, ok := cpuQuota(cgroupRoot); ok && quota < cpus {
		cpus = quota
	}
	return Host{CPUs: cpus, MemoryMB: memoryLimitMB("/")}
}

// memoryLimitMB reads the memory limit under root: cgroup v2 memory.max, cgroup
// v1 memory.limit_in_bytes, else /proc/meminfo MemTotal. 0 when unknown.
func memoryLimitMB(root string) int {
	total := meminfoTotalMB(root)
	for _, rel := range []string{"sys/fs/cgroup/memory.max", "sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		v := strings.TrimSpace(string(raw))
		if v == "max" {
			break
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			break
		}
		mb := int(n >> 20)
		// cgroup v1 reports "unlimited" as a huge page-aligned number.
		if total > 0 && mb >= total {
			break
		}
		return mb
	}
	return total
}

func meminfoTotalMB(root string) int {
	raw, err := os.ReadFile(filepath.Join(root, "proc/meminfo"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.Atoi(fields[1])
				return kb / 1024
			}
		}
	}
	return 0
}
