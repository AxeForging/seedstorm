package compare

import (
	"math"
	"sort"
	"strings"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/relations"
)

// ShapeStatus classifies one relationship across two databases.
type ShapeStatus string

const (
	ShapeSame       ShapeStatus = "same"
	ShapeDiffers    ShapeStatus = "differs"
	ShapeSourceOnly ShapeStatus = "source_only"
	ShapeTargetOnly ShapeStatus = "target_only"
	// ShapeUnknown: a side was not measured (timed out, skipped, failed).
	ShapeUnknown ShapeStatus = "unknown"
)

// ShapeDrift is how a relationship's shape differs from source to target.
// Deltas are target minus source.
type ShapeDrift struct {
	Child          string           `json:"child"`
	Column         string           `json:"column"`
	Status         ShapeStatus      `json:"status"`
	Source         *relations.Shape `json:"source,omitempty"`
	Target         *relations.Shape `json:"target,omitempty"`
	AvgDelta       float64          `json:"avgDelta"`
	P95Delta       int64            `json:"p95Delta"`
	MaxDelta       int64            `json:"maxDelta"`
	ZeroShareDelta float64          `json:"zeroShareDelta"`
}

// shapeTolerance is how close two averages and shares must be to count as the
// same shape.
const shapeTolerance = 0.05

// DiffShapes matches relationships by child table and column, ignoring case.
func DiffShapes(source, target []relations.Shape) []ShapeDrift {
	key := func(s relations.Shape) string { return strings.ToLower(s.Child + "." + s.Column) }
	tgt := make(map[string]relations.Shape, len(target))
	for _, s := range target {
		tgt[key(s)] = s
	}
	var out []ShapeDrift
	matched := map[string]bool{}
	for _, s := range source {
		src := s
		d := ShapeDrift{Child: s.Child, Column: s.Column, Source: &src}
		t, ok := tgt[key(s)]
		if !ok {
			d.Status = ShapeSourceOnly
			out = append(out, d)
			continue
		}
		matched[key(s)] = true
		t2 := t
		d.Target = &t2
		if !measured(s) || !measured(t) {
			d.Status = ShapeUnknown
			out = append(out, d)
			continue
		}
		d.AvgDelta = t.Avg - s.Avg
		d.P95Delta = t.P95 - s.P95
		d.MaxDelta = t.Max - s.Max
		d.ZeroShareDelta = t.ZeroShare - s.ZeroShare
		d.Status = ShapeSame
		if math.Abs(d.AvgDelta) > shapeTolerance*math.Max(1, s.Avg) || d.P95Delta != 0 || d.MaxDelta != 0 || math.Abs(d.ZeroShareDelta) > shapeTolerance {
			d.Status = ShapeDiffers
		}
		out = append(out, d)
	}
	for _, t := range target {
		if matched[key(t)] {
			continue
		}
		t2 := t
		out = append(out, ShapeDrift{Child: t.Child, Column: t.Column, Status: ShapeTargetOnly, Target: &t2})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Child+"."+out[i].Column) < strings.ToLower(out[j].Child+"."+out[j].Column)
	})
	return out
}

func measured(s relations.Shape) bool {
	return s.Outcome == db.OutcomeOK || s.Outcome == relations.OutcomeEstimated || s.Outcome == relations.OutcomeSkippedUnindexed
}
