package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// A named SSE event called "error" also fires EventSource.onerror in browsers,
// which closed the stream before "end" arrived: every failed job left the page
// waiting forever. The terminal failure must use another event name.
func TestStreamJob_FailedJobEndsWithFailureNotErrorEvent(t *testing.T) {
	m := NewManager()
	job := m.Start(context.Background(), "boom", func(ctx context.Context, jc JobControl) (map[string]any, error) {
		jc.Phase("connect")
		return nil, errors.New("target: ping database: connection refused")
	})
	<-job.Done()

	srv := &Server{jobs: m}
	w := httptest.NewRecorder()
	srv.streamJob(w, httptest.NewRequest(http.MethodGet, "/api/jobs/"+job.ID+"/stream", nil), job)

	body := w.Body.String()
	if strings.Contains(body, "event: error\n") {
		t.Fatalf("stream uses the reserved 'error' event name:\n%s", body)
	}
	for _, needle := range []string{
		"event: status\ndata: failed",
		"event: failure\ndata: target: ping database: connection refused",
		"event: end",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("missing %q in:\n%s", needle, body)
		}
	}
	if strings.Index(body, "event: failure") > strings.Index(body, "event: end") {
		t.Fatalf("failure must come before end:\n%s", body)
	}
}

// Every job event carries an SSE id so a reconnecting client can resume after
// the last one it saw instead of replaying (and duplicating) the whole log.
func TestStreamJob_ResumesAfterLastSeenEvent(t *testing.T) {
	m := NewManager()
	job := m.Start(context.Background(), "resume", func(ctx context.Context, jc JobControl) (map[string]any, error) {
		jc.Phase("one")
		jc.Phase("two")
		jc.Phase("three")
		return nil, nil
	})
	<-job.Done()
	srv := &Server{jobs: m}

	w := httptest.NewRecorder()
	srv.streamJob(w, httptest.NewRequest(http.MethodGet, "/api/jobs/"+job.ID+"/stream", nil), job)
	if body := w.Body.String(); !strings.Contains(body, "id: 1\n") || !strings.Contains(body, "id: 3\n") {
		t.Fatalf("events carry no SSE ids:\n%s", body)
	}

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/jobs/"+job.ID+"/stream?after=2", nil),
		func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/api/jobs/"+job.ID+"/stream", nil)
			r.Header.Set("Last-Event-ID", "2")
			return r
		}(),
	} {
		w := httptest.NewRecorder()
		srv.streamJob(w, req, job)
		body := w.Body.String()
		if strings.Contains(body, "] one") || strings.Contains(body, "] two") {
			t.Fatalf("resumed stream replayed events already seen:\n%s", body)
		}
		if !strings.Contains(body, "] three") || !strings.Contains(body, "event: end") {
			t.Fatalf("resumed stream lost later events:\n%s", body)
		}
	}
}

// A job that writes faster than the browser reads must not lose events: the
// subscriber's buffer overflows, and the stream fills the gap from the job's
// own event list (CI: "stream skipped events: got seq 9, want 8").
func TestJobStream_SlowReaderStillSeesEveryEvent(t *testing.T) {
	const lines = 500
	s, err := New(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: "stream-sess", DBType: "pgx"}
	s.sessions.add(sess)
	started := make(chan struct{})
	job := s.jobs.StartFor(context.Background(), sess.ID, "noisy", func(ctx context.Context, jc JobControl) (map[string]any, error) {
		<-started
		for i := 0; i < lines; i++ {
			_, _ = fmt.Fprintf(jc, "line %d\n", i)
		}
		return nil, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/jobs/"+job.ID+"/stream", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Handler().ServeHTTP(rec, req)
	}()
	close(started)
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the stream did not end with the job")
	}

	var seqs []int
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if id, ok := strings.CutPrefix(line, "id: "); ok {
			n, err := strconv.Atoi(strings.TrimSpace(id))
			if err != nil {
				t.Fatalf("unreadable event id %q", id)
			}
			seqs = append(seqs, n)
		}
	}
	if len(seqs) < lines {
		t.Fatalf("stream carried %d events, want at least %d", len(seqs), lines)
	}
	for i, seq := range seqs {
		if seq != i+1 {
			t.Fatalf("event %d has seq %d: the stream skipped events", i+1, seq)
		}
	}
}
