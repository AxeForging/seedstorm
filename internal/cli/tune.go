package cli

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/tuning"
)

// tuneCaveat is printed with every recommendation.
const tuneCaveat = "Estimates from benchmarks on one machine: real numbers depend on the database's load and network. Watch the rate during the first minute and adjust."

func tuneCmd() *cli.Command {
	return &cli.Command{
		Name:  "tune",
		Usage: "Recommend writers and generators for a database, from its limits and size",
		Description: `Reads the database's connection limit, buffers and used space (read-only),
combines them with what SQL cannot report (vCPU, memory, disk) and prints
recommended --workers and --gen-workers, the reason for each, and whether the
rows fit on the disk. seed, gaps and mirror accept --workers auto to use the
recommendation from detected values only.`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "db", Usage: "Database type: mysql or postgres", Value: "postgres", Sources: cli.EnvVars("SEEDSTORM_DB")},
			&cli.StringFlag{Name: "dsn", Usage: "Data source name (connection string)", Required: true, Sources: cli.EnvVars("SEEDSTORM_DSN")},
			&cli.IntFlag{Name: "rows", Usage: "Rows the run will write in total (for the disk check)"},
			&cli.IntFlag{Name: "avg-row-bytes", Usage: "Average row size in bytes (for the disk check)", Value: 256},
			&cli.Float64Flag{Name: "vcpu", Usage: "Database vCPU"},
			&cli.IntFlag{Name: "memory-mb", Usage: "Database memory in MB"},
			&cli.StringFlag{Name: "storage", Usage: "Database disk: local-ssd, network-ssd or hdd"},
			&cli.IntFlag{Name: "storage-gb", Usage: "Database disk size in GB"},
			&cli.IntFlag{Name: "iops", Usage: "Provisioned IOPS of a network disk"},
			&cli.BoolFlag{Name: "shared", Usage: "Other workloads use the database"},
			&cli.BoolFlag{Name: "ha", Usage: "High availability (synchronous replica)"},
			&cli.BoolFlag{Name: "production", Usage: "The database is production: stay conservative", Sources: cli.EnvVars("SEEDSTORM_PRODUCTION")},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			storage := tuning.StorageType(cmd.String("storage"))
			switch storage {
			case tuning.StorageUnknown, tuning.StorageLocalSSD, tuning.StorageNetworkSSD, tuning.StorageHDD:
			default:
				return fmt.Errorf("unknown --storage %q (use local-ssd, network-ssd or hdd)", cmd.String("storage"))
			}
			dbType := normalizeDBType(cmd.String("db"))
			conn, err := sql.Open(dbType, cmd.String("dsn"))
			if err != nil {
				return err
			}
			defer conn.Close()
			if err := pingWithin(ctx, conn); err != nil {
				return fmt.Errorf("%s did not answer: %w", dsnLabel(dbType, cmd.String("dsn")), err)
			}
			info, err := db.DetectServer(ctx, conn, dbType)
			if err != nil {
				return err
			}
			target := serverTarget(info)
			target.VCPU = cmd.Float64("vcpu")
			target.MemoryMB = int(cmd.Int("memory-mb"))
			target.Storage = storage
			target.StorageGB = int(cmd.Int("storage-gb"))
			target.IOPS = int(cmd.Int("iops"))
			target.Shared, target.HA, target.Production = cmd.Bool("shared"), cmd.Bool("ha"), cmd.Bool("production")
			host := tuning.DetectHost()
			rec := tuning.Recommend(host, target, tuning.Run{Rows: int64(cmd.Int("rows")), AvgRowBytes: int(cmd.Int("avg-row-bytes")), IndexFactor: 0.5})
			if _, err := fmt.Fprintf(os.Stdout, "Host         %g CPUs, %dMB memory (this machine or container, running seedstorm)\n", host.CPUs, host.MemoryMB); err != nil {
				return err
			}
			return printRecommendation(os.Stdout, info, rec)
		},
	}
}

func serverTarget(info db.ServerInfo) tuning.Database {
	return tuning.Database{
		Engine: info.Engine, MaxConnections: info.MaxConnections, UsedConnections: info.UsedConnections,
		BufferPoolBytes: info.BufferPoolBytes, LogBufferBytes: info.LogBufferBytes, SharedBuffersBytes: info.SharedBuffersBytes,
		MaxAllowedPacket: info.MaxAllowedPacket, UsedBytes: info.UsedBytes,
	}
}

func printRecommendation(w io.Writer, info db.ServerInfo, rec tuning.Recommendation) error {
	replica := ""
	if info.Replica {
		replica = " (replica)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Database     %s %s%s, %d/%d connections in use\n", info.Engine, info.Version, replica, info.UsedConnections, info.MaxConnections)
	fmt.Fprintf(&b, "Writers      %d   (--workers)\n", rec.Writers)
	fmt.Fprintf(&b, "Generators   %d   (--gen-workers)\n", rec.Generators)
	b.WriteString("\nWhy:\n")
	for _, r := range rec.Reasons {
		fmt.Fprintf(&b, "  - %s\n", r)
	}
	fmt.Fprintf(&b, "\nDisk: %s\n\n%s\n", rec.Growth.Message, tuneCaveat)
	_, err := io.WriteString(w, b.String())
	return err
}

// workersFromFlag reads --workers: a number, or auto (recommended from the
// database's detected limits, logged with its reason).
func workersFromFlag(ctx context.Context, cmd *cli.Command, conn *sql.DB, dbType string) (int, error) {
	raw := strings.TrimSpace(cmd.String("workers"))
	if !strings.EqualFold(raw, "auto") {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("--workers must be a number of at least 1, or auto (got %q)", raw)
		}
		return n, nil
	}
	info, err := db.DetectServer(ctx, conn, dbType)
	if err != nil {
		logging.Log.Warn().Err(err).Msg("Could not read the database's limits; using the default writers")
		return defaultWorkers, nil
	}
	target := serverTarget(info)
	target.Production = cmd.Bool("production")
	rec := tuning.Recommend(tuning.DetectHost(), target, tuning.Run{})
	logging.Log.Info().Int("writers", rec.Writers).Str("why", strings.Join(rec.Reasons, "; ")).Msg("Writers chosen for this database (--workers auto)")
	return rec.Writers, nil
}
