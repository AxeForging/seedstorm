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
	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/AxeForging/seedstorm/internal/tui"
	"github.com/urfave/cli/v3"
)

func gapsCmd() *cli.Command {
	return &cli.Command{
		Name:  "gaps",
		Usage: "Show unpopulated tables and optionally seed them",
		Description: `Connects to the database, queries row counts for every table in the
schema, and prints a gap analysis report showing which tables are empty.

Use --fill to seed only the empty tables (populated tables are skipped).
Use --fill --dry-run to preview the SQL without executing it.`,
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
				Usage:   "Rows to insert per empty table (when --fill is set)",
				Value:   100,
			},
			&cli.IntFlag{
				Name:  "enum-rows",
				Usage: "Rows per enum value for empty tables with enum columns (0 = use --rows)",
				Value: 0,
			},
			&cli.BoolFlag{
				Name:  "fill",
				Usage: "Seed all empty tables (populated tables are skipped)",
			},
			&cli.BoolFlag{
				Name:    "dry-run",
				Aliases: []string{"n"},
				Usage:   "Print SQL without executing (requires --fill)",
			},
			&cli.BoolFlag{
				Name:    "yes",
				Aliases: []string{"y"},
				Usage:   "Skip confirmation prompt (use with --fill)",
			},
			&cli.IntFlag{
				Name:  "batch-size",
				Usage: "Number of rows per INSERT statement (batched multi-row VALUES)",
				Value: 100,
			},
			&cli.BoolFlag{
				Name:    "interactive",
				Aliases: []string{"i"},
				Usage:   "Launch interactive TUI to select empty tables and configure filling",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			log := logging.Log
			schemaPath := cmd.String("schema")
			dbType := normalizeDBType(cmd.String("db"))
			dsn := cmd.String("dsn")
			rows := cmd.Int("rows")
			enumRows := cmd.Int("enum-rows")
			fill := cmd.Bool("fill")
			dryRun := cmd.Bool("dry-run")
			yes := cmd.Bool("yes")
			batchSize := cmd.Int("batch-size")

			log.Info().Str("path", schemaPath).Msg("Loading schema")
			s, err := schema.Load(schemaPath)
			if err != nil {
				return err
			}

			// Resolve full topological order (used for both analysis and fill).
			log.Info().Msg("Building dependency graph")
			g := graph.Build(s)
			allSorted, err := g.TopologicalSort()
			if err != nil {
				return err
			}

			// Connect to DB.
			log.Info().Str("db", cmd.String("db")).Msg("Connecting to database")
			dbConn, err := sql.Open(dbType, dsn)
			if err != nil {
				return fmt.Errorf("failed to open connection: %w", err)
			}
			defer dbConn.Close()
			if err := dbConn.PingContext(ctx); err != nil {
				return fmt.Errorf("failed to ping database: %w", err)
			}

			// Query current row counts for all tables.
			log.Info().Int("tables", len(allSorted)).Msg("Scanning tables")
			counts, err := db.GetTableRowCounts(ctx, dbConn, dbType, allSorted)
			if err != nil {
				return fmt.Errorf("row count scan failed: %w", err)
			}

			if cmd.Bool("interactive") {
				return tui.RunGaps(ctx, s, dbType, dsn, counts, rows, batchSize, enumRows)
			}

			// Build FK parents map for display: table → []parent tables.
			fkParents := buildFKParents(s, allSorted)

			// Identify gap tables in topological order.
			var gapTables []string
			for _, t := range allSorted {
				if counts[t] == 0 {
					gapTables = append(gapTables, t)
				}
			}

			// Print gap analysis report.
			printGapReport(allSorted, counts, fkParents, gapTables, rows)

			if len(gapTables) == 0 {
				fmt.Println("\nAll tables are populated — nothing to do.")
				return nil
			}

			if !fill {
				fmt.Println("\nRun with --fill to populate empty tables.")
				return nil
			}

			// --fill: seed only gap tables.
			if dryRun {
				log.Info().Msg("Dry-run mode — SQL will be printed, not executed")
			} else if !yes {
				fmt.Fprintf(os.Stderr,
					"\nAbout to seed %d empty tables (%d rows each). Type \"yes\" to continue: ",
					len(gapTables), rows)
				scanner := bufio.NewScanner(os.Stdin)
				scanner.Scan()
				if strings.TrimSpace(scanner.Text()) != "yes" {
					return fmt.Errorf("fill aborted")
				}
			}

			start := time.Now()
			log.Info().
				Int("rows", rows).
				Int("gap_tables", len(gapTables)).
				Msg("Generating fake data for empty tables")

			// Generate data for gap tables only; allSorted is used internally to
			// preload existing PKs from already-populated parent tables.
			data, err := faker.GenerateFiltered(s, allSorted, gapTables, rows, enumRows, dbConn, dbType)
			if err != nil {
				return fmt.Errorf("data generation failed: %w", err)
			}

			totalRows := 0
			for _, tableName := range gapTables {
				tableRows := data[tableName]
				log.Info().
					Str("table", tableName).
					Int("rows", len(tableRows)).
					Msg("Seeding table")

				if dryRun {
					for _, row := range tableRows {
						query, _ := buildInsert(tableName, row, dbType)
						fmt.Println(query)
					}
				} else {
					for i := 0; i < len(tableRows); i += batchSize {
						end := i + batchSize
						if end > len(tableRows) {
							end = len(tableRows)
						}
						query, values := buildBatchInsert(tableName, tableRows[i:end], dbType)
						if _, err := dbConn.ExecContext(ctx, query, values...); err != nil {
							return fmt.Errorf("insert into %s failed: %w", tableName, err)
						}
					}
				}
				totalRows += len(tableRows)
			}

			elapsed := time.Since(start).Round(time.Millisecond)
			log.Info().
				Int("tables", len(gapTables)).
				Int("total_rows", totalRows).
				Dur("duration", elapsed).
				Msg("Gap fill complete")

			return nil
		},
	}
}

