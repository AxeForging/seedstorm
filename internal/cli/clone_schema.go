package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/tui"
	"github.com/urfave/cli/v3"
)

func cloneSchemaCmd() *cli.Command {
	return &cli.Command{
		Name:  "clone-schema",
		Usage: "Copy schema-only structure from one database into another",
		Description: `Introspects a source database and creates matching tables in a target database.
This is same-engine schema cloning for local/test databases, not a lossless migration tool.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "source-db",
				Usage:   "Source database type: mysql or postgres",
				Value:   "postgres",
				Sources: cli.EnvVars("SEEDSTORM_SOURCE_DB"),
			},
			&cli.StringFlag{
				Name:     "source-dsn",
				Usage:    "Source data source name",
				Required: true,
				Sources:  cli.EnvVars("SEEDSTORM_SOURCE_DSN"),
			},
			&cli.StringFlag{
				Name:    "target-db",
				Usage:   "Target database type: mysql or postgres",
				Value:   "postgres",
				Sources: cli.EnvVars("SEEDSTORM_TARGET_DB"),
			},
			&cli.StringFlag{
				Name:     "target-dsn",
				Usage:    "Target data source name",
				Required: true,
				Sources:  cli.EnvVars("SEEDSTORM_TARGET_DSN"),
			},
			&cli.BoolFlag{
				Name:  "drop-existing",
				Usage: "Drop existing target tables before creating the cloned schema",
			},
			&cli.BoolFlag{
				Name:    "dry-run",
				Aliases: []string{"n"},
				Usage:   "Print DDL without executing it",
			},
			&cli.BoolFlag{
				Name:    "interactive",
				Aliases: []string{"i"},
				Usage:   "Review and confirm the clone in the terminal UI",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			log := logging.Log
			sourceType := normalizeDBType(cmd.String("source-db"))
			targetType := normalizeDBType(cmd.String("target-db"))
			opts := db.CloneOptions{
				DropExisting: cmd.Bool("drop-existing"),
				DryRun:       cmd.Bool("dry-run"),
			}
			if cmd.Bool("interactive") {
				return tui.RunClone(ctx, sourceType, cmd.String("source-dsn"), targetType, cmd.String("target-dsn"), opts)
			}
			result, err := db.CloneSchema(ctx, sourceType, cmd.String("source-dsn"), targetType, cmd.String("target-dsn"), opts)
			if err != nil {
				return err
			}
			if opts.DryRun {
				fmt.Println(strings.Join(result.Statements, ";\n") + ";")
				return nil
			}
			log.Info().Int("tables", result.Tables).Int("statements", len(result.Statements)).Msg("Schema cloned")
			return nil
		},
	}
}
