package compare

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSnapshotFromReport_RebuildsEachSideAndRoundTrips(t *testing.T) {
	taken := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	src := Snapshot{Label: "prod", DBType: "pgx", CountMode: CountEstimate, TakenAt: taken, Tables: map[string]TableStat{
		"users":  {Rows: 1200, Estimated: true, Bytes: 4096, Columns: []string{"id", "email"}},
		"orders": {Rows: 5000, Bytes: -1},
		"legacy": {Rows: 3},
	}}
	tgt := Snapshot{Label: "stage", DBType: "mysql", CountMode: CountExact, TakenAt: taken, Tables: map[string]TableStat{
		"USERS":  {Rows: 10, Columns: []string{"ID", "EMAIL"}},
		"orders": {Rows: 0},
		"extra":  {Rows: 9},
	}}
	report := Diff(src, tgt)

	gotSrc, err := SnapshotFromReport(report, SideSource)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotSrc.Tables, src.Tables) || gotSrc.Label != "prod" || gotSrc.CountMode != CountEstimate || !gotSrc.TakenAt.Equal(taken) {
		t.Fatalf("source = %+v, want %+v", gotSrc, src)
	}
	gotTgt, err := SnapshotFromReport(report, SideTarget)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotTgt.Tables, tgt.Tables) {
		t.Fatalf("target tables = %+v, want target names kept (USERS)", gotTgt.Tables)
	}

	// An exported side parses back and diffs to the same report.
	data, err := EncodeSnapshot(gotSrc, FormatYAML)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseSnapshot(data)
	if err != nil {
		t.Fatalf("parse exported snapshot: %v\n%s", err, data)
	}
	if again := Diff(back, tgt); !reflect.DeepEqual(again.Totals, report.Totals) {
		t.Fatalf("totals after round trip = %+v, want %+v", again.Totals, report.Totals)
	}
}

func TestSnapshotFromReport_Rejects(t *testing.T) {
	if _, err := SnapshotFromReport(Report{}, "left"); err == nil || !strings.Contains(err.Error(), "unknown side") {
		t.Fatalf("bad side err = %v", err)
	}
	onlySource := Diff(Snapshot{Tables: map[string]TableStat{"a": {Rows: 1}}}, Snapshot{Tables: map[string]TableStat{}})
	if _, err := SnapshotFromReport(onlySource, SideTarget); err == nil || !strings.Contains(err.Error(), "no tables") {
		t.Fatalf("empty side err = %v", err)
	}
}
