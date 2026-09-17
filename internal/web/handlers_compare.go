package web

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/relations"
	"github.com/AxeForging/seedstorm/internal/runerr"
	"github.com/AxeForging/seedstorm/internal/seeder"
)

// ConnRef points at a connection: a live session id, or a saved connection that
// is opened (and kept live) on demand.
type ConnRef struct {
	ID      string `json:"id,omitempty"`
	SavedID string `json:"savedId,omitempty"`
}

// resolveConnection turns a reference into a live session. role names the side
// in error messages ("source", "target").
func (s *Server) resolveConnection(ref ConnRef, role string) (*Session, error) {
	if ref.SavedID != "" && s.store != nil {
		saved, ok, err := s.store.Get(ref.SavedID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%s connection not found", role)
		}
		if needsPassword(saved) {
			return nil, fmt.Errorf("%s connection %q has no saved password; connect it once first", role, saved.Label)
		}
		form := savedToForm(saved, saved.Password)
		driver, dsn, info, err := form.dsnFor()
		if err != nil {
			return nil, err
		}
		info.Label = saved.Label
		sess, err := s.sessions.OpenDSN(driver, dsn, info)
		if err == nil && sess.SavedID == "" {
			sess.SavedID = saved.ID
		}
		return sess, err
	}
	if ref.ID != "" {
		sess, ok := s.sessions.Get(ref.ID)
		if !ok {
			return nil, fmt.Errorf("%s connection not found (was it disconnected?)", role)
		}
		return sess, nil
	}
	return nil, fmt.Errorf("choose a %s connection", role)
}

func sessionLabel(sess *Session) string {
	return connectionLabelForLog(sess.Info)
}

// endpointFor describes a session to the seeder. write opens a fresh handle for
// mutating runs (see Session.OpenRunConn); the caller closes it.
func endpointFor(ctx context.Context, sess *Session, write bool) (seeder.Endpoint, func(), error) {
	sc, err := sess.Schema(false)
	if err != nil {
		return seeder.Endpoint{}, nil, fmt.Errorf("%s schema: %w", sessionLabel(sess), err)
	}
	ep := seeder.Endpoint{Conn: sess.Conn(), DBType: sess.DBType, DSN: sess.DSN, Label: sessionLabel(sess), Schema: sc}
	if !write {
		return ep, func() {}, nil
	}
	conn, err := sess.OpenRunConn(ctx)
	if err != nil {
		return seeder.Endpoint{}, nil, err
	}
	ep.Conn = conn
	return ep, func() { _ = conn.Close() }, nil
}

func (s *Server) handleComparePage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.sessions.fromRequest(r); err != nil {
		http.Redirect(w, r, "/connect", http.StatusSeeOther)
		return
	}
	s.render(w, r, "compare", pageData{Title: "Compare", Active: "compare"})
}

// CompareRequest compares two connections read-only. SourceSnapshot, when
// set, replaces the source connection with an imported counts file.
type CompareRequest struct {
	Source         ConnRef           `json:"source"`
	SourceSnapshot *compare.Snapshot `json:"sourceSnapshot,omitempty"`
	Target         ConnRef           `json:"target"`
	Counts         string            `json:"counts"`
}

func (s *Server) handleCompareRun(w http.ResponseWriter, r *http.Request) {
	startRun(s, w, r, "compare", s.runCompare)
}

