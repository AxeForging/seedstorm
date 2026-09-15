package compare

import (
	"reflect"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/schema"
)

func snap(label string, tables map[string]TableStat) Snapshot {
	return Snapshot{Label: label, DBType: "pgx", CountMode: CountExact, Tables: tables}
}

func stat(rows int64, cols ...string) TableStat {
	return TableStat{Rows: rows, Bytes: rows * 100, Columns: cols}
}

func rowFor(t *testing.T, r Report, table string) Row {
	t.Helper()
	for _, row := range r.Rows {
		if row.Table == table {
			return row
		}
	}
	t.Fatalf("no row for %s in %+v", table, r.Rows)
	return Row{}
}

func TestDiff_ClassifiesEveryTable(t *testing.T) {
	src := snap("prod", map[string]TableStat{
		"users":   stat(100, "id", "email"),
		"orders":  stat(40, "id", "user_id", "total"),
		"legacy":  stat(7, "id"),
		"invites": stat(0, "id"),
	})
	tgt := snap("stage", map[string]TableStat{
		"users":   stat(100, "id", "email"),
		"orders":  stat(10, "id", "user_id", "note"),
		"invites": stat(0, "id"),
		"metrics": stat(3, "id"),
	})
	r := Diff(src, tgt)

	if got := rowFor(t, r, "users"); got.Status != StatusSame || got.Delta != 0 {
		t.Errorf("users = %+v", got)
	}
	orders := rowFor(t, r, "orders")
	if orders.Status != StatusDiffers || orders.Delta != -30 {
		t.Errorf("orders = %+v", orders)
	}
	if !reflect.DeepEqual(orders.MissingColumns, []string{"total"}) || !reflect.DeepEqual(orders.ExtraColumns, []string{"note"}) {
		t.Errorf("orders columns missing=%v extra=%v", orders.MissingColumns, orders.ExtraColumns)
	}
	if got := rowFor(t, r, "legacy"); got.Status != StatusSourceOnly || got.Target != nil || got.Delta != -7 {
		t.Errorf("legacy = %+v", got)
	}
	if got := rowFor(t, r, "metrics"); got.Status != StatusTargetOnly || got.Source != nil {
		t.Errorf("metrics = %+v", got)
	}
	want := Totals{SourceRows: 147, TargetRows: 113, SourceBytes: 14700, TargetBytes: 11300, Same: 2, Differs: 1, SourceOnly: 1, TargetOnly: 1, ColumnDrift: 1}
	if r.Totals != want {
		t.Errorf("totals = %+v, want %+v", r.Totals, want)
	}
	names := make([]string, len(r.Rows))
	for i, row := range r.Rows {
		names[i] = row.Table
	}
	if !reflect.DeepEqual(names, []string{"invites", "legacy", "metrics", "orders", "users"}) {
		t.Errorf("rows not sorted: %v", names)
	}
}

func TestDiff_MatchesTablesAcrossIdentifierCase(t *testing.T) {
	// MySQL keeps upper-case names, Postgres folds them: USER_ENTITY vs user_entity.
	src := snap("mysql", map[string]TableStat{"USER_ENTITY": stat(5, "ID", "EMAIL")})
	tgt := snap("pg", map[string]TableStat{"user_entity": stat(2, "id", "email")})
	r := Diff(src, tgt)
	if len(r.Rows) != 1 {
		t.Fatalf("rows = %+v", r.Rows)
	}
	row := r.Rows[0]
	if row.Status != StatusDiffers || row.TargetTable != "user_entity" || len(row.MissingColumns) != 0 {
		t.Fatalf("row = %+v", row)
	}
}

func TestDiff_UnknownCountsDoNotPoisonTotals(t *testing.T) {
	src := snap("a", map[string]TableStat{"t": {Rows: db.UnknownCount, Bytes: db.UnknownCount}})
	tgt := snap("b", map[string]TableStat{"t": {Rows: 4, Bytes: db.UnknownCount}})
	r := Diff(src, tgt)
	if r.Totals.SourceRows != 0 || r.Totals.TargetRows != 4 || r.Totals.SourceBytes != 0 {
		t.Fatalf("totals = %+v", r.Totals)
	}
}

func TestParseModes(t *testing.T) {
	if m, err := ParseCountMode(""); err != nil || m != CountExact {
		t.Fatalf("default count mode = %v %v", m, err)
	}
	if _, err := ParseCountMode("fast"); err == nil {
		t.Fatal("expected error for unknown count mode")
	}
	if m, err := ParseMirrorMode("top-up"); err != nil || m != ModeTopUp {
		t.Fatalf("top-up = %v %v", m, err)
	}
	if _, err := ParseMirrorMode("merge"); err == nil {
		t.Fatal("expected error for unknown mirror mode")
	}
}

