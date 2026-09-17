package db

import (
	"reflect"
	"testing"
)

func TestApplyPartitionBound_ReadsWhatPostgresPrints(t *testing.T) {
	p := &Partitioning{Strategy: "range"}
	for _, b := range []string{
		"FOR VALUES FROM ('2025-01-01') TO ('2026-01-01')",
		"FOR VALUES FROM (MINVALUE) TO (0)",
		"DEFAULT",
	} {
		applyPartitionBound(p, b)
	}
	want := []PartitionRange{{From: "'2025-01-01'", To: "'2026-01-01'"}, {From: "MINVALUE", To: "0"}}
	if !reflect.DeepEqual(p.Ranges, want) || !p.Default {
		t.Fatalf("range partitioning = %+v", p)
	}

	l := &Partitioning{Strategy: "list"}
	applyPartitionBound(l, "FOR VALUES IN ('eu-west', 'it''s, fine', NULL)")
	applyPartitionBound(l, "FOR VALUES IN (7)")
	if want := []string{"eu-west", "it's, fine", "7"}; !reflect.DeepEqual(l.Values, want) {
		t.Fatalf("list values = %q, want %q", l.Values, want)
	}

	h := &Partitioning{Strategy: "hash"}
	applyPartitionBound(h, "FOR VALUES WITH (modulus 2, remainder 0)")
	if h.Default || len(h.Ranges) != 0 || len(h.Values) != 0 {
		t.Fatalf("hash bounds recorded as ranges or values: %+v", h)
	}
}

func TestParseTextArray_KeepsExpressionSlots(t *testing.T) {
	if got := parseTextArray(`{created_at}`); !reflect.DeepEqual(got, []string{"created_at"}) {
		t.Fatalf("got %q", got)
	}
	if got := parseTextArray(`{"",region}`); !reflect.DeepEqual(got, []string{"", "region"}) {
		t.Fatalf("got %q", got)
	}
	if got := parseTextArray(`{}`); got != nil {
		t.Fatalf("got %q", got)
	}
}
