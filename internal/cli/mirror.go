package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/urfave/cli/v3"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/profiles"
	"github.com/AxeForging/seedstorm/internal/rules"
	"github.com/AxeForging/seedstorm/internal/seeder"
	"github.com/AxeForging/seedstorm/internal/tui"
)

func mirrorCmd() *cli.Command {
	flags := append(endpointFlags(),
		&cli.StringFlag{Name: "mode", Usage: "topup (keep target rows, insert the shortfall) or reset (truncate affected target tables first)", Value: string(compare.ModeTopUp)},
		&cli.FloatFlag{Name: "scale", Usage: "Multiply source volumes (0.1 = a tenth, 2 = double)", Value: 1},
		&cli.Int64Flag{Name: "max-rows", Usage: "Cap any single table's target volume (0 = no cap)"},
		&cli.Int64Flag{Name: "parent-rows", Usage: "Rows for a required FK parent that is empty on the target", Value: compare.DefaultParentRows},
		&cli.StringSliceFlag{Name: "tables", Usage: "Limit to these tables, repeatable or comma-separated (default: all)"},
		profileFlag(),
		&cli.IntFlag{Name: "batch-size", Usage: "Rows per INSERT statement", Value: seeder.DefaultBatchSize},
		workersFlag(),
		&cli.IntFlag{Name: "self-ref-depth", Usage: "Maximum generated depth for self-referential FK chains", Value: faker.DefaultSelfRefDepth},
		&cli.BoolFlag{Name: "dry-run", Aliases: []string{"n"}, Usage: "Print the plan and sample rows without writing"},
		&cli.IntFlag{Name: "preview-rows", Usage: "Sample rows per table shown by --dry-run", Value: 3},
		&cli.StringFlag{Name: "format", Aliases: []string{"f"}, Usage: "Output format: table or json", Value: "table"},
		&cli.BoolFlag{Name: "stop-on-error", Usage: "Abort at the first rejected insert instead of retrying row by row and continuing"},
		&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "Skip the confirmation prompt for --mode reset"},
		&cli.IntFlag{Name: "seed", Usage: "Random seed for reproducible data generation (0 = random)"},
		&cli.BoolFlag{Name: "interactive", Aliases: []string{"i"}, Usage: "Review the plan, preview samples and confirm in the terminal UI"},
	)
	flags = append(flags, productionFlags()...)
	return &cli.Command{
		Name:  "mirror",
		Usage: "Seed a target database so its table volumes follow a source database",
		Description: `Compares source and target, then generates fake rows on the TARGET so each
table reaches the source's row count (times --scale). The source is only read.
Rows are generated from the target's own schema, optionally shaped by a seed
profile, in FK-safe order. Rejected inserts are retried row by row; tables that
cannot progress are reported instead of blocking the run.
The source can be a file made by "seedstorm snapshot" (--source-snapshot); the
same-database safety check cannot run then, so double-check --target-dsn.`,
		Flags: flags,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			log := logging.Log
			mode, err := compare.ParseMirrorMode(cmd.String("mode"))
			if err != nil {
				return err
			}
			if !cmd.Bool("dry-run") {
				if err := refuseProductionWrite(cmd, "mirror into the target"); err != nil {
					return err
				}
			}
			counts, err := countMode(cmd)
			if err != nil {
				return err
			}
			format := cmd.String("format")
			if format != "table" && format != "json" {
				return fmt.Errorf("unknown format %q (use table or json)", format)
			}
			if seed := cmd.Int("seed"); seed != 0 {
				faker.SeedRandom(int64(seed))
			}
			var profile *rules.RuleSet
			if ref := cmd.String("profile"); ref != "" {
				store, err := profileStore()
				if err != nil {
					return err
				}
				if profile, err = profiles.Resolve(ref, store); err != nil {
					return err
				}
			}

			source, target, err := openEndpoints(ctx, cmd)
			if err != nil {
				return err
			}
			defer closeEndpoints(source, target)

			log.Info().Str("source", source.Label).Str("target", target.Label).Msg("Comparing databases")
			job, err := seeder.PrepareMirror(ctx, source, target, seeder.MirrorConfig{
				OnCount: sideStepLogger("Counting", time.Now),
				Options: compare.MirrorOptions{
					Mode:       mode,
					Scale:      cmd.Float("scale"),
					MaxRows:    cmd.Int64("max-rows"),
					ParentRows: cmd.Int64("parent-rows"),
					Tables:     splitList(cmd.StringSlice("tables")),
				},
				CountMode: counts,
				Profile:   profile,
			})
			if err != nil {
				return err
			}
			for _, notice := range job.Servers.Notices() {
				log.Warn().Msg(notice)
			}
			if job.SameDatabaseUnchecked {
				log.Warn().Str("source", source.Label).Msg("Source is a snapshot file: cannot check that source and target are different databases")
			}
			for _, issue := range job.Issues {
				if issue.Severity == rules.SeverityWarning {
					log.Warn().Str("path", issue.Path).Msg(issue.Message)
				}
			}
			workers, err := workersFromFlag(ctx, cmd, target.Conn, target.DBType)
			if err != nil {
				return err
			}
			runOpts := seeder.Options{
				BatchSize:   cmd.Int("batch-size"),
				Workers:     workers,
				StopOnError: cmd.Bool("stop-on-error"),
				Generate:    faker.GenerateOptions{SelfRefDepth: cmd.Int("self-ref-depth")},
			}

			if cmd.Bool("interactive") {
				return tui.RunMirror(ctx, job, runOpts, cmd.Int("preview-rows"), cmd.Bool("dry-run"))
			}
			if cmd.Bool("dry-run") {
				return printMirrorDryRun(job, cmd.Int("preview-rows"), cmd.Int("self-ref-depth"), format)
			}
			if format == "table" {
				compare.RenderPlan(os.Stdout, job.Plan)
				fmt.Println()
			}
			if job.Plan.TotalInsert == 0 {
				if format == "json" {
					return writeJSON(map[string]any{"plan": job.Plan, "result": seeder.Result{}})
				}
				return nil
			}
			if mode == compare.ModeReset && !cmd.Bool("yes") {
				if err := confirmReset(target.Label, job.Plan.Truncate); err != nil {
					return err
				}
			}

			start := time.Now()
			logProgress, _ := progressLogger(time.Now)
			runOpts.OnProgress = func(p seeder.Progress) {
				logProgress(p)
				if p.Inserted >= p.Requested {
					log.Info().Str("table", p.Table).Int64("rows", p.Inserted).Msg(fmt.Sprintf("[%d/%d] filled", p.TableIndex, p.Tables))
				}
			}
			result, runErr := job.Run(ctx, runOpts, func(done, total int, table string) {
				log.Info().Int("done", done).Int("total", total).Msg("Truncating target tables")
			})
			log.Info().Int64("inserted", result.Inserted).Dur("duration", time.Since(start).Round(time.Millisecond)).Msg("Mirror finished")
			if format == "json" {
				if err := writeJSON(map[string]any{"plan": job.Plan, "result": result}); err != nil {
					return err
				}
			} else {
				seeder.RenderResult(os.Stdout, result)
			}
			if runErr != nil {
				return runErr
			}
			if problems := result.Problems(); len(problems) > 0 {
				return fmt.Errorf("mirror incomplete: %d table(s) missing %d rows", len(problems), result.Missing)
			}
			return nil
		},
	}
}

