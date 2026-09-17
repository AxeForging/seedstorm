package relations

import (
	"math"
	"testing"

	"github.com/AxeForging/seedstorm/internal/db"
)

func TestShapeFromHistogram_ComputesDegreesSharesAndPercentiles(t *testing.T) {
	// Degrees 1, 1, 3, 10 over 5 parents, 2 NULL child rows.
	buckets := []db.DegreeBucket{
		{Min: 1, Max: 1, Parents: 2, Children: 2},
		{Min: 3, Max: 3, Parents: 1, Children: 3},
		{Min: 10, Max: 10, Parents: 1, Children: 10},
	}
	s := shapeFromHistogram(buckets, 5, 2)
	if s.Parents != 5 || s.Children != 15 || s.NullRows != 2 || s.ParentsWithChildren != 4 {
		t.Fatalf("counts = %+v", s)
	}
	if s.Min != 1 || s.Max != 10 || s.Avg != 3.75 || s.P50 != 1 || s.P95 != 10 {
		t.Fatalf("degrees = %+v", s)
	}
	if math.Abs(s.ZeroShare-0.2) > 1e-9 || math.Abs(s.NullShare-2.0/17.0) > 1e-9 {
		t.Fatalf("shares = %+v", s)
	}
}

// Above the exact buckets a percentile is the bucket's largest observed degree,
// never beyond the maximum.
func TestShapeFromHistogram_PercentileInAPowerOfTwoBucket(t *testing.T) {
	buckets := []db.DegreeBucket{
		{Min: 1, Max: 1, Parents: 90, Children: 90},
		{Min: 40, Max: 60, Parents: 10, Children: 500},
	}
	s := shapeFromHistogram(buckets, 100, 0)
	if s.P50 != 1 || s.P95 != 60 || s.Max != 60 || s.ZeroShare != 0 {
		t.Fatalf("shape = %+v", s)
	}
}

func TestShapeFromHistogram_NoChildren(t *testing.T) {
	s := shapeFromHistogram(nil, 7, 3)
	if s.ParentsWithChildren != 0 || s.ZeroShare != 1 || s.Avg != 0 || s.NullShare != 1 {
		t.Fatalf("shape = %+v", s)
	}
}

// Postgres most_common_freqs are shares of all rows (NULLs included).
func TestShapeFromEstimate(t *testing.T) {
	s := shapeFromEstimate(db.DegreeEstimate{ChildRows: 1000, ParentRows: 100, Distinct: 80, NullFraction: 0.1, MaxFraction: 0.05})
	if !s.Estimated || s.Parents != 100 || s.ParentsWithChildren != 80 || math.Abs(s.ZeroShare-0.2) > 1e-9 {
		t.Fatalf("estimate = %+v", s)
	}
	if math.Abs(s.Avg-900.0/80.0) > 1e-9 || s.Max != 50 || s.NullRows != 100 {
		t.Fatalf("estimate degrees = %+v", s)
	}
	unknown := shapeFromEstimate(db.DegreeEstimate{ChildRows: -1, ParentRows: -1, Distinct: -1, NullFraction: -1, MaxFraction: -1})
	if unknown.Avg != 0 || unknown.Max != -1 {
		t.Fatalf("unknown estimate = %+v", unknown)
	}
}
