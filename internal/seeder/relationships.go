package seeder

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/relations"
	"github.com/AxeForging/seedstorm/internal/runerr"
	"github.com/AxeForging/seedstorm/internal/safego"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// ErrNoRelationships: a snapshot endpoint was asked for shapes it does not hold.
var ErrNoRelationships = errors.New("the snapshot file has no relationships (take it with --relationships)")

// Shapes returns the endpoint's relationship shapes: the snapshot's when it
// is one, otherwise a read-only scan (introspecting first when the endpoint
// carries no schema).
func (e Endpoint) Shapes(ctx context.Context, opts relations.Options) ([]relations.Shape, error) {
	if e.Snapshot != nil {
		if len(e.Snapshot.Relationships) == 0 {
			return nil, ErrNoRelationships
		}
		return e.Snapshot.Relationships, nil
	}
	if e.Conn == nil {
		return nil, errors.New("no database connection or snapshot")
	}
	sc := e.Schema
	if sc == nil {
		tables, err := db.IntrospectConn(ctx, e.Conn, e.DBType, nil)
		if err != nil {
			return nil, runerr.At(runerr.PhaseIntrospect, "", err)
		}
		sc = faker.BuildSchema(e.DBType, tables)
	}
	shapes, err := relations.Scan(ctx, e.Conn, e.DBType, sc, opts)
	if err != nil {
		return nil, fmt.Errorf("relationship scan: %w", err)
	}
	return shapes, nil
}

// CompareShapes scans (or reads) both sides at once and diffs them. onEdge
// reports progress per side.
func CompareShapes(ctx context.Context, source, target Endpoint, opts relations.Options, onEdge func(side string, done, total int, s relations.Shape)) ([]compare.ShapeDrift, error) {
	sideOpts := func(side string) relations.Options {
		o := opts
		if onEdge != nil {
			o.OnEdge = func(done, total int, s relations.Shape) { onEdge(side, done, total, s) }
		}
		return o
	}
	var src, tgt []relations.Shape
	var srcErr, tgtErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		srcErr = safego.Run("relationships source", func() (err error) { src, err = source.Shapes(ctx, sideOpts("source")); return err })
	}()
	go func() {
		defer wg.Done()
		tgtErr = safego.Run("relationships target", func() (err error) { tgt, err = target.Shapes(ctx, sideOpts("target")); return err })
	}()
	wg.Wait()
	if srcErr != nil {
		return nil, runerr.OnSide(runerr.SideSource, srcErr)
	}
	if tgtErr != nil {
		return nil, runerr.OnSide(runerr.SideTarget, tgtErr)
	}
	return compare.DiffShapes(src, tgt), nil
}

// ShapeResult compares a shaped key's target with what the table holds after
// a run (stored rows included).
type ShapeResult struct {
	Key      string          `json:"key"`
	Target   faker.Shape     `json:"target"`
	Achieved relations.Shape `json:"achieved"`
}

// MeasureShapes reads the shaped keys back exactly (read-only, indexed or
// not: the tables were just written by this run).
func MeasureShapes(ctx context.Context, conn *sql.DB, dbType string, sc *schema.Schema, shapes map[string]faker.Shape) ([]ShapeResult, error) {
	if len(shapes) == 0 || conn == nil {
		return nil, nil
	}
	only := make(map[string]bool, len(shapes))
	for k := range shapes {
		only[k] = true
	}
	measured, err := relations.Scan(ctx, conn, dbType, sc, relations.Options{Mode: relations.Exact, IncludeUnindexed: true, Only: only})
	if err != nil {
		return nil, err
	}
	out := make([]ShapeResult, 0, len(measured))
	for _, m := range measured {
		key := faker.ShapeKey(m.Child, m.Column)
		out = append(out, ShapeResult{Key: key, Target: shapes[key], Achieved: m})
	}
	return out, nil
}
