package graph

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// ignoreShop: tenants ← users ← orders; users.manager_id is a nullable self-FK;
// notes.user_id is a nullable FK; flyway_history stands alone.
func ignoreShop() *schema.Schema {
	return makeSchema(map[string]map[string]schema.Column{
		"flyway_history": {"id": {Type: "integer", PK: true}},
		"tenants":        {"id": {Type: "integer", PK: true}},
		"users": {
			"id":         {Type: "integer", PK: true},
			"tenant_id":  {Type: "integer", FK: "tenants.id"},
			"manager_id": {Type: "integer", FK: "users.id", Nullable: true},
		},
		"orders": {
			"id":      {Type: "integer", PK: true},
			"user_id": {Type: "integer", FK: "users.id"},
		},
		"notes": {
			"id":      {Type: "integer", PK: true},
			"user_id": {Type: "integer", FK: "users.id", Nullable: true},
		},
	})
}

var ignoreOrder = []string{"flyway_history", "tenants", "users", "notes", "orders"}

func rowsIn(tables ...string) (func(string) (bool, error), *[]string) {
	asked := &[]string{}
	has := map[string]bool{}
	for _, t := range tables {
		has[t] = true
	}
	return func(table string) (bool, error) {
		*asked = append(*asked, table)
		return has[table], nil
	}, asked
}

func TestApplyIgnore(t *testing.T) {
	cases := []struct {
		name      string
		order     []string
		ignored   map[string]bool
		populated []string
		want      []string
		wantErr   []string
		wantAsked []string
	}{
		{
			name:  "nothing ignored keeps order and asks nothing",
			order: ignoreOrder, want: ignoreOrder,
		},
		{
			name:    "unreferenced ignored table is dropped without a count",
			order:   ignoreOrder,
			ignored: map[string]bool{"flyway_history": true},
			want:    []string{"tenants", "users", "notes", "orders"},
		},
		{
			name:      "populated ignored parent is referenced",
			order:     ignoreOrder,
			ignored:   map[string]bool{"users": true},
			populated: []string{"users"},
			want:      []string{"flyway_history", "tenants", "notes", "orders"},
			wantAsked: []string{"users"},
		},
		{
			name:      "empty ignored parent of a NOT NULL FK fails naming both tables",
			order:     ignoreOrder,
			ignored:   map[string]bool{"users": true},
			wantErr:   []string{"orders.user_id", "ignored table users", "empty"},
			wantAsked: []string{"users"},
		},
		{
			name:    "nullable FK to an empty ignored parent is fine",
			order:   []string{"tenants", "notes"},
			ignored: map[string]bool{"users": true},
			want:    []string{"tenants", "notes"},
		},
		{
			name:      "ignored parent not in the order still guards its children",
			order:     []string{"orders"},
			ignored:   map[string]bool{"users": true},
			wantErr:   []string{"orders.user_id", "users"},
			wantAsked: []string{"users"},
		},
		{
			name:      "chained: only the direct ignored parent matters",
			order:     ignoreOrder,
			ignored:   map[string]bool{"tenants": true, "users": true},
			populated: []string{"users"},
			want:      []string{"flyway_history", "notes", "orders"},
			wantAsked: []string{"users"},
		},
		{
			name:      "chained: kept middle table needs its empty ignored grandparent",
			order:     []string{"users", "orders"},
			ignored:   map[string]bool{"tenants": true},
			wantErr:   []string{"users.tenant_id", "tenants"},
			wantAsked: []string{"tenants"},
		},
		{
			name:    "all tables ignored",
			order:   ignoreOrder,
			ignored: map[string]bool{"flyway_history": true, "tenants": true, "users": true, "notes": true, "orders": true},
			want:    []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			populated, asked := rowsIn(tc.populated...)
			got, err := ApplyIgnore(ignoreShop(), tc.order, tc.ignored, populated)
			if len(tc.wantErr) > 0 {
				if err == nil {
					t.Fatalf("got %v, want error", got)
				}
				for _, part := range tc.wantErr {
					if !strings.Contains(err.Error(), part) {
						t.Errorf("error %q lacks %q", err, part)
					}
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("kept = %v, want %v", got, tc.want)
				}
			}
			if len(*asked) != len(tc.wantAsked) || (len(*asked) > 0 && !reflect.DeepEqual(*asked, tc.wantAsked)) {
				t.Errorf("populated asked for %v, want %v", *asked, tc.wantAsked)
			}
		})
	}
}

func TestApplyIgnore_ListsEveryEmptyParentChild(t *testing.T) {
	sc := ignoreShop()
	sc.Tables["invoices"] = schema.Table{Columns: map[string]schema.Column{
		"id":      {Type: "integer", PK: true},
		"user_id": {Type: "integer", FK: "users.id"},
	}}
	_, err := ApplyIgnore(sc, []string{"orders", "invoices"}, map[string]bool{"users": true}, func(string) (bool, error) { return false, nil })
	if err == nil || !strings.Contains(err.Error(), "invoices.user_id") || !strings.Contains(err.Error(), "orders.user_id") {
		t.Fatalf("err = %v", err)
	}
	if strings.Index(err.Error(), "invoices") > strings.Index(err.Error(), "orders") {
		t.Errorf("children should be listed in name order: %v", err)
	}
}

func TestApplyIgnore_CountErrorsPropagate(t *testing.T) {
	boom := errors.New("permission denied")
	_, err := ApplyIgnore(ignoreShop(), ignoreOrder, map[string]bool{"users": true}, func(string) (bool, error) { return false, boom })
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "users") {
		t.Fatalf("err = %v", err)
	}
	_, err = ApplyIgnore(ignoreShop(), ignoreOrder, map[string]bool{"users": true}, nil)
	if err == nil {
		t.Fatal("nil populated with a required ignored parent must error, not guess")
	}
}
