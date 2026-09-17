package cli

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/runerr"
	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/AxeForging/seedstorm/internal/seeder"
	"github.com/AxeForging/seedstorm/internal/tui"
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
			&cli.IntFlag{
				Name:    "rows",
				Aliases: []string{"r"},
				Usage:   "Number of rows to insert per table",
				Value:   100,
			},
			&cli.StringSliceFlag{
				Name:  "table-rows",
				Usage: "Per-table row override, repeatable or comma-separated (table=rows)",
			},
			&cli.IntFlag{
				Name:  "enum-rows",
				Usage: "Rows per enum value for tables with enum columns (0 = use --rows)",
				Value: 0,
			},
			&cli.IntFlag{
				Name:  "self-ref-depth",
				Usage: "Maximum generated depth for self-referential FK chains",
				Value: faker.DefaultSelfRefDepth,
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
			&cli.BoolFlag{
				Name:  "truncate",
				Usage: "Truncate all tables before seeding (prompts for confirmation unless --yes is set)",
			},
			&cli.BoolFlag{
				Name:    "yes",
				Aliases: []string{"y"},
				Usage:   "Skip confirmation prompt (use with --truncate)",
			},
			&cli.IntFlag{
				Name:  "batch-size",
				Usage: "Number of rows per INSERT statement (batched multi-row VALUES)",
				Value: seeder.DefaultBatchSize,
			},
			&cli.IntFlag{
				Name:  "seed",
				Usage: "Random seed for reproducible data generation (0 = random)",
				Value: 0,
			},
			&cli.BoolFlag{
				Name:    "interactive",
				Aliases: []string{"i"},
				Usage:   "Launch interactive TUI to select tables and configure seeding",
			},
			workersFlag(),
			genWorkersFlag(),
			profileFlag(),
			shapeRowsFlag(),
			productionFlags()[0],
			productionFlags()[1],
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			log := logging.Log
			schemaPath := cmd.String("schema")
			dbType := normalizeDBType(cmd.String("db"))
			dsn := cmd.String("dsn")
			rows := cmd.Int("rows")
			tableRows, err := parseTableRows(cmd.StringSlice("table-rows"))
			if err != nil {
				return err
			}
			enumRows := cmd.Int("enum-rows")
			selfRefDepth := cmd.Int("self-ref-depth")
			disableFK := cmd.Bool("disable-fk")
			dryRun := cmd.Bool("dry-run")
			truncate := cmd.Bool("truncate")
			yes := cmd.Bool("yes")
			batchSize := cmd.Int("batch-size")
			seed := cmd.Int("seed")
			if !dryRun {
				if err := refuseProductionWrite(cmd, "seed it"); err != nil {
					return err
				}
			}

			if seed != 0 {
				faker.SeedRandom(int64(seed))
				log.Info().Int("seed", seed).Msg("Using fixed random seed")
			}

			log.Info().Str("path", schemaPath).Msg("Loading schema")
			s, err := schema.Load(schemaPath)
			if err != nil {
				return err
			}

			profile, err := loadProfile(cmd, s)
			if err != nil {
				return err
			}
			tableRows = profile.tableRows(tableRows)

			if cmd.Bool("interactive") {
				return tui.Run(ctx, s, dbType, dsn, rows, batchSize, enumRows, truncate, selfRefDepth, profile.tui())
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

			if err := pingWithin(ctx, dbConn); err != nil {
				return runerr.At(runerr.PhaseConnect, "", fmt.Errorf("%s did not answer: %w", dsnLabel(dbType, dsn), err))
			}
			workers, err := workersFromFlag(ctx, cmd, dbConn, dbType)
			if err != nil {
				return err
			}

			// Every table stays in the preload so FKs can reference rows of ignored
			// tables; only the kept ones are written.
			allTables := sortedTables
			if sortedTables, err = profile.applyIgnore(ctx, dbConn, dbType, sortedTables); err != nil {
				return err
			}

			derivedRows := deriveShapedRows(cmd, s, sortedTables, rows, tableRows, profile.shapes)
			if dryRun {
				log.Info().Msg("Dry-run mode — SQL will be printed, not executed")
				fmt.Print(graph.RenderPlanWithCounts(s, sortedTables, rows, planCounts(tableRows, derivedRows)))
				fmt.Println("--- SQL ---")
			}

			// Refuse tables that cannot be generated before anything is truncated.
			if err := faker.CheckSeedable(s, sortedTables, profile.overrides); err != nil {
				return err
			}

			// Truncate tables before seeding
			if truncate && !dryRun {
				if !yes {
					fmt.Fprintf(os.Stderr, "\nAbout to TRUNCATE %d tables. All existing data will be deleted.\nType \"yes\" to continue or press Ctrl+C to abort: ", len(sortedTables))
					scanner := bufio.NewScanner(os.Stdin)
					scanner.Scan()
					if strings.TrimSpace(scanner.Text()) != "yes" {
						return fmt.Errorf("truncate aborted")
					}
				}
				log.Info().Int("tables", len(sortedTables)).Msg("Truncating tables")
				if err := db.TruncateConcurrently(ctx, dbConn, dbType, sortedTables, workers, nil); err != nil {
					return runerr.At(runerr.PhaseTruncate, "", fmt.Errorf("truncate failed: %w", err))
				}
				log.Info().Msg("Truncate complete")
			}

			// Rows are generated and written chunk by chunk (memory stays flat for
			// any --rows), on --workers connections at once.
			start := time.Now()
			log.Info().Int("tables", len(sortedTables)).Int("rows", rows).Int("workers", workers).Msg("Seeding: generating and writing in chunks")
			if !dryRun {
				defer syncSequences(ctx, dbConn, dbType, sortedTables)
			}
			onProgress, onTable := progressLogger(time.Now)
			res, err := seeder.Seed(ctx, dbConn, dbType, s, allTables, sortedTables, seeder.SeedOptions{
				Rows: rows, EnumRows: enumRows, TableRows: tableRows, DerivedRows: derivedRows, BatchSize: batchSize, DryRun: dryRun,
				Workers: workers, OnProgress: onProgress, OnTable: onTable,
				GenWorkers: genWorkers(cmd), Reproducible: cmd.Int("seed") != 0,
				Generate: faker.GenerateOptions{
					SelfRefDepth: selfRefDepth,
					Overrides:    profile.overrides,
					Shapes:       profile.shapes,
					OnWarning:    logWarning,
				},
				OnRows:   printDryRunSQL(dryRun, dbType),
				OnNotice: func(msg string) { log.Warn().Msg(msg) },
				OnTableStart: func(table string) error {
					log.Info().Str("table", table).Msg("Seeding table")
					return nil
				},
			})
			if err != nil {
				if !dryRun {
					logPartialRun(res, sortedTables)
				}
				return err
			}

			elapsed := time.Since(start).Round(time.Millisecond)
			log.Info().
				Int("tables", len(sortedTables)).
				Int("total_rows", res.Total).
				Dur("duration", elapsed).
				Msg("Seeding complete")

			if !dryRun {
				logShapeResults(ctx, dbConn, dbType, s, profile.shapes)
			}

			// Summary
			for _, tableName := range sortedTables {
				log.Info().
					Str("table", tableName).
					Int("rows", res.Counts[tableName]).
					Msg("  ↳ inserted")
			}

			return nil
		},
	}
}
