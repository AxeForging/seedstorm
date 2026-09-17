package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/logging"
)

func snapshotCmd() *cli.Command {
	return &cli.Command{
		Name:  "snapshot",
		Usage: "Save every table's row count and size to a file for later compare or mirror",
		Description: `Reads one database (read-only) and writes a table-counts snapshot: row count,
on-disk size and column names per table, wrapped in a versioned envelope
(kind: seedstorm.table-counts). --relationships adds each foreign key's shape
(children per parent: min, avg, p50, p95, max, histogram) and writes version 2. Pass the file to
"compare --source-snapshot" or "mirror --source-snapshot" to follow a database
you cannot (or should not) connect to at mirror time.

A hand-written file with only row counts also works:

  tables:
    users: 1200
    orders: 5000`,
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "db", Usage: "Database type: mysql or postgres", Value: "postgres", Sources: cli.EnvVars("SEEDSTORM_DB")},
			&cli.StringFlag{Name: "dsn", Usage: "Data source name (connection string)", Required: true, Sources: cli.EnvVars("SEEDSTORM_DSN")},
			&cli.StringFlag{Name: "counts", Usage: "Row counts: exact (COUNT(*)) or estimate (planner statistics, fast on large tables)", Value: "exact"},
			&cli.StringFlag{Name: "format", Aliases: []string{"f"}, Usage: "Output format: yaml or json", Value: compare.FormatYAML},
			&cli.StringFlag{Name: "out", Aliases: []string{"o"}, Usage: "Write the snapshot to this file (default: stdout)"},
			&cli.BoolFlag{Name: "relationships", Usage: "Also measure every foreign key's shape (read-only; exact or estimate per --counts)"},
		}, relationshipFlags()...),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			log := logging.Log
			mode, err := countMode(cmd)
			if err != nil {
				return err
			}
			format := strings.ToLower(cmd.String("format"))
			if format != compare.FormatYAML && format != compare.FormatJSON {
				return fmt.Errorf("unknown format %q (use yaml or json)", cmd.String("format"))
			}
			ep, err := openEndpoint(ctx, cmd.String("db"), cmd.String("dsn"))
			if err != nil {
				return err
			}
			defer closeEndpoints(ep)

			log.Info().Str("database", ep.Label).Str("counts", string(mode)).Msg("Reading table counts")
			snap, err := compare.Take(ctx, ep.Conn, ep.DBType, ep.Label, mode, stepLogger("Counting", time.Now))
			if err != nil {
				return err
			}
			if cmd.Bool("relationships") {
				logScanServer(ctx, ep)
				if snap.Relationships, err = ep.Shapes(ctx, relationshipOptions(cmd, mode, "")); err != nil {
					return err
				}
				logShapeSummary(snap.Relationships)
			}
			data, err := compare.EncodeSnapshot(snap, format)
			if err != nil {
				return err
			}
			out := cmd.String("out")
			if out == "" {
				_, err = os.Stdout.Write(data)
				return err
			}
			if err := os.WriteFile(out, data, 0o644); err != nil { //nolint:gosec // a counts file holds no secrets
				return fmt.Errorf("write snapshot: %w", err)
			}
			log.Info().Str("path", out).Int("tables", len(snap.Tables)).Msg("Snapshot saved")
			return nil
		},
	}
}
