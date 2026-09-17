package seeder

import (
	"reflect"
	"testing"
)

// Gaps are tables known to be empty. A table whose count failed is unknown and
// must never be treated as empty: seeding it could double a populated table.
func TestGapTables_OnlyTablesKnownToBeEmpty(t *testing.T) {
	order := []string{"users", "orders", "audit", "tags"}
	counts := map[string]int64{"users": 0, "orders": 12, "tags": 0} // audit's count failed

	if got := GapTables(order, counts, nil); !reflect.DeepEqual(got, []string{"users", "tags"}) {
		t.Fatalf("GapTables = %v", got)
	}
	// Scoped to a selection: only selected tables that are known to be empty.
	if got := GapTables(order, counts, []string{"audit", "tags", "orders"}); !reflect.DeepEqual(got, []string{"tags"}) {
		t.Fatalf("scoped GapTables = %v", got)
	}
}