func printMirrorDryRun(job *seeder.MirrorJob, previewRows, selfRefDepth int, format string) error {
	preview, previewErr := job.Preview(previewRows, selfRefDepth)
	if format == "json" {
		out := map[string]any{"report": job.Report, "plan": job.Plan, "preview": preview, "issues": job.Issues}
		if previewErr != nil {
			out["previewError"] = previewErr.Error()
		}
		return writeJSON(out)
	}
	compare.RenderPlan(os.Stdout, job.Plan)
	if previewErr != nil {
		fmt.Printf("\nSample rows unavailable: %v\n", previewErr)
		return nil
	}
	if len(job.Plan.Order) > 0 {
		fmt.Printf("\nSample rows (up to %d per table, run %s, nothing written):\n", previewRows, job.RunID)
		ordered := yaml.MapSlice{}
		for _, t := range job.Plan.Order {
			ordered = append(ordered, yaml.MapItem{Key: t, Value: preview[t]})
		}
		b, err := yaml.Marshal(ordered)
		if err != nil {
			return err
		}
		fmt.Print(string(b))
	}
	return nil
}

func confirmReset(target string, tables []string) error {
	fmt.Fprintf(os.Stderr, "\nReset will TRUNCATE %d table(s) on %s: %s\nType \"yes\" to continue or press Ctrl+C to abort: ",
		len(tables), target, strings.Join(tables, ", "))
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	if strings.TrimSpace(scanner.Text()) != "yes" {
		return fmt.Errorf("mirror aborted")
	}
	return nil
}

func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func splitList(values []string) []string {
	var out []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}
