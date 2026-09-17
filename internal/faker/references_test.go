package faker

import (
	"fmt"
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// referenceSchema has foreign keys whose target is not the parent's single
// primary key: a UNIQUE code column, and one column of a composite key.
func referenceSchema() *schema.Schema {
	return &schema.Schema{Tables: map[string]schema.Table{
		"accounts": {Columns: map[string]schema.Column{
			"id":   {Type: "integer", PK: true},
			"code": {Type: "varchar(12)", Faker: "uuid", Unique: true},
		}},
		"ledger": {Columns: map[string]schema.Column{
			"id":           {Type: "integer", PK: true},
			"account_code": {Type: "varchar(12)", FK: "accounts.code"},
		}},
		"memberships": {Columns: map[string]schema.Column{
			"tenant_id": {Type: "integer", PK: true, Faker: "number(1000,1999)"},
			"member_no": {Type: "integer", PK: true, Faker: "number(500000,599999)"},
		}},
		"badges": {Columns: map[string]schema.Column{
			"id":        {Type: "integer", PK: true},
			"member_no": {Type: "integer", FK: "memberships.member_no"},
		}},
	}}
}

func columnValues(rows []map[string]interface{}, col string) map[string]bool {
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[fmt.Sprint(r[col])] = true
	}
	return out
}

// An FK must hold values of the column it references. It used to draw from the
// parent's primary-key pool, so ledger.account_code got account ids and
// badges.member_no got a mix of tenant ids and member numbers.
func TestGenerate_ForeignKeysUseTheReferencedColumn(t *testing.T) {
	order := []string{"accounts", "memberships", "ledger", "badges"}
	for _, fork := range []bool{false, true} {
		t.Run(fmt.Sprintf("fork=%v", fork), func(t *testing.T) {
			g, err := NewStream(referenceSchema(), order, order, nil, "pgx", nil)
			if err != nil {
				t.Fatal(err)
			}
			out := map[string][]map[string]interface{}{}
			for _, table := range order {
				gen := g
				if fork {
					gen = g.ForkTable(table)
				}
				err := gen.GenerateChunks(table, 60, 0, false, 25, DefaultGenerateOptions(), func(rows []map[string]interface{}) error {
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
			checks := []struct{ child, col, parent, parentCol string }{
				{"ledger", "account_code", "accounts", "code"},
				{"badges", "member_no", "memberships", "member_no"},
			}
			for _, c := range checks {
				allowed := columnValues(out[c.parent], c.parentCol)
				if len(out[c.child]) == 0 {
					t.Fatalf("%s generated nothing", c.child)
				}
				for i, row := range out[c.child] {
					if v := fmt.Sprint(row[c.col]); !allowed[v] {
						t.Fatalf("%s row %d: %s=%s is not a %s.%s value", c.child, i, c.col, v, c.parent, c.parentCol)
					}
				}
			}
		})
	}
}
