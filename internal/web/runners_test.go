package web

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

type testJobControl struct{}

func (testJobControl) Write(p []byte) (int, error) { return len(p), nil }
func (testJobControl) Phase(string)                {}
func (testJobControl) Progress(int, int, string)   {}

func TestTableRowCounts(t *testing.T) {
	data := map[string][]map[string]any{
		"users": {
			{"id": 1},
			{"id": 2},
		},
		"orders": {
			{"id": 10},
		},
	}

	counts, total := tableRowCounts(data, []string{"users", "orders", "missing"})

	want := map[string]int{"users": 2, "orders": 1, "missing": 0}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("counts = %+v, want %+v", counts, want)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
}

func TestCleanTableRowsKeepsPositiveOverrides(t *testing.T) {
	got := cleanTableRows(map[string]int{
		"users":  4,
		"orders": 0,
		"":       9,
		"items":  -1,
	})

	want := map[string]int{"users": 4}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanTableRows = %+v, want %+v", got, want)
	}
}

func TestCleanTableRowsReturnsNilForEmptyInput(t *testing.T) {
	if got := cleanTableRows(nil); got != nil {
		t.Fatalf("cleanTableRows(nil) = %+v, want nil", got)
	}
	if got := cleanTableRows(map[string]int{"users": 0}); got != nil {
		t.Fatalf("cleanTableRows(non-positive only) = %+v, want nil", got)
	}
}

func TestRunSeedDryRunAppliesTableRowOverrides(t *testing.T) {
	srv, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := &Session{
		DBType: "pgx",
		schema: runnerRowCountSchema(),
	}

	result, err := srv.runSeed(context.Background(), sess, SeedRequest{
		Rows:      2,
		BatchSize: 100,
		DryRun:    true,
		TableRows: map[string]int{"orders": 4},
	}, testJobControl{})
	if err != nil {
		t.Fatalf("runSeed: %v", err)
	}
	if got := result["totalRows"]; got != 6 {
		t.Fatalf("totalRows = %v, want 6", got)
	}
	counts, ok := result["tableCounts"].(map[string]int)
	if !ok {
		t.Fatalf("tableCounts type = %T, want map[string]int", result["tableCounts"])
	}
	if counts["users"] != 2 || counts["orders"] != 4 {
		t.Fatalf("tableCounts = %+v, want users=2 orders=4", counts)
	}
	if result["output"] == "" {
		t.Fatalf("dry-run output should include generated SQL")
	}
}

func TestRunGenerateAppliesTableRowOverridesWithoutBreakingDefaults(t *testing.T) {
	srv, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := &Session{
		DBType: "pgx",
		schema: runnerRowCountSchema(),
	}

	result, err := srv.runGenerate(context.Background(), sess, GenerateRequest{
		Rows:      3,
		Format:    "yaml",
		TableRows: map[string]int{"orders": 1},
	}, testJobControl{})
	if err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	if got := result["totalRows"]; got != 4 {
		t.Fatalf("totalRows = %v, want 4", got)
	}
	counts, ok := result["tableCounts"].(map[string]int)
	if !ok {
		t.Fatalf("tableCounts type = %T, want map[string]int", result["tableCounts"])
	}
	if counts["users"] != 3 || counts["orders"] != 1 {
		t.Fatalf("tableCounts = %+v, want users=3 orders=1", counts)
	}
}

func TestRunSeedDryRunHandlesHardSelfReference(t *testing.T) {
	srv, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := &Session{
		DBType: "pgx",
		schema: hardSelfReferenceSchema(),
	}

	depth := 2
	result, err := srv.runSeed(context.Background(), sess, SeedRequest{
		Rows:         3,
		BatchSize:    100,
		SelfRefDepth: &depth,
		DryRun:       true,
	}, testJobControl{})
	if err != nil {
		t.Fatalf("runSeed: %v", err)
	}
	if got := result["totalRows"]; got != 3 {
		t.Fatalf("totalRows = %v, want 3", got)
	}
	output, _ := result["output"].(string)
	if output == "" || !containsAll(output, "employees", "manager_id") {
		t.Fatalf("dry-run output should include self-referential insert SQL, got %q", output)
	}
}

