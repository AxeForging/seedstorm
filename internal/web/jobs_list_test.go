package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A page that is left and reopened finds the jobs its session started: running
// ones to reattach to, finished ones with their outcome. The server's boot id
// tells the page when the server restarted and older job ids are gone.
func TestJobsList_ShowsThisSessionsJobsAndTheBootID(t *testing.T) {
	s, err := New(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	mine := &Session{ID: "sess-mine"}
	other := &Session{ID: "sess-other"}
	release := make(chan struct{})
	running := s.jobs.StartFor(context.Background(), mine.ID, "seed", func(ctx context.Context, jc JobControl) (map[string]any, error) {
		jc.Phase("insert")
		jc.Progress(40, 100, "users")
		<-release
		return nil, nil
	})
	defer close(release)
	s.jobs.StartFor(context.Background(), other.ID, "seed", func(ctx context.Context, jc JobControl) (map[string]any, error) { return nil, nil })

	deadline := time.Now().Add(2 * time.Second)
	for {
		if evs := running.Events(); len(evs) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not report progress")
		}
		time.Sleep(10 * time.Millisecond)
	}

	w := httptest.NewRecorder()
	s.writeJobList(w, mine.ID)
	var body struct {
		BootID string `json:"bootId"`
		Jobs   []struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Phase    string `json:"phase"`
			Progress struct {
				Done  int    `json:"done"`
				Total int    `json:"total"`
				Label string `json:"label"`
			} `json:"progress"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	if body.BootID == "" || body.BootID != s.bootID {
		t.Fatalf("bootId = %q", body.BootID)
	}
	if len(body.Jobs) != 1 || body.Jobs[0].ID != running.ID {
		t.Fatalf("jobs = %+v, want only this session's job", body.Jobs)
	}
	j := body.Jobs[0]
	if j.Status != "running" || j.Phase != "insert" || j.Progress.Done != 40 || j.Progress.Total != 100 || j.Progress.Label != "users" {
		t.Fatalf("job = %+v", j)
	}
}

// Finished jobs are evicted: a server running for days must not keep every
// job's log in memory.
func TestManager_EvictsOldFinishedJobs(t *testing.T) {
	m := NewManager()
	m.keepFinished = 3
	var ids []string
	for i := 0; i < 6; i++ {
		j := m.Start(context.Background(), "quick", func(ctx context.Context, jc JobControl) (map[string]any, error) { return nil, nil })
		<-j.Done()
		ids = append(ids, j.ID)
	}
	blocking := make(chan struct{})
	live := m.Start(context.Background(), "long", func(ctx context.Context, jc JobControl) (map[string]any, error) { <-blocking; return nil, nil })
	defer close(blocking)
	for _, id := range ids[:3] {
		if _, ok := m.Get(id); ok {
			t.Errorf("old finished job %s was kept", id)
		}
	}
	for _, id := range ids[3:] {
		if _, ok := m.Get(id); !ok {
			t.Errorf("recent finished job %s was evicted", id)
		}
	}
	if _, ok := m.Get(live.ID); !ok {
		t.Fatal("a running job was evicted")
	}
}

// A quiet job still sends a keepalive, so the page can tell a slow step (the
// stream is alive) from a lost server.
func TestStreamJob_SendsKeepalivesWhileQuiet(t *testing.T) {
	defer func(old time.Duration) { streamKeepalive = old }(streamKeepalive)
	streamKeepalive = 20 * time.Millisecond
	m := NewManager()
	release := make(chan struct{})
	job := m.Start(context.Background(), "quiet", func(ctx context.Context, jc JobControl) (map[string]any, error) {
		<-release
		return nil, nil
	})
	srv := &Server{jobs: m}
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.streamJob(w, httptest.NewRequest(http.MethodGet, "/api/jobs/"+job.ID+"/stream", nil), job)
		close(done)
	}()
	time.Sleep(120 * time.Millisecond)
	close(release)
	<-done
	if n := strings.Count(w.Body.String(), "event: ping\n"); n < 2 {
		t.Fatalf("%d keepalives in:\n%s", n, w.Body.String())
	}
}