// shopTarget: users ← orders ← order_items; audit_logs → users via a nullable FK.
func shopTarget() *schema.Schema {
	return &schema.Schema{Tables: map[string]schema.Table{
		"users": {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}}},
		"orders": {Columns: map[string]schema.Column{
			"id":      {Type: "integer", PK: true},
			"user_id": {Type: "integer", FK: "users.id"},
		}},
		"order_items": {Columns: map[string]schema.Column{
			"id":       {Type: "integer", PK: true},
			"order_id": {Type: "integer", FK: "orders.id"},
		}},
		"audit_logs": {Columns: map[string]schema.Column{
			"id":      {Type: "integer", PK: true},
			"user_id": {Type: "integer", FK: "users.id", Nullable: true},
		}},
	}}
}

func shopReport(src, tgt map[string]int64) Report {
	s := map[string]TableStat{}
	for k, v := range src {
		s[k] = stat(v)
	}
	g := map[string]TableStat{}
	for k, v := range tgt {
		g[k] = stat(v)
	}
	return Diff(snap("src", s), snap("tgt", g))
}

func entry(t *testing.T, p MirrorPlan, table string) PlanEntry {
	t.Helper()
	for _, e := range p.Entries {
		if e.Table == table {
			return e
		}
	}
	t.Fatalf("no plan entry for %s: %+v", table, p.Entries)
	return PlanEntry{}
}

func TestPlanMirror_TopUpInsertsShortfallInFKOrder(t *testing.T) {
	report := shopReport(
		map[string]int64{"users": 100, "orders": 300, "order_items": 900, "audit_logs": 50},
		map[string]int64{"users": 40, "orders": 300, "order_items": 0, "audit_logs": 80},
	)
	plan, err := PlanMirror(report, shopTarget(), MirrorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Order, []string{"users", "order_items"}) {
		t.Fatalf("order = %v", plan.Order)
	}
	if e := entry(t, plan, "users"); e.Insert != 60 || e.Want != 100 || e.Reason != ReasonMatch {
		t.Errorf("users = %+v", e)
	}
	if e := entry(t, plan, "order_items"); e.Insert != 900 {
		t.Errorf("order_items = %+v", e)
	}
	if plan.TotalInsert != 960 || len(plan.Truncate) != 0 {
		t.Errorf("total=%d truncate=%v", plan.TotalInsert, plan.Truncate)
	}
	skipped := map[string]string{}
	for _, s := range plan.Skipped {
		skipped[s.Table] = s.Reason
	}
	if skipped["orders"] != ReasonSatisfied || skipped["audit_logs"] != ReasonSatisfied {
		t.Errorf("skipped = %+v", plan.Skipped)
	}
	if got := plan.Counts(); got["users"] != 60 || got["order_items"] != 900 || len(got) != 2 {
		t.Errorf("counts = %v", got)
	}
}

func TestPlanMirror_ScaleRoundsUpAndMaxRowsCaps(t *testing.T) {
	report := shopReport(map[string]int64{"users": 3, "orders": 1000}, map[string]int64{"users": 0, "orders": 0})
	plan, err := PlanMirror(report, shopTarget(), MirrorOptions{Scale: 0.5, MaxRows: 200})
	if err != nil {
		t.Fatal(err)
	}
	if e := entry(t, plan, "users"); e.Want != 2 || e.Insert != 2 {
		t.Errorf("users = %+v (ceil(3*0.5)=2)", e)
	}
	if e := entry(t, plan, "orders"); e.Want != 200 || e.Reason != ReasonCapped {
		t.Errorf("orders = %+v", e)
	}
}

