package seeder

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/rules"
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
	target    Endpoint
}

// Snapshots reads both sides and diffs them. It is the read-only half of a
// mirror, also used on its own by the compare views.
func Snapshots(ctx context.Context, source, target Endpoint, mode compare.CountMode, onCount func(side string, done, total int, table string)) (compare.Report, error) {
	progress := func(side string) func(int, int, string) {
		if onCount == nil {
			return nil
		}
		return func(done, total int, table string) { onCount(side, done, total, table) }
	}
	src, err := compare.Take(ctx, source.Conn, source.DBType, source.Label, mode, progress("source"))
	if err != nil {
		return compare.Report{}, fmt.Errorf("source: %w", err)
	}
	tgt, err := compare.Take(ctx, target.Conn, target.DBType, target.Label, mode, progress("target"))
	if err != nil {
		return compare.Report{}, fmt.Errorf("target: %w", err)
	}
	return compare.Diff(src, tgt), nil
}

// PrepareMirror snapshots both sides, plans the mirror against the target
// schema and compiles the profile. It refuses to proceed when both endpoints
// are the same database.
func PrepareMirror(ctx context.Context, source, target Endpoint, cfg MirrorConfig) (*MirrorJob, error) {
	srcID, err := db.Identity(ctx, source.Conn, source.DBType)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	tgtID, err := db.Identity(ctx, target.Conn, target.DBType)
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	if srcID == tgtID {
		return nil, ErrSameDatabase
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
	plan, err := compare.PlanMirror(report, sc, cfg.Options)
	if err != nil {
		return nil, err
	}
	job := &MirrorJob{Report: report, Plan: plan, Schema: sc, RunID: cfg.RunID, target: target}
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
	if j.Plan.Mode == compare.ModeReset && len(j.Plan.Truncate) > 0 {
		if err := db.TruncateWithProgress(ctx, j.target.Conn, j.target.DBType, j.Plan.Truncate, onTruncate); err != nil {
			return Result{}, fmt.Errorf("truncate target: %w", err)
		}
	}
	return Fill(ctx, j.target.Conn, j.target.DBType, j.Schema, j.Plan.Order, j.Plan.Counts(), opts)
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
