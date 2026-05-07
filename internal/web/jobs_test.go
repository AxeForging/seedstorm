package web

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestManager_RunsAndCompletes(t *testing.T) {
	m := NewManager()
	job := m.Start(context.Background(), "ok", func(ctx context.Context, log io.Writer) (map[string]any, error) {
		_, _ = log.Write([]byte("hello\n"))
		_, _ = log.Write([]byte("world\n"))
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
	job := m.Start(context.Background(), "fail", func(ctx context.Context, log io.Writer) (map[string]any, error) {
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
	job := m.Start(context.Background(), "long", func(ctx context.Context, log io.Writer) (map[string]any, error) {
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
	job := m.Start(context.Background(), "stream", func(ctx context.Context, log io.Writer) (map[string]any, error) {
		<-start
		for i := 0; i < 3; i++ {
			_, _ = log.Write([]byte("line\n"))
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
		case line, ok := <-ch:
			if !ok {
				goto done
			}
			if !strings.HasPrefix(line.Text, "line") {
				t.Fatalf("unexpected text %q", line.Text)
			}
			got++
		case <-timeout:
			t.Fatal("timed out waiting for lines")
		}
	}
done:
	<-job.Done()
}

func TestManager_SubscribeReplaysBacklog(t *testing.T) {
	m := NewManager()
	job := m.Start(context.Background(), "fast", func(ctx context.Context, log io.Writer) (map[string]any, error) {
		_, _ = log.Write([]byte("a\n"))
		_, _ = log.Write([]byte("b\n"))
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
}
