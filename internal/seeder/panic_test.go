package seeder

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/runerr"
	"github.com/AxeForging/seedstorm/internal/safego"
)

// A panic while writing (here the database driver panics, the boundary the
// writer calls) used to take the whole process down: every job of `serve` with
// it. It must end this run with an error that says where.
func TestSeed_PanicInAWriterFailsTheRunInsteadOfTheProcess(t *testing.T) {
	conn, rec := openRecording(t, nil, func(table string, n int) error {
		if table == "users" && n == 2 {
			panic("driver exploded")
		}
		return nil
	})
	order := []string{"users", "reviewers", "audit", "posts"}
	_, err := Seed(withDeadline(t, 20*time.Second), conn, "mysql", usersPostsAudit(), order, order, SeedOptions{
		Rows: 3000, BatchSize: 500, ChunkRows: 1000, Workers: 4,
	})
	var p *safego.PanicError
	if !errors.As(err, &p) || !strings.Contains(err.Error(), "driver exploded") {
		t.Fatalf("err = %v, want a recovered panic", err)
	}
	e, ok := runerr.As(err)
	if !ok || e.Phase != runerr.PhaseWrite || e.Table != "users" {
		t.Fatalf("location = %+v (%v), want write · users", e, ok)
	}
	if got := rec.inserts("posts"); len(got) != 0 {
		t.Fatalf("posts wrote %d batches after its parent failed", len(got))
	}
}

// A panic on a parallel generator (here a value rule panics) fails the run the
// same way.
func TestSeed_PanicInAParallelGeneratorFailsTheRun(t *testing.T) {
	conn, _ := openRecording(t, nil, nil)
	order := []string{"users", "reviewers", "audit", "posts"}
	overrides := faker.Overrides{"audit": {"note": func(row int, _ interface{}) (interface{}, error) {
		if row == 7 {
			var boom []int
			_ = boom[3]
		}
		return "ok", nil
	}}}
	opts := SeedOptions{Rows: 200, BatchSize: 50, ChunkRows: 100, Workers: 4, GenWorkers: 3}
	opts.Generate = faker.DefaultGenerateOptions()
	opts.Generate.Overrides = overrides
	_, err := Seed(withDeadline(t, 20*time.Second), conn, "mysql", usersPostsAudit(), order, order, opts)
	var p *safego.PanicError
	if !errors.As(err, &p) || !strings.Contains(err.Error(), "index out of range") {
		t.Fatalf("err = %v, want a recovered panic", err)
	}
	if e, ok := runerr.As(err); !ok || e.Phase != runerr.PhaseGenerate || e.Table != "audit" {
		t.Fatalf("location = %+v (%v), want generate · audit", e, ok)
	}
}

// Fill (mirror) splits chunks across workers too.
func TestFill_PanicInAPieceFailsTheTable(t *testing.T) {
	conn, _ := openRecording(t, nil, func(table string, n int) error {
		if table == "users" {
			panic("fill driver exploded")
		}
		return nil
	})
	res, err := Fill(withDeadline(t, 20*time.Second), conn, "mysql", usersPostsAudit(), []string{"users"}, map[string]int{"users": 2000}, Options{BatchSize: 200, Workers: 4, StopOnError: true})
	var p *safego.PanicError
	if !errors.As(err, &p) {
		t.Fatalf("err = %v (result %+v), want a recovered panic", err, res)
	}
}
