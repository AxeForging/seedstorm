package web

import (
	"fmt"
	"net/http"
	"strings"
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

	ch, backlog := job.Subscribe()
	defer job.Unsubscribe(ch)

	maxSeq := 0
	for _, ev := range backlog {
		writeEvent(w, ev)
		if ev.Seq > maxSeq {
			maxSeq = ev.Seq
		}
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, alive := <-ch:
			if !alive {
				writeSSE(w, "status", string(job.Status))
				if job.Err != nil {
					writeSSE(w, "error", job.Err.Error())
				}
				writeSSE(w, "end", "")
				flusher.Flush()
				return
			}
			writeEvent(w, ev)
			if ev.Seq > maxSeq {
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
				}
			}
			writeSSE(w, "status", string(job.Status))
			if job.Err != nil {
				writeSSE(w, "error", job.Err.Error())
			}
			writeSSE(w, "end", "")
			flusher.Flush()
			return
		}
	}
}

func writeEvent(w http.ResponseWriter, ev Event) {
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
	return map[string]any{
		"id":     j.ID,
		"name":   j.Name,
		"status": j.Status,
		"start":  j.StartedAt,
		"end":    j.EndedAt,
		"error": func() string {
			if j.Err != nil {
				return j.Err.Error()
			}
			return ""
		}(),
		"result": j.Result,
	}
}
