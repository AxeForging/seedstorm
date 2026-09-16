package faker

import (
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/schema"
)

func docsSchema() *schema.Schema {
	return &schema.Schema{Tables: map[string]schema.Table{
		"docs": {Columns: map[string]schema.Column{
			"id":    {Type: "integer", PK: true},
			"title": {Type: "varchar(40)", Faker: "word"},
		}},
	}}
}

// Wide rows must come in chunks bounded by memory, not only by row count. The
// width here comes from a value rule, applied after generation: measuring raw
// rows would miss it, which is how the first implementation still held 320MB.
func TestGenerateChunks_ChunkBytesBoundsWideRowsAddedByRules(t *testing.T) {
	sc := docsSchema()
	body := strings.Repeat("x", 4000)
	opts := GenerateOptions{
		ChunkBytes: 256 << 10, // ~60 rows of 4KB
		Overrides: Overrides{"docs": {"title": func(_ int, _ interface{}) (interface{}, error) {
			return body, nil
		}}},
	}
	g, err := NewStream(sc, []string{"docs"}, []string{"docs"}, nil, "pgx", opts.Overrides)
	if err != nil {
		t.Fatal(err)
	}
	chunks := collectChunks(t, g, "docs", 20000, 0, false, 20000, opts)
	if got := len(flatten(chunks)); got != 20000 {
		t.Fatalf("rows = %d, want 20000", got)
	}
	if len(chunks[0]) != firstChunkRows {
		t.Fatalf("first chunk = %d rows, want the %d-row measuring chunk", len(chunks[0]), firstChunkRows)
	}
	for i, c := range chunks[1:] {
		if mem := db.RowsMemory(c); mem > opts.ChunkBytes+db.RowsMemory(c[:1]) {
			t.Fatalf("chunk %d holds %d bytes in %d rows, cap %d", i+1, mem, len(c), opts.ChunkBytes)
		}
	}
}

// Narrow rows and small tables must chunk exactly as without the byte cap:
// chunk boundaries shape self-references and UNIQUE repair, and --seed output.
func TestGenerateChunks_ChunkBytesLeavesNarrowTablesAlone(t *testing.T) {
	for _, tc := range []struct {
		rows, chunk int
		want        []int
	}{
		{rows: 1500, chunk: 20000, want: []int{1500}},
		{rows: 5000, chunk: 1000, want: []int{1000, 1000, 1000, 1000, 1000}},
		{rows: 30000, chunk: 20000, want: []int{2048, 20000, 7952}},
	} {
		g, err := NewStream(docsSchema(), []string{"docs"}, []string{"docs"}, nil, "pgx", nil)
		if err != nil {
			t.Fatal(err)
		}
		chunks := collectChunks(t, g, "docs", tc.rows, 0, false, tc.chunk, GenerateOptions{ChunkBytes: 32 << 20})
		var got []int
		for _, c := range chunks {
			got = append(got, len(c))
		}
		if !equalInts(got, tc.want) {
			t.Fatalf("rows %d chunk %d: chunk sizes %v, want %v", tc.rows, tc.chunk, got, tc.want)
		}
	}
}

func TestChunkLimit(t *testing.T) {
	cases := []struct{ chunk, bytes, row, want int }{
		{20000, 0, 5000, 20000},     // no byte cap
		{20000, 32 << 20, 0, 20000}, // not measured yet
		{20000, 32 << 20, 1000, 20000},
		{20000, 32 << 20, 6000, 5592},
		{20000, 32 << 20, 50 << 20, 32}, // one huge row: floor, not zero
		{10, 32 << 20, 50 << 20, 10},    // floor never exceeds the row cap
	}
	for _, c := range cases {
		if got := chunkLimit(c.chunk, c.bytes, c.row); got != c.want {
			t.Errorf("chunkLimit(%d, %d, %d) = %d, want %d", c.chunk, c.bytes, c.row, got, c.want)
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
