package faker

import (
	"fmt"
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// collectChunks runs GenerateChunks and returns every chunk it emitted.
func collectChunks(t *testing.T, g *Stream, table string, rows, enumRows int, overridden bool, chunk int, opts GenerateOptions) [][]map[string]interface{} {
	t.Helper()
	var chunks [][]map[string]interface{}
	err := g.GenerateChunks(table, rows, enumRows, overridden, chunk, opts, func(c []map[string]interface{}) error {
		chunks = append(chunks, c)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return chunks
}

func flatten(chunks [][]map[string]interface{}) []map[string]interface{} {
	var out []map[string]interface{}
	for _, c := range chunks {
		out = append(out, c...)
	}
	return out
}

func assertChunkSizes(t *testing.T, chunks [][]map[string]interface{}, limit int) {
	t.Helper()
	if len(chunks) < 2 {
		t.Fatalf("%d chunks: the table must be split", len(chunks))
	}
	for i, c := range chunks {
		if len(c) == 0 || len(c) > limit {
			t.Fatalf("chunk %d holds %d rows, limit %d", i, len(c), limit)
		}
	}
}

// Enum coverage counts across chunks: a per-chunk top-up would add the full
// requested count for every value in every chunk.
func TestGenerateChunks_EnumCoverageSpansChunks(t *testing.T) {
	s := &schema.Schema{Tables: map[string]schema.Table{
		"tickets": makeEnumTable(map[string][]string{"status": {"open", "pending", "closed"}, "priority": {"low", "high"}}),
	}}
	g, err := NewStream(s, []string{"tickets"}, []string{"tickets"}, nil, "pgx", nil)
	if err != nil {
		t.Fatal(err)
	}
	const rows, chunk = 50, 7
	chunks := collectChunks(t, g, "tickets", rows, 0, false, chunk, DefaultGenerateOptions())
	assertChunkSizes(t, chunks, chunk)
	all := flatten(chunks)

	counts := countEnumValues(nil, findAllEnumColumns(s.Tables["tickets"]), all)
	for col, vals := range map[string][]string{"status": {"open", "pending", "closed"}, "priority": {"low", "high"}} {
		for _, v := range vals {
			if counts[col][v] < rows {
				t.Fatalf("%s=%s appears %d times, want at least %d", col, v, counts[col][v], rows)
			}
		}
	}
	// Each column alone needs at most len(values)×rows rows; the widest column bounds the table.
	if len(all) > 3*rows {
		t.Fatalf("%d rows for %d requested: enum top-up repeated per chunk", len(all), rows)
	}
	ids := map[interface{}]bool{}
	for _, row := range all {
		if ids[row["id"]] {
			t.Fatalf("id %v emitted twice", row["id"])
		}
		ids[row["id"]] = true
	}
}

// Rows per enum value on a junction: pieces are generated independently, so
// keys taken by earlier pieces must be remembered or combinations repeat.
func TestGenerateChunks_EnumRowsOnAJunctionNeverRepeatAKey(t *testing.T) {
	junc := junctionTable()
	junc.Columns["status"] = schema.Column{Type: "varchar", Faker: "randomstring(active,paused,closed)"}
	s := &schema.Schema{Tables: map[string]schema.Table{
		"a":    {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}}},
		"b":    {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}}},
		"junc": junc,
	}}
	g, err := NewStream(s, []string{"a", "b", "junc"}, []string{"a", "b", "junc"}, nil, "pgx", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Generate([]string{"a", "b"}, 4, 0, nil, DefaultGenerateOptions()); err != nil {
		t.Fatal(err)
	}
	chunks := collectChunks(t, g, "junc", 0, 4, false, 5, DefaultGenerateOptions())
	assertChunkSizes(t, chunks, 5)
	perValue := map[interface{}]int{}
	keys := map[string]bool{}
	for _, row := range flatten(chunks) {
		key := compositePKKey(row, junc)
		if keys[key] {
			t.Fatalf("junction key %s emitted twice", key)
		}
		keys[key] = true
		perValue[row["status"]]++
	}
	for _, v := range []string{"active", "paused", "closed"} {
		if perValue[v] != 4 {
			t.Fatalf("status %s has %d rows, want 4", v, perValue[v])
		}
	}
}

func TestGenerateChunks_FiniteJunctionWarnsOnceWithTheTotal(t *testing.T) {
	s := &schema.Schema{Tables: map[string]schema.Table{
		"a":    {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}}},
		"b":    {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}}},
		"junc": junctionTable(),
	}}
	g, err := NewStream(s, []string{"a", "b", "junc"}, []string{"a", "b", "junc"}, nil, "pgx", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Generate([]string{"a", "b"}, 3, 0, nil, DefaultGenerateOptions()); err != nil {
		t.Fatal(err)
	}
	var warnings []GenerationWarning
	opts := DefaultGenerateOptions()
	opts.OnWarning = func(w GenerationWarning) { warnings = append(warnings, w) }
	all := flatten(collectChunks(t, g, "junc", 20, 0, true, 4, opts))
	if len(all) != 9 {
		t.Fatalf("%d rows, want the 9 possible combinations", len(all))
	}
	if len(warnings) != 1 || warnings[0].Requested != 20 || warnings[0].Generated != 9 {
		t.Fatalf("warnings = %+v, want one covering the whole table", warnings)
	}
}

func TestGenerateChunks_RulesNumberRowsAcrossChunksAndSelfReferencesResolve(t *testing.T) {
	s := &schema.Schema{Tables: map[string]schema.Table{
		"staff": {Columns: map[string]schema.Column{
			"id":         {Type: "integer", PK: true},
			"manager_id": {Type: "integer", FK: "staff.id", Nullable: true},
			"badge":      {Type: "varchar", Faker: "word"},
		}},
	}}
	opts := DefaultGenerateOptions()
	opts.Overrides = Overrides{"staff": {"badge": func(row int, _ interface{}) (interface{}, error) { return row, nil }}}
	g, err := NewStream(s, []string{"staff"}, []string{"staff"}, nil, "pgx", opts.Overrides)
	if err != nil {
		t.Fatal(err)
	}
	chunks := collectChunks(t, g, "staff", 25, 0, true, 10, opts)
	assertChunkSizes(t, chunks, 10)
	emitted := map[interface{}]bool{}
	for i, row := range flatten(chunks) {
		if fmt.Sprint(row["badge"]) != fmt.Sprint(i) {
			t.Fatalf("row %d numbered %v by the rule, want %d", i, row["badge"], i)
		}
		emitted[row["id"]] = true
		if m := row["manager_id"]; m != nil && !emitted[m] {
			t.Fatalf("row %v references manager %v, which was not emitted before or with it", row["id"], m)
		}
	}
}
