package seeder

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// genClock records when a table's rows were generated: a value rule runs for
// every generated row, slowly enough for generation to take measurable time.
type genClock struct {
	mu          sync.Mutex
	first, last map[string]time.Time
}

func (c *genClock) rule(table string) faker.ColumnOverride {
	return func(_ int, auto interface{}) (interface{}, error) {
		time.Sleep(50 * time.Microsecond)
		now := time.Now()
		c.mu.Lock()
		if _, ok := c.first[table]; !ok {
			c.first[table] = now
		}
		c.last[table] = now
		c.mu.Unlock()
		return auto, nil
	}
}

// deptsSchema: users ← posts; audit unrelated; depts (earlier) has a nullable
// FK to staff (later), and staff requires depts: the near-cycle.
func deptsSchema() *schema.Schema {
	return &schema.Schema{Tables: map[string]schema.Table{
		"users": {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}, "name": {Type: "varchar", Faker: "word"}}},
		"audit": {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}, "note": {Type: "varchar", Faker: "word"}}},
		"posts": {Columns: map[string]schema.Column{
			"id": {Type: "integer", PK: true}, "user_id": {Type: "integer", FK: "users.id"}, "title": {Type: "varchar", Faker: "word"},
		}},
		"depts": {Columns: map[string]schema.Column{
			"id": {Type: "integer", PK: true}, "head_id": {Type: "integer", FK: "staff.id", Nullable: true}, "name": {Type: "varchar", Faker: "word"},
		}},
		"staff": {Columns: map[string]schema.Column{
			"id": {Type: "integer", PK: true}, "dept_id": {Type: "integer", FK: "depts.id"}, "name": {Type: "varchar", Faker: "word"},
		}},
	}}
}

