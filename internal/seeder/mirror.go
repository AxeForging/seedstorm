package seeder

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/rules"
	"github.com/AxeForging/seedstorm/internal/runerr"
	"github.com/AxeForging/seedstorm/internal/safego"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// ErrSameDatabase refuses a mirror whose source and target are one database.
var ErrSameDatabase = errors.New("source and target are the same database")

// Endpoint is one side of a mirror.
type Endpoint struct {
	Conn   *sql.DB
	DBType string // driver name: pgx or mysql
	DSN    string // used to introspect when Schema is nil
	Label  string
	Schema *schema.Schema
	// Snapshot, when set, is a pre-taken snapshot (for example a file made by
	// `seedstorm snapshot`) used instead of reading the database: Conn and DSN
	// may be empty. Only a source can meaningfully be a snapshot for a mirror,
	// which writes to the target.
	Snapshot *compare.Snapshot
}

// FromSnapshot reports whether the endpoint is a pre-taken snapshot rather
// than a live connection.
func (e Endpoint) FromSnapshot() bool { return e.Snapshot != nil }

// take returns the endpoint's snapshot, reading the database unless one was given.
func (e Endpoint) take(ctx context.Context, mode compare.CountMode, progress func(int, int, string)) (compare.Snapshot, error) {
	if e.Snapshot != nil {
		snap := *e.Snapshot
		if snap.Label == "" {
			snap.Label = e.Label
		}
		if snap.DBType == "" {
			snap.DBType = e.DBType
		}
		return snap, nil
	}
	if e.Conn == nil {
		return compare.Snapshot{}, errors.New("no database connection or snapshot")
	}
	return compare.Take(ctx, e.Conn, e.DBType, e.Label, mode, progress)
}

// MirrorConfig configures PrepareMirror.
type MirrorConfig struct {
	Options   compare.MirrorOptions
	CountMode compare.CountMode
	Profile   *rules.RuleSet
	RunID     string
	// OnCount reports snapshot progress: side is "source" or "target".
	OnCount func(side string, done, total int, table string)
}

// MirrorJob is a prepared, reviewable mirror. Nothing has been written yet.
type MirrorJob struct {
	Report    compare.Report
	Plan      compare.MirrorPlan
	Schema    *schema.Schema
	Overrides faker.Overrides
	Issues    []rules.Issue
	RunID     string
	// SameDatabaseUnchecked is true when a side was a pre-taken snapshot, so
	// PrepareMirror could not verify that source and target differ.
	SameDatabaseUnchecked bool
	target                Endpoint
}

// Snapshots reads both sides and diffs them. It is the read-only half of a
// mirror, also used on its own by the compare views. A side with a Snapshot is
// not read; mode only applies to live sides.
func Snapshots(ctx context.Context, source, target Endpoint, mode compare.CountMode, onCount func(side string, done, total int, table string)) (compare.Report, error) {
	progress := func(side string) func(int, int, string) {
		if onCount == nil {
			return nil
		}
		return func(done, total int, table string) { onCount(side, done, total, table) }
	}
	// The two databases are independent: read them at the same time.
	var src, tgt compare.Snapshot
	var srcErr, tgtErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		srcErr = safego.Run("count source", func() (err error) { src, err = source.take(ctx, mode, progress("source")); return err })
	}()
	go func() {
		defer wg.Done()
		tgtErr = safego.Run("count target", func() (err error) { tgt, err = target.take(ctx, mode, progress("target")); return err })
	}()
	wg.Wait()
	if srcErr != nil {
		return compare.Report{}, runerr.OnSide(runerr.SideSource, runerr.At(runerr.PhaseCount, "", srcErr))
	}
	if tgtErr != nil {
		return compare.Report{}, runerr.OnSide(runerr.SideTarget, runerr.At(runerr.PhaseCount, "", tgtErr))
	}
	return compare.Diff(src, tgt), nil
}

