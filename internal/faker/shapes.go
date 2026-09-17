package faker

import (
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// Shape is the target degree distribution of one foreign key: how many
// children each parent gets. Histogram buckets are weighted by how many
// parents fall in them; without buckets degrees spread evenly around Avg.
type Shape struct {
	Min       int           `json:"min" yaml:"min"`
	Avg       float64       `json:"avg" yaml:"avg"`
	Max       int           `json:"max" yaml:"max"`
	ZeroShare float64       `json:"zeroShare,omitempty" yaml:"zeroShare,omitempty"`
	NullShare float64       `json:"nullShare,omitempty" yaml:"nullShare,omitempty"`
	Histogram []ShapeBucket `json:"histogram,omitempty" yaml:"histogram,omitempty"`
}

// ShapeBucket is a degree range and how many parents had a degree in it.
type ShapeBucket struct {
	Min     int   `json:"min" yaml:"min"`
	Max     int   `json:"max" yaml:"max"`
	Parents int64 `json:"parents" yaml:"parents"`
}

// ShapeKey names a shaped foreign key: "table.column".
func ShapeKey(table, column string) string { return table + "." + column }

// ShapeSkipReason says why a foreign key cannot be shaped, or "" when it can.
func ShapeSkipReason(sc *schema.Schema, table, column string) string {
	t, ok := sc.Tables[table]
	if !ok {
		return "table is not in the schema"
	}
	col, ok := t.Columns[column]
	if !ok {
		return "column is not in the table"
	}
	parent, _ := splitFK(col.FK)
	switch {
	case parent == "":
		return "column is not a foreign key"
	case parent == table:
		return "self-references are generated per chunk and cannot be shaped"
	case keyIsAllForeign(t):
		return "junction keys are enumerated and cannot be shaped"
	case col.PK:
		return "key columns cannot be shaped"
	}
	return ""
}

// shaper deals shaped foreign keys for the tables one stream generates. It is
// only created when shapes are given, so unshaped runs draw exactly the same
// random numbers as before.
type shaper struct {
	edges map[string]*edgeDealer
	// begun marks tables whose edges were set up (a table filled in rounds
	// keeps one dealer across calls).
	begun map[string]bool
	// row is what the row being generated took, returned when it is retried.
	row []shapePick
}

type shapePick struct {
	edge   *edgeDealer
	parent int // -1: NULL, -2: an unshaped overflow pick
}

func newShaper() *shaper {
	return &shaper{edges: map[string]*edgeDealer{}, begun: map[string]bool{}}
}

// beginTable prepares the table's shaped edges for total rows (the whole
// table, across chunks and calls). Edges that cannot be shaped are reported.
// beginTable prepares the shaped edges of tableName for total rows. sampled
// names parent tables whose key pool is a sample of a larger table.
func (s *shaper) beginTable(sc *schema.Schema, tableName string, total int, sampled map[string]bool, opts GenerateOptions) {
	if s == nil || s.begun[tableName] {
		return
	}
	s.begun[tableName] = true
	// Dealers hold a few bytes per parent: drop those of finished tables.
	for key, e := range s.edges {
		if e.table != tableName {
			delete(s.edges, key)
		}
	}
	table := sc.Tables[tableName]
	for _, colName := range sortedColumnNames(table) {
		key := ShapeKey(tableName, colName)
		shape, ok := opts.Shapes[key]
		if !ok {
			continue
		}
		if reason := ShapeSkipReason(sc, tableName, colName); reason != "" {
			warnShape(opts, tableName, key+" not shaped: "+reason)
			continue
		}
		col := table.Columns[colName]
		parent, _ := splitFK(col.FK)
		s.edges[key] = &edgeDealer{
			key: key, table: tableName, shape: shape, nullable: col.Nullable, total: total,
			sampledParent: sampled[parent],
			warn:          func(msg string) { warnShape(opts, tableName, msg) },
		}
	}
}

func warnShape(opts GenerateOptions, table, reason string) {
	if opts.OnWarning != nil {
		opts.OnWarning(GenerationWarning{Table: table, Reason: reason})
	}
}

func (s *shaper) beginRow() {
	if s != nil {
		s.row = s.row[:0]
	}
}

// undoRow returns the slots of a row that was generated and then discarded.
func (s *shaper) undoRow() {
	if s == nil {
		return
	}
	for _, p := range s.row {
		p.edge.giveBack(p.parent)
	}
	s.row = s.row[:0]
}

// pick chooses a value for a shaped foreign key; ok is false when the column
// is not shaped (the caller picks uniformly as before).
func (s *shaper) pick(rnd randomSource, tableName, colName string, pool []interface{}) (interface{}, bool) {
	if s == nil || len(pool) == 0 {
		return nil, false
	}
	e := s.edges[ShapeKey(tableName, colName)]
	if e == nil {
		return nil, false
	}
	e.ensure(rnd, pool)
	parent := e.draw(rnd)
	s.row = append(s.row, shapePick{edge: e, parent: parent})
	if parent == -1 {
		return nil, true
	}
	if parent < 0 {
		parent = rnd.Number(0, len(e.pool)-1)
	}
	return e.pool[parent], true
}

// returnRows gives back the slots of rows dropped after generation (UNIQUE
// groups), matched to parents by value.
func (s *shaper) returnRows(tableName string, rows []map[string]interface{}) {
	if s == nil || len(rows) == 0 {
		return
	}
	for key, e := range s.edges {
		if e.table != tableName || e.pool == nil {
			continue
		}
		col := strings.TrimPrefix(key, tableName+".")
		for _, row := range rows {
			v, ok := row[col]
			if !ok {
				continue
			}
			if v == nil {
				e.giveBack(-1)
				continue
			}
			if i, ok := e.indexOf(v); ok {
				e.giveBack(i)
			}
		}
	}
}

// fork returns a shaper for one table generated on a fork: it keeps the
// dealers of that table only (each fork generates a different table).
func (s *shaper) fork(tableName string) *shaper {
	if s == nil {
		return nil
	}
	out := newShaper()
	for key, e := range s.edges {
		if e.table == tableName {
			out.edges[key] = e
		}
	}
	out.begun[tableName] = s.begun[tableName]
	return out
}

// edgeDealer hands out one foreign key's values so the children per parent
// follow the target shape: every parent gets a degree up front, and each pick
// takes a remaining slot at random (weighted by the slots left, a Fenwick
// tree), which is a shuffled deck without storing the deck.
type edgeDealer struct {
	key      string
	table    string
	shape    Shape
	nullable bool
	total    int
	warn     func(string)

	// sampledParent means the pool holds a sample of a larger parent table:
	// rows are then dealt a round at a time, sized to the sample, instead of
	// crowding every child onto the sampled parents.
	sampledParent bool
	pool          []interface{}
	degree        []int32
	remain        []int32
	tree          []int64
	slots         int64
	rowsLeft      int64
	nullsLeft     int64
	overflow      int64
	index         map[string]int
	approx        bool
	// dealt marks the first deal: later rounds repeat its adjustments, which
	// the user has already been told about.
	dealt bool
}

// ensure deals degrees over pool, again when the pool changed (a parent
// sample was redrawn), for the rows still to come.
func (e *edgeDealer) ensure(rnd randomSource, pool []interface{}) {
	if e.pool != nil && samePool(e.pool, pool) {
		return
	}
	rows := int64(e.total)
	if e.pool != nil {
		rows = max(e.rowsLeft, 0)
	}
	if e.sampledParent {
		// Only this sample's share of the children: the rest go to the parents
		// of later samples (redrawParents rotates them), so each parent keeps
		// the shape instead of absorbing the whole table's children.
		if round := e.roundRows(len(pool)); round < rows {
			rows = round
			if !e.approx {
				e.approx = true
				e.warn(e.key + ": the parent table is larger than the key pool, so the shape is dealt over each sample of parents in turn")
			}
		}
	}
	e.pool, e.index = pool, nil
	e.deal(rnd, rows)
}

// deal gives every parent in the pool a degree for the next rows children.
func (e *edgeDealer) deal(rnd randomSource, rows int64) {
	e.rowsLeft = rows
	e.nullsLeft = 0
	if e.nullable {
		e.nullsLeft = int64(math.Round(e.shape.NullShare * float64(rows)))
	}
	slots := rows - e.nullsLeft
	degrees, notes := dealDegrees(rnd, len(e.pool), slots, e.shape)
	if !e.dealt {
		e.dealt = true
		for _, n := range notes {
			e.warn(e.key + ": " + n)
		}
	}
	e.degree = degrees
	e.remain = append([]int32(nil), degrees...)
	e.tree = make([]int64, len(degrees)+1)
	e.slots = 0
	for i, d := range degrees {
		e.add(i, int64(d))
		e.slots += int64(d)
	}
}

// roundRows is how many children a sample of n parents should take: the
// shape's average over the parents that have children, plus their NULL share.
func (e *edgeDealer) roundRows(n int) int64 {
	avg := e.shape.Avg
	if avg <= 0 {
		avg = float64(max(e.shape.Min, 1))
	}
	rows := float64(n) * (1 - e.shape.ZeroShare) * avg
	if e.nullable && e.shape.NullShare > 0 && e.shape.NullShare < 1 {
		rows /= 1 - e.shape.NullShare
	}
	return max(int64(math.Round(rows)), 1)
}

func samePool(a, b []interface{}) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

// draw returns a parent index, -1 for NULL, or -2 when every dealt slot is
// taken (rows beyond the plan, e.g. enum coverage): the caller picks uniformly.
func (e *edgeDealer) draw(rnd randomSource) int {
	if e.nullsLeft > 0 && (e.rowsLeft <= e.nullsLeft || rnd.Number(0, int(e.rowsLeft)-1) < int(e.nullsLeft)) {
		e.nullsLeft--
		e.rowsLeft--
		return -1
	}
	e.rowsLeft--
	if e.slots <= 0 && e.sampledParent {
		// This sample's round is used up and no new sample arrived: deal
		// another round over the parents at hand rather than crowding them.
		e.deal(rnd, e.roundRows(len(e.pool)))
	}
	if e.slots <= 0 {
		if e.overflow == 0 {
			e.warn(e.key + ": more rows than the shape planned; extra rows pick parents uniformly")
		}
		e.overflow++
		return -2
	}
	r := int64(rnd.Number(0, int(e.slots)-1))
	i := e.find(r)
	e.remain[i]--
	e.add(i, -1)
	e.slots--
	return i
}

func (e *edgeDealer) giveBack(parent int) {
	e.rowsLeft++
	switch {
	case parent == -1:
		e.nullsLeft++
	case parent == -2:
		e.overflow--
	case parent >= 0 && parent < len(e.remain) && e.remain[parent] < e.degree[parent]:
		e.remain[parent]++
		e.add(parent, 1)
		e.slots++
	}
}

func (e *edgeDealer) indexOf(v interface{}) (int, bool) {
	if e.index == nil {
		e.index = make(map[string]int, len(e.pool))
		for i, p := range e.pool {
			e.index[keyValue("", p)] = i
		}
	}
	i, ok := e.index[keyValue("", v)]
	return i, ok
}

func (e *edgeDealer) add(i int, delta int64) {
	for j := i + 1; j < len(e.tree); j += j & -j {
		e.tree[j] += delta
	}
}

// find returns the parent holding the r-th remaining slot (0-based).
func (e *edgeDealer) find(r int64) int {
	pos := 0
	step := 1
	for step*2 < len(e.tree) {
		step *= 2
	}
	for ; step > 0; step /= 2 {
		if next := pos + step; next < len(e.tree) && e.tree[next] <= r {
			pos = next
			r -= e.tree[next]
		}
	}
	return pos
}

// dealDegrees gives each of n parents a degree so the degrees sum to slots,
// drawn from the shape and adjusted in bounded passes. notes explain every
// change made to fit (zero share raised, max raised). It never loops without
// progress: at most maxPasses proportional passes, then one exact pass.
func dealDegrees(rnd randomSource, n int, slots int64, shape Shape) ([]int32, []string) {
	degrees := make([]int32, n)
	if n == 0 || slots <= 0 {
		return degrees, nil
	}
	var notes []string
	lo := int64(max(shape.Min, 1))
	hi := int64(shape.Max)
	if hi < lo {
		hi = lo
	}
	active := n - int(math.Round(shape.ZeroShare*float64(n)))
	active = min(max(active, 1), n)
	if need := ceilDiv(slots, hi); int64(active) < need {
		if need > int64(n) {
			newHi := ceilDiv(slots, int64(n))
			notes = append(notes, fmt.Sprintf("%d rows over %d parents do not fit max %d: max raised to %d", slots, n, hi, newHi))
			hi = newHi
			need = int64(n)
		} else {
			notes = append(notes, fmt.Sprintf("%d rows need %d parents with children at max %d: share of parents without children lowered", slots, need, hi))
		}
		active = int(need)
	}
	if int64(active)*lo > slots {
		fit := int(max(slots/lo, 1))
		notes = append(notes, fmt.Sprintf("%d rows cannot give %d parents at least %d children: %d parents get children", slots, active, lo, fit))
		active = fit
		if int64(active)*lo > slots {
			lo = 1
		}
	}

	// Choose which parents get children: a partial shuffle of the indexes.
	order := make([]int32, n)
	for i := range order {
		order[i] = int32(i)
	}
	for i := 0; i < active; i++ {
		j := rnd.Number(i, n-1)
		order[i], order[j] = order[j], order[i]
	}
	chosen := order[:active]
	var sum int64
	for _, i := range chosen {
		d := drawDegree(rnd, shape, lo, hi)
		degrees[i] = int32(d)
		sum += d
	}

	// Move the sum to slots: proportional steps first, then an exact sweep.
	const maxPasses = 8
	for pass := 0; pass < maxPasses && sum != slots; pass++ {
		diff := slots - sum
		movable := 0
		for _, i := range chosen {
			if (diff > 0 && int64(degrees[i]) < hi) || (diff < 0 && int64(degrees[i]) > lo) {
				movable++
			}
		}
		if movable == 0 {
			break
		}
		step := ceilDiv(abs64(diff), int64(movable))
		for _, i := range chosen {
			if sum == slots {
				break
			}
			left := abs64(slots - sum)
			d := int64(degrees[i])
			var move int64
			if diff > 0 {
				move = min(step, hi-d, left)
			} else {
				move = -min(step, d-lo, left)
			}
			degrees[i] = int32(d + move)
			sum += move
		}
	}
	for _, i := range chosen {
		if sum == slots {
			break
		}
		d := int64(degrees[i])
		if sum < slots {
			move := min(hi-d, slots-sum)
			degrees[i] = int32(d + move)
			sum += move
		} else {
			move := min(d-lo, sum-slots)
			degrees[i] = int32(d - move)
			sum -= move
		}
	}
	return degrees, notes
}

// drawDegree draws one parent's degree from the histogram (a bucket by its
// parent weight, then a value inside it), or around Avg without one.
func drawDegree(rnd randomSource, shape Shape, lo, hi int64) int64 {
	clamp := func(d int64) int64 { return min(max(d, lo), hi) }
	var total int64
	for _, b := range shape.Histogram {
		if b.Parents > 0 {
			total += b.Parents
		}
	}
	if total > 0 {
		r := int64(rnd.Number(0, int(min(total, math.MaxInt32))-1))
		if total > math.MaxInt32 {
			r = int64(float64(r) / float64(math.MaxInt32) * float64(total))
		}
		for _, b := range shape.Histogram {
			if b.Parents <= 0 {
				continue
			}
			if r < b.Parents {
				bMin, bMax := int64(max(b.Min, 1)), int64(max(b.Max, b.Min, 1))
				if bMax > bMin {
					return clamp(int64(rnd.Number(int(bMin), int(bMax))))
				}
				return clamp(bMin)
			}
			r -= b.Parents
		}
	}
	avg := int64(math.Round(shape.Avg))
	if avg < lo {
		return lo
	}
	spread := min(avg-lo, hi-avg)
	return clamp(avg - spread + int64(rnd.Number(0, int(2*spread))))
}

func ceilDiv(a, b int64) int64 {
	if b <= 0 {
		return a
	}
	return (a + b - 1) / b
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// droppedRows lists rows of before that are not in kept (same maps).
func droppedRows(before, kept []map[string]interface{}) []map[string]interface{} {
	if len(before) == len(kept) {
		return nil
	}
	keep := make(map[uintptr]bool, len(kept))
	for _, r := range kept {
		keep[reflect.ValueOf(r).Pointer()] = true
	}
	var out []map[string]interface{}
	for _, r := range before {
		if !keep[reflect.ValueOf(r).Pointer()] {
			out = append(out, r)
		}
	}
	return out
}

// DeriveShapedRows plans row counts from shapes: a shaped child table gets
// parents × (1 − zeroShare) × avg / (1 − nullShare) rows, top-down in order.
// Tables with an explicit count (tableRows) keep it. When a table's shaped
// keys imply different counts, the key with the most parent rows decides and
// notes name the others. It is a pure function: nothing loops or retries.
func DeriveShapedRows(sc *schema.Schema, order []string, rows int, tableRows map[string]int, shapes map[string]Shape) (map[string]int, []string) {
	derived := map[string]int{}
	var notes []string
	planned := func(table string) (int, bool) {
		if n, ok := derived[table]; ok {
			return n, true
		}
		n, _ := TableRowCount(table, rows, tableRows)
		return n, true
	}
	inRun := make(map[string]bool, len(order))
	for _, t := range order {
		inRun[t] = true
	}
	for _, tableName := range order {
		if _, explicit := tableRows[tableName]; explicit {
			continue
		}
		table := sc.Tables[tableName]
		best, bestParents := 0, -1
		bestKey := ""
		var implied []string
		counts := map[int]bool{}
		for _, colName := range sortedColumnNames(table) {
			key := ShapeKey(tableName, colName)
			shape, ok := shapes[key]
			if !ok || ShapeSkipReason(sc, tableName, colName) != "" {
				continue
			}
			parent, _ := splitFK(table.Columns[colName].FK)
			if !inRun[parent] {
				continue
			}
			parents, _ := planned(parent)
			avg := shape.Avg
			if avg <= 0 {
				avg = float64(max(shape.Min, 1))
			}
			n := float64(parents) * (1 - shape.ZeroShare) * avg
			if shape.NullShare > 0 && shape.NullShare < 1 {
				n /= 1 - shape.NullShare
			}
			count := int(math.Round(n))
			implied = append(implied, fmt.Sprintf("%s → %d", key, count))
			counts[count] = true
			if parents > bestParents {
				best, bestParents, bestKey = count, parents, key
			}
		}
		if bestKey == "" {
			continue
		}
		derived[tableName] = max(best, 0)
		if len(counts) > 1 {
			notes = append(notes, fmt.Sprintf("%s: shaped keys imply different row counts (%s); %s decides %d rows, the other keys are dealt over them", tableName, strings.Join(implied, ", "), bestKey, best))
		}
	}
	return derived, notes
}
