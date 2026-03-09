package cli

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/urfave/cli/v3"
)

func seedCmd() *cli.Command {
	return &cli.Command{
		Name:  "seed",
		Usage: "Generate and insert fake data directly into the database",
		Description: `Loads a schema YAML, resolves FK insertion order via topological sort,
generates fake data using gofakeit, and inserts rows into the database.
Use --dry-run to print SQL statements without executing them.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "schema",
				Aliases: []string{"s"},
				Usage:   "Schema YAML file",
				Value:   "schema.yaml",
			},
			&cli.StringFlag{
				Name:     "db",
				Usage:    "Database type: mysql or postgres",
				Value:    "postgres",
				Sources:  cli.EnvVars("SEEDSTORM_DB"),
			},
			&cli.StringFlag{
				Name:     "dsn",
				Usage:    "Data source name (connection string)",
				Required: true,
				Sources:  cli.EnvVars("SEEDSTORM_DSN"),
			},
			&cli.IntFlag{
				Name:    "rows",
				Aliases: []string{"r"},
				Usage:   "Number of rows to insert per table",
				Value:   100,
			},
			&cli.IntFlag{
				Name:  "enum-rows",
				Usage: "Rows per enum value for tables with enum columns (0 = use --rows)",
				Value: 0,
			},
			&cli.BoolFlag{
				Name:  "disable-fk",
				Usage: "Skip FK ordering (seed in arbitrary order)",
			},
			&cli.BoolFlag{
				Name:    "dry-run",
				Aliases: []string{"n"},
				Usage:   "Print SQL without executing",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			log := logging.Log
			schemaPath := cmd.String("schema")
			dbType := normalizeDBType(cmd.String("db"))
			dsn := cmd.String("dsn")
			rows := cmd.Int("rows")
			enumRows := cmd.Int("enum-rows")
			disableFK := cmd.Bool("disable-fk")
			dryRun := cmd.Bool("dry-run")

			log.Info().Str("path", schemaPath).Msg("Loading schema")
			s, err := schema.Load(schemaPath)
			if err != nil {
				return err
			}

			// Resolve seed order
			var sortedTables []string
			if disableFK {
				for name := range s.Tables {
					sortedTables = append(sortedTables, name)
				}
				log.Debug().Msg("FK ordering disabled — using arbitrary table order")
			} else {
				log.Info().Msg("Building dependency graph")
				g := graph.Build(s)
				sortedTables, err = g.TopologicalSort()
				if err != nil {
					return err
				}
				log.Info().
					Str("order", strings.Join(sortedTables, " → ")).
					Msg("Seed order resolved")
			}

			// Connect to DB
			log.Info().Str("db", cmd.String("db")).Msg("Connecting to database")
			dbConn, err := sql.Open(dbType, dsn)
			if err != nil {
				return fmt.Errorf("failed to open connection: %w", err)
			}
			defer dbConn.Close()

			if err := dbConn.PingContext(ctx); err != nil {
				return fmt.Errorf("failed to ping database: %w", err)
			}

			if dryRun {
				log.Info().Msg("Dry-run mode — SQL will be printed, not executed")
			}

			// Generate data
			start := time.Now()
			log.Info().Int("rows", rows).Msg("Generating fake data")
			data, err := faker.Generate(s, sortedTables, rows, enumRows, dbConn)
			if err != nil {
				return fmt.Errorf("data generation failed: %w", err)
			}

			// Insert data
			totalRows := 0
			for _, tableName := range sortedTables {
				tableRows := data[tableName]
				log.Info().
					Str("table", tableName).
					Int("rows", len(tableRows)).
					Msg("Seeding table")

				for _, row := range tableRows {
					query, values := buildInsert(tableName, row, dbType)
					if dryRun {
						fmt.Println(query)
						continue
					}
					if _, err := dbConn.ExecContext(ctx, query, values...); err != nil {
						return fmt.Errorf("insert into %s failed: %w", tableName, err)
					}
				}
				totalRows += len(tableRows)
			}

			elapsed := time.Since(start).Round(time.Millisecond)
			log.Info().
				Int("tables", len(sortedTables)).
				Int("total_rows", totalRows).
				Dur("duration", elapsed).
				Msg("Seeding complete")

			// Summary
			for _, tableName := range sortedTables {
				log.Info().
					Str("table", tableName).
					Int("rows", len(data[tableName])).
					Msg("  ↳ inserted")
			}

			return nil
		},
	}
}

func buildInsert(tableName string, row map[string]interface{}, dbType string) (string, []interface{}) {
	columns := make([]string, 0, len(row))
	placeholders := make([]string, 0, len(row))
	values := make([]interface{}, 0, len(row))

	i := 1
	for colName, val := range row {
		columns = append(columns, colName)
		if dbType == "pgx" {
			placeholders = append(placeholders, fmt.Sprintf("$%d", i))
		} else {
			placeholders = append(placeholders, "?")
		}
		values = append(values, val)
		i++
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		tableName,
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "),
	)
	return query, values
}
