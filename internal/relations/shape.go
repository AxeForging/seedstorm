// Package relations measures relationship shapes: for every foreign key, how
// many children each parent has (minimum, maximum, average, percentiles and a
// histogram), how many parents have none, and how many keys are NULL.
package relations

import (
	"math"

	"github.com/AxeForging/seedstorm/internal/db"
)

// Shape is one foreign key's degree distribution. Estimated shapes come from
// planner statistics; unknown numbers are -1.
type Shape struct {
	Child        string `json:"child" yaml:"child"`
	Column       string `json:"column" yaml:"column"`
	Parent       string `json:"parent" yaml:"parent"`
	ParentColumn string `json:"parentColumn" yaml:"parentColumn"`
	SelfRef      bool   `json:"selfRef,omitempty" yaml:"selfRef,omitempty"`

	Parents             int64   `json:"parents" yaml:"parents"`
	Children            int64   `json:"children" yaml:"children"`
	NullRows            int64   `json:"nullRows" yaml:"nullRows"`
	ParentsWithChildren int64   `json:"parentsWithChildren" yaml:"parentsWithChildren"`
	ZeroShare           float64 `json:"zeroShare" yaml:"zeroShare"`
	NullShare           float64 `json:"nullShare" yaml:"nullShare"`
	Min                 int64   `json:"min" yaml:"min"`
	Max                 int64   `json:"max" yaml:"max"`
	Avg                 float64 `json:"avg" yaml:"avg"`
	P50                 int64   `json:"p50" yaml:"p50"`
	P95                 int64   `json:"p95" yaml:"p95"`

	Histogram []db.DegreeBucket `json:"histogram,omitempty" yaml:"histogram,omitempty"`
	Estimated bool              `json:"estimated,omitempty" yaml:"estimated,omitempty"`
	// Indexed reports the key leads an index (exact scans are cheap).
	Indexed bool `json:"indexed" yaml:"indexed"`
	// Large marks a child table above Options.LargeRows (warned before exact scans).
	Large   bool           `json:"large,omitempty" yaml:"large,omitempty"`
	Outcome db.ReadOutcome `json:"outcome" yaml:"outcome"`
	Detail  string         `json:"detail,omitempty" yaml:"detail,omitempty"`
}

// shapeFromHistogram derives a shape from bucketed degrees, the parent count
// and NULL child rows.
func shapeFromHistogram(buckets []db.DegreeBucket, parents, nulls int64) Shape {
	s := Shape{Parents: parents, NullRows: nulls, Histogram: buckets, Min: 0, Max: 0}
	for i, b := range buckets {
		s.ParentsWithChildren += b.Parents
		s.Children += b.Children
		if i == 0 || b.Min < s.Min {
			s.Min = b.Min
		}
		s.Max = max(s.Max, b.Max)
	}
	if s.ParentsWithChildren > 0 {
		s.Avg = float64(s.Children) / float64(s.ParentsWithChildren)
		s.P50 = percentile(buckets, s.ParentsWithChildren, 0.50)
		s.P95 = percentile(buckets, s.ParentsWithChildren, 0.95)
	}
	if parents > 0 {
		s.ZeroShare = float64(max(parents-s.ParentsWithChildren, 0)) / float64(parents)
	}
	if total := s.Children + nulls; total > 0 {
		s.NullShare = float64(nulls) / float64(total)
	}
	return s
}

// percentile walks the buckets to the q-th parent; within a bucket spanning
// several degrees it reports the bucket's largest observed degree.
func percentile(buckets []db.DegreeBucket, parents int64, q float64) int64 {
	rank := int64(math.Ceil(q * float64(parents)))
	var seen int64
	for _, b := range buckets {
		seen += b.Parents
		if seen >= rank {
			return b.Max
		}
	}
	if len(buckets) == 0 {
		return 0
	}
	return buckets[len(buckets)-1].Max
}

// shapeFromEstimate derives what statistics can tell: average degree, the
// share of parents without children, and a maximum from the most common key.
func shapeFromEstimate(e db.DegreeEstimate) Shape {
	s := Shape{Estimated: true, Parents: e.ParentRows, Min: -1, Max: -1, P50: -1, P95: -1, NullRows: -1, NullShare: -1, ZeroShare: -1}
	if e.ChildRows >= 0 && e.NullFraction >= 0 {
		s.NullRows = int64(math.Round(float64(e.ChildRows) * e.NullFraction))
		s.NullShare = e.NullFraction
	}
	nonNull := e.ChildRows
	if s.NullRows > 0 {
		nonNull -= s.NullRows
	}
	s.Children = nonNull
	if e.Distinct > 0 {
		s.ParentsWithChildren = e.Distinct
		if nonNull >= 0 {
			s.Avg = float64(nonNull) / float64(e.Distinct)
		}
		if e.ParentRows > 0 {
			s.ZeroShare = math.Max(0, 1-float64(e.Distinct)/float64(e.ParentRows))
		}
	}
	if e.MaxFraction > 0 && e.ChildRows > 0 {
		s.Max = int64(math.Round(e.MaxFraction * float64(e.ChildRows)))
	}
	return s
}
