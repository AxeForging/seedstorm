package cli

import (
	"context"
	"fmt"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/urfave/cli/v3"
)

func introspectCmd() *cli.Command {
	return &cli.Command{
		Name:  "introspect",
		Usage: "Discover database schema and generate a schema YAML file",
		Description: `Connects to a MySQL or PostgreSQL database and introspects all tables,
columns, data types, primary keys, foreign keys, and enum values.
Outputs a schema.yaml that can be used for seeding or AI enrichment.`,
		Flags: []cli.Flag{
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
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			log := logging.Log
			dbType := normalizeDBType(cmd.String("db"))
			dsn := cmd.String("dsn")
			out := cmd.String("out")

			log.Info().
				Str("db", cmd.String("db")).
				Msg("Connecting to database")

			tables, err := db.Introspect(dbType, dsn)
			if err != nil {
				return fmt.Errorf("introspection failed: %w", err)
			}

			log.Info().
				Int("tables", len(tables)).
				Msg("Schema discovered")

			s := &schema.Schema{
				Tables: make(map[string]schema.Table, len(tables)),
			}

			for _, t := range tables {
				st := schema.Table{
					Columns: make(map[string]schema.Column, len(t.Columns)),
				}
				for _, c := range t.Columns {
					sc := schema.Column{
						Type:     c.Type,
						PK:       c.IsPK,
						Nullable: c.IsNullable,
						Faker:    faker.MapColumnToFaker(dbType, c),
					}
					if c.FK != nil {
						sc.FK = fmt.Sprintf("%s.%s", c.FK.TableName, c.FK.ColumnName)
					}
					st.Columns[c.Name] = sc
				}
				s.Tables[t.Name] = st
			}

			if err := schema.Save(out, s); err != nil {
				return fmt.Errorf("failed to save schema: %w", err)
			}

			log.Info().
				Str("path", out).
				Int("tables", len(tables)).
				Msg("Schema saved")

			return nil
		},
	}
}
