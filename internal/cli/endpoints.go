package cli

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/seeder"
)

// endpointFlags are the source/target connection flags shared by compare and
// mirror (named like clone-schema's).
func endpointFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "source-db", Usage: "Source database type: mysql or postgres", Value: "postgres", Sources: cli.EnvVars("SEEDSTORM_SOURCE_DB")},
		&cli.StringFlag{Name: "source-dsn", Usage: "Source data source name", Required: true, Sources: cli.EnvVars("SEEDSTORM_SOURCE_DSN")},
		&cli.StringFlag{Name: "target-db", Usage: "Target database type: mysql or postgres", Value: "postgres", Sources: cli.EnvVars("SEEDSTORM_TARGET_DB")},
		&cli.StringFlag{Name: "target-dsn", Usage: "Target data source name", Required: true, Sources: cli.EnvVars("SEEDSTORM_TARGET_DSN")},
		&cli.StringFlag{Name: "counts", Usage: "Row counts: exact (COUNT(*)) or estimate (planner statistics, fast on large tables)", Value: "exact"},
	}
}

// openEndpoints connects to both sides. The caller closes the connections.
func openEndpoints(ctx context.Context, cmd *cli.Command) (source, target seeder.Endpoint, err error) {
	source, err = openEndpoint(ctx, cmd.String("source-db"), cmd.String("source-dsn"))
	if err != nil {
		return source, target, fmt.Errorf("source: %w", err)
	}
	target, err = openEndpoint(ctx, cmd.String("target-db"), cmd.String("target-dsn"))
	if err != nil {
		_ = source.Conn.Close()
		return source, target, fmt.Errorf("target: %w", err)
	}
	return source, target, nil
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
	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return seeder.Endpoint{}, fmt.Errorf("ping database: %w", err)
	}
	return seeder.Endpoint{Conn: conn, DBType: driver, DSN: dsn, Label: dsnLabel(driver, dsn)}, nil
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
