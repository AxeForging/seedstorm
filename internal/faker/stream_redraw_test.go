package faker

import (
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// parentRowsDriver serves `SELECT "id" FROM "parent"` as ids 1..parentRows and
// every other query as empty, counting how often the parent is read.
type parentRowsDriver struct{}

const parentRows = 1000

var (
	parentDriverOnce sync.Once
	parentScansMu    sync.Mutex
	parentScans      int
)

func (parentRowsDriver) Open(string) (driver.Conn, error) { return parentConn{}, nil }

type parentConn struct{}

func (parentConn) Prepare(query string) (driver.Stmt, error) { return parentStmt{query: query}, nil }
func (parentConn) Close() error                              { return nil }
func (parentConn) Begin() (driver.Tx, error)                 { return nil, io.EOF }

type parentStmt struct{ query string }

func (parentStmt) Close() error                               { return nil }
func (parentStmt) NumInput() int                              { return -1 }
func (parentStmt) Exec([]driver.Value) (driver.Result, error) { return driver.RowsAffected(0), nil }

func (s parentStmt) Query([]driver.Value) (driver.Rows, error) {
	if !strings.Contains(s.query, `FROM "parent"`) {
		return &idRows{}, nil
	}
	parentScansMu.Lock()
	parentScans++
	parentScansMu.Unlock()
	return &idRows{n: parentRows}, nil
}

type idRows struct{ i, n int }

func (r *idRows) Columns() []string { return []string{"id"} }
func (r *idRows) Close() error      { return nil }
func (r *idRows) Next(dest []driver.Value) error {
	if r.i >= r.n {
		return io.EOF
	}
	r.i++
	dest[0] = int64(r.i)
	return nil
}

func TestStream_ChildrenOfASampledParentSpreadOverTheWholeParent(t *testing.T) {
	defer func(old int) { poolLimit = old }(poolLimit)
	poolLimit = 100
	parentDriverOnce.Do(func() { sql.Register("faker_parent_rows", parentRowsDriver{}) })
	conn, err := sql.Open("faker_parent_rows", "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	parentScansMu.Lock()
	parentScans = 0
	parentScansMu.Unlock()

	s := &schema.Schema{Tables: map[string]schema.Table{
		"parent": {Columns: map[string]schema.Column{"id": {Type: "bigint", PK: true}}},
		"child":  {Columns: map[string]schema.Column{"id": {Type: "bigint", PK: true}, "parent_id": {Type: "bigint", FK: "parent.id"}}},
	}}
	g, err := NewStream(s, []string{"child", "parent"}, []string{"child"}, conn, "pgx", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(g.pks["parent"]); n != poolLimit {
		t.Fatalf("parent pool holds %d ids, want a %d-id sample", n, poolLimit)
	}
	referenced := map[interface{}]bool{}
	for chunk := 0; chunk < 10; chunk++ {
		data, err := g.Generate([]string{"child"}, 100, 0, nil, DefaultGenerateOptions())
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range data["child"] {
			referenced[row["parent_id"]] = true
		}
	}
	// One sample can only ever reach 100 parents; fresh samples reach far more.
	if len(referenced) <= 2*poolLimit {
		t.Fatalf("children reference %d distinct parents out of %d", len(referenced), parentRows)
	}
	parentScansMu.Lock()
	defer parentScansMu.Unlock()
	if parentScans > 10 {
		t.Fatalf("parent read %d times for 10 chunks: redraws must follow poolLimit rows, not every chunk", parentScans)
	}
}
