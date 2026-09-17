//go:build integration && loadsim

package integration_test

// Database profiles modelled on managed Cloud SQL instances. Server settings
// that Cloud SQL derives from memory are set explicitly, never left to the
// container's defaults (docs/specs 004, D13).

type loadsimProfile struct {
	name   string
	cpus   string // docker --cpus
	memory string // docker --memory (and --memory-swap: no swap)
	// writeIOPS limits writes to the data disk (0: unlimited). The Cloud SQL
	// storage docs give 30 IOPS per GB of SSD.
	writeIOPS int
	postgres  []string // postgres -c settings
	mysql     []string // mysqld flags
	// source records where the settings come from.
	source string
}

var loadsimProfiles = map[string]loadsimProfile{
	// db-f1-micro: 1 shared vCPU, 628.74MB, 10GB SSD.
	"cloudsql-micro": {
		name: "cloudsql-micro", cpus: "1", memory: "629m", writeIOPS: 300,
		postgres: []string{
			"max_connections=25", "shared_buffers=207MB", "effective_cache_size=251MB",
			"work_mem=4MB", "maintenance_work_mem=64MB", "temp_buffers=8MB",
		},
		mysql: []string{
			"--innodb-buffer-pool-size=53477376", "--innodb-flush-log-at-trx-commit=1", "--innodb-flush-method=O_DIRECT",
			"--innodb-io-capacity=5000", "--innodb-io-capacity-max=10000", "--innodb-log-buffer-size=67108864",
			"--innodb-redo-log-capacity=104857600", "--max-allowed-packet=33554432", "--max-connections=280",
			"--performance-schema=OFF", "--table-open-cache=4000", "--thread-cache-size=10",
			"--tmp-table-size=16777216", "--max-heap-table-size=16777216", "--sort-buffer-size=262144", "--join-buffer-size=262144",
		},
		source: "postgres: Cloud SQL docs (flags, memory best practices); mysql: SHOW VARIABLES on a MySQL 8.4.10 Enterprise 1 vCPU / 628.74MB instance, 2026-09-17",
	},
	// db-custom-2-7680: 2 vCPU, 7.5GB, 100GB SSD assumed.
	"cloudsql-2vcpu": {
		name: "cloudsql-2vcpu", cpus: "2", memory: "7680m", writeIOPS: 3000,
		postgres: []string{
			"max_connections=400", "shared_buffers=2534MB", "effective_cache_size=3072MB",
			"work_mem=4MB", "maintenance_work_mem=64MB", "temp_buffers=8MB",
		},
		mysql: []string{
			"--innodb-buffer-pool-size=5793165312", "--innodb-flush-log-at-trx-commit=1", "--innodb-flush-method=O_DIRECT",
			"--innodb-io-capacity=5000", "--innodb-io-capacity-max=10000", "--innodb-log-buffer-size=67108864",
			"--innodb-redo-log-capacity=104857600", "--max-allowed-packet=33554432", "--max-connections=280",
			"--performance-schema=OFF", "--table-open-cache=4000", "--thread-cache-size=10",
			"--tmp-table-size=16777216", "--max-heap-table-size=16777216", "--sort-buffer-size=262144", "--join-buffer-size=262144",
		},
		source: "postgres: Cloud SQL docs; mysql: ESTIMATED (buffer pool ~72% of memory per Cloud SQL docs, other values as the micro reference)",
	},
}
