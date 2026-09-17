package web

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/relations"
	"github.com/AxeForging/seedstorm/internal/runerr"
	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/AxeForging/seedstorm/internal/seeder"
	"github.com/rs/zerolog"
)

// RelationshipsRequest measures the active connection's relationship shapes.
type RelationshipsRequest struct {
	// Counts is exact (aggregate each key) or estimate (planner statistics).
	Counts        string `json:"counts"`
	ScanUnindexed bool   `json:"scanUnindexed"`
	// ConfirmExact allows exact scans on a production connection.
	ConfirmExact bool `json:"confirmExact"`
}

// handleRelationships: GET returns the shapes measured so far on this
// connection; POST starts a scan job.
func (s *Server) handleRelationships(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		sess, err := s.sessions.fromRequest(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		_, production := s.productionConnection(sessionTarget(sess))
		writeJSON(w, http.StatusOK, struct {
			shapeView
			Production bool `json:"production"`
		}{sess.shapes.view(), production})
		return
	}
	startRun(s, w, r, "relationships", s.runRelationships)
}

// relationshipOptions are the scan options of a request. A production
// connection runs one query at a time, and in estimate mode unless exact
// scans were confirmed.
func (s *Server) relationshipOptions(t connectionTarget, counts string, scanUnindexed, confirmExact bool) (relations.Options, []string, error) {
	mode, err := compare.ParseCountMode(counts)
	if err != nil {
		return relations.Options{}, nil, err
	}
	opts := relations.Options{Mode: relations.Exact, IncludeUnindexed: scanUnindexed}
	if mode == compare.CountEstimate {
		opts.Mode = relations.Estimate
	}
	var notes []string
	if c, ok := s.productionConnection(t); ok {
		opts.Limits.Concurrency = 1
		if opts.Mode == relations.Exact && !confirmExact {
			opts.Mode = relations.Estimate
			notes = append(notes, fmt.Sprintf("%s is a production connection: reading estimates only (confirm an exact scan to aggregate every key)", c.Label))
		}
	}
	return opts, notes, nil
}

func (s *Server) runRelationships(ctx context.Context, sess *Session, req RelationshipsRequest, jc JobControl) (map[string]any, error) {
	log := jobLogger(jc)
	opts, notes, err := s.relationshipOptions(sessionTarget(sess), req.Counts, req.ScanUnindexed, req.ConfirmExact)
	if err != nil {
		return nil, err
	}
	for _, n := range notes {
		log.Warn().Msg(n)
	}
	jc.Phase("introspect")
	ep, _, err := endpointFor(ctx, sess, false)
	if err != nil {
		return nil, runerr.At(runerr.PhaseIntrospect, "", err)
	}
	jc.Phase("scan")
	if notice := seeder.ScanNotice(ctx, ep); notice != "" {
		log.Info().Msg(notice)
	}
	gen := sess.shapes.begin(opts.Mode == relations.Estimate)
	defer sess.shapes.finish(gen)
	log.Info().Str("database", ep.Label).Str("mode", string(opts.Mode)).Bool("scan_unindexed", opts.IncludeUnindexed).Msg("Measuring relationships (read-only)")
	opts.OnEdge = func(done, total int, sh relations.Shape) {
		sess.shapes.put(gen, total, sh)
		logShape(jc, sh)
		jc.Progress(done, total, sh.Child+"."+sh.Column)
	}
	shapes, err := ep.Shapes(ctx, opts)
	if err != nil {
		return nil, err
	}
	outcomes := map[db.ReadOutcome]int{}
	for _, sh := range shapes {
		outcomes[sh.Outcome]++
	}
	if ctx.Err() != nil {
		return map[string]any{"shapes": shapes, "outcomes": outcomes}, ctx.Err()
	}
	jc.Phase("done")
	log.Info().Int("relationships", len(shapes)).Msg("Relationships measured")
	return map[string]any{"shapes": shapes, "outcomes": outcomes}, nil
}

