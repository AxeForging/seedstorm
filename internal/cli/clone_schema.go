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
				Name:  "views",
				Usage: "Also clone views (and Postgres materialized views, created WITH NO DATA)",
			},
			&cli.BoolFlag{
				Name:  "routines",
				Usage: "Also clone functions and procedures",
			},
			&cli.BoolFlag{
				Name:  "triggers",
				Usage: "Also clone triggers",
			},
			&cli.StringFlag{
				Name:  "objects",
				Usage: "Comma-separated object kinds to clone: views, routines, triggers, or all",
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
			objects, err := db.ParseCloneObjects(cmd.String("objects"))
			if err != nil {
				return err
			}
			objects.Views = objects.Views || cmd.Bool("views")
			objects.Routines = objects.Routines || cmd.Bool("routines")
			objects.Triggers = objects.Triggers || cmd.Bool("triggers")
			opts := db.CloneOptions{
				DropExisting: cmd.Bool("drop-existing"),
				DryRun:       cmd.Bool("dry-run"),
				Objects:      objects,
			}
			if cmd.Bool("interactive") {
				return tui.RunClone(ctx, sourceType, cmd.String("source-dsn"), targetType, cmd.String("target-dsn"), opts)
			}
			result, err := db.CloneSchema(ctx, sourceType, cmd.String("source-dsn"), targetType, cmd.String("target-dsn"), opts)
			if err != nil {
				return err
			}
			for _, skipped := range result.Skipped {
				log.Warn().Str("kind", string(skipped.Kind)).Str("name", skipped.Name).Str("reason", skipped.Reason).Msg("Object not cloned")
			}
			if opts.DryRun {
				fmt.Println(strings.Join(result.Statements, ";\n") + ";")
				return nil
			}
			event := log.Info().Int("tables", result.Tables).Int("statements", len(result.Statements))
			if opts.Objects.Any() {
				event = event.Int("objects", result.Objects).Int("skipped", len(result.Skipped))
			}
			event.Msg("Schema cloned")
			return nil
		},
	}
}
