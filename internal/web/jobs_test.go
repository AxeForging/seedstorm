package web

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestManager_RunsAndCompletes(t *testing.T) {
	m := NewManager()
	job := m.Start(context.Background(), "ok", func(ctx context.Context, jc JobControl) (map[string]any, error) {
		_, _ = jc.Write([]byte("hello\n"))
		_, _ = jc.Write([]byte("world\n"))
		return map[string]any{"n": 2}, nil
	})
	select {
	case <-job.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("job did not complete in time")
	}
	if job.Status != JobDone {
		t.Fatalf("status = %v, want done", job.Status)
	}
	lines := job.Lines()
	if len(lines) != 2 || lines[0].Text != "hello" || lines[1].Text != "world" {
		t.Fatalf("unexpected lines: %+v", lines)
	}
	if job.Result["n"].(int) != 2 {
		t.Fatalf("missing result")
	}
}

func TestManager_PropagatesError(t *testing.T) {
	m := NewManager()
	want := errors.New("boom")
	job := m.Start(context.Background(), "fail", func(ctx context.Context, jc JobControl) (map[string]any, error) {
		return nil, want
	})
	<-job.Done()
	if job.Status != JobFailed {
		t.Fatalf("status = %v, want failed", job.Status)
	}
	if !errors.Is(job.Err, want) {
		t.Fatalf("err = %v, want %v", job.Err, want)
	}
}

func TestManager_CancelMarksJobCanceled(t *testing.T) {
	m := NewManager()
	job := m.Start(context.Background(), "long", func(ctx context.Context, jc JobControl) (map[string]any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if !m.Cancel(job.ID) {
		t.Fatal("Cancel returned false")
	}
	<-job.Done()
	if job.Status != JobCanceled {
		t.Fatalf("status = %v, want canceled", job.Status)
	}
}

func TestManager_SubscribeReceivesLiveLines(t *testing.T) {
	m := NewManager()
	start := make(chan struct{})
	job := m.Start(context.Background(), "stream", func(ctx context.Context, jc JobControl) (map[string]any, error) {
		<-start
		for i := 0; i < 3; i++ {
			_, _ = jc.Write([]byte("line\n"))
		}
		return nil, nil
	})
	ch, backlog := job.Subscribe()
	if len(backlog) != 0 {
		t.Fatalf("expected empty backlog before start, got %d", len(backlog))
	}
	close(start)
	got := 0
	timeout := time.After(2 * time.Second)
	for got < 3 {
		select {
		case ev, ok := <-ch:
			if !ok {
				goto done
			}
			if ev.Kind != EventLog || !strings.HasPrefix(ev.Text, "line") {
				t.Fatalf("unexpected event %+v", ev)
			}
			got++
		case <-timeout:
			t.Fatal("timed out waiting for events")
		}
	}
done:
	<-job.Done()
}

func TestManager_SubscribeReplaysBacklog(t *testing.T) {
	m := NewManager()
	job := m.Start(context.Background(), "fast", func(ctx context.Context, jc JobControl) (map[string]any, error) {
		_, _ = jc.Write([]byte("a\n"))
		_, _ = jc.Write([]byte("b\n"))
		return nil, nil
	})
	<-job.Done()
	_, backlog := job.Subscribe()
	if len(backlog) != 2 {
		t.Fatalf("backlog len = %d, want 2", len(backlog))
	}
	if backlog[0].Text != "a" || backlog[1].Text != "b" {
		t.Fatalf("unexpected backlog: %+v", backlog)
	}
	if backlog[0].Kind != EventLog {
		t.Fatalf("backlog[0].Kind = %v, want %v", backlog[0].Kind, EventLog)
	}
}

func TestManager_PhaseAndProgressEvents(t *testing.T) {
	m := NewManager()
	job := m.Start(context.Background(), "phased", func(ctx context.Context, jc JobControl) (map[string]any, error) {
		jc.Phase("build")
		_, _ = jc.Write([]byte("graph built\n"))
		jc.Phase("insert")
		jc.Progress(1, 3, "users")
		jc.Progress(2, 3, "orders")
		jc.Progress(3, 3, "items")
		jc.Phase("done")
		return nil, nil
	})
	<-job.Done()

	events := job.Events()
	// Expected sequence: phase build, log, phase insert, progress 1/3, progress 2/3, progress 3/3, phase done.
	wantKinds := []EventKind{
		EventPhase, EventLog, EventPhase, EventProgress, EventProgress, EventProgress, EventPhase,
	}
	if len(events) != len(wantKinds) {
		t.Fatalf("event count = %d, want %d (%+v)", len(events), len(wantKinds), events)
	}
	for i, want := range wantKinds {
		if events[i].Kind != want {
			t.Fatalf("events[%d].Kind = %s, want %s (%+v)", i, events[i].Kind, want, events[i])
		}
	}
	if events[0].Text != "build" || events[2].Text != "insert" || events[6].Text != "done" {
		t.Fatalf("phase names wrong: %s / %s / %s", events[0].Text, events[2].Text, events[6].Text)
	}
	if events[3].Done != 1 || events[3].Total != 3 || events[3].Text != "users" {
		t.Fatalf("first progress wrong: %+v", events[3])
	}
	if events[5].Done != 3 || events[5].Total != 3 || events[5].Text != "items" {
		t.Fatalf("last progress wrong: %+v", events[5])
	}

	// Lines() must continue to return only the log-kind events.
	lines := job.Lines()
	if len(lines) != 1 || lines[0].Text != "graph built" {
		t.Fatalf("Lines() backwards-compat broken: %+v", lines)
	}
}

func TestManager_SubscribeReceivesPhaseAndProgressLive(t *testing.T) {
	m := NewManager()
	gate := make(chan struct{})
	job := m.Start(context.Background(), "live", func(ctx context.Context, jc JobControl) (map[string]any, error) {
		<-gate
		jc.Phase("p1")
		jc.Progress(1, 2, "a")
		_, _ = jc.Write([]byte("midway\n"))
		jc.Progress(2, 2, "b")
		return nil, nil
	})
	ch, _ := job.Subscribe()
	close(gate)

	var kinds []EventKind
	timeout := time.After(2 * time.Second)
	for len(kinds) < 4 {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed early after %d events: %v", len(kinds), kinds)
			}
			kinds = append(kinds, ev.Kind)
		case <-timeout:
			t.Fatalf("timed out — got %v", kinds)
		}
	}
	want := []EventKind{EventPhase, EventProgress, EventLog, EventProgress}
	for i, w := range want {
		if kinds[i] != w {
			t.Fatalf("kinds[%d] = %s, want %s (full: %v)", i, kinds[i], w, kinds)
		}
	}
	<-job.Done()
}