func (s *Server) runCompare(ctx context.Context, _ *Session, req CompareRequest, jc JobControl) (map[string]any, error) {
	log := jobLogger(jc)
	mode, err := compare.ParseCountMode(req.Counts)
	if err != nil {
		return nil, err
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
	jc.Phase("count")
	log.Info().Str("source", srcEP.Label).Str("target", tgtEP.Label).Str("counts", string(mode)).Msg("Reading table volumes")
	report, err := seeder.Snapshots(ctx, srcEP, tgtEP, mode, func(side string, done, total int, table string) {
		jc.Progress(done, total, side+": "+table)
	})
	if err != nil {
		return nil, err
	}
	jc.Phase("done")
	t := report.Totals
	log.Info().Int("same", t.Same).Int("differs", t.Differs).Int("source_only", t.SourceOnly).Int("target_only", t.TargetOnly).Msg("Comparison complete")
	return map[string]any{"report": report, "sameConnection": source != nil && source.ID == target.ID, "sourceIsSnapshot": source == nil}, nil
}

// MirrorRequest seeds the target so its volumes follow the source.
type MirrorRequest struct {
	Source         ConnRef           `json:"source"`
	SourceSnapshot *compare.Snapshot `json:"sourceSnapshot,omitempty"`
	Workers        int               `json:"workers,omitempty"`
	Target         ConnRef           `json:"target"`
	Counts         string            `json:"counts"`
	Mode           string            `json:"mode"`
	Scale          float64           `json:"scale"`
	MaxRows        int64             `json:"maxRows"`
	ParentRows     int64             `json:"parentRows"`
	Tables         []string          `json:"tables,omitempty"`
	ProfileID      string            `json:"profileId,omitempty"`
	BatchSize      int               `json:"batchSize"`
	SelfRefDepth   *int              `json:"selfRefDepth,omitempty"`
	StopOnError    bool              `json:"stopOnError"`
	DryRun         bool              `json:"dryRun"`
	PreviewRows    int               `json:"previewRows"`
	// SourceShapes are the source's relationship shapes (from the compare
	// report); when set the target's foreign keys are seeded like them.
	SourceShapes []relations.Shape `json:"sourceShapes,omitempty"`
	// ConfirmProduction is the target's label, typed to write to a
	// production connection.
	ConfirmProduction string `json:"confirmProduction,omitempty"`
}

func (s *Server) handleMirrorRun(w http.ResponseWriter, r *http.Request) {
	startGuardedRun(s, w, r, "mirror", s.runMirror, func(req MirrorRequest, _ *Session) *productionRefusal {
		if req.DryRun {
			return nil
		}
		return s.guardProduction(s.refTarget(req.Target), req.ConfirmProduction, "mirror into it")
	})
}

func (s *Server) runMirror(ctx context.Context, _ *Session, req MirrorRequest, jc JobControl) (map[string]any, error) {
	log := jobLogger(jc)
	mode, err := compare.ParseMirrorMode(req.Mode)
	if err != nil {
		return nil, err
	}
	counts, err := compare.ParseCountMode(req.Counts)
	if err != nil {
		return nil, err
	}
	profile, err := s.profileByID(req.ProfileID)
	if err != nil {
		return nil, err
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
	if !req.DryRun {
		defer target.InvalidateCounts()
	}
	tgtEP, closeTarget, err := endpointFor(ctx, target, !req.DryRun)
	if err != nil {
		return nil, runerr.OnSide(runerr.SideTarget, runerr.At(runerr.PhaseConnect, "", err))
	}
	defer closeTarget()

	jc.Phase("plan")
	log.Info().Str("source", srcEP.Label).Str("target", tgtEP.Label).Str("mode", string(mode)).Msg("Planning mirror")
	job, err := seeder.PrepareMirror(ctx, srcEP, tgtEP, seeder.MirrorConfig{
		Options: compare.MirrorOptions{
			Mode: mode, Scale: req.Scale, MaxRows: req.MaxRows, ParentRows: req.ParentRows, Tables: req.Tables,
		},
		CountMode:    counts,
		Profile:      profile,
		SourceShapes: req.SourceShapes,
		OnCount: func(side string, done, total int, table string) {
			jc.Progress(done, total, side+": "+table)
		},
	})
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"report": job.Report,
		"plan":   job.Plan,
		"issues": job.Issues,
		"runId":  job.RunID,
		"dryRun": req.DryRun,
		"target": tgtEP.Label,
		// A snapshot source cannot be checked against the target.
		"sameDatabaseUnchecked": job.SameDatabaseUnchecked,
		"serverNotices":         job.Servers.Notices(),
		"shapedKeys":            len(job.Shapes),
	}
	for _, notice := range job.Servers.Notices() {
		log.Warn().Msg(notice)
	}
	if job.SameDatabaseUnchecked {
		log.Warn().Msg("Source is an imported counts file: cannot check that source and target are different databases")
	}
	log.Info().Int64("rows", job.Plan.TotalInsert).Int("tables", len(job.Plan.Entries)).Int("truncate", len(job.Plan.Truncate)).Msg("Mirror planned")
	depth := requestSelfRefDepth(req.SelfRefDepth)

	if req.DryRun {
		jc.Phase("preview")
		preview, err := job.Preview(req.PreviewRows, depth)
		if err != nil {
			result["previewError"] = err.Error()
		} else {
			result["preview"] = preview
		}
		jc.Phase("done")
		return result, nil
	}
	if job.Plan.TotalInsert == 0 {
		jc.Phase("done")
		log.Info().Msg("Target already matches the plan; nothing to insert")
		result["run"] = seeder.Result{}
		return result, nil
	}

	jc.Phase("seed")
	meter := seeder.NewMeter(time.Now())
	var lastTick time.Time
	run, runErr := job.Run(ctx, seeder.Options{
		BatchSize:   req.BatchSize,
		StopOnError: req.StopOnError,
		Workers:     requestWorkers(req.Workers),
		Generate: faker.GenerateOptions{
			SelfRefDepth: depth,
			OnWarning: func(w faker.GenerationWarning) {
				log.Warn().Str("table", w.Table).Str("reason", w.Reason).Msg("Generation capped")
			},
		},
		OnProgress: func(p seeder.Progress) {
			now := time.Now()
			est := meter.Observe(now, p.RowsDone, p.RowsTotal)
			if now.Sub(lastTick) >= progressEvery || p.Inserted >= p.Requested {
				lastTick = now
				jc.Progress(int(est.Done), int(est.Total), progressLabel(p, est))
			}
			if p.Inserted >= p.Requested {
				log.Info().Str("table", p.Table).Int64("rows", p.Inserted).Msg("Filling table")
			}
		},
	}, func(done, total int, table string) {
		jc.Progress(done, total, "truncate "+table)
	})
	result["run"] = run
	for _, problem := range run.Problems() {
		log.Warn().Str("table", problem.Table).Str("status", problem.Status).Int64("missing", problem.Missing).Msg(problem.Error)
	}
	if runErr != nil {
		result["failure"] = failureView(runErr)
		return result, runErr
	}
	if shapes := measureShapes(ctx, log, tgtEP.Conn, tgtEP.DBType, job.Schema, job.Shapes); shapes != nil {
		result["shapes"] = shapes
	}
	jc.Phase("done")
	log.Info().Int64("inserted", run.Inserted).Int64("missing", run.Missing).Msg("Mirror complete")
	return result, nil
}
