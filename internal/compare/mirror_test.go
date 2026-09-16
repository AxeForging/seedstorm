package compare

import (
	"reflect"
	"strings"
	"testing"
)

func skipFor(p MirrorPlan, table string) (PlanSkip, bool) {
	for _, s := range p.Skipped {
		if s.Table == table {
			return s, true
		}
	}
	return PlanSkip{}, false
}

func assertNeverWritten(t *testing.T, p MirrorPlan, tables ...string) {
	t.Helper()
	counts := p.Counts()
	for _, table := range tables {
		if contains(p.Order, table) || contains(p.Truncate, table) {
			t.Errorf("%s is written: order=%v truncate=%v", table, p.Order, p.Truncate)
		}
		if _, ok := counts[table]; ok {
			t.Errorf("%s has a count: %v", table, counts)
		}
	}
}

func TestPlanMirror_IgnoredTablesAreSkippedAndNeverPlanned(t *testing.T) {
	report := shopReport(
		map[string]int64{"users": 100, "orders": 300, "order_items": 900, "audit_logs": 50},
		map[string]int64{"users": 40, "orders": 300, "order_items": 0, "audit_logs": 0},
	)
	// Case differs from the target name: MySQL profiles often say AUDIT_LOGS.
	plan, err := PlanMirror(report, shopTarget(), MirrorOptions{Ignore: map[string]bool{"AUDIT_LOGS": true}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Order, []string{"users", "order_items"}) {
		t.Fatalf("order = %v", plan.Order)
	}
	assertNeverWritten(t, plan, "audit_logs")
	if s, ok := skipFor(plan, "audit_logs"); !ok || s.Reason != ReasonIgnored {
		t.Errorf("audit_logs skip = %+v, %v", s, ok)
	}
}

func TestPlanMirror_IgnoredFlagFalseIsNotIgnored(t *testing.T) {
	report := shopReport(map[string]int64{"users": 5}, map[string]int64{"users": 0})
	plan, err := PlanMirror(report, shopTarget(), MirrorOptions{Ignore: map[string]bool{"users": false}})
	if err != nil {
		t.Fatal(err)
	}
	if e := entry(t, plan, "users"); e.Insert != 5 {
		t.Fatalf("users = %+v", e)
	}
}

func TestPlanMirror_IgnoredParent(t *testing.T) {
	cases := []struct {
		name      string
		src, tgt  map[string]int64
		opts      MirrorOptions
		wantOrder []string
		wantSkip  map[string]string // table → reason
		detail    map[string]string // table → detail substring
	}{
		{
			name:      "populated ignored parent is referenced, not topped up",
			src:       map[string]int64{"users": 100, "orders": 10},
			tgt:       map[string]int64{"users": 5, "orders": 0},
			opts:      MirrorOptions{Ignore: map[string]bool{"users": true}},
			wantOrder: []string{"orders"},
			wantSkip:  map[string]string{"users": ReasonIgnored},
		},
		{
			name:      "empty ignored parent skips the child instead of failing the run",
			src:       map[string]int64{"users": 100, "orders": 10, "audit_logs": 4},
			tgt:       map[string]int64{"users": 0, "orders": 0, "audit_logs": 0},
			opts:      MirrorOptions{Ignore: map[string]bool{"users": true}},
			wantOrder: []string{"audit_logs"}, // nullable FK: seeded with NULL
			wantSkip:  map[string]string{"users": ReasonIgnored, "orders": ReasonIgnoredParent},
			detail:    map[string]string{"orders": "ignored table users"},
		},
		{
			name:      "chained: grandchild whose auto-parent needs an ignored empty table is skipped",
			src:       map[string]int64{"order_items": 20},
			tgt:       map[string]int64{"users": 0, "orders": 0, "order_items": 0},
			opts:      MirrorOptions{Tables: []string{"order_items"}, Ignore: map[string]bool{"users": true}},
			wantOrder: nil,
			wantSkip:  map[string]string{"order_items": ReasonIgnoredParent},
			detail:    map[string]string{"order_items": "ignored table users"},
		},
		{
			name:      "chained: skipped middle table strands its children too",
			src:       map[string]int64{"orders": 10, "order_items": 20},
			tgt:       map[string]int64{"users": 0, "orders": 0, "order_items": 0},
			opts:      MirrorOptions{Tables: []string{"orders", "order_items"}, Ignore: map[string]bool{"users": true}},
			wantOrder: nil,
			wantSkip:  map[string]string{"orders": ReasonIgnoredParent, "order_items": ReasonIgnoredParent},
		},
		{
			name:      "chained: populated middle table shields the child",
			src:       map[string]int64{"order_items": 20},
			tgt:       map[string]int64{"users": 0, "orders": 7, "order_items": 0},
			opts:      MirrorOptions{Tables: []string{"order_items"}, Ignore: map[string]bool{"users": true}},
			wantOrder: []string{"order_items"},
		},
		{
			name:      "everything ignored plans nothing",
			src:       map[string]int64{"users": 1, "orders": 1, "order_items": 1, "audit_logs": 1},
			tgt:       map[string]int64{"users": 0, "orders": 0, "order_items": 0, "audit_logs": 0},
			opts:      MirrorOptions{Ignore: map[string]bool{"users": true, "orders": true, "order_items": true, "audit_logs": true}},
			wantOrder: nil,
			wantSkip:  map[string]string{"users": ReasonIgnored, "orders": ReasonIgnored, "order_items": ReasonIgnored, "audit_logs": ReasonIgnored},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PlanMirror(shopReport(tc.src, tc.tgt), shopTarget(), tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(plan.Order, tc.wantOrder) {
				t.Fatalf("order = %v, want %v (entries %+v)", plan.Order, tc.wantOrder, plan.Entries)
			}
			for table := range tc.opts.Ignore {
				assertNeverWritten(t, plan, table)
			}
			for table, reason := range tc.wantSkip {
				s, ok := skipFor(plan, table)
				if !ok || s.Reason != reason {
					t.Errorf("%s skip = %+v (found %v), want %q", table, s, ok, reason)
				}
				if want := tc.detail[table]; want != "" && !strings.Contains(s.Detail, want) {
					t.Errorf("%s detail = %q, want it to mention %q", table, s.Detail, want)
				}
			}
		})
	}
}

func TestPlanMirror_ResetNeverTruncatesIgnoredDescendants(t *testing.T) {
	report := shopReport(
		map[string]int64{"users": 10, "orders": 20, "order_items": 30, "audit_logs": 5},
		map[string]int64{"users": 99, "orders": 99, "order_items": 99, "audit_logs": 99},
	)
	plan, err := PlanMirror(report, shopTarget(), MirrorOptions{
		Mode:   ModeReset,
		Tables: []string{"users", "orders"},
		Ignore: map[string]bool{"audit_logs": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertNeverWritten(t, plan, "audit_logs", "users")
	s, ok := skipFor(plan, "users")
	if !ok || s.Reason != ReasonTruncatesIgnored || !strings.Contains(s.Detail, "audit_logs") {
		t.Fatalf("users skip = %+v (found %v)", s, ok)
	}
	// orders has no ignored descendants, so it still resets with its child.
	if !reflect.DeepEqual(plan.Truncate, []string{"orders", "order_items"}) {
		t.Errorf("truncate = %v", plan.Truncate)
	}
	if e := entry(t, plan, "orders"); e.Insert != 20 {
		t.Errorf("orders = %+v", e)
	}
	if e := entry(t, plan, "order_items"); e.Insert != 30 || e.Reason != ReasonDependent {
		t.Errorf("order_items = %+v", e)
	}
}

func TestPlanMirror_ResetRootNeedingIgnoredEmptyParentIsNotTruncated(t *testing.T) {
	report := shopReport(
		map[string]int64{"users": 10, "orders": 20, "order_items": 30},
		map[string]int64{"users": 0, "orders": 99, "order_items": 99},
	)
	plan, err := PlanMirror(report, shopTarget(), MirrorOptions{
		Mode:   ModeReset,
		Tables: []string{"orders"},
		Ignore: map[string]bool{"users": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Truncate) != 0 || len(plan.Order) != 0 {
		t.Fatalf("a reset that cannot refill orders must not empty it: truncate=%v order=%v", plan.Truncate, plan.Order)
	}
	if s, ok := skipFor(plan, "orders"); !ok || s.Reason != ReasonIgnoredParent {
		t.Fatalf("orders skip = %+v (found %v)", s, ok)
	}
}
