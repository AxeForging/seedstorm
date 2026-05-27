package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/AxeForging/seedstorm/internal/tui"
	"github.com/brianvoe/gofakeit/v6"
	"github.com/goccy/go-yaml"
	"github.com/urfave/cli/v3"
)

func generateCmd() *cli.Command {
	return &cli.Command{
		Name:  "generate",
		Usage: "Generate fake data without inserting into the database",
		Description: `Generates fake data rows based on a schema YAML and writes them to a file
(YAML, JSON, or SQL format). Useful for inspecting generated data before seeding.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "schema",
				Aliases: []string{"s"},
				Usage:   "Schema YAML file",
				Value:   "schema.yaml",
			},
			&cli.IntFlag{
				Name:    "rows",
				Aliases: []string{"r"},
				Usage:   "Rows per table",
				Value:   10,
			},
			&cli.StringSliceFlag{
				Name:  "table-rows",
				Usage: "Per-table row override, repeatable or comma-separated (table=rows)",
			},
			&cli.IntFlag{
				Name:  "self-ref-depth",
				Usage: "Maximum generated depth for self-referential FK chains",
				Value: faker.DefaultSelfRefDepth,
			},
			&cli.StringFlag{
				Name:    "format",
				Aliases: []string{"f"},
				Usage:   "Output format: yaml, json, sql",
				Value:   "yaml",
			},
			&cli.StringFlag{
				Name:    "out",
				Aliases: []string{"o"},
				Usage:   "Output file (default: stdout)",
				Value:   "",
			},
			&cli.StringFlag{
				Name:  "db",
				Usage: "Database type for SQL output: mysql or postgres",
				Value: "postgres",
			},
			&cli.IntFlag{
				Name:  "seed",
				Usage: "Random seed for reproducible data generation (0 = random)",
				Value: 0,
			},
			&cli.BoolFlag{
				Name:    "interactive",
				Aliases: []string{"i"},
				Usage:   "Launch interactive TUI to select tables and configure generation",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			log := logging.Log
			schemaPath := cmd.String("schema")
			rows := cmd.Int("rows")
			tableRows, err := parseTableRows(cmd.StringSlice("table-rows"))
			if err != nil {
				return err
			}
			selfRefDepth := cmd.Int("self-ref-depth")
			format := cmd.String("format")
			outPath := cmd.String("out")
			dbType := normalizeDBType(cmd.String("db"))
			seed := cmd.Int("seed")

			if seed != 0 {
				gofakeit.Seed(int64(seed))
				log.Info().Int("seed", seed).Msg("Using fixed random seed")
			}

			log.Info().Str("path", schemaPath).Msg("Loading schema")
			s, err := schema.Load(schemaPath)
			if err != nil {
				return err
			}

			if cmd.Bool("interactive") {
				return tui.RunGenerate(ctx, s, dbType, format, outPath, rows, selfRefDepth)
			}

			log.Info().Msg("Building dependency graph")
			g := graph.Build(s)
			sortedTables, err := g.TopologicalSort()
			if err != nil {
				return err
			}

			log.Info().Int("rows", rows).Msg("Generating data")
			data, err := faker.GenerateFilteredWithOptions(s, sortedTables, sortedTables, rows, 0, tableRows, nil, dbType, faker.GenerateOptions{
				SelfRefDepth: selfRefDepth,
			})
			if err != nil {
				return fmt.Errorf("generation failed: %w", err)
			}

			var output string
			switch strings.ToLower(format) {
			case "json":
				b, err := json.MarshalIndent(data, "", "  ")
				if err != nil {
					return fmt.Errorf("JSON marshal failed: %w", err)
				}
				output = string(b)
			case "sql":
				var sb strings.Builder
				for _, tableName := range sortedTables {
					for _, row := range data[tableName] {
						query, _ := buildInsert(tableName, row, dbType)
						sb.WriteString(query)
						sb.WriteString(";\n")
					}
				}
				output = sb.String()
			default: // yaml
				b, err := yaml.Marshal(data)
				if err != nil {
					return fmt.Errorf("YAML marshal failed: %w", err)
				}
				output = string(b)
			}

			if outPath == "" {
				fmt.Print(output)
			} else {
				if err := os.WriteFile(outPath, []byte(output), 0o644); err != nil {
					return fmt.Errorf("failed to write output: %w", err)
				}
				log.Info().
					Str("path", outPath).
					Str("format", format).
					Msg("Data written")
			}

			return nil
		},
	}
}
