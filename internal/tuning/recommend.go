package tuning

import (
	"fmt"
	"math"
	"strings"
)

// StorageType is the kind of disk behind a database.
type StorageType string

const (
	StorageUnknown    StorageType = ""
	StorageLocalSSD   StorageType = "local-ssd"
	StorageNetworkSSD StorageType = "network-ssd"
	StorageHDD        StorageType = "hdd"
)

// Host is the machine running seedstorm.
type Host struct {
	CPUs     float64
	MemoryMB int
}

// Database is what is known about the server a run writes to: detected over
// SQL (connections, buffers, used space) or entered by the user (size, disk).
type Database struct {
	Engine          string // postgres | mysql
	VCPU            float64
	MemoryMB        int
	Storage         StorageType
	StorageGB       int
	IOPS            int
	Shared          bool // other workloads use the server
	HA              bool // synchronous replication (high availability)
	Production      bool
	MaxConnections  int
	UsedConnections int
	// Memory the server reserves (MySQL buffer pool + log buffer, Postgres
	// shared buffers); what is left serves connections.
	BufferPoolBytes    int64
	LogBufferBytes     int64
	SharedBuffersBytes int64
	MaxAllowedPacket   int64
	UsedBytes          int64
}

// Run is the shape of the rows to write.
type Run struct {
	Rows        int64
	AvgRowBytes int
	// IndexFactor is index bytes per data byte (0.5: indexes add half).
	IndexFactor float64
}

// GrowthStatus is the verdict of the disk growth check.
type GrowthStatus string

const (
	GrowthOK      GrowthStatus = "ok"
	GrowthWarn    GrowthStatus = "warn"
	GrowthRefuse  GrowthStatus = "refuse"
	GrowthUnknown GrowthStatus = "unknown"
)

// Growth estimates how much the run adds to the database's disk.
type Growth struct {
	Status        GrowthStatus `json:"status"`
	ExpectedBytes int64        `json:"expectedBytes"`
	FreeBytes     int64        `json:"freeBytes"`
	Message       string       `json:"message"`
}

// Recommendation is a starting point, with the reason for each value.
type Recommendation struct {
	Writers    int      `json:"writers"`
	Generators int      `json:"generators"`
	ChunkBytes int      `json:"chunkBytes"`
	BatchBytes int      `json:"batchBytes"`
	Reasons    []string `json:"reasons"`
	Growth     Growth   `json:"growth"`
}

const (
	maxWriters        = 32
	defaultChunkBytes = 32 << 20
	minChunkBytes     = 4 << 20
	defaultBatchBytes = 1 << 20
	// iopsPerWriter: a writer's batches need about this many IOPS on a
	// network disk before a second writer only adds contention. The probe on a
	// 300-IOPS Cloud SQL-sized MySQL still gained from 4 writers.
	iopsPerWriter = 75
	// perWriterMB is a connection's working memory on the server.
	perWriterMB = 8
	// serverOverheadMB is what the server needs besides buffers and connections.
	serverOverheadMB = 150
	// singleCoreRowsPerSec is about what one generator produces
	// (docs/benchmarks.md); only a database faster than that benefits from more.
	fastWriterThreshold = 8
)

