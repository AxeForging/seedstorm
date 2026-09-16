package seeder

import (
	"testing"

	"github.com/AxeForging/seedstorm/internal/faker"
)

// A table's key pool is freed once nothing left in the run can read it, never
// before: users is read by posts (later), audit by nobody, and depts by staff
// while depts itself references staff (a nullable near-cycle).
func TestPoolReleases_FreeAPoolOnlyWhenNoRemainingTableReadsIt(t *testing.T) {
	sc := deptsSchema()
	order := []string{"users", "audit", "depts", "posts", "staff"}
	stream, err := faker.NewStream(sc, order, order, nil, "mysql", nil)
	if err != nil {
		t.Fatal(err)
	}
	releases := newPoolReleases(sc, order)
	pools := func() map[string]int {
		out := map[string]int{}
		for _, table := range order {
			out[table] = stream.KeyPoolLen(table)
		}
		return out
	}
	generate := func(table string) {
		if err := stream.GenerateChunks(table, 20, 0, false, 0, faker.GenerateOptions{}, func([]map[string]interface{}) error { return nil }); err != nil {
			t.Fatal(err)
		}
		releases.generated(stream, table)
	}

	generate("users")
	generate("audit")
	if p := pools(); p["users"] != 20 || p["audit"] != 0 {
		t.Fatalf("after users+audit pools = %v: users is still needed by posts, audit by nobody", p)
	}
	generate("depts")
	if p := pools(); p["depts"] != 20 {
		t.Fatalf("depts pool freed before staff read it: %v", p)
	}
	generate("posts")
	if p := pools(); p["users"] != 0 || p["posts"] != 0 {
		t.Fatalf("after posts pools = %v: users and posts are no longer read", p)
	}
	generate("staff")
	if p := pools(); p["depts"] != 0 || p["staff"] != 0 {
		t.Fatalf("after staff pools = %v, want every pool freed", p)
	}
}
