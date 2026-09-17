package faker

import (
	"math"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/schema"
)

func shapeSchema() *schema.Schema {
	return &schema.Schema{Tables: map[string]schema.Table{
		"customers": {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}}},
		"orders": {Columns: map[string]schema.Column{
			"id":          {Type: "integer", PK: true},
			"customer_id": {Type: "integer", FK: "customers.id"},
			"coupon_id":   {Type: "integer", FK: "customers.id", Nullable: true},
			"note":        {Type: "text", Faker: "word"},
		}},
		"tree": {Columns: map[string]schema.Column{
			"id":        {Type: "integer", PK: true},
			"parent_id": {Type: "integer", FK: "tree.id", Nullable: true},
		}},
	}}
}

// degreesOf counts children per parent id from generated rows.
func degreesOf(rows []map[string]interface{}, col string) (map[interface{}]int, int) {
	out := map[interface{}]int{}
	nulls := 0
	for _, r := range rows {
		if r[col] == nil {
			nulls++
			continue
		}
		out[r[col]]++
	}
	return out, nulls
}

type shapeStats struct {
	avg       float64
	max, p95  int
	zeroShare float64
}

func statsOf(degrees map[interface{}]int, parents int) shapeStats {
	var vals []int
	sum := 0
	for _, d := range degrees {
		vals = append(vals, d)
		sum += d
	}
	sort.Ints(vals)
	st := shapeStats{zeroShare: float64(parents-len(vals)) / float64(parents)}
	if len(vals) > 0 {
		st.avg = float64(sum) / float64(len(vals))
		st.max = vals[len(vals)-1]
		st.p95 = vals[int(math.Ceil(0.95*float64(len(vals))))-1]
	}
	return st
}

