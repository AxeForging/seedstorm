package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/relations"
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
