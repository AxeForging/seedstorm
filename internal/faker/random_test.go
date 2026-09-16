package faker

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// With --enum-rows, one enum column drives per-value rows. Picking it from a
// map made the same seed generate different data from run to run.
func TestSeedRandom_EnumRowsAreReproducible(t *testing.T) {
	sc := &schema.Schema{Tables: map[string]schema.Table{
		"tickets": {Columns: map[string]schema.Column{
			"id":       {Type: "integer", PK: true},
			"status":   {Type: "varchar", Faker: "randomstring(open,closed)"},
			"priority": {Type: "varchar", Faker: "randomstring(low,high,urgent)"},
			"opened":   {Type: "date", Faker: "date"},
		}},
	}}
	run := func() []map[string]interface{} {
		SeedRandom(99)
		defer SeedRandom(0)
		data, err := GenerateFilteredWithCounts(sc, []string{"tickets"}, []string{"tickets"}, 10, 3, nil, nil, "pgx")
		if err != nil {
			t.Fatal(err)
		}
		return data["tickets"]
	}
	want := run()
	for i := 0; i < 30; i++ {
		if got := run(); !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d differs from the first with the same seed:\n%v\n%v", i, got[:2], want[:2])
		}
	}
}

func TestSeedRandom_DatesEndAtAFixedInstantOnlyWhenSeeded(t *testing.T) {
	SeedRandom(5)
	if got := dateRangeEnd(); !got.Equal(reproducibleEnd) {
		t.Fatalf("seeded date range end = %v, want %v", got, reproducibleEnd)
	}
	SeedRandom(0)
	if got := dateRangeEnd(); time.Since(got) > time.Minute {
		t.Fatalf("unseeded date range end = %v, want now", got)
	}
}

func TestGenerate_CachedFakerSpecsBehaveLikeParsingEachTime(t *testing.T) {
	cases := []struct {
		faker, wantErr string
	}{
		{"number(1, 5)", ""},
		{"number(x,5)", "number: bad min arg"},
		{"number(1)", "number: bad max arg"},
		{"price(1.5,2)", ""},
		{"price(1.5,oops)", "price: bad max arg"},
		{"paragraph(2)", ""},
		{"lexify(??-##)", ""},
		{"  word  ", ""},
	}
	for _, c := range cases {
		t.Run(c.faker, func(t *testing.T) {
			for i := 0; i < 2; i++ { // second call hits the cache
				v, err := generate(c.faker)
				switch {
				case c.wantErr == "" && err != nil:
					t.Fatalf("call %d: %v", i, err)
				case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
					t.Fatalf("call %d: err = %v, want %q", i, err, c.wantErr)
				case c.wantErr == "" && v == nil:
					t.Fatalf("call %d: nil value", i)
				}
			}
		})
	}
	if v, _ := generate("number(3,3)"); fmt.Sprint(v) != "3" {
		t.Fatalf("number(3,3) = %v", v)
	}
}

// Self-references pick the latest row that may still have children; the O(1)
// tracking must choose exactly what the backwards scan chose.
func TestBackfillSelfReferences_MatchesTheBackwardsScan(t *testing.T) {
	table := schema.Table{Columns: map[string]schema.Column{
		"id":        {Type: "integer", PK: true},
		"parent_id": {Type: "integer", FK: "nodes.id", Nullable: true},
	}}
	for _, depth := range []int{0, 1, 2, 3, 7} {
		rows := make([]map[string]interface{}, 500)
		for i := range rows {
			rows[i] = map[string]interface{}{"id": i + 1}
		}
		if err := backfillSelfReferences(rows, table, "nodes", depth); err != nil {
			t.Fatal(err)
		}
		// Replay with the reference scan.
		levels := make([]int, len(rows))
		for i := range rows {
			want := interface{}(nil)
			if i > 0 {
				if p := chooseSelfRefParent(levels, i, depth); p >= 0 {
					want = rows[p]["id"]
					levels[i] = levels[p] + 1
				}
			}
			if !reflect.DeepEqual(rows[i]["parent_id"], want) {
				t.Fatalf("depth %d row %d: parent %v, backwards scan picks %v", depth, i, rows[i]["parent_id"], want)
			}
		}
	}
}
