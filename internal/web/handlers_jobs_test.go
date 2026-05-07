package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestStreamJob_EmitsLogPhaseProgressInOrder feeds a deterministic event
// sequence into a job and asserts the SSE response contains the matching
// `event:` lines in order, with the right wire format for each kind.
func TestStreamJob_EmitsLogPhaseProgressInOrder(t *testing.T) {
	m := NewManager()
	gate := make(chan struct{})
	job := m.Start(context.Background(), "wire", func(ctx context.Context, jc JobControl) (map[string]any, error) {
		<-gate
		jc.Phase("build")
		_, _ = jc.Write([]byte("first\n"))
		jc.Phase("insert")
		jc.Progress(1, 2, "users")
		jc.Progress(2, 2, "orders")
		jc.Phase("done")
		return nil, nil
	})

	srv := &Server{jobs: m}
	r := httptest.NewRequest(http.MethodGet, "/api/jobs/"+job.ID+"/stream", nil)
	w := httptest.NewRecorder()

	// Subscribe before unblocking so all events flow through the live channel
	// rather than the backlog replay path. Run streamJob in a goroutine and
	// stop it once the job ends.
	done := make(chan struct{})
	go func() {
		srv.streamJob(w, r, job)
		close(done)
	}()
	// Give the handler a tick to subscribe before the runner starts emitting.
	time.Sleep(20 * time.Millisecond)
	close(gate)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("streamJob did not finish")
	}

	body := w.Body.String()
	wantOrder := []string{
		"event: phase\ndata: ", // build
		"build",
		"event: log\ndata: ",
		"first",
		"event: phase\ndata: ",
		"insert",
		"event: progress\ndata: ",
		"1/2 users",
		"event: progress\ndata: ",
		"2/2 orders",
		"event: phase\ndata: ",
		"done",
		"event: status\ndata: done",
		"event: end",
	}
	pos := 0
	for _, needle := range wantOrder {
		idx := strings.Index(body[pos:], needle)
		if idx < 0 {
			t.Fatalf("missing %q after pos %d in:\n%s", needle, pos, body)
		}
		pos += idx + len(needle)
	}
}

// TestStreamJob_NoDuplicatesAcrossLiveAndDone exercises the race where Done()
// fires while events still sit on the live channel. The handler must not
// re-emit those events from the snapshot.
func TestStreamJob_NoDuplicatesAcrossLiveAndDone(t *testing.T) {
	for trial := 0; trial < 25; trial++ {
		m := NewManager()
		gate := make(chan struct{})
		job := m.Start(context.Background(), "racy", func(ctx context.Context, jc JobControl) (map[string]any, error) {
			<-gate
			jc.Phase("a")
			jc.Phase("b")
			jc.Phase("c")
			return nil, nil
		})

		srv := &Server{jobs: m}
		r := httptest.NewRequest(http.MethodGet, "/api/jobs/"+job.ID+"/stream", nil)
		w := httptest.NewRecorder()

		done := make(chan struct{})
		go func() {
			srv.streamJob(w, r, job)
			close(done)
		}()
		// Let the handler subscribe before the runner starts emitting.
		time.Sleep(5 * time.Millisecond)
		close(gate)

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("trial %d: streamJob did not finish", trial)
		}

		body := w.Body.String()
		// Each phase name must appear exactly once on the wire.
		for _, name := range []string{"] a", "] b", "] c"} {
			if c := strings.Count(body, name); c != 1 {
				t.Fatalf("trial %d: phase %q appeared %d times in body:\n%s", trial, name, c, body)
			}
		}
	}
}

// TestStreamJob_ReplaysBacklog covers a late subscriber: the job has finished
// before SSE opens, so all events come from the backlog replay path.
func TestStreamJob_ReplaysBacklog(t *testing.T) {
	m := NewManager()
	job := m.Start(context.Background(), "replay", func(ctx context.Context, jc JobControl) (map[string]any, error) {
		jc.Phase("only-phase")
		_, _ = jc.Write([]byte("only-line\n"))
		jc.Progress(1, 1, "only-step")
		return nil, nil
	})
	<-job.Done()

	srv := &Server{jobs: m}
	r := httptest.NewRequest(http.MethodGet, "/api/jobs/"+job.ID+"/stream", nil)
	w := httptest.NewRecorder()
	srv.streamJob(w, r, job)

	body := w.Body.String()
	for _, needle := range []string{
		"event: phase",
		"only-phase",
		"event: log",
		"only-line",
		"event: progress",
		"1/1 only-step",
		"event: status\ndata: done",
		"event: end",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("missing %q in replayed body:\n%s", needle, body)
		}
	}
}