// buildFKParents returns a map of table → list of parent table names referenced
// via FK columns in that table's schema definition.
func buildFKParents(s *schema.Schema, tableNames []string) map[string][]string {
	parents := make(map[string][]string, len(tableNames))
	for _, tableName := range tableNames {
		seen := make(map[string]bool)
		tbl := s.Tables[tableName]
		for _, col := range tbl.Columns {
			if col.FK == "" {
				continue
			}
			// FK format is "parent_table.column"
			parts := strings.SplitN(col.FK, ".", 2)
			if len(parts) == 2 && !seen[parts[0]] && parts[0] != tableName {
				seen[parts[0]] = true
				parents[tableName] = append(parents[tableName], parts[0])
			}
		}
	}
	return parents
}

// printGapReport prints the formatted gap analysis table to stdout.
func printGapReport(allSorted []string, counts map[string]int64, fkParents map[string][]string, gapTables []string, rowsPerTable int) {
	gapSet := make(map[string]bool, len(gapTables))
	for _, t := range gapTables {
		gapSet[t] = true
	}

	// Determine column widths.
	nameWidth := len("Table")
	for _, t := range allSorted {
		if len(t) > nameWidth {
			nameWidth = len(t)
		}
	}

	fmt.Println()
	fmt.Println("Gap Analysis")
	fmt.Println(strings.Repeat("─", nameWidth+50))
	fmt.Printf("  %-*s  %6s  %s\n", nameWidth, "Table", "Rows", "Status")
	fmt.Printf("  %-*s  %6s  %s\n", nameWidth, strings.Repeat("─", nameWidth), "──────", strings.Repeat("─", 40))

	totalGapRows := 0
	for _, tableName := range allSorted {
		count := counts[tableName]
		if gapSet[tableName] {
			deps := formatFKDeps(fkParents[tableName], gapSet, counts)
			fmt.Printf("  %-*s  %6d  EMPTY → would seed %d rows%s\n",
				nameWidth, tableName, count, rowsPerTable, deps)
			totalGapRows += rowsPerTable
		} else {
			fmt.Printf("  %-*s  %6d  populated\n", nameWidth, tableName, count)
		}
	}

	fmt.Println()
	if len(gapTables) > 0 {
		fmt.Printf("  Gaps: %d table(s) empty · Would seed: %d rows total\n",
			len(gapTables), totalGapRows)
	} else {
		fmt.Println("  No gaps found — all tables are populated.")
	}
}

// formatFKDeps returns a parenthetical annotation like " [FK → users, products]"
// showing which parent tables a gap table references, with a (filling) note if
// the parent is also empty.
func formatFKDeps(parents []string, gapSet map[string]bool, counts map[string]int64) string {
	if len(parents) == 0 {
		return ""
	}
	parts := make([]string, 0, len(parents))
	for _, p := range parents {
		if gapSet[p] {
			parts = append(parts, p+" (filling)")
		} else {
			parts = append(parts, fmt.Sprintf("%s (%d rows)", p, counts[p]))
		}
	}
	return "  [FK → " + strings.Join(parts, ", ") + "]"
}
