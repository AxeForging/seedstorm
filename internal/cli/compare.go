package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/seeder"
)

func compareCmd() *cli.Command {
	flags := append(endpointFlags(),
		&cli.StringFlag{Name: "format", Aliases: []string{"f"}, Usage: "Output format: table or json", Value: "table"},
		&cli.BoolFlag{Name: "only-diff", Usage: "Hide tables whose row counts and columns match"},
	)
	return &cli.Command{
		Name:  "compare",
		Usage: "Compare row counts, sizes and columns between two databases",
		Description: `Reads every table on a source and a target database (read-only) and reports,
per table, row counts, on-disk size, the difference, and column-name drift.
Works across engines: tables are matched by name, case-insensitively.`,
		Flags: flags,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			mode, err := countMode(cmd)
			if err != nil {
				return err
			}
			source, target, err := openEndpoints(ctx, cmd)
			if err != nil {
				return err
			}
			defer source.Conn.Close()
			defer target.Conn.Close()

			report, err := seeder.Snapshots(ctx, source, target, mode, nil)
			if err != nil {
				return err
			}
			switch cmd.String("format") {
			case "json":
				return writeJSON(report)
			case "table", "":
				compare.RenderReport(os.Stdout, report, cmd.Bool("only-diff"))
				return nil
			default:
				return fmt.Errorf("unknown format %q (use table or json)", cmd.String("format"))
			}
		},
	}
}
