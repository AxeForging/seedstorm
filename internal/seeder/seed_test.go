package seeder

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

func TestSeed_InsertsChunkByChunkNeverHoldingTheWholeTable(t *testing.T) {
	conn, script := openScripted(t, func(int) error { return nil })
	var chunks []int
	res, err := Seed(context.Background(), conn, "pgx", notesSchema(), []string{"notes"}, []string{"notes"}, SeedOptions{
		Rows: 100, ChunkRows: 30, BatchSize: 1000,
		OnRows: func(_ string, rows []map[string]interface{}) error {
			// Each chunk is written before the next is generated.
			if want := len(chunks); script.count() != want {
				t.Fatalf("chunk %d generated after %d inserts, want %d", len(chunks), script.count(), want)
			}
			chunks = append(chunks, len(rows))
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 4 || chunks[0] != 30 || chunks[3] != 10 {
		t.Fatalf("chunks = %v, want 30,30,30,10", chunks)
	}
	if res.Counts["notes"] != 100 || res.Total != 100 || script.count() != 4 {
		t.Fatalf("result %+v after %d inserts", res, script.count())
	}
}

func TestSeed_StopsAtTheFirstRefusedInsert(t *testing.T) {
	conn, script := openScripted(t, func(n int) error {
		if n == 2 {
			return errCheck
		}
		return nil
	})
	res, err := Seed(context.Background(), conn, "pgx", notesSchema(), []string{"notes"}, []string{"notes"}, SeedOptions{Rows: 100, ChunkRows: 30})
	if err == nil || !errors.Is(err, errCheck) || !strings.Contains(err.Error(), "insert into notes") {
		t.Fatalf("err = %v, want the refused insert", err)
	}
	if res.Counts["notes"] != 30 || script.count() != 2 {
		t.Fatalf("result %+v after %d inserts, want only the first chunk counted and no retry", res, script.count())
	}
}

func TestSeed_DryRunWritesNothingAndNeedsNoConnection(t *testing.T) {
	sc := &schema.Schema{Tables: map[string]schema.Table{
		"users": {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}}},
		"posts": {Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}, "user_id": {Type: "integer", FK: "users.id"}}},
	}}
	users := map[interface{}]bool{}
	var tables []string
	res, err := Seed(context.Background(), nil, "pgx", sc, []string{"users", "posts"}, []string{"users", "posts"}, SeedOptions{
		Rows: 25, ChunkRows: 10, DryRun: true,
		TableRows: map[string]int{"posts": 60},
		OnRows: func(table string, rows []map[string]interface{}) error {
			for _, row := range rows {
				if table == "users" {
					users[row["id"]] = true
				} else if !users[row["user_id"]] {
					t.Fatalf("post references user %v, never generated", row["user_id"])
				}
			}
			return nil
		},
		OnTableStart: func(table string) error { tables = append(tables, "start:"+table); return nil },
		OnTable:      func(p Progress) { tables = append(tables, fmt.Sprintf("%s=%d", p.Table, p.Inserted)) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts["users"] != 25 || res.Counts["posts"] != 60 || res.Total != 85 {
		t.Fatalf("counts = %+v", res)
	}
	if got := strings.Join(tables, ","); got != "start:users,users=25,start:posts,posts=60" {
		t.Fatalf("tables reported = %v", tables)
	}
}
