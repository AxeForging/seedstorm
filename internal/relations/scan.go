package relations

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/safego"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// Mode selects how shapes are measured.
type Mode string

const (
	// Exact aggregates the key in the database: one pass over it.
	Exact Mode = "exact"
	// Estimate reads planner statistics: instant, approximate, partial.
	Estimate Mode = "estimate"
)

// Outcomes beyond db.ReadOutcome.
const (
	OutcomeSkippedUnindexed db.ReadOutcome = "skipped: unindexed"
	OutcomeEstimated        db.ReadOutcome = "estimated"
)

// DefaultStatementTimeout bounds one relationship's exact scan.
const DefaultStatementTimeout = 60 * time.Second

// DefaultLargeRows is the child table size warned about before an exact scan.
const DefaultLargeRows = 5_000_000

// Options configure a scan.
type Options struct {
	Mode Mode
	// Limits apply to each exact scan; a zero StatementTimeout means
	// DefaultStatementTimeout. Concurrency defaults to 2.
	Limits db.ReadLimits
	// IncludeUnindexed scans keys that lead no index (full table scans).
	IncludeUnindexed bool
	// LargeRows marks child tables above it as Large (0: DefaultLargeRows).
	LargeRows int64
	// Only limits the scan to these "child.column" keys (nil: every one).
	Only map[string]bool
	// OnEdge is called after each relationship, one call at a time.
	OnEdge func(done, total int, s Shape)
}

// Edges lists the schema's single-column foreign keys (self-references
// included), sorted by child table and column.
func Edges(sc *schema.Schema) []Shape {
	var out []Shape
	for childName, t := range sc.Tables {
		for colName, col := range t.Columns {
			parent, parentCol := splitFK(col.FK)
			if parent == "" {
				continue
			}
			out = append(out, Shape{Child: childName, Column: colName, Parent: parent, ParentColumn: parentCol, SelfRef: parent == childName})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Child != out[j].Child {
			return out[i].Child < out[j].Child
		}
		return out[i].Column < out[j].Column
	})
	return out
}

// Scan measures every relationship of sc, cheapest first, read-only. It
// returns every edge with an outcome: finished edges are kept when ctx is
// cancelled, and one edge timing out does not stop the others.
func Scan(ctx context.Context, conn *sql.DB, dbType string, sc *schema.Schema, opts Options) ([]Shape, error) {
	if opts.Mode == "" {
		opts.Mode = Exact
	}
	if opts.Limits.StatementTimeout == 0 {
		opts.Limits.StatementTimeout = DefaultStatementTimeout
	}
	if opts.Limits.LockTimeout == 0 {
		opts.Limits.LockTimeout = db.DefaultCountLimits.LockTimeout
	}
	concurrency := max(opts.Limits.Concurrency, 2)
	if opts.Limits.Concurrency == 1 {
		concurrency = 1
	}
	opts.Limits.Concurrency = 1 // each worker runs one statement at a time
	if opts.LargeRows <= 0 {
		opts.LargeRows = DefaultLargeRows
	}

	edges := Edges(sc)
	if opts.Only != nil {
		kept := edges[:0]
		for _, e := range edges {
			if opts.Only[e.Child+"."+e.Column] {
				kept = append(kept, e)
			}
		}
		edges = kept
	}
	estimates, _ := db.GetEstimatedRowCounts(ctx, conn, dbType)
	size := func(table string) int64 {
		if n, ok := estimates[table]; ok && n >= 0 {
			return n
		}
		return 1 << 50
	}
	sort.SliceStable(edges, func(i, j int) bool { return size(edges[i].Child) < size(edges[j].Child) })

	out := make([]Shape, len(edges))
	var mu sync.Mutex
	done := 0
	work := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				edge := edges[i]
				var shape Shape
				err := safego.Run("relationship "+edge.Child+"."+edge.Column, func() error {
					shape = measure(ctx, conn, dbType, edge, opts)
					return nil
				})
				if err != nil {
					shape = edge
					shape.Outcome, shape.Detail = db.OutcomeFailed, err.Error()
				}
				shape.Large = size(edge.Child) > opts.LargeRows && size(edge.Child) < 1<<50
				mu.Lock()
				out[i] = shape
				done++
				if opts.OnEdge != nil {
					opts.OnEdge(done, len(edges), shape)
				}
				mu.Unlock()
			}
		}()
	}
	for i := range edges {
		work <- i
	}
	close(work)
	wg.Wait()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Child != out[j].Child {
			return out[i].Child < out[j].Child
		}
		return out[i].Column < out[j].Column
	})
	return out, nil
}

// measure scans one relationship.
func measure(ctx context.Context, conn *sql.DB, dbType string, edge Shape, opts Options) Shape {
	withEdge := func(s Shape) Shape {
		s.Child, s.Column, s.Parent, s.ParentColumn, s.SelfRef = edge.Child, edge.Column, edge.Parent, edge.ParentColumn, edge.SelfRef
		return s
	}
	if ctx.Err() != nil {
		s := withEdge(Shape{})
		s.Outcome, s.Detail = db.OutcomeCancelled, ctx.Err().Error()
		return s
	}
	var indexed bool
	_ = db.ReadOnce(ctx, conn, dbType, db.DefaultCountLimits, func(ctx context.Context, q db.Querier) (err error) {
		indexed, err = db.LeadingIndexed(ctx, q, dbType, edge.Child, edge.Column)
		return err
	})

	estimate := func(outcome db.ReadOutcome, detail string) Shape {
		var est db.DegreeEstimate
		err := db.ReadOnce(ctx, conn, dbType, db.DefaultCountLimits, func(ctx context.Context, q db.Querier) (err error) {
			est, err = db.EstimateDegrees(ctx, q, dbType, edge.Child, edge.Column, edge.Parent)
			return err
		})
		s := withEdge(shapeFromEstimate(est))
		s.Indexed = indexed
		s.Outcome, s.Detail = outcome, detail
		if err != nil {
			s.Outcome, s.Detail = db.ReadOutcomeOf(ctx, err), err.Error()
		}
		return s
	}
	if opts.Mode == Estimate {
		return estimate(OutcomeEstimated, "")
	}
	if !indexed && !opts.IncludeUnindexed {
		return estimate(OutcomeSkippedUnindexed, "the key leads no index: an exact scan reads the whole table (numbers are estimates)")
	}

	var buckets []db.DegreeBucket
	var nulls, parents int64
	err := db.ReadOnce(ctx, conn, dbType, opts.Limits, func(ctx context.Context, q db.Querier) (err error) {
		if buckets, nulls, err = db.DegreeHistogram(ctx, q, dbType, edge.Child, edge.Column); err != nil {
			return err
		}
		//nolint:gosec // identifier is quoted
		return q.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+db.QuoteIdent(edge.Parent, dbType)).Scan(&parents)
	})
	s := withEdge(shapeFromHistogram(buckets, parents, nulls))
	s.Indexed = indexed
	s.Outcome = db.OutcomeOK
	if err != nil {
		s = withEdge(Shape{Indexed: indexed})
		s.Outcome, s.Detail = db.ReadOutcomeOf(ctx, err), err.Error()
	}
	return s
}

// splitFK splits a schema FK "table.column".
func splitFK(fk string) (string, string) {
	parts := strings.SplitN(fk, ".", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}