// logShape writes one relationship's result, warning when it was not measured.
func logShape(jc JobControl, sh relations.Shape) {
	log := jobLogger(jc)
	name := sh.Child + "." + sh.Column
	switch sh.Outcome {
	case db.OutcomeOK, relations.OutcomeEstimated:
		ev := log.Info()
		if sh.Large && sh.Outcome == db.OutcomeOK {
			ev = log.Warn().Bool("large", true)
		}
		ev.Str("relationship", name).Float64("avg", round2(sh.Avg)).Int64("max", sh.Max).Str("outcome", string(sh.Outcome)).Msg("Relationship")
	default:
		log.Warn().Str("relationship", name).Str("outcome", string(sh.Outcome)).Msg(sh.Detail)
	}
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

// CompareRelationshipsRequest compares relationship shapes of two connections
// (or a snapshot with relationships and a connection).
type CompareRelationshipsRequest struct {
	CompareRequest
	ScanUnindexed bool `json:"scanUnindexed"`
	ConfirmExact  bool `json:"confirmExact"`
}

func (s *Server) handleCompareRelationships(w http.ResponseWriter, r *http.Request) {
	startRun(s, w, r, "compare relationships", s.runCompareRelationships)
}

func (s *Server) runCompareRelationships(ctx context.Context, _ *Session, req CompareRelationshipsRequest, jc JobControl) (map[string]any, error) {
	log := jobLogger(jc)
	if req.SourceSnapshot != nil && len(req.SourceSnapshot.Relationships) == 0 {
		return nil, runerr.OnSide(runerr.SideSource, seeder.ErrNoRelationships)
	}
	jc.Phase("connect")
	source, target, err := s.connectBoth(ctx, log, req.Source, req.SourceSnapshot, req.Target)
	if err != nil {
		return nil, err
	}
	srcEP, err := snapshotOrSessionEndpoint(ctx, source, req.SourceSnapshot)
	if err != nil {
		return nil, runerr.OnSide(runerr.SideSource, runerr.At(runerr.PhaseIntrospect, "", err))
	}
	tgtEP, _, err := endpointFor(ctx, target, false)
	if err != nil {
		return nil, runerr.OnSide(runerr.SideTarget, runerr.At(runerr.PhaseIntrospect, "", err))
	}
	// The stricter side decides: production anywhere means one query at a
	// time, and estimates unless confirmed.
	opts, notes, err := s.relationshipOptions(s.refTarget(req.Target), req.Counts, req.ScanUnindexed, req.ConfirmExact)
	if err != nil {
		return nil, err
	}
	if source != nil {
		srcOpts, srcNotes, _ := s.relationshipOptions(sessionTarget(source), req.Counts, req.ScanUnindexed, req.ConfirmExact)
		if srcOpts.Mode == relations.Estimate {
			opts.Mode = relations.Estimate
		}
		if srcOpts.Limits.Concurrency == 1 {
			opts.Limits.Concurrency = 1
		}
		notes = append(srcNotes, notes...)
	}
	for _, n := range notes {
		log.Warn().Msg(n)
	}
	jc.Phase("scan")
	log.Info().Str("source", srcEP.Label).Str("target", tgtEP.Label).Str("mode", string(opts.Mode)).Msg("Comparing relationships (read-only)")
	drift, err := seeder.CompareShapes(ctx, srcEP, tgtEP, opts, func(side string, done, total int, sh relations.Shape) {
		if sh.Outcome != db.OutcomeOK && sh.Outcome != relations.OutcomeEstimated {
			log.Warn().Str("side", side).Str("relationship", sh.Child+"."+sh.Column).Str("outcome", string(sh.Outcome)).Msg(sh.Detail)
		}
		jc.Progress(done, total, side+": "+sh.Child+"."+sh.Column)
	})
	if err != nil {
		return nil, err
	}
	counts := map[compare.ShapeStatus]int{}
	for _, d := range drift {
		counts[d.Status]++
	}
	jc.Phase("done")
	log.Info().Int("same", counts[compare.ShapeSame]).Int("differs", counts[compare.ShapeDiffers]).Int("unknown", counts[compare.ShapeUnknown]).Msg("Relationships compared")
	return map[string]any{"relationships": drift}, nil
}

// measureShapes reads shaped keys back after a run that wrote, logging target
// next to achieved. Failures only warn: the rows are written already.
func measureShapes(ctx context.Context, log zerolog.Logger, conn *sql.DB, dbType string, sc *schema.Schema, shapes map[string]faker.Shape) []seeder.ShapeResult {
	if len(shapes) == 0 {
		return nil
	}
	results, err := seeder.MeasureShapes(ctx, conn, dbType, sc, shapes)
	if err != nil {
		log.Warn().Err(err).Msg("Could not measure the seeded relationship shapes")
		return nil
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
			Msg("Relationship shape (target → table now)")
	}
	return results
}
