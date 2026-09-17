package tuning

import (
	"strings"
	"testing"
)

const mb = int64(1 << 20)

func micro() Database {
	return Database{
		Engine: "mysql", VCPU: 1, MemoryMB: 629, Storage: StorageNetworkSSD, StorageGB: 10, IOPS: 300,
		MaxConnections: 280, UsedConnections: 3, BufferPoolBytes: 53477376, LogBufferBytes: 67108864,
		MaxAllowedPacket: 33554432, UsedBytes: 200 * mb,
	}
}

func TestRecommend_WritersFollowTheDatabaseNotItsConnectionLimit(t *testing.T) {
	host := Host{CPUs: 8, MemoryMB: 16000}
	cases := []struct {
		name string
		db   func() Database
		want int
		why  string
	}{
		// max_connections 280 on 629MB says nothing about capacity: storage and
		// CPU decide. The probe measured 4 writers faster than 2 at 300 IOPS.
		{"cloud sql micro", micro, 2, "vCPU"},
		{"few free connections", func() Database {
			d := micro()
			d.VCPU, d.MaxConnections, d.UsedConnections, d.IOPS = 8, 100, 90, 3000
			return d
		}, 5, "connections"},
		{"hdd", func() Database { d := micro(); d.VCPU, d.Storage, d.IOPS = 16, StorageHDD, 0; return d }, 2, "storage"},
		{"network ssd by iops", func() Database { d := micro(); d.VCPU, d.IOPS = 16, 450; return d }, 6, "IOPS"},
		{"production shared", func() Database {
			d := micro()
			d.VCPU, d.IOPS, d.Production, d.Shared = 16, 30000, true, true
			return d
		}, 2, "production"},
		{"high availability halves storage", func() Database { d := micro(); d.VCPU, d.IOPS, d.HA = 16, 600, true; return d }, 4, "high availability"},
		{"never above 32", func() Database {
			d := micro()
			d.VCPU, d.Storage, d.IOPS, d.MaxConnections = 64, StorageLocalSSD, 0, 5000
			return d
		}, 32, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Recommend(host, c.db(), Run{Rows: 10000, AvgRowBytes: 200})
			if r.Writers != c.want {
				t.Fatalf("writers = %d, want %d (reasons %v)", r.Writers, c.want, r.Reasons)
			}
			if c.why != "" && !strings.Contains(strings.Join(r.Reasons, " | "), c.why) {
				t.Fatalf("reasons %v do not mention %q", r.Reasons, c.why)
			}
		})
	}
}

func TestRecommend_GeneratorsChunksAndBatches(t *testing.T) {
	r := Recommend(Host{CPUs: 2, MemoryMB: 16000}, micro(), Run{Rows: 1000, AvgRowBytes: 100})
	if r.Generators != 1 {
		t.Errorf("generators on a 2-core host = %d, want 1", r.Generators)
	}
	if r.BatchBytes > int(33554432/4) || r.BatchBytes > 1<<20 {
		t.Errorf("batch bytes = %d, want <= 1MB and <= max_allowed_packet/4", r.BatchBytes)
	}

	fast := Database{Engine: "postgres", VCPU: 16, MemoryMB: 64000, Storage: StorageLocalSSD, MaxConnections: 400}
	r = Recommend(Host{CPUs: 16, MemoryMB: 32000}, fast, Run{Rows: 10_000_000, AvgRowBytes: 200})
	if r.Generators < 2 || r.Generators > 15 {
		t.Errorf("generators for a fast Postgres = %d, want several but below the host's cores", r.Generators)
	}

	small := Recommend(Host{CPUs: 16, MemoryMB: 512}, fast, Run{Rows: 10_000_000, AvgRowBytes: 200})
	if int64(small.Generators+2)*int64(small.ChunkBytes) > 512*mb/4 {
		t.Errorf("chunks do not fit a 512MB host: %d generators × %d bytes", small.Generators, small.ChunkBytes)
	}
}

func TestRecommend_GrowthCheck(t *testing.T) {
	d := micro() // 10GB, 200MB used
	ok := Recommend(Host{CPUs: 2, MemoryMB: 4000}, d, Run{Rows: 1_000_000, AvgRowBytes: 2000, IndexFactor: 0.5})
	if ok.Growth.Status != GrowthWarn && ok.Growth.Status != GrowthOK {
		t.Fatalf("1M×2KB on 10GB: %+v", ok.Growth)
	}
	d.StorageGB = 2
	refused := Recommend(Host{CPUs: 2, MemoryMB: 4000}, d, Run{Rows: 1_000_000, AvgRowBytes: 2000, IndexFactor: 0.5})
	if refused.Growth.Status != GrowthRefuse || !strings.Contains(refused.Growth.Message, "GB") {
		t.Fatalf("1M×2KB on 2GB: %+v", refused.Growth)
	}
	d.StorageGB = 0
	unknown := Recommend(Host{CPUs: 2, MemoryMB: 4000}, d, Run{Rows: 1_000_000, AvgRowBytes: 2000})
	if unknown.Growth.Status != GrowthUnknown {
		t.Fatalf("no storage size: %+v", unknown.Growth)
	}
}

// Run-start clamps only lower writers when the server truly lacks free
// connections for the run (writers + generators + one for sequences).
func TestClampWriters_OnlyWhenConnectionsRunOut(t *testing.T) {
	cases := []struct{ requested, generators, max, used, want int }{
		{4, 1, 100, 85, 4}, // 15 free: 4+1+1 fit
		{4, 1, 100, 96, 2}, // 4 free: 2 writers + 1 generator + 1
		{8, 1, 25, 24, 1},  // 1 free: never below 1
		{4, 1, 0, 0, 4},    // limit unknown: unchanged
	}
	for _, c := range cases {
		if got := ClampWriters(c.requested, c.generators, c.max, c.used); got != c.want {
			t.Errorf("ClampWriters(%d, gen %d, max %d, used %d) = %d, want %d", c.requested, c.generators, c.max, c.used, got, c.want)
		}
	}
}