func TestRunSeedUsesFreshConnectionForMutatingServeJob(t *testing.T) {
	registerServeRunnerTestDriver()
	staleConn, err := sql.Open(serveRunnerTestDriverName, "stale")
	if err != nil {
		t.Fatalf("open stale conn: %v", err)
	}
	defer staleConn.Close()

	oldOpen := sqlOpen
	sqlOpen = func(driverName, dataSourceName string) (*sql.DB, error) {
		if driverName != "pgx" || dataSourceName != "fresh" {
			t.Fatalf("sqlOpen called with %s %s, want pgx fresh", driverName, dataSourceName)
		}
		return sql.Open(serveRunnerTestDriverName, "fresh")
	}
	defer func() { sqlOpen = oldOpen }()

	srv, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := &Session{
		DBType: "pgx",
		DSN:    "fresh",
		conn:   staleConn,
		schema: enumInsertSchema(),
	}

	result, err := srv.runSeed(context.Background(), sess, SeedRequest{
		Rows:      1,
		BatchSize: 100,
	}, testJobControl{})
	if err != nil {
		t.Fatalf("runSeed should use fresh run connection instead of stale session connection: %v", err)
	}
	if got := result["totalRows"]; got != 2 {
		t.Fatalf("totalRows = %v, want 2", got)
	}
}

func TestRunSeedTruncateWithZeroRowsDoesNotReSeed(t *testing.T) {
	registerServeRunnerTestDriver()
	staleConn, err := sql.Open(serveRunnerTestDriverName, "stale")
	if err != nil {
		t.Fatalf("open stale conn: %v", err)
	}
	defer staleConn.Close()

	oldOpen := sqlOpen
	sqlOpen = func(driverName, dataSourceName string) (*sql.DB, error) {
		if driverName != "pgx" || dataSourceName != "truncate-only" {
			t.Fatalf("sqlOpen called with %s %s, want pgx truncate-only", driverName, dataSourceName)
		}
		return sql.Open(serveRunnerTestDriverName, "truncate-only")
	}
	defer func() { sqlOpen = oldOpen }()

	srv, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := &Session{
		DBType: "pgx",
		DSN:    "truncate-only",
		conn:   staleConn,
		schema: runnerRowCountSchema(),
	}

	result, err := srv.runSeed(context.Background(), sess, SeedRequest{
		Rows:      0,
		BatchSize: 0,
		Truncate:  true,
		Tables:    []string{"orders"},
	}, testJobControl{})
	if err != nil {
		t.Fatalf("runSeed truncate-only should not generate inserts: %v", err)
	}
	if got := result["totalRows"]; got != 0 {
		t.Fatalf("totalRows = %v, want 0", got)
	}
	if got := result["truncated"]; got != true {
		t.Fatalf("truncated = %v, want true", got)
	}
	counts, ok := result["tableCounts"].(map[string]int)
	if !ok {
		t.Fatalf("tableCounts type = %T, want map[string]int", result["tableCounts"])
	}
	if counts["users"] != 0 || counts["orders"] != 0 {
		t.Fatalf("tableCounts = %+v, want users=0 orders=0", counts)
	}
	auto, ok := result["auto"].([]string)
	if !ok || len(auto) != 1 || auto[0] != "users" {
		t.Fatalf("auto = %#v, want users auto-selected for orders", result["auto"])
	}
}

func TestRunGenerateHandlesHardSelfReference(t *testing.T) {
	srv, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := &Session{
		DBType: "pgx",
		schema: hardSelfReferenceSchema(),
	}

	depth := 2
	result, err := srv.runGenerate(context.Background(), sess, GenerateRequest{
		Rows:         3,
		SelfRefDepth: &depth,
		Format:       "yaml",
	}, testJobControl{})
	if err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	if got := result["totalRows"]; got != 3 {
		t.Fatalf("totalRows = %v, want 3", got)
	}
	output, _ := result["output"].(string)
	if output == "" || !containsAll(output, "employees", "manager_id") {
		t.Fatalf("generated output should include self-referential values, got %q", output)
	}
}

