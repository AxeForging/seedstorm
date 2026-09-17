package faker

import (
	"strings"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/db"
)

func TestPartitionKeyFaker_StaysInsideTheBounds(t *testing.T) {
	cases := []struct {
		name       string
		colType    string
		part       db.Partitioning
		wantFaker  string
		wantRefuse string
	}{
		{
			"contiguous date ranges", "date",
			db.Partitioning{
				Strategy: "range", Columns: []string{"created_at"},
				Ranges: []db.PartitionRange{{From: "'2026-01-01'", To: "'2027-01-01'"}, {From: "'2025-01-01'", To: "'2026-01-01'"}},
			},
			"daterange(2025-01-01,2027-01-01)", "",
		},
		{
			"timestamp range", "timestamp without time zone",
			db.Partitioning{
				Strategy: "range", Columns: []string{"at"},
				Ranges: []db.PartitionRange{{From: "'2025-01-01 00:00:00'", To: "'2025-02-01 00:00:00'"}},
			},
			"datetimerange(2025-01-01 00:00:00,2025-02-01 00:00:00)", "",
		},
		{
			"gap between ranges uses the widest one", "integer",
			db.Partitioning{
				Strategy: "range", Columns: []string{"n"},
				Ranges: []db.PartitionRange{{From: "0", To: "10"}, {From: "100", To: "1000"}},
			},
			"number(100,999)", "",
		},
		{
			"open lower bound", "bigint",
			db.Partitioning{
				Strategy: "range", Columns: []string{"n"},
				Ranges: []db.PartitionRange{{From: "MINVALUE", To: "500"}},
			},
			"number(0,499)", "",
		},
		{
			"list", "text",
			db.Partitioning{Strategy: "list", Columns: []string{"region"}, Values: []string{"eu-west", "us-east"}},
			"randomstring(eu-west,us-east)", "",
		},
		{"default partition accepts anything", "date", db.Partitioning{
			Strategy: "range", Columns: []string{"d"},
			Ranges: []db.PartitionRange{{From: "'2025-01-01'", To: "'2026-01-01'"}}, Default: true,
		}, "", ""},
		{"hash accepts anything", "integer", db.Partitioning{Strategy: "hash", Columns: []string{"id"}}, "", ""},
		{"expression key", "timestamp", db.Partitioning{
			Strategy: "range", Columns: []string{""},
			Ranges: []db.PartitionRange{{From: "'2026-01-01'", To: "'2026-02-01'"}},
		}, "", "partitioned by an expression"},
		{"two-column key", "integer", db.Partitioning{
			Strategy: "range", Columns: []string{"a", "b"},
			Ranges: []db.PartitionRange{{From: "1, 1", To: "2, 2"}},
		}, "", "partitioned by several columns"},
		{"range on text", "text", db.Partitioning{
			Strategy: "range", Columns: []string{"s"},
			Ranges: []db.PartitionRange{{From: "'a'", To: "'m'"}},
		}, "", "range-partitioned on a text column"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			faker, refuse := partitionKeyFaker(c.colType, &c.part)
			if faker != c.wantFaker {
				t.Errorf("faker = %q, want %q", faker, c.wantFaker)
			}
			if (c.wantRefuse == "") != (refuse == "") || !strings.Contains(refuse, c.wantRefuse) {
				t.Errorf("refusal = %q, want containing %q", refuse, c.wantRefuse)
			}
		})
	}
}

func TestGenerate_DateRangesStayInsideTheirBounds(t *testing.T) {
	from, to := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 500; i++ {
		v, err := defaultGen.generate("daterange(2025-01-01,2025-02-01)")
		if err != nil {
			t.Fatal(err)
		}
		d, err := time.Parse("2006-01-02", v.(string))
		if err != nil || d.Before(from) || !d.Before(to) {
			t.Fatalf("daterange produced %v (%v)", v, err)
		}
		ts, err := defaultGen.generate("datetimerange(2025-01-01 00:00:00,2025-02-01 00:00:00)")
		if err != nil {
			t.Fatal(err)
		}
		if tt := ts.(time.Time); tt.Before(from) || !tt.Before(to) {
			t.Fatalf("datetimerange produced %v", tt)
		}
	}
	if !ValidFaker("daterange(2025-01-01,2025-02-01)") || !ValidFaker("datetimerange(2025-01-01 00:00:00,2025-02-01 00:00:00)") {
		t.Fatal("range fakers are not recognised")
	}
	if _, err := defaultGen.generate("daterange(2025-02-01,2025-01-01)"); err == nil {
		t.Fatal("an empty range was accepted")
	}
}
