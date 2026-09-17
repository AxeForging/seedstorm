package cli

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/relations"
	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/AxeForging/seedstorm/internal/seeder"
)

// relationshipFlags tune a relationship scan (--relationships on snapshot,
// compare and introspect). The scan mode follows --counts.
func relationshipFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "scan-unindexed", Usage: "With --relationships: also scan foreign keys that lead no index (full table scans; estimates otherwise)"},
		&cli.DurationFlag{Name: "read-timeout", Usage: "With --relationships: server-side time limit per relationship (a slower one is reported as timed out)", Value: relations.DefaultStatementTimeout},
	}
}

// relationshipOptions builds scan options; OnEdge logs throttled progress and
// a warning for every relationship that was not measured exactly.
func relationshipOptions(cmd *cli.Command, mode compare.CountMode, side string) relations.Options {
	what := "Scanning relationships"
	if side != "" {
		what += " (" + side + ")"
	}
	step := stepLogger(what, time.Now)
	scanMode := relations.Exact
	if mode == compare.CountEstimate {
		scanMode = relations.Estimate
	}
	return relations.Options{
		Mode:             scanMode,
		Limits:           db.ReadLimits{StatementTimeout: cmd.Duration("read-timeout")},
		IncludeUnindexed: cmd.Bool("scan-unindexed"),
		OnEdge: func(done, total int, s relations.Shape) {
			logShapeWarning(side, s)
			step(done, total, s.Child+"."+s.Column)
		},
	}
}

// logShapeWarning explains a relationship that is estimated, unknown or on a
// large table. Safe to call from scan workers (OnEdge runs one at a time).
func logShapeWarning(side string, s relations.Shape) {
	if s.Outcome == db.OutcomeOK || s.Outcome == relations.OutcomeEstimated {
		if !s.Large || s.Outcome == relations.OutcomeEstimated {
			return
		}
		s.Detail = "large child table: measured exactly, which reads the whole key"
	}
	ev := logging.Log.Warn()
	if side != "" {
		ev = ev.Str("side", side)
	}
	ev.Str("relationship", s.Child+"."+s.Column).Str("outcome", string(s.Outcome)).Msg(s.Detail)
}

// logShapeSummary counts relationships by outcome.
func logShapeSummary(shapes []relations.Shape) {
	byOutcome := map[db.ReadOutcome]int{}
	for _, s := range shapes {
		byOutcome[s.Outcome]++
	}
	ev := logging.Log.Info().Int("relationships", len(shapes))
	outcomes := make([]string, 0, len(byOutcome))
	for outcome := range byOutcome {
		outcomes = append(outcomes, string(outcome))
	}
	sort.Strings(outcomes)
	for _, outcome := range outcomes {
		ev = ev.Int(outcome, byOutcome[db.ReadOutcome(outcome)])
	}
	ev.Msg("Relationships measured")
}

// writeRelationshipsSnapshot saves estimated counts plus exact relationship
// shapes to path, as YAML or JSON by its extension.
func writeRelationshipsSnapshot(ctx context.Context, cmd *cli.Command, ep seeder.Endpoint, path string) error {
	logging.Log.Info().Msg("Reading estimated table counts")
	snap, err := compare.Take(ctx, ep.Conn, ep.DBType, ep.Label, compare.CountEstimate, stepLogger("Counting", time.Now))
	if err != nil {
		return err
	}
	logScanServer(ctx, ep)
	if snap.Relationships, err = ep.Shapes(ctx, relationshipOptions(cmd, compare.CountExact, "")); err != nil {
		return err
	}
	logShapeSummary(snap.Relationships)
	format := compare.FormatYAML
	if strings.EqualFold(filepath.Ext(path), ".json") {
		format = compare.FormatJSON
	}
	data, err := compare.EncodeSnapshot(snap, format)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // a counts file holds no secrets
		return fmt.Errorf("write relationships: %w", err)
	}
	logging.Log.Info().Str("path", path).Int("relationships", len(snap.Relationships)).Msg("Relationships saved")
	return nil
}

// logScanServer says whether a scan reads a replica or a primary.
func logScanServer(ctx context.Context, eps ...seeder.Endpoint) {
	for _, ep := range eps {
		if notice := seeder.ScanNotice(ctx, ep); notice != "" {
			logging.Log.Info().Msg(notice)
		}
	}
}

// shapeRowsFlag derives child row counts from the profile's relationships.
func shapeRowsFlag() cli.Flag {
	return &cli.BoolFlag{Name: "shape-rows", Usage: "Derive row counts of shaped child tables from their parents (parents × avg children); --table-rows still wins"}
}

// deriveShapedRows plans row counts when --shape-rows is set, logging each
// derived table and every conflict between shaped keys.
func deriveShapedRows(cmd *cli.Command, sc *schema.Schema, order []string, rows int, tableRows map[string]int, shapes map[string]faker.Shape) map[string]int {
	if !cmd.Bool("shape-rows") {
		return nil
	}
	log := logging.Log
	if len(shapes) == 0 {
		log.Warn().Msg("--shape-rows needs a profile with relationships: row counts unchanged")
		return nil
	}
	derived, notes := faker.DeriveShapedRows(sc, order, rows, tableRows, shapes)
	for _, n := range notes {
		log.Warn().Msg(n)
	}
	for _, t := range order {
		if n, ok := derived[t]; ok {
			log.Info().Str("table", t).Int("rows", n).Msg("Rows derived from relationship shapes")
		}
	}
	return derived
}

// logShapeResults measures shaped keys after a run and logs target next to
// achieved. A failed measurement is a warning: the rows are already written.
func logShapeResults(ctx context.Context, conn *sql.DB, dbType string, sc *schema.Schema, shapes map[string]faker.Shape) {
	if len(shapes) == 0 {
		return
	}
	log := logging.Log
	results, err := seeder.MeasureShapes(ctx, conn, dbType, sc, shapes)
	if err != nil {
		log.Warn().Err(err).Msg("Could not measure the seeded relationship shapes")
		return
	}
	for _, r := range results {
		a := r.Achieved
		if a.Outcome != db.OutcomeOK {
			log.Warn().Str("relationship", r.Key).Str("outcome", string(a.Outcome)).Msg(a.Detail)
			continue
		}
		ev := log.Info()
		if a.Max > int64(r.Target.Max) {
			ev = log.Warn()
		}
		ev.Str("relationship", r.Key).
			Str("avg", fmt.Sprintf("%.2f → %.2f", r.Target.Avg, a.Avg)).
			Str("max", fmt.Sprintf("%d → %d", r.Target.Max, a.Max)).
			Str("without_children", fmt.Sprintf("%.0f%% → %.0f%%", r.Target.ZeroShare*100, a.ZeroShare*100)).
			Msg("Relationship shape (target → table now)")
	}
}

// planCounts overlays derived counts under explicit ones, for plan output.
func planCounts(tableRows, derived map[string]int) map[string]int {
	if len(derived) == 0 {
		return tableRows
	}
	out := make(map[string]int, len(tableRows)+len(derived))
	for k, v := range derived {
		out[k] = v
	}
	for k, v := range tableRows {
		out[k] = v
	}
	return out
}
