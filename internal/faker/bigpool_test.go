package faker

import (
	"strings"
	"testing"
)

// A parent table larger than the key pool: the pool holds a sample, so the
// children are dealt a round at a time over that sample instead of crowding
// every child onto it. With a live connection the sample rotates
// (redrawParents), so the whole parent table gets its share; generating
// without one, the sampled parents keep the shape among themselves. Either
// way the run says the pool is smaller than the table.
func TestShapes_ParentsAboveThePoolLimitAreDealtInRounds(t *testing.T) {
	defer func(old int) { poolLimit = old }(poolLimit)
	poolLimit = 200
	const parents, children = 2000, 6000
	shape := Shape{Min: 1, Avg: 3, Max: 8, ZeroShare: 0.1}
	var warnings []string
	opts := DefaultGenerateOptions()
	opts.Shapes = map[string]Shape{"orders.customer_id": shape}
	opts.OnWarning = func(w GenerationWarning) { warnings = append(warnings, w.Reason) }
	g, err := NewStream(shapeSchema(), []string{"customers", "orders"}, []string{"customers", "orders"}, nil, "pgx", nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := g.Generate([]string{"customers", "orders"}, 0, 0, map[string]int{"customers": parents, "orders": children}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(data["orders"]) != children {
		t.Fatalf("%d rows, want %d", len(data["orders"]), children)
	}
	if notice := strings.Join(warnings, " | "); !strings.Contains(notice, "larger than the key pool") {
		t.Fatalf("the run never said the parent table is larger than the pool: %q", notice)
	}
	// Inside the sample the shape holds: about 90% of the 200 pooled parents
	// have children, not a handful carrying everything.
	degrees, _ := degreesOf(data["orders"], "customer_id")
	if len(degrees) < 160 || len(degrees) > poolLimit {
		t.Fatalf("%d parents have children, want about 90%% of the %d in the pool", len(degrees), poolLimit)
	}
	// Each round respects max, so no parent takes more than its rounds allow.
	rounds := (children + 539) / 540 // 200 parents × 0.9 with children × 3 each
	if st := statsOf(degrees, parents); st.max > int(shape.Max)*rounds {
		t.Fatalf("max %d above %d rounds of at most %d children", st.max, rounds, shape.Max)
	}
}
