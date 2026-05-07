package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
)

// startRun is a helper that decodes a JSON body into req, requires an active
// session, kicks off a job that delegates to runner, and returns the job ID.
func startRun[T any](
	s *Server,
	w http.ResponseWriter,
	r *http.Request,
	jobName string,
	runner func(ctx context.Context, sess *Session, req T, jc JobControl) (map[string]any, error),
) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	sess, err := s.sessions.fromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	var req T
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
	}
	job := s.jobs.Start(context.Background(), jobName, func(ctx context.Context, jc JobControl) (map[string]any, error) {
		return runner(ctx, sess, req, jc)
	})
	writeJSON(w, http.StatusAccepted, jobView(job))
}

func (s *Server) handleSeedRun(w http.ResponseWriter, r *http.Request) {
	startRun(s, w, r, "seed", s.runSeed)
}

func (s *Server) handleGapsRun(w http.ResponseWriter, r *http.Request) {
	startRun(s, w, r, "gaps", s.runGaps)
}

func (s *Server) handleGenerateRun(w http.ResponseWriter, r *http.Request) {
	startRun(s, w, r, "generate", s.runGenerate)
}

func (s *Server) handleEnrichRun(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("GEMINI_API_KEY") == "" {
		writeError(w, http.StatusBadRequest, "GEMINI_API_KEY env var must be set on the seedstorm server")
		return
	}
	startRun(s, w, r, "ai-enrich", s.runEnrich)
}

func (s *Server) handleExportRun(w http.ResponseWriter, r *http.Request) {
	startRun(s, w, r, "export", s.runExport)
}
