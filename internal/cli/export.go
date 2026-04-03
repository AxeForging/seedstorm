package cli

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/goccy/go-yaml"
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
				Usage:   "Output format: sql, csv, json",
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

			log.Info().Str("path", dataPath).Msg("Loading data file")

			raw, err := os.ReadFile(dataPath)
			if err != nil {
				return fmt.Errorf("failed to read data file: %w", err)
			}

			var data map[string][]map[string]interface{}
			if err := yaml.Unmarshal(raw, &data); err != nil {
				return fmt.Errorf("failed to parse data file: %w", err)
			}

			var output string
			switch strings.ToLower(format) {
			case "json":
				b, err := json.MarshalIndent(data, "", "  ")
				if err != nil {
					return fmt.Errorf("JSON marshal failed: %w", err)
				}
				output = string(b)
			case "csv":
				output, err = toCSV(data)
				if err != nil {
					return err
				}
			default: // sql
				var sb strings.Builder
				for tableName, rows := range data {
					for i := 0; i < len(rows); i += batchSize {
						end := i + batchSize
						if end > len(rows) {
							end = len(rows)
						}
						query, _ := buildBatchInsert(tableName, rows[i:end], dbType)
						sb.WriteString(query)
						sb.WriteString(";\n")
					}
				}
				output = sb.String()
			}

			if outPath == "" {
				fmt.Print(output)
			} else {
				if err := os.WriteFile(outPath, []byte(output), 0o644); err != nil {
					return fmt.Errorf("failed to write output: %w", err)
				}
				log.Info().Str("path", outPath).Str("format", format).Msg("Export complete")
			}

			return nil
		},
	}
}

func toCSV(data map[string][]map[string]interface{}) (string, error) {
	var sb strings.Builder
	w := csv.NewWriter(&sb)

	for tableName, rows := range data {
		if len(rows) == 0 {
			continue
		}
		// Header row: table_name, col1, col2, ...
		headers := []string{"_table"}
		for k := range rows[0] {
			headers = append(headers, k)
		}
		if err := w.Write(headers); err != nil {
			return "", err
		}
		for _, row := range rows {
			record := []string{tableName}
			for _, k := range headers[1:] {
				record = append(record, fmt.Sprintf("%v", row[k]))
			}
			if err := w.Write(record); err != nil {
				return "", err
			}
		}
	}
	w.Flush()
	return sb.String(), w.Error()
}
