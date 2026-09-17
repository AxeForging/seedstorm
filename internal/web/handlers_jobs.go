package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleJobsAPI dispatches GET/POST under /api/jobs/.
//
//	GET  /api/jobs/{id}          -> JSON snapshot
//	GET  /api/jobs/{id}/stream   -> SSE stream of log lines
//	POST /api/jobs/{id}/cancel   -> cancel the job
func (s *Server) handleJobsAPI(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "job id required")
		return
	}
	id := parts[0]
	job, ok := s.jobs.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown job")
		return
	}
	tail := ""
	if len(parts) > 1 {
		tail = parts[1]
	}
	switch tail {
	case "":
		writeJSON(w, http.StatusOK, jobView(job))
	case "stream":
		s.streamJob(w, r, job)
	case "cancel":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		s.jobs.Cancel(id)
		writeJSON(w, http.StatusOK, jobView(job))
	default:
		writeError(w, http.StatusNotFound, "unknown action")
	}
}

// streamKeepalive is how often a quiet job stream sends a ping event, so the page
// can tell a slow step from a lost server.
var streamKeepalive = 15 * time.Second

// handleJobList lists the jobs of the requesting session:
//
//	GET /api/jobs -> {bootId, jobs: [{id, name, status, phase, progress, ...}]}
func (s *Server) handleJobList(w http.ResponseWriter, r *http.Request) {
	sess, err := s.sessions.fromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"bootId": s.bootID, "jobs": []any{}})
		return
	}
	s.writeJobList(w, sess.ID)
}

func (s *Server) writeJobList(w http.ResponseWriter, owner string) {
	jobs := []map[string]any{}
	for _, j := range s.jobs.ForOwner(owner) {
		view := jobView(j)
		delete(view, "result")
		phase, progress := j.Position()
		view["phase"] = phase
		if progress != nil {
			view["progress"] = map[string]any{"done": progress.Done, "total": progress.Total, "label": progress.Text}
		}
		jobs = append(jobs, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"bootId": s.bootID, "jobs": jobs})
}

func (s *Server) streamJob(w http.ResponseWriter, r *http.Request, job *Job) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// A reconnecting client passes the last event it saw (?after= or the
	// browser's Last-Event-ID header) so the backlog is not replayed twice.
	after := lastSeenEvent(r)

	ch, backlog := job.Subscribe()
	defer job.Unsubscribe(ch)

	maxSeq := after
	for _, ev := range backlog {
		if ev.Seq > maxSeq {
			writeEvent(w, ev)
			maxSeq = ev.Seq
		}
	}
	flusher.Flush()

	keepalive := time.NewTicker(streamKeepalive)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			// A named event, not an SSE comment: the page cannot see comments,
			// and it needs the ping to tell a quiet job from a lost server.
			writeSSE(w, "ping", "")
			flusher.Flush()
		case ev, alive := <-ch:
			if !alive {
				writeJobEnd(w, job)
				flusher.Flush()
				return
			}
			if ev.Seq > maxSeq {
				writeEvent(w, ev)
				maxSeq = ev.Seq
			}
			flusher.Flush()
		case <-job.Done():
			// Drain any events not already emitted via the live channel.
			// Done fires before the subscriber channel closes, so we may have
			// buffered events still pending; maxSeq dedupes against them.
			for _, ev := range job.Events() {
				if ev.Seq > maxSeq {
					writeEvent(w, ev)
					maxSeq = ev.Seq
				}
			}
			writeJobEnd(w, job)
			flusher.Flush()
			return
		}
	}
}

// writeJobEnd closes a job stream: status, the failure reason when there is
// one, then end. The reason is sent as "failure", never "error": browsers
// treat a named "error" event as a connection error and fire onerror, which
// closed the stream before "end" arrived.
func writeJobEnd(w http.ResponseWriter, job *Job) {
	st := job.State()
	writeSSE(w, "status", string(st.Status))
	if st.Err != nil {
		writeSSE(w, "failure", st.Err.Error())
	}
	writeSSE(w, "end", "")
}

// lastSeenEvent reads the resume point of a stream request.
func lastSeenEvent(r *http.Request) int {
	raw := r.URL.Query().Get("after")
	if raw == "" {
		raw = r.Header.Get("Last-Event-ID")
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func writeEvent(w http.ResponseWriter, ev Event) {
	_, _ = fmt.Fprintf(w, "id: %d\n", ev.Seq)
	switch ev.Kind {
	case EventPhase:
		writeSSE(w, "phase", fmt.Sprintf("[%d] %s", ev.Seq, ev.Text))
	case EventProgress:
		writeSSE(w, "progress", fmt.Sprintf("[%d] %d/%d %s", ev.Seq, ev.Done, ev.Total, ev.Text))
	default:
		writeSSE(w, "log", fmt.Sprintf("[%d] %s", ev.Seq, ev.Text))
	}
}

func writeSSE(w http.ResponseWriter, event, data string) {
	if event != "" {
		_, _ = fmt.Fprintf(w, "event: %s\n", event)
	}
	for _, line := range strings.Split(data, "\n") {
		_, _ = fmt.Fprintf(w, "data: %s\n", line)
	}
	_, _ = fmt.Fprint(w, "\n")
}

func jobView(j *Job) map[string]any {
	st := j.State()
	errText := ""
	if st.Err != nil {
		errText = st.Err.Error()
	}
	return map[string]any{
		"id":     j.ID,
		"name":   j.Name,
		"status": st.Status,
		"start":  j.StartedAt,
		"end":    st.EndedAt,
		"error":  errText,
		"result": st.Result,
	}
}