func generateShaped(t *testing.T, parents, children int, shapes map[string]Shape) (map[string][]map[string]interface{}, []GenerationWarning) {
	t.Helper()
	sc := shapeSchema()
	var mu sync.Mutex
	var warnings []GenerationWarning
	opts := DefaultGenerateOptions()
	opts.Shapes = shapes
	opts.OnWarning = func(w GenerationWarning) { mu.Lock(); warnings = append(warnings, w); mu.Unlock() }
	g, err := NewStream(sc, []string{"customers", "orders"}, []string{"customers", "orders"}, nil, "pgx", nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := g.Generate([]string{"customers", "orders"}, 0, 0, map[string]int{"customers": parents, "orders": children}, opts)
	if err != nil {
		t.Fatal(err)
	}
	return data, warnings
}

// A shaped key on 10k parents lands on the target: average and zero share
// within tolerance, p95 close, and no parent above max.
func TestShapes_DegreesFollowTheHistogramAndNeverExceedMax(t *testing.T) {
	shape := Shape{Min: 1, Avg: 3.2, Max: 40, ZeroShare: 0.25, Histogram: []ShapeBucket{
		{Min: 1, Max: 1, Parents: 3000}, {Min: 2, Max: 3, Parents: 2500}, {Min: 4, Max: 7, Parents: 1700}, {Min: 8, Max: 40, Parents: 300},
	}}
	parents, children := 10_000, 24_000 // 7,500 parents with children × 3.2
	data, warnings := generateShaped(t, parents, children, map[string]Shape{"orders.customer_id": shape})
	if len(data["orders"]) != children {
		t.Fatalf("%d orders, want %d", len(data["orders"]), children)
	}
	degrees, nulls := degreesOf(data["orders"], "customer_id")
	st := statsOf(degrees, parents)
	if nulls != 0 || st.max > shape.Max {
		t.Fatalf("max %d (limit %d), nulls %d", st.max, shape.Max, nulls)
	}
	if math.Abs(st.avg-shape.Avg) > 0.1 || math.Abs(st.zeroShare-shape.ZeroShare) > 0.01 || st.p95 < 7 || st.p95 > 12 {
		t.Fatalf("achieved %+v, want avg %.1f zero %.2f p95 ≈8", st, shape.Avg, shape.ZeroShare)
	}
	for _, w := range warnings {
		t.Errorf("unexpected warning on a feasible shape: %+v", w)
	}
	// Unshaped coupon_id stays uniform: no parent collects most rows.
	coupons, _ := degreesOf(data["orders"], "coupon_id")
	if statsOf(coupons, parents).max > 12 {
		t.Fatalf("unshaped key is not uniform: %+v", statsOf(coupons, parents))
	}
}

func TestShapes_NullShareIsExact(t *testing.T) {
	data, _ := generateShaped(t, 1000, 5000, map[string]Shape{"orders.coupon_id": {Min: 1, Avg: 4, Max: 6, NullShare: 0.3}})
	degrees, nulls := degreesOf(data["orders"], "coupon_id")
	if nulls != 1500 {
		t.Fatalf("%d NULL coupons, want exactly 1500", nulls)
	}
	if st := statsOf(degrees, 1000); st.max > 6 {
		t.Fatalf("max %d above 6", st.max)
	}
}

// Same seed, same shaped rows; and a shape on one key leaves every other
// key's picks exactly as without shapes (no extra random draws).
func TestShapes_ReproducibleAndUnshapedPicksUnchanged(t *testing.T) {
	shapes := map[string]Shape{"orders.customer_id": {Min: 1, Avg: 2, Max: 5}}
	run := func(s map[string]Shape) map[string][]map[string]interface{} {
		SeedRandom(42)
		defer SeedRandom(0)
		data, _ := generateShaped(t, 200, 400, s)
		return data
	}
	if a, b := run(shapes), run(shapes); !reflect.DeepEqual(a, b) {
		t.Fatal("same seed produced different shaped rows")
	}
	unrelated := map[string]Shape{"payments.customer_id": {Min: 1, Avg: 2, Max: 5}}
	if a, b := run(nil), run(unrelated); !reflect.DeepEqual(a, b) {
		t.Fatal("a shape for a key outside the run changed the generated rows")
	}
}

// Infeasible shapes are adjusted in bounded steps with a report, never loops.
func TestDealDegrees_InfeasibleInputsAdjustAndReport(t *testing.T) {
	cases := []struct {
		name    string
		n       int
		slots   int64
		shape   Shape
		note    string
		wantMax int32
	}{
		{"rows above parents × max", 100, 1000, Shape{Min: 1, Avg: 2, Max: 3}, "max raised to 10", 10},
		{"zero share too high for max", 100, 300, Shape{Min: 1, Avg: 3, Max: 3, ZeroShare: 0.9}, "share of parents without children lowered", 3},
		{"min above what rows allow", 100, 50, Shape{Min: 5, Avg: 5, Max: 9}, "10 parents get children", 9},
		{"one parent", 1, 7, Shape{Min: 1, Avg: 1, Max: 1}, "max raised to 7", 7},
		{"feasible", 100, 200, Shape{Min: 1, Avg: 2, Max: 4}, "", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			degrees, notes := dealDegrees(defaultGen.rnd, tc.n, tc.slots, tc.shape)
			var sum int64
			var top int32
			for _, d := range degrees {
				sum += int64(d)
				top = max(top, d)
			}
			if sum != tc.slots {
				t.Fatalf("degrees sum to %d, want %d", sum, tc.slots)
			}
			if top > tc.wantMax {
				t.Fatalf("max degree %d above %d", top, tc.wantMax)
			}
			joined := strings.Join(notes, "; ")
			if tc.note == "" && joined != "" || tc.note != "" && !strings.Contains(joined, tc.note) {
				t.Fatalf("notes %q, want %q", joined, tc.note)
			}
		})
	}
}

// Dealing stays linear: 500k parents (the pool limit) with 5M slots.
func TestDealDegrees_PoolLimitIsFast(t *testing.T) {
	start := time.Now()
	degrees, _ := dealDegrees(defaultGen.rnd, 500_000, 5_000_000, Shape{Min: 1, Avg: 10, Max: 1000, ZeroShare: 0.1})
	var sum int64
	for _, d := range degrees {
		sum += int64(d)
	}
	if sum != 5_000_000 {
		t.Fatalf("sum %d", sum)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("dealing took %s", elapsed)
	}
}

