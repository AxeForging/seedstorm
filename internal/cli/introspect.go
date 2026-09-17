package cli

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/runerr"
	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/AxeForging/seedstorm/internal/seeder"
	"github.com/urfave/cli/v3"
)

func introspectCmd() *cli.Command {
	return &cli.Command{
		Name:  "introspect",
		Usage: "Discover database schema and generate a schema YAML file",
		Description: `Connects to a MySQL or PostgreSQL database and introspects all tables,
columns, data types, primary keys, foreign keys, and enum values.
Outputs a schema.yaml that can be used for seeding or AI enrichment.
--relationships <file> also measures every foreign key's shape (read-only,
exact) and writes it with estimated table counts to a snapshot file.`,
		Flags: append([]cli.Flag{
			&cli.StringFlag{
				Name:    "db",
				Usage:   "Database type: mysql or postgres",
				Value:   "postgres",
				Sources: cli.EnvVars("SEEDSTORM_DB"),
			},
			&cli.StringFlag{
				Name:     "dsn",
				Usage:    "Data source name (connection string)",
				Required: true,
				Sources:  cli.EnvVars("SEEDSTORM_DSN"),
			},
			&cli.StringFlag{
				Name:    "out",
				Aliases: []string{"o"},
				Usage:   "Output schema YAML file path",
				Value:   "schema.yaml",
			},
			&cli.StringFlag{
				Name:  "relationships",
				Usage: "Also measure foreign-key shapes and write them (with estimated counts) to this snapshot file",
			},
		}, relationshipFlags()...),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			log := logging.Log
			dbType := normalizeDBType(cmd.String("db"))
			dsn := cmd.String("dsn")
			out := cmd.String("out")

			log.Info().
				Str("db", cmd.String("db")).
				Msg("Connecting to database")
			conn, err := sql.Open(dbType, dsn)
			if err != nil {
				return fmt.Errorf("introspection failed: %w", err)
			}
			defer conn.Close()
			if err := pingWithin(ctx, conn); err != nil {
				return runerr.At(runerr.PhaseConnect, "", fmt.Errorf("%s did not answer: %w", dsnLabel(dbType, dsn), err))
			}
			log.Info().Msg("Reading the catalog")
			tables, err := db.IntrospectConn(ctx, conn, dbType, stepLogger("Introspecting", time.Now))
			if err != nil {
				return runerr.At(runerr.PhaseIntrospect, "", fmt.Errorf("introspection failed: %w", err))
			}

			log.Info().
				Int("tables", len(tables)).
				Msg("Schema discovered")

			s := faker.BuildSchema(dbType, tables)

			if err := schema.Save(out, s); err != nil {
				return fmt.Errorf("failed to save schema: %w", err)
			}

			log.Info().
				Str("path", out).
				Int("tables", len(tables)).
				Msg("Schema saved")

			if path := cmd.String("relationships"); path != "" {
				return writeRelationshipsSnapshot(ctx, cmd, seeder.Endpoint{Conn: conn, DBType: dbType, Label: dsnLabel(dbType, dsn), Schema: s}, path)
			}
			return nil
		},
	}
}