func TestPlanMirror_EmptyRequiredParentGetsParentRows(t *testing.T) {
	// Only order_items is selected; its hard parents are empty on the target.
	report := shopReport(
		map[string]int64{"users": 0, "orders": 0, "order_items": 20},
		map[string]int64{"users": 0, "orders": 0, "order_items": 0},
	)
	plan, err := PlanMirror(report, shopTarget(), MirrorOptions{Tables: []string{"order_items"}, ParentRows: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Order, []string{"users", "orders", "order_items"}) {
		t.Fatalf("order = %v", plan.Order)
	}
	for _, parent := range []string{"users", "orders"} {
		if e := entry(t, plan, parent); e.Insert != 3 || e.Reason != ReasonParent {
			t.Errorf("%s = %+v", parent, e)
		}
	}
}

func TestPlanMirror_PopulatedParentIsLeftAlone(t *testing.T) {
	report := shopReport(
		map[string]int64{"users": 0, "orders": 10},
		map[string]int64{"users": 5, "orders": 0},
	)
	plan, err := PlanMirror(report, shopTarget(), MirrorOptions{Tables: []string{"orders"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Order, []string{"orders"}) {
		t.Fatalf("order = %v, users already has rows", plan.Order)
	}
}

func TestPlanMirror_ResetTruncatesDescendantsAndRefillsThem(t *testing.T) {
	report := shopReport(
		map[string]int64{"users": 10, "orders": 20, "order_items": 30, "audit_logs": 5},
		map[string]int64{"users": 99, "orders": 99, "order_items": 99, "audit_logs": 99},
	)
	plan, err := PlanMirror(report, shopTarget(), MirrorOptions{Mode: ModeReset, Tables: []string{"users"}})
	if err != nil {
		t.Fatal(err)
	}
	truncate := append([]string(nil), plan.Truncate...)
	for _, want := range []string{"users", "orders", "order_items", "audit_logs"} {
		if !contains(truncate, want) {
			t.Errorf("truncate %v missing %s (nullable FKs count too)", truncate, want)
		}
	}
	if e := entry(t, plan, "users"); e.Insert != 10 || e.Reason != ReasonMatch {
		t.Errorf("users = %+v", e)
	}
	if e := entry(t, plan, "order_items"); e.Insert != 30 || e.Reason != ReasonDependent {
		t.Errorf("order_items = %+v", e)
	}
	// Independent tables may come in any order; parents must precede children.
	pos := map[string]int{}
	for i, name := range plan.Order {
		pos[name] = i
	}
	if pos["users"] > pos["orders"] || pos["orders"] > pos["order_items"] {
		t.Errorf("order = %v, parents must come before children", plan.Order)
	}
}

func TestPlanMirror_SkipsTablesItCannotMirror(t *testing.T) {
	src := snap("src", map[string]TableStat{
		"users":  stat(5),
		"ghosts": stat(9),
		"orders": {Rows: db.UnknownCount},
	})
	tgt := snap("tgt", map[string]TableStat{"users": stat(0), "orders": stat(0), "order_items": stat(0), "audit_logs": stat(0)})
	plan, err := PlanMirror(Diff(src, tgt), shopTarget(), MirrorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, s := range plan.Skipped {
		reasons[s.Table] = s.Reason
	}
	if reasons["ghosts"] != ReasonNotTarget || reasons["orders"] != ReasonUnknown {
		t.Fatalf("skipped = %+v", plan.Skipped)
	}
}

func TestPlanMirror_RejectsBadOptionsAndHardCycles(t *testing.T) {
	report := shopReport(map[string]int64{"users": 1}, map[string]int64{"users": 0})
	if _, err := PlanMirror(report, shopTarget(), MirrorOptions{Scale: -1}); err == nil {
		t.Error("negative scale accepted")
	}
	if _, err := PlanMirror(report, shopTarget(), MirrorOptions{MaxRows: -5}); err == nil {
		t.Error("negative max rows accepted")
	}
	cyclic := &schema.Schema{Tables: map[string]schema.Table{
		"a": {Columns: map[string]schema.Column{"b_id": {Type: "integer", FK: "b.id"}}},
		"b": {Columns: map[string]schema.Column{"a_id": {Type: "integer", FK: "a.id"}}},
	}}
	_, err := PlanMirror(shopReport(map[string]int64{"a": 1, "b": 1}, map[string]int64{"a": 0, "b": 0}), cyclic, MirrorOptions{})
	if err == nil || !strings.Contains(err.Error(), "target schema") {
		t.Errorf("cycle err = %v", err)
	}
}

func TestPlanMirror_CaseFoldedTargetNamesAreUsedForInsert(t *testing.T) {
	src := snap("mysql", map[string]TableStat{"USERS": stat(4)})
	tgt := snap("pg", map[string]TableStat{"users": stat(1), "orders": stat(0), "order_items": stat(0), "audit_logs": stat(0)})
	plan, err := PlanMirror(Diff(src, tgt), shopTarget(), MirrorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	e := entry(t, plan, "users")
	if e.Insert != 3 || e.SourceTable != "USERS" {
		t.Fatalf("users = %+v", e)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestFormatBytes(t *testing.T) {
	cases := map[int64]string{-1: "?", 0: "0 B", 1023: "1023 B", 1024: "1.0 KB", 1536: "1.5 KB", 5 << 30: "5.0 GB"}
	for in, want := range cases {
		if got := FormatBytes(in); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderPlan_ShowsTruncateWarningAndSkips(t *testing.T) {
	report := shopReport(
		map[string]int64{"users": 10, "orders": 20, "order_items": 30, "audit_logs": 5, "ghosts": 1},
		map[string]int64{"users": 1, "orders": 1, "order_items": 1, "audit_logs": 1},
	)
	plan, err := PlanMirror(report, shopTarget(), MirrorOptions{Mode: ModeReset})
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	RenderPlan(&sb, plan)
	out := sb.String()
	for _, want := range []string{"TRUNCATE on target (4 tables)", "order_items", "ghosts — not in target", "65 rows into 4 tables"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderReport_MarksEstimatedCounts(t *testing.T) {
	src := snap("prod", map[string]TableStat{"users": {Rows: 1200, Estimated: true, Bytes: 10}, "tags": {Rows: 3, Bytes: 10}})
	tgt := snap("stage", map[string]TableStat{"users": {Rows: 5, Bytes: 10}, "tags": {Rows: 3, Bytes: 10}})
	var sb strings.Builder
	RenderReport(&sb, Diff(src, tgt), false)
	out := sb.String()
	if !strings.Contains(out, "~1200") || strings.Contains(out, "~3") || strings.Contains(out, "~5") {
		t.Fatalf("estimated counts must carry ~ and exact ones must not:\n%s", out)
	}
	if !strings.Contains(out, "~ = estimated") {
		t.Fatalf("legend missing:\n%s", out)
	}
}