// A retried row gives its slots back, and a row dropped for a UNIQUE group
// returns its parent's slot: the dealt total is still reached exactly.
func TestShaper_UndoAndReturnRestoreSlots(t *testing.T) {
	sc := shapeSchema()
	s := newShaper()
	opts := GenerateOptions{Shapes: map[string]Shape{"orders.customer_id": {Min: 2, Avg: 2, Max: 2}, "orders.coupon_id": {Min: 1, Avg: 1, Max: 1, NullShare: 0.5}}}
	s.beginTable(sc, "orders", 4, nil, opts)
	pool := []interface{}{int64(1), int64(2)}
	s.beginRow()
	v, ok := s.pick(defaultGen.rnd, "orders", "customer_id", pool)
	if !ok || v == nil {
		t.Fatalf("pick = %v %v", v, ok)
	}
	s.pick(defaultGen.rnd, "orders", "coupon_id", pool)
	edge := s.edges["orders.customer_id"]
	if edge.slots != 3 {
		t.Fatalf("slots after one pick = %d", edge.slots)
	}
	s.undoRow()
	if edge.slots != 4 || edge.rowsLeft != 4 || s.edges["orders.coupon_id"].rowsLeft != 4 {
		t.Fatalf("undo did not restore: slots %d rows %d", edge.slots, edge.rowsLeft)
	}
	s.beginRow()
	v, _ = s.pick(defaultGen.rnd, "orders", "customer_id", pool)
	s.returnRows("orders", []map[string]interface{}{{"customer_id": v}})
	if edge.slots != 4 {
		t.Fatalf("returned row did not give its slot back: %d", edge.slots)
	}
	for i := 0; i < 4; i++ {
		s.beginRow()
		s.pick(defaultGen.rnd, "orders", "customer_id", pool)
	}
	if edge.remain[0] != 0 || edge.remain[1] != 0 || edge.overflow != 0 {
		t.Fatalf("every parent should hold exactly 2: remain %v overflow %d", edge.remain, edge.overflow)
	}
}

func TestShapeSkipReason(t *testing.T) {
	sc := shapeSchema()
	sc.Tables["links"] = schema.Table{Columns: map[string]schema.Column{
		"a_id": {Type: "integer", PK: true, FK: "customers.id"},
		"b_id": {Type: "integer", PK: true, FK: "customers.id"},
	}}
	for key, want := range map[string]string{
		"orders.customer_id": "",
		"orders.note":        "not a foreign key",
		"tree.parent_id":     "self-references",
		"links.a_id":         "junction keys",
		"orders.missing":     "not in the table",
		"nowhere.x":          "not in the schema",
	} {
		table, col, _ := strings.Cut(key, ".")
		got := ShapeSkipReason(sc, table, col)
		if (want == "" && got != "") || (want != "" && !strings.Contains(got, want)) {
			t.Errorf("%s: %q, want %q", key, got, want)
		}
	}
}

func TestDeriveShapedRows_TopDownExplicitWinsAndConflictsNamed(t *testing.T) {
	sc := shapeSchema()
	sc.Tables["items"] = schema.Table{Columns: map[string]schema.Column{
		"id":       {Type: "integer", PK: true},
		"order_id": {Type: "integer", FK: "orders.id"},
		"buyer_id": {Type: "integer", FK: "customers.id"},
	}}
	order := []string{"customers", "orders", "items", "tree"}
	shapes := map[string]Shape{
		"orders.customer_id": {Min: 1, Avg: 4, Max: 9, ZeroShare: 0.5}, // 100 × 0.5 × 4 = 200
		"orders.coupon_id":   {Min: 1, Avg: 1, Max: 1, NullShare: 0.5}, // parents 100 → 200 too
		"items.order_id":     {Min: 1, Avg: 3, Max: 5},                 // 200 × 3 = 600
		"items.buyer_id":     {Min: 1, Avg: 1, Max: 2},                 // 100 × 1 = 100, fewer parents
		"tree.parent_id":     {Min: 1, Avg: 2, Max: 3},                 // self-reference: not derived
	}
	derived, notes := DeriveShapedRows(sc, order, 100, nil, shapes)
	if derived["orders"] != 200 || derived["items"] != 600 || len(derived) != 2 {
		t.Fatalf("derived = %v", derived)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "items: shaped keys imply different row counts") || !strings.Contains(notes[0], "items.order_id decides 600") {
		t.Fatalf("notes = %v", notes)
	}
	// An explicit orders count wins; items then follows its larger parent
	// (100 customers → 100) over 50 orders × 3 = 150.
	derived, notes = DeriveShapedRows(sc, order, 100, map[string]int{"orders": 50}, shapes)
	if _, has := derived["orders"]; has || derived["items"] != 100 || !strings.Contains(strings.Join(notes, ";"), "items.order_id → 150") {
		t.Fatalf("explicit orders count must win and feed items: %v %v", derived, notes)
	}
}
