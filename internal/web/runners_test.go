package web

import (
	"reflect"
	"testing"
)

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