// PrepareMirror snapshots both sides, plans the mirror against the target
// schema and compiles the profile. It refuses to proceed when both endpoints
// are the same live database. The check cannot run when either side is a
// pre-taken snapshot (see Endpoint.Snapshot); job.SameDatabaseUnchecked says so.
func PrepareMirror(ctx context.Context, source, target Endpoint, cfg MirrorConfig) (*MirrorJob, error) {
	unchecked := source.FromSnapshot() || target.FromSnapshot()
	if !unchecked {
		if err := refuseSameDatabase(ctx, source, target); err != nil {
			return nil, err
		}
	}
	sc := target.Schema
	if sc == nil {
		tables, err := db.Introspect(target.DBType, target.DSN)
		if err != nil {
			return nil, fmt.Errorf("introspect target: %w", err)
		}
		sc = faker.BuildSchema(target.DBType, tables)
	}
	if cfg.CountMode == "" {
		cfg.CountMode = compare.CountExact
	}
	report, err := Snapshots(ctx, source, target, cfg.CountMode, cfg.OnCount)
	if err != nil {
		return nil, err
	}
	if cfg.Profile != nil && cfg.Options.Ignore == nil {
		// The profile's ignored tables are never truncated or filled.
		cfg.Options.Ignore = cfg.Profile.IgnoredSet(sc)
	}
	plan, err := compare.PlanMirror(report, sc, cfg.Options)
	if err != nil {
		return nil, err
	}
	job := &MirrorJob{Report: report, Plan: plan, Schema: sc, RunID: cfg.RunID, SameDatabaseUnchecked: unchecked, target: target}
	if job.RunID == "" {
		job.RunID = rules.NewRunID()
	}
	if cfg.Profile != nil {
		job.Issues = cfg.Profile.Validate(sc)
		if job.Overrides, err = cfg.Profile.Compile(sc, job.RunID); err != nil {
			return job, err
		}
	}
	return job, nil
}

func refuseSameDatabase(ctx context.Context, source, target Endpoint) error {
	if source.Conn == nil || target.Conn == nil {
		return errors.New("cannot check that source and target differ: an endpoint has no database connection")
	}
	srcID, err := db.Identity(ctx, source.Conn, source.DBType)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	tgtID, err := db.Identity(ctx, target.Conn, target.DBType)
	if err != nil {
		return fmt.Errorf("target: %w", err)
	}
	if srcID == tgtID {
		return ErrSameDatabase
	}
	return nil
}

// Preview generates up to perTable sample rows per planned table, writing nothing.
func (j *MirrorJob) Preview(perTable int, selfRefDepth int) (map[string][]map[string]interface{}, error) {
	return Preview(j.target.Conn, j.target.DBType, j.Schema, j.Plan.Order, j.Plan.Counts(), perTable, j.generateOptions(selfRefDepth))
}

// Run executes the plan on the target: truncate (reset mode), then fill.
// opts.Generate.Overrides is replaced by the compiled profile.
func (j *MirrorJob) Run(ctx context.Context, opts Options, onTruncate func(done, total int, table string)) (Result, error) {
	gen := j.generateOptions(opts.Generate.SelfRefDepth)
	gen.OnWarning = opts.Generate.OnWarning
	opts.Generate = gen
	// Refuse tables that cannot be generated before anything is truncated.
	if err := faker.CheckSeedable(j.Schema, j.Plan.Order, j.Overrides); err != nil {
		return Result{}, err
	}
	if j.Plan.Mode == compare.ModeReset && len(j.Plan.Truncate) > 0 {
		if err := db.TruncateConcurrently(ctx, j.target.Conn, j.target.DBType, j.Plan.Truncate, max(opts.Workers, 1), onTruncate); err != nil {
			return Result{}, runerr.OnSide(runerr.SideTarget, runerr.At(runerr.PhaseTruncate, "", err))
		}
	}
	res, err := Fill(ctx, j.target.Conn, j.target.DBType, j.Schema, j.Plan.Order, j.Plan.Counts(), opts)
	return res, runerr.OnSide(runerr.SideTarget, err)
}

func (j *MirrorJob) generateOptions(selfRefDepth int) faker.GenerateOptions {
	opts := faker.DefaultGenerateOptions()
	if selfRefDepth > 0 {
		opts.SelfRefDepth = selfRefDepth
	}
	opts.Overrides = j.Overrides
	return opts
}

// Preview generates up to perTable rows for each planned table without writing
// anything. FK columns resolve against rows already in the target plus the
// preview rows of earlier tables, so samples look like the real run.
func Preview(conn *sql.DB, dbType string, sc *schema.Schema, order []string, counts map[string]int, perTable int, gen faker.GenerateOptions) (map[string][]map[string]interface{}, error) {
	if perTable <= 0 {
		perTable = 3
	}
	sample := make(map[string]int, len(order))
	for _, t := range order {
		n := counts[t]
		if n > perTable {
			n = perTable
		}
		if n > 0 {
			sample[t] = n
		}
	}
	// Read PK pools only for the planned tables and their parents.
	seen := map[string]bool{}
	var preload []string
	for _, t := range order {
		for _, name := range preloadTables(sc, t) {
			if !seen[name] {
				seen[name] = true
				preload = append(preload, name)
			}
		}
	}
	sort.Strings(preload)
	return faker.GenerateFilteredWithOptions(sc, preload, order, 0, 0, sample, conn, dbType, gen)
}
