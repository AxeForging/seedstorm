package seeder

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/rules"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// shopSchema: users ← orders (required FK).
func shopSchema() *schema.Schema {
	return &schema.Schema{Tables: map[string]schema.Table{
		"users": {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}}},
		"orders": {Columns: map[string]schema.Column{
			"id":      {Type: "integer", PK: true},
			"user_id": {Type: "integer", FK: "users.id"},
		}},
	}}
}

func parsedSnapshot(t *testing.T, doc string) *compare.Snapshot {
	t.Helper()
	s, err := compare.ParseSnapshot([]byte(doc))
	if err != nil {
		t.Fatalf("ParseSnapshot: %v", err)
	}
	return &s
}

func TestPrepareMirror_SnapshotSourcePlansWithoutAConnection(t *testing.T) {
	// A hand-written MySQL-style snapshot (upper-case names) against a target
	// whose counts are also known, so no database is touched at all.
	source := Endpoint{Label: "prod.yaml", Snapshot: parsedSnapshot(t, "tables: {USERS: 100, ORDERS: 300, legacy: 7}")}
	target := Endpoint{
		DBType:   "pgx",
		Label:    "stage",
		Schema:   shopSchema(),
		Snapshot: &compare.Snapshot{Label: "stage", Tables: map[string]compare.TableStat{"users": {Rows: 40}, "orders": {Rows: 0}}},
	}
	job, err := PrepareMirror(context.Background(), source, target, MirrorConfig{Options: compare.MirrorOptions{Scale: 1}})
	if err != nil {
		t.Fatalf("PrepareMirror: %v", err)
	}
	if !job.SameDatabaseUnchecked {
		t.Error("SameDatabaseUnchecked = false for a snapshot source; the UI must warn the check did not run")
	}
	if job.Report.Source.Label != "prod.yaml" {
		t.Errorf("report source label = %q, want the endpoint label as fallback", job.Report.Source.Label)
	}
	if !reflect.DeepEqual(job.Plan.Order, []string{"users", "orders"}) {
		t.Fatalf("order = %v, want FK order", job.Plan.Order)
	}
	want := map[string]int{"users": 60, "orders": 300}
	if got := job.Plan.Counts(); !reflect.DeepEqual(got, want) {
		t.Errorf("counts = %v, want %v", got, want)
	}
	if job.Plan.TotalInsert != 360 {
		t.Errorf("total insert = %d", job.Plan.TotalInsert)
	}
	for _, e := range job.Plan.Entries {
		if e.SourceTable == "" {
			t.Errorf("%s: source table not recorded for a case-insensitive match: %+v", e.Table, e)
		}
	}
}

func TestPrepareMirror_SnapshotSourceWithUnknownRowsIsSkipped(t *testing.T) {
	source := Endpoint{Snapshot: parsedSnapshot(t, "tables: {users: -1, orders: 5}")}
	target := Endpoint{Schema: shopSchema(), Snapshot: &compare.Snapshot{Tables: map[string]compare.TableStat{"users": {Rows: 2}, "orders": {Rows: 0}}}}
	job, err := PrepareMirror(context.Background(), source, target, MirrorConfig{})
	if err != nil {
		t.Fatalf("PrepareMirror: %v", err)
	}
	if got := job.Plan.Counts(); got["users"] != 0 || got["orders"] != 5 {
		t.Errorf("counts = %v, want users untouched (unknown) and orders 5", got)
	}
}

func TestPrepareMirror_LiveEndpointsStillRunTheSameDatabaseCheck(t *testing.T) {
	// Without a snapshot the identity check is mandatory, so endpoints that
	// cannot be identified are refused instead of silently planned.
	_, err := PrepareMirror(context.Background(), Endpoint{DBType: "pgx"}, Endpoint{DBType: "pgx", Schema: shopSchema()}, MirrorConfig{})
	if err == nil || !strings.Contains(err.Error(), "cannot check that source and target differ") {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, ErrSameDatabase) {
		t.Fatal("missing connections misreported as the same database")
	}
}

func TestSnapshots_SnapshotSideIsNotReadAndLiveSideNeedsAConnection(t *testing.T) {
	source := Endpoint{Snapshot: parsedSnapshot(t, "tables: {users: 1}")}
	_, err := Snapshots(context.Background(), source, Endpoint{DBType: "pgx"}, compare.CountExact, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "target · count: no database connection or snapshot") {
		t.Fatalf("err = %v", err)
	}

	var sides []string
	report, err := Snapshots(context.Background(), source, Endpoint{Snapshot: &compare.Snapshot{Tables: map[string]compare.TableStat{"users": {Rows: 1}}}},
		compare.CountExact, func(side string, _, _ int, _ string) { sides = append(sides, side) })
	if err != nil {
		t.Fatal(err)
	}
	if len(sides) != 0 {
		t.Errorf("progress reported for snapshot sides: %v", sides)
	}
	if report.Totals.Same != 1 {
		t.Errorf("totals = %+v", report.Totals)
	}
	if source.Snapshot.Label != "" {
		t.Errorf("Snapshots mutated the caller's snapshot: %+v", source.Snapshot)
	}
}

func TestPrepareMirror_ProfileIgnoreListSkipsTables(t *testing.T) {
	source := Endpoint{Snapshot: parsedSnapshot(t, "tables: {users: 10, orders: 30}")}
	target := Endpoint{Schema: shopSchema(), Snapshot: &compare.Snapshot{Tables: map[string]compare.TableStat{"users": {Rows: 4}, "orders": {Rows: 0}}}}
	profile, err := rules.Parse([]byte("name: p\nignore: [USERS]\n"))
	if err != nil {
		t.Fatal(err)
	}
	job, err := PrepareMirror(context.Background(), source, target, MirrorConfig{Profile: profile})
	if err != nil {
		t.Fatalf("PrepareMirror: %v", err)
	}
	// users is ignored but populated: orders still fills and references it.
	if got := job.Plan.Counts(); !reflect.DeepEqual(got, map[string]int{"orders": 30}) {
		t.Fatalf("counts = %v, want only orders", got)
	}
	var reasons []string
	for _, sk := range job.Plan.Skipped {
		reasons = append(reasons, sk.Table+": "+sk.Reason)
	}
	if !reflect.DeepEqual(reasons, []string{"users: " + compare.ReasonIgnored}) {
		t.Fatalf("skipped = %v", reasons)
	}
}
