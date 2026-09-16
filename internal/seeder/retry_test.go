package seeder

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// A statement the database refused as a deadlock victim wrote nothing, so the
// writer retries it: every row lands exactly once. A constraint error is a real
// refusal and is never retried.
func TestSeed_DeadlockedBatchIsRetriedOnceAndWrittenOnce(t *testing.T) {
	opts := SeedOptions{Rows: 1000, BatchSize: 100, Workers: 4}
	var clean int
	t.Run("clean run", func(t *testing.T) {
		conn, rec := openRecording(t, nil, nil)
		if _, err := Seed(withDeadline(t, 20*time.Second), conn, "mysql", notesSchema(), []string{"notes"}, []string{"notes"}, opts); err != nil {
			t.Fatal(err)
		}
		clean = rec.perTab["notes"]
	})

	var injected atomic.Bool
	conn, rec := openRecording(t, nil, func(table string, n int) error {
		if table == "notes" && n == 3 && injected.CompareAndSwap(false, true) {
			return &mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock"}
		}
		return nil
	})
	res, err := Seed(withDeadline(t, 20*time.Second), conn, "mysql", notesSchema(), []string{"notes"}, []string{"notes"}, opts)
	deadlocked := injected.Load()
	if err != nil {
		t.Fatal(err)
	}
	rows := rec.rowsOf("notes")
	seen := map[interface{}]bool{}
	for _, r := range rows {
		if seen[r["id"]] {
			t.Fatalf("id %v written twice after the retry", r["id"])
		}
		seen[r["id"]] = true
	}
	if !deadlocked || len(rows) != 1000 || res.Total != 1000 {
		t.Fatalf("deadlock injected=%v, rows written %d, result %d; want 1000 exactly once", deadlocked, len(rows), res.Total)
	}
	// Exactly one statement more than the clean run reached the database.
	if got := rec.perTab["notes"]; got != clean+1 {
		t.Fatalf("%d INSERT statements, want the clean run's %d + 1 retry", got, clean)
	}
}

func TestSeed_ConstraintErrorsAreNotRetried(t *testing.T) {
	refused := &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}
	conn, rec := openRecording(t, nil, func(table string, n int) error {
		if n == 2 {
			return refused
		}
		return nil
	})
	_, err := Seed(withDeadline(t, 20*time.Second), conn, "mysql", notesSchema(), []string{"notes"}, []string{"notes"}, SeedOptions{
		Rows: 1000, BatchSize: 500, Workers: 1,
	})
	if !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the duplicate-key refusal", err)
	}
	if got := rec.perTab["notes"]; got != 2 {
		t.Fatalf("%d INSERT statements, want 2: a constraint error must not be retried", got)
	}
}
