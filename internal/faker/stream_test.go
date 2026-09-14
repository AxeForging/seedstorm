package faker

import (
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// A table filled chunk by chunk from one Stream must look like a table filled in
// one go: no repeated keys, UNIQUE tuples or sequence values across chunks.
func TestStream_LaterChunksNeverRepeatEarlierChunks(t *testing.T) {
	s := &schema.Schema{Tables: map[string]schema.Table{
		"a":    {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}}},
		"b":    {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}}},
		"junc": junctionTable(),
		"accounts": {
			Columns: map[string]schema.Column{
				"id":     {Type: "uuid", PK: true},
				"realm":  {Type: "character varying", Faker: "word"},
				"handle": {Type: "character varying", Faker: "numerify(#)"},
				"ext":    {Type: "bigint", Faker: uniqueSequenceFaker},
			},
			Unique: [][]string{{"realm", "handle"}},
		},
	}}
	opts := DefaultGenerateOptions()
	opts.Overrides = Overrides{"accounts": {"realm": func(int, interface{}) (interface{}, error) { return "r1", nil }}}
	g, err := NewStream(s, []string{"a", "b", "junc", "accounts"}, []string{"a", "b", "junc", "accounts"}, nil, "pgx", opts.Overrides)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Generate([]string{"a", "b"}, 10, 0, nil, opts); err != nil {
		t.Fatal(err)
	}

	junc := map[string]bool{}
	for chunk := 0; chunk < 4; chunk++ {
		data, err := g.Generate([]string{"junc"}, 0, 0, map[string]int{"junc": 20}, opts)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range data["junc"] {
			key := compositePKKey(row, s.Tables["junc"])
			if junc[key] {
				t.Fatalf("chunk %d repeated junction key %s", chunk, key)
			}
			junc[key] = true
		}
	}
	if len(junc) != 80 {
		t.Fatalf("%d junction rows, want 80", len(junc))
	}

	// realm is pinned and handle has 10 values: only 10 tuples exist in total.
	tuples := map[string]bool{}
	ext := map[interface{}]bool{}
	for chunk := 0; chunk < 3; chunk++ {
		data, err := g.Generate([]string{"accounts"}, 0, 0, map[string]int{"accounts": 6}, opts)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range data["accounts"] {
			tuple := uniqueTupleKey(s.Tables["accounts"], []string{"realm", "handle"}, row)
			if tuples[tuple] {
				t.Fatalf("chunk %d repeated UNIQUE tuple %q", chunk, tuple)
			}
			tuples[tuple] = true
			if ext[row["ext"]] {
				t.Fatalf("chunk %d repeated sequence value %v", chunk, row["ext"])
			}
			ext[row["ext"]] = true
		}
	}
	if len(tuples) > 10 {
		t.Fatalf("%d tuples, but only 10 exist", len(tuples))
	}
}

func TestStream_PoolsStayBoundedWhileIDsKeepIncreasing(t *testing.T) {
	defer func(old int) { poolLimit = old }(poolLimit)
	poolLimit = 50
	s := &schema.Schema{Tables: map[string]schema.Table{
		"items": {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}, "name": {Type: "text", Faker: "word"}}},
		"lines": {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}, "item_id": {Type: "integer", FK: "items.id"}}},
	}}
	g, err := NewStream(s, []string{"items", "lines"}, []string{"items", "lines"}, nil, "pgx", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := 1
	for chunk := 0; chunk < 10; chunk++ {
		data, err := g.Generate([]string{"items"}, 40, 0, nil, DefaultGenerateOptions())
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range data["items"] {
			if row["id"] != want {
				t.Fatalf("chunk %d: id %v, want %d", chunk, row["id"], want)
			}
			want++
		}
		if n := len(g.pks["items"]); n > poolLimit {
			t.Fatalf("items pool holds %d ids, limit %d", n, poolLimit)
		}
	}
	data, err := g.Generate([]string{"lines"}, 200, 0, nil, DefaultGenerateOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range data["lines"] {
		id, _ := asInt(row["item_id"])
		if id < 1 || id > 400 {
			t.Fatalf("line references item %v, which was never generated", row["item_id"])
		}
	}
}