func TestRunGenerateReturnsCapacityWarnings(t *testing.T) {
	srv, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := &Session{
		DBType: "pgx",
		schema: manyToManyCapacitySchema(),
	}

	result, err := srv.runGenerate(context.Background(), sess, GenerateRequest{
		Rows:   2,
		Format: "yaml",
		TableRows: map[string]int{
			"entity_links": 10,
		},
	}, testJobControl{})
	if err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	warnings, ok := result["warnings"].([]map[string]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want one warning", result["warnings"])
	}
	if warnings[0]["table"] != "entity_links" || warnings[0]["requested"] != 10 || warnings[0]["generated"] != 4 {
		t.Fatalf("warning = %#v, want entity_links requested=10 generated=4", warnings[0])
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}

const serveRunnerTestDriverName = "seedstorm_web_runner_test"

var registerServeRunnerDriverOnce sync.Once

func registerServeRunnerTestDriver() {
	registerServeRunnerDriverOnce.Do(func() {
		sql.Register(serveRunnerTestDriverName, serveRunnerTestDriver{})
	})
}

type serveRunnerTestDriver struct{}

func (serveRunnerTestDriver) Open(name string) (driver.Conn, error) {
	return &serveRunnerTestConn{name: name}, nil
}

type serveRunnerTestConn struct {
	name string
}

func (c *serveRunnerTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not implemented")
}

func (c *serveRunnerTestConn) Close() error { return nil }

func (c *serveRunnerTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions not implemented")
}

func (c *serveRunnerTestConn) Ping(context.Context) error { return nil }

func (c *serveRunnerTestConn) Query(query string, args []driver.Value) (driver.Rows, error) {
	return &serveRunnerRows{columns: []string{"id"}}, nil
}

func (c *serveRunnerTestConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &serveRunnerRows{columns: []string{"id"}}, nil
}

func (c *serveRunnerTestConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if c.name == "stale" && strings.Contains(query, "INSERT") {
		return nil, errors.New("cache lookup failed for type 34868 (SQLSTATE XX000)")
	}
	if c.name == "truncate-only" && strings.Contains(query, "INSERT") {
		return nil, errors.New("truncate-only run should not insert rows")
	}
	return driver.RowsAffected(1), nil
}

type serveRunnerRows struct {
	columns []string
}

func (r *serveRunnerRows) Columns() []string { return r.columns }
func (r *serveRunnerRows) Close() error      { return nil }
func (r *serveRunnerRows) Next([]driver.Value) error {
	return io.EOF
}

func runnerRowCountSchema() *schema.Schema {
	return &schema.Schema{
		Tables: map[string]schema.Table{
			"users": {
				Columns: map[string]schema.Column{
					"id":   {Type: "integer", PK: true},
					"name": {Type: "varchar", Faker: "name"},
				},
			},
			"orders": {
				Columns: map[string]schema.Column{
					"id":      {Type: "integer", PK: true},
					"user_id": {Type: "integer", FK: "users.id"},
				},
			},
		},
	}
}

func enumInsertSchema() *schema.Schema {
	return &schema.Schema{
		Tables: map[string]schema.Table{
			"purchase_orders": {
				Columns: map[string]schema.Column{
					"id":     {Type: "integer", PK: true},
					"status": {Type: "po_status", Faker: "randomstring(draft,submitted)"},
				},
			},
		},
	}
}

func hardSelfReferenceSchema() *schema.Schema {
	return &schema.Schema{
		Tables: map[string]schema.Table{
			"employees": {
				Columns: map[string]schema.Column{
					"id":         {Type: "integer", PK: true},
					"manager_id": {Type: "integer", FK: "employees.id"},
				},
			},
		},
	}
}

func manyToManyCapacitySchema() *schema.Schema {
	return &schema.Schema{
		Tables: map[string]schema.Table{
			"left_entities": {
				Columns: map[string]schema.Column{
					"id": {Type: "integer", PK: true},
				},
			},
			"right_entities": {
				Columns: map[string]schema.Column{
					"id": {Type: "integer", PK: true},
				},
			},
			"entity_links": {
				Columns: map[string]schema.Column{
					"left_id":  {Type: "integer", PK: true, FK: "left_entities.id"},
					"right_id": {Type: "integer", PK: true, FK: "right_entities.id"},
				},
			},
		},
	}
}