func TestSeed_GenWorkersGenerateUnrelatedTablesTogetherAndKeepEveryReference(t *testing.T) {
	clock := &genClock{first: map[string]time.Time{}, last: map[string]time.Time{}}
	rules := faker.Overrides{
		"users": {"name": clock.rule("users")}, "audit": {"note": clock.rule("audit")},
		"posts": {"title": clock.rule("posts")}, "depts": {"name": clock.rule("depts")},
		"staff": {"name": clock.rule("staff")},
	}
	conn, rec := openRecording(t, nil, nil)
	order := []string{"users", "audit", "depts", "posts", "staff"}
	res, err := Seed(withDeadline(t, 30*time.Second), conn, "mysql", deptsSchema(), order, order, SeedOptions{
		Rows: 1500, BatchSize: 300, ChunkRows: 500, Workers: 4, GenWorkers: 4,
		Generate: faker.GenerateOptions{Overrides: rules},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1500*5 {
		t.Fatalf("total = %d, want %d", res.Total, 1500*5)
	}
	// Unrelated tables generated at the same time.
	if !clock.first["audit"].Before(clock.last["users"]) {
		t.Fatal("audit did not generate while users was generating")
	}
	// A child generated only after its parent's keys were final...
	if clock.first["posts"].Before(clock.last["users"]) {
		t.Fatal("posts started generating before users finished")
	}
	users := map[interface{}]bool{}
	for _, r := range rec.rowsOf("users") {
		users[r["id"]] = true
	}
	for _, r := range rec.rowsOf("posts") {
		if !users[r["user_id"]] {
			t.Fatalf("post references user %v that was never written", r["user_id"])
		}
	}
	// ...and the near-cycle keeps its ordered meaning: depts generated before
	// staff existed, so every head_id is NULL, and staff waited for depts.
	if clock.first["staff"].Before(clock.last["depts"]) {
		t.Fatal("staff started generating before depts finished")
	}
	for _, r := range rec.rowsOf("depts") {
		if r["head_id"] != nil {
			t.Fatalf("dept head_id = %v, want NULL: staff keys leaked into an earlier table", r["head_id"])
		}
	}
}

func TestSeed_ReproducibleRunsGenerateInOrderEvenWithGenWorkers(t *testing.T) {
	var mu sync.Mutex
	var starts []string
	conn, _ := openRecording(t, nil, nil)
	order := []string{"users", "audit", "depts", "posts", "staff"}
	_, err := Seed(withDeadline(t, 30*time.Second), conn, "mysql", deptsSchema(), order, order, SeedOptions{
		Rows: 200, Workers: 4, GenWorkers: 4, Reproducible: true,
		OnTableStart: func(table string) error {
			mu.Lock()
			starts = append(starts, table)
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(starts, order) {
		t.Fatalf("generation order = %v, want %v", starts, order)
	}
}

func TestSeed_GenWorkersStopAtTheFirstGenerationErrorWithoutHanging(t *testing.T) {
	boom := errors.New("rule failed")
	rules := faker.Overrides{"audit": {"note": func(row int, auto interface{}) (interface{}, error) {
		if row == 50 {
			return nil, boom
		}
		return auto, nil
	}}}
	conn, rec := openRecording(t, map[string]time.Duration{"users": 5 * time.Millisecond}, nil)
	order := []string{"users", "audit", "depts", "posts", "staff"}
	_, err := Seed(withDeadline(t, 20*time.Second), conn, "mysql", deptsSchema(), order, order, SeedOptions{
		Rows: 20000, BatchSize: 500, ChunkRows: 2000, Workers: 4, GenWorkers: 4,
		Generate: faker.GenerateOptions{Overrides: rules},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the rule error", err)
	}
	if n := len(rec.rowsOf("staff")); n != 0 {
		t.Fatalf("staff wrote %d rows after the run failed", n)
	}
}

func TestSeed_CancelDuringParallelGenerationReturns(t *testing.T) {
	conn, _ := openRecording(t, map[string]time.Duration{"users": 10 * time.Millisecond, "audit": 10 * time.Millisecond}, nil)
	ctx, cancel := context.WithCancel(withDeadline(t, 20*time.Second))
	time.AfterFunc(80*time.Millisecond, cancel)
	order := []string{"users", "audit", "depts", "posts", "staff"}
	_, err := Seed(ctx, conn, "mysql", deptsSchema(), order, order, SeedOptions{
		Rows: 200_000, BatchSize: 500, ChunkRows: 2000, Workers: 2, GenWorkers: 4,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestGenerationDeps(t *testing.T) {
	order := []string{"users", "audit", "depts", "posts", "staff"}
	got := generationDeps(deptsSchema(), order)
	want := [][]int{
		nil, // users
		nil, // audit
		nil, // depts (its FK to staff is later: staff waits instead)
		{0}, // posts waits for users
		{2}, // staff waits for depts (both directions reduce to this)
	}
	for i := range order {
		if strings.Trim(strings.Join(intsToStrings(got[i]), ","), ",") != strings.Join(intsToStrings(want[i]), ",") {
			t.Fatalf("deps[%s] = %v, want %v", order[i], got[i], want[i])
		}
	}
}

func intsToStrings(v []int) []string {
	out := make([]string, len(v))
	for i, n := range v {
		out[i] = string(rune('0' + n))
	}
	return out
}

// An earlier table with a nullable FK to a later, otherwise unrelated table:
// in order it generates while the later table has no keys, so the FK is NULL.
// Here notes also waits for a slow parent, so without the "an earlier table
// references me" rule tags would finish first and notes would fork seeing tags'
// keys, while notes' writer does not wait for tags: rows could then reference
// tags not yet written.
func TestSeed_GenWorkersKeepNullableForwardReferencesNull(t *testing.T) {
	sc := &schema.Schema{Tables: map[string]schema.Table{
		"authors": {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}, "name": {Type: "varchar", Faker: "word"}}},
		"notes": {Columns: map[string]schema.Column{
			"id": {Type: "integer", PK: true}, "author_id": {Type: "integer", FK: "authors.id"},
			"tag_id": {Type: "integer", FK: "tags.id", Nullable: true},
		}},
		"tags": {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}, "name": {Type: "varchar", Faker: "word"}}},
	}}
	slow := faker.Overrides{"authors": {"name": func(_ int, auto interface{}) (interface{}, error) {
		time.Sleep(300 * time.Microsecond) // authors, and so notes, finish long after tags could
		return auto, nil
	}}}
	conn, rec := openRecording(t, nil, nil)
	order := []string{"authors", "notes", "tags"}
	if _, err := Seed(withDeadline(t, 30*time.Second), conn, "mysql", sc, order, order, SeedOptions{
		Rows: 600, BatchSize: 200, ChunkRows: 100, Workers: 3, GenWorkers: 3,
		Generate: faker.GenerateOptions{Overrides: slow},
	}); err != nil {
		t.Fatal(err)
	}
	notes := rec.rowsOf("notes")
	if len(notes) != 600 {
		t.Fatalf("notes = %d rows", len(notes))
	}
	for _, r := range notes {
		if r["tag_id"] != nil {
			t.Fatalf("note tag_id = %v, want NULL: tags generated before notes", r["tag_id"])
		}
	}
}