// Recommend suggests writers, generators, chunk and batch sizes for a run, and
// checks the disk has room. Constants are starting points calibrated against
// docs/benchmarks.md and the loadsim measurements; the numbers depend on the
// database's load, so the page and CLI always say so.
func Recommend(host Host, db Database, run Run) Recommendation {
	r := Recommendation{ChunkBytes: defaultChunkBytes, BatchBytes: defaultBatchBytes}
	type cap struct {
		n   int
		why string
	}
	var caps []cap
	add := func(n int, why string) { caps = append(caps, cap{max(1, n), why}) }

	if db.VCPU > 0 {
		perCPU := 2.0
		why := fmt.Sprintf("%g vCPU on the database, 2 writers each", db.VCPU)
		if db.Shared {
			perCPU = 1
			why = fmt.Sprintf("%g vCPU on the database, shared with other workloads", db.VCPU)
		}
		add(int(math.Ceil(db.VCPU*perCPU)), why)
	}
	if db.MaxConnections > 0 {
		free := db.MaxConnections - db.UsedConnections
		headroom := max(5, min(db.MaxConnections/5, free/2))
		add(free-headroom, fmt.Sprintf("%d free connections of %d, leaving %d for applications", free, db.MaxConnections, headroom))
	}
	if db.MemoryMB > 0 {
		reserved := (db.BufferPoolBytes+db.LogBufferBytes+db.SharedBuffersBytes)/(1<<20) + serverOverheadMB
		if left := int64(db.MemoryMB) - reserved; left > 0 {
			add(int(left/perWriterMB), fmt.Sprintf("%dMB of database memory left for connections", left))
		} else {
			add(1, "database memory is fully reserved by its buffers")
		}
	}
	storageCap := 0
	storageWhy := ""
	switch db.Storage {
	case StorageHDD:
		storageCap, storageWhy = 2, "spinning disk (storage): writes wait on seeks"
	case StorageNetworkSSD:
		if db.IOPS > 0 {
			storageCap, storageWhy = max(2, db.IOPS/iopsPerWriter), fmt.Sprintf("%d IOPS on a network disk", db.IOPS)
		} else {
			storageCap, storageWhy = 4, "network disk of unknown IOPS"
		}
	}
	if storageCap > 0 && db.HA {
		storageCap, storageWhy = max(1, storageCap/2), storageWhy+", halved for high availability (synchronous replication)"
	}
	if storageCap > 0 {
		add(storageCap, storageWhy)
	}
	add(maxWriters, fmt.Sprintf("at most %d writers per run", maxWriters))

	writers, why := maxWriters, ""
	for _, c := range caps {
		if c.n < writers {
			writers, why = c.n, c.why
		}
	}
	if db.Production {
		limit := 4
		if db.Shared {
			limit = 2
		}
		if writers > limit {
			writers, why = limit, fmt.Sprintf("production database: at most %d writers", limit)
		}
	}
	r.Writers = writers
	r.Reasons = append(r.Reasons, fmt.Sprintf("writers %d: %s", writers, why))

	// Generators help only when the database takes rows faster than one core
	// makes them.
	r.Generators = 1
	genWhy := "one generator keeps up with this database"
	fast := strings.EqualFold(db.Engine, "postgres") && writers >= fastWriterThreshold && !db.Shared && db.Storage != StorageHDD
	hostCores := int(math.Floor(host.CPUs))
	if fast && hostCores > 2 {
		r.Generators = hostCores - 1
		genWhy = fmt.Sprintf("a fast Postgres (COPY) outpaces one core; %d of %d host cores", r.Generators, hostCores)
	}
	if host.MemoryMB > 0 {
		budget := int64(host.MemoryMB) * (1 << 20) / 4
		if limit := int(budget/defaultChunkBytes) - 2; r.Generators > max(1, limit) {
			r.Generators = max(1, limit)
			genWhy = fmt.Sprintf("host memory %dMB fits %d generators' chunks", host.MemoryMB, r.Generators)
		}
		if int64(r.Generators+2)*int64(r.ChunkBytes) > budget {
			r.ChunkBytes = max(minChunkBytes, int(budget/int64(r.Generators+2)))
			r.Reasons = append(r.Reasons, fmt.Sprintf("chunks %dMB: queued rows stay under a quarter of the host's %dMB", r.ChunkBytes>>20, host.MemoryMB))
		}
	}
	r.Reasons = append(r.Reasons, fmt.Sprintf("generators %d: %s", r.Generators, genWhy))

	if db.MaxAllowedPacket > 0 && db.MaxAllowedPacket/4 < int64(r.BatchBytes) {
		r.BatchBytes = int(db.MaxAllowedPacket / 4)
		r.Reasons = append(r.Reasons, fmt.Sprintf("batches %dKB: a quarter of max_allowed_packet", r.BatchBytes>>10))
	}

	r.Growth = growth(db, run)
	return r
}

// growth compares the rows a run adds with the free space on the database's disk.
func growth(db Database, run Run) Growth {
	expected := int64(float64(run.Rows) * float64(max(run.AvgRowBytes, 1)) * (1 + run.IndexFactor) * 1.5)
	g := Growth{ExpectedBytes: expected}
	if db.StorageGB <= 0 {
		g.Status = GrowthUnknown
		g.Message = fmt.Sprintf("About %s will be written; enter the database's storage size to check it fits.", gigabytes(expected))
		return g
	}
	g.FreeBytes = int64(db.StorageGB)*(1<<30) - db.UsedBytes
	share := float64(expected) / float64(max(g.FreeBytes, 1))
	switch {
	case g.FreeBytes <= 0 || share > 0.9:
		g.Status = GrowthRefuse
		g.Message = fmt.Sprintf("About %s would be written (rows, indexes and logs) but only %s is free: the disk could fill up.", gigabytes(expected), gigabytes(max(g.FreeBytes, 0)))
	case share > 0.5:
		g.Status = GrowthWarn
		g.Message = fmt.Sprintf("About %s of the %s free will be used (%.0f%%).", gigabytes(expected), gigabytes(g.FreeBytes), share*100)
	default:
		g.Status = GrowthOK
		g.Message = fmt.Sprintf("About %s of the %s free will be used.", gigabytes(expected), gigabytes(g.FreeBytes))
	}
	return g
}

func gigabytes(b int64) string {
	return fmt.Sprintf("%.1fGB", float64(b)/(1<<30))
}

// ClampWriters lowers the requested writers only when the server lacks free
// connections for the whole run: writers, generators (they read parent keys)
// and one for sequence updates. An unknown limit changes nothing.
func ClampWriters(requested, generators, maxConnections, usedConnections int) int {
	if maxConnections <= 0 || requested <= 1 {
		return requested
	}
	free := maxConnections - usedConnections
	if free >= requested+generators+1 {
		return requested
	}
	return max(1, free-generators-1)
}
