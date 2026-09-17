package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/relations"
	"github.com/AxeForging/seedstorm/internal/seeder"
)

func compareCmd() *cli.Command {
	flags := append(endpointFlags(),
		&cli.StringFlag{Name: "format", Aliases: []string{"f"}, Usage: "Output format: table or json", Value: "table"},
		&cli.BoolFlag{Name: "only-diff", Usage: "Hide tables (and relationships) that match"},
		&cli.BoolFlag{Name: "relationships", Usage: "Also compare every foreign key's shape (children per parent) on both sides; a snapshot source must include them"},
	)
	flags = append(flags, relationshipFlags()...)
	return &cli.Command{
		Name:  "compare",
		Usage: "Compare row counts, sizes and columns between two databases",
		Description: `Reads every table on a source and a target database (read-only) and reports,
per table, row counts, on-disk size, the difference, and column-name drift.
Works across engines: tables are matched by name, case-insensitively.
The source can be a file made by "seedstorm snapshot" (--source-snapshot)
instead of a live database. --relationships adds a per-foreign-key shape
comparison (average, p95 and max children per parent), measured read-only.`,
		Flags: flags,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			mode, err := countMode(cmd)
			if err != nil {
				return err
			}
			logging.Log.Info().Msg("Connecting to source and target")
			source, target, err := openEndpoints(ctx, cmd)
			if err != nil {
				return err
			}
			defer closeEndpoints(source, target)

			logging.Log.Info().Str("source", source.Label).Str("target", target.Label).Str("counts", string(mode)).Msg("Reading table volumes")
			report, err := seeder.Snapshots(ctx, source, target, mode, sideStepLogger("Counting", time.Now))
			if err != nil {
				return err
			}
			if cmd.Bool("relationships") {
				logging.Log.Info().Msg("Comparing relationship shapes")
				logScanServer(ctx, source, target)
				opts := relationshipOptions(cmd, mode, "")
				steps := map[string]func(int, int, relations.Shape){
					"source": relationshipOptions(cmd, mode, "source").OnEdge,
					"target": relationshipOptions(cmd, mode, "target").OnEdge,
				}
				opts.OnEdge = nil
				report.Relationships, err = seeder.CompareShapes(ctx, source, target, opts, func(side string, done, total int, s relations.Shape) {
					steps[side](done, total, s)
				})
				if err != nil {
					return err
				}
			}
			switch cmd.String("format") {
			case "json":
				return writeJSON(report)
			case "table", "":
				compare.RenderReport(os.Stdout, report, cmd.Bool("only-diff"))
				if cmd.Bool("relationships") {
					compare.RenderShapeDrift(os.Stdout, report.Relationships, cmd.Bool("only-diff"))
				}
				return nil
			default:
				return fmt.Errorf("unknown format %q (use table or json)", cmd.String("format"))
			}
		},
	}
}
