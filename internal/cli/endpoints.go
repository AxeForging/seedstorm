package cli

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/runerr"
	"github.com/AxeForging/seedstorm/internal/seeder"
)

// endpointFlags are the source/target connection flags shared by compare and
// mirror (named like clone-schema's).
func endpointFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "source-db", Usage: "Source database type: mysql or postgres", Value: "postgres", Sources: cli.EnvVars("SEEDSTORM_SOURCE_DB")},
		&cli.StringFlag{Name: "source-dsn", Usage: "Source data source name (required unless --source-snapshot is given)", Sources: cli.EnvVars("SEEDSTORM_SOURCE_DSN")},
		&cli.StringFlag{Name: "source-snapshot", Usage: "Read source row counts from a file made by `seedstorm snapshot` (JSON or YAML) instead of connecting"},
		&cli.StringFlag{Name: "target-db", Usage: "Target database type: mysql or postgres", Value: "postgres", Sources: cli.EnvVars("SEEDSTORM_TARGET_DB")},
		&cli.StringFlag{Name: "target-dsn", Usage: "Target data source name", Required: true, Sources: cli.EnvVars("SEEDSTORM_TARGET_DSN")},
		&cli.StringFlag{Name: "counts", Usage: "Row counts: exact (COUNT(*)) or estimate (planner statistics, fast on large tables)", Value: "exact"},
	}
}

// openEndpoints connects to both sides; the source may instead be a snapshot
// file (--source-snapshot). The caller releases them with closeEndpoints.
func openEndpoints(ctx context.Context, cmd *cli.Command) (source, target seeder.Endpoint, err error) {
	snapshotPath, sourceDSN := cmd.String("source-snapshot"), cmd.String("source-dsn")
	switch {
	case snapshotPath != "" && sourceDSN != "":
		return source, target, fmt.Errorf("use either --source-dsn or --source-snapshot, not both")
	case snapshotPath != "":
		if source, err = snapshotEndpoint(snapshotPath); err != nil {
			return source, target, runerr.OnSide(runerr.SideSource, runerr.At(runerr.PhaseConnect, "", fmt.Errorf("snapshot file: %w", err)))
		}
	case sourceDSN != "":
		if source, err = openEndpoint(ctx, cmd.String("source-db"), sourceDSN); err != nil {
			return source, target, runerr.OnSide(runerr.SideSource, runerr.At(runerr.PhaseConnect, "", err))
		}
	default:
		return source, target, fmt.Errorf("a source is required: pass --source-dsn (or SEEDSTORM_SOURCE_DSN) or --source-snapshot <file>")
	}
	target, err = openEndpoint(ctx, cmd.String("target-db"), cmd.String("target-dsn"))
	if err != nil {
		closeEndpoints(source)
		return source, target, runerr.OnSide(runerr.SideTarget, runerr.At(runerr.PhaseConnect, "", err))
	}
	return source, target, nil
}

// snapshotEndpoint loads a table-counts file as a source that needs no connection.
func snapshotEndpoint(path string) (seeder.Endpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return seeder.Endpoint{}, err
	}
	snap, err := compare.ParseSnapshot(data)
	if err != nil {
		return seeder.Endpoint{}, fmt.Errorf("%s: %w", path, err)
	}
	label := "snapshot " + filepath.Base(path)
	if snap.Label == "" {
		snap.Label = label
	}
	return seeder.Endpoint{DBType: snap.DBType, Label: label, Snapshot: &snap}, nil
}

// closeEndpoints closes every live connection; snapshot endpoints have none.
func closeEndpoints(endpoints ...seeder.Endpoint) {
	for _, ep := range endpoints {
		if ep.Conn != nil {
			_ = ep.Conn.Close()
		}
	}
}

func openEndpoint(ctx context.Context, dbFlag, dsn string) (seeder.Endpoint, error) {
	driver := normalizeDBType(dbFlag)
	if driver != "pgx" && driver != "mysql" {
		return seeder.Endpoint{}, fmt.Errorf("unsupported database type %q (use mysql or postgres)", dbFlag)
	}
	conn, err := sql.Open(driver, dsn)
	if err != nil {
		return seeder.Endpoint{}, fmt.Errorf("open connection: %w", err)
	}
	if err := pingWithin(ctx, conn); err != nil {
		_ = conn.Close()
		return seeder.Endpoint{}, fmt.Errorf("%s did not answer: %w", dsnLabel(driver, dsn), err)
	}
	return seeder.Endpoint{Conn: conn, DBType: driver, DSN: dsn, Label: dsnLabel(driver, dsn)}, nil
}

// connectTimeout bounds how long a database may take to answer before a
// command reports it unreachable instead of waiting on the OS TCP timeout.
var connectTimeout = 10 * time.Second

// pingWithin pings conn, giving up after connectTimeout.
func pingWithin(ctx context.Context, conn *sql.DB) error {
	pctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	return conn.PingContext(pctx)
}

// dsnLabel names a connection for reports without leaking its password.
func dsnLabel(driver, dsn string) string {
	if driver == "pgx" {
		if u, err := url.Parse(dsn); err == nil && u.Host != "" {
			return strings.TrimPrefix(u.Path, "/") + "@" + u.Host
		}
	}
	if at := strings.LastIndex(dsn, "@"); at >= 0 {
		rest := dsn[at+1:]
		if q := strings.Index(rest, "?"); q >= 0 {
			rest = rest[:q]
		}
		host, name, _ := strings.Cut(rest, "/")
		host = strings.TrimSuffix(strings.TrimPrefix(host, "tcp("), ")")
		return name + "@" + host
	}
	return driver + " database"
}

func countMode(cmd *cli.Command) (compare.CountMode, error) {
	return compare.ParseCountMode(cmd.String("counts"))
}
