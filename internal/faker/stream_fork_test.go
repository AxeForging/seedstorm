package faker

import (
	"reflect"
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// forkSchema covers every piece of per-table state a fork must carry: an
// integer key pool, a junction cursor, a UNIQUE sequence, a self-reference, an
// enum, and a value rule numbering rows with {{seq}}.
func forkSchema() *schema.Schema {
	return &schema.Schema{Tables: map[string]schema.Table{
		"users": {Columns: map[string]schema.Column{
			"id":    {Type: "integer", PK: true},
			"code":  {Type: "integer", Faker: uniqueSequenceFaker, Unique: true},
			"email": {Type: "varchar(60)", Faker: "email"},
			"role":  {Type: "varchar(10)", Faker: "randomstring(admin,user)"},
		}},
		"tags": {Columns: map[string]schema.Column{
			"id":   {Type: "integer", PK: true},
			"name": {Type: "varchar(20)", Faker: "word"},
		}},
		"user_tags": {Columns: map[string]schema.Column{
			"user_id": {Type: "integer", PK: true, FK: "users.id"},
			"tag_id":  {Type: "integer", PK: true, FK: "tags.id"},
		}},
		"posts": {Columns: map[string]schema.Column{
			"id":        {Type: "integer", PK: true},
			"user_id":   {Type: "integer", FK: "users.id"},
			"parent_id": {Type: "integer", FK: "posts.id", Nullable: true},
			"title":     {Type: "varchar(40)", Faker: "sentence"},
		}},
	}}
}

func seqRule() Overrides {
	return Overrides{"posts": {"title": func(row int, _ interface{}) (interface{}, error) { return row, nil }}}
}

// Generating each table on a fork, in chunks and in two passes, must produce
// exactly what the shared stream produces with the same seed: the fork carries
// every piece of state generation reads or advances.
func TestForkTable_GeneratesExactlyLikeTheSharedStream(t *testing.T) {
	order := []string{"tags", "users", "user_tags", "posts"}
	opts := GenerateOptions{SelfRefDepth: 2, Overrides: seqRule()}
	run := func(fork bool) map[string][]map[string]interface{} {
		SeedRandom(4242)
		defer SeedRandom(0)
		g, err := NewStream(forkSchema(), order, order, nil, "pgx", opts.Overrides)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]map[string]interface{}{}
		for pass := 0; pass < 2; pass++ { // a second pass continues keys, cursors, sequences and {{seq}}
			for _, table := range order {
				gen := g
				if fork {
					gen = g.ForkTable(table)
				}
				err := gen.GenerateChunks(table, 45, 0, false, 20, opts, func(rows []map[string]interface{}) error {
					out[table] = append(out[table], rows...)
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if fork {
					g.MergeTable(gen, table)
				}
			}
		}
		return out
	}
	shared, forked := run(false), run(true)
	for _, table := range []string{"tags", "users", "user_tags", "posts"} {
		if len(shared[table]) == 0 {
			t.Fatalf("%s generated nothing", table)
		}
		if !reflect.DeepEqual(shared[table], forked[table]) {
			t.Fatalf("%s differs between the shared stream and forks:\nshared %v\nforked %v", table, shared[table][:3], forked[table][:3])
		}
	}
}
