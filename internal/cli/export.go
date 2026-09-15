package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/AxeForging/seedstorm/internal/dataio"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/urfave/cli/v3"
)

func exportCmd() *cli.Command {
	return &cli.Command{
		Name:  "export",
		Usage: "Export a generated data YAML file to SQL, CSV, or JSON",
		Description: `Reads a data YAML file (e.g. produced by 'generate') and converts it
to the desired output format. Supports sql, csv, and json.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "data",
				Aliases:  []string{"d"},
				Usage:    "Input data YAML file",
				Required: true,
			},
			&cli.StringFlag{
				Name:    "format",
				Aliases: []string{"f"},
				Usage:   "Output format: sql, csv, json, yaml",
				Value:   "sql",
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
				Name:  "batch-size",
				Usage: "Number of rows per INSERT statement for SQL output",
				Value: 100,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			log := logging.Log
			dataPath := cmd.String("data")
			format := cmd.String("format")
			outPath := cmd.String("out")
			dbType := normalizeDBType(cmd.String("db"))
			batchSize := cmd.Int("batch-size")

			log.Info().Str("path", dataPath).Msg("Reading data file")
			in, err := os.Open(dataPath)
			if err != nil {
				return fmt.Errorf("failed to read data file: %w", err)
			}
			defer func() { _ = in.Close() }()
			out, commit, err := openOutput(outPath)
			if err != nil {
				return err
			}
			defer func() { _ = commit(false) }()
			w, err := dataio.NewWriter(out, format, dbType, batchSize)
			if err != nil {
				return err
			}
			// The file is read and written a chunk at a time, whatever its size.
			tables, total := 0, 0
			err = dataio.ReadTables(in, 0, func(table string, rows []map[string]interface{}) error {
				if rows == nil {
					tables++
					return w.Table(table)
				}
				total += len(rows)
				return w.Rows(rows)
			})
			if err != nil {
				return fmt.Errorf("failed to parse data file: %w", err)
			}
			if err := w.Close(); err != nil {
				return err
			}
			if err := commit(true); err != nil {
				return err
			}
			if outPath != "" {
				log.Info().Str("path", outPath).Str("format", format).Int("tables", tables).Int("rows", total).Msg("Export complete")
			}
			return nil
		},
	}
}
